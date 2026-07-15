package wui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// addMockEngine registers a second mock engine on the connector. Returns
// the mock and the hub for injecting events to the engine.
func addMockEngine(t *testing.T, c *WUIConnector, engineID string) (*mockEngine, *hub.Hub) {
	t.Helper()
	h := hub.NewHub()
	mock := &mockEngine{}
	slot := &engineSlot{
		engineID:    engineID,
		engine:      mock,
		hub:         h,
		taskToolIDs: make(map[string]bool),
	}
	slot.unsubscribe = h.Subscribe(&engineHubShim{engineID: engineID, c: c})
	c.slotsMu.Lock()
	c.slots[engineID] = slot
	c.slotsMu.Unlock()
	t.Cleanup(func() {
		slot.unsubscribe()
	})
	return mock, h
}

// TestMultiEngine_InactiveEngineBuffersOnly verifies that events from a
// non-active engine are buffered but NOT forwarded to the Handle path.
// Only the active engine's events are dispatched through Handle → WS.
//
// New counting: text_delta accumulates into one text entry (count=1),
// tool_start = 1 tool entry (count=1). Total = 2 for engineB.
func TestMultiEngine_InactiveEngineBuffersOnly(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Send events from engineB (inactive) — should be buffered.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "fromB"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Grep"}})

	// Wait for hub dispatch to complete (async).
	if !waitFor(time.Second, func() bool {
		return streamStateCount(c, "engineB") >= 2
	}) {
		bBuf := streamStateCount(c, "engineB")
		t.Errorf("expected >=2 buffered events for inactive engine, got %d", bBuf)
	}

	// Verify engineB's buffer has the events.
	mainBuf := streamStateCount(c, "main")
	bBuf := streamStateCount(c, "engineB")

	if bBuf < 2 {
		t.Errorf("expected >=2 buffered events for inactive engine, got %d", bBuf)
	}
	// Main engine should have 0 events (nothing sent to it).
	if mainBuf != 0 {
		t.Errorf("main engine should have 0 events, got %d", mainBuf)
	}
}

// TestMultiEngine_ActiveEngineWritesRealTime verifies events to the active
// engine are dispatched through Handle and buffered.
func TestMultiEngine_ActiveEngineWritesRealTime(t *testing.T) {
	c := newTestConnector(t)

	// Active engine events should be buffered.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "live"})

	mainBuf := streamStateCount(c, "main")

	if mainBuf < 1 {
		t.Errorf("active engine should buffer events, got %d buffered", mainBuf)
	}
}

// TestMultiEngine_SwitchReplaysTargetStreamBuf verifies that engine_switch
// does not crash and switches the active engine.
func TestMultiEngine_SwitchReplaysTargetStreamBuf(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Buffer events on engineB (inactive).
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "bufferedB"})

	// Switch to engineB.
	c.handleEngineSwitch("engineB")

	active := c.ActiveID()

	if active != "engineB" {
		t.Errorf("active engine after switch = %q, want %q", active, "engineB")
	}
}

// TestMultiEngine_DualStreamSwitch verifies that when two engines are both
// streaming, switching between them delivers the correct engine's live events
// and streamState snapshot on each switch. The WS should never receive events
// from a non-active engine in real-time.
//
// New protocol: engine switch sends a single metadata frame, then
// onEngineEvent sends streamState snapshot on the first live event.
// The streamState snapshot contains accumulated text (not individual deltas).
//
// Flow:
//  1. Both main and engineB stream concurrently
//  2. Switch main→engineB: WS gets metadata + streamState (b-1 text) + live events
//  3. Switch engineB→main: WS gets metadata + streamState (main-1 text) + live events
//  4. Verify no cross-contamination
func TestMultiEngine_DualStreamSwitch(t *testing.T) {
	// Create connector with an explicit hub for main engine events.
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, hubB := addMockEngine(t, c, "engineB")

	// Both engines buffer some events while main is active.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-1"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-1"})

	// Connect WS and drain the metadata frame.
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// main-1 is buffered but not yet on the WS (it was dispatched before
	// takeover). The first live event to main triggers a streamState snapshot.

	// Send a live event to main — should arrive on WS as streamState + event.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-2"})
	// Read streamState snapshot (contains accumulated text "main-1main-2"),
	// then the event frame for main-2.
	drainUntilEvent(t, ws, "main-2")

	// While main is streaming, engineB also buffers events.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-2"})

	// Switch to engineB mid-stream. This sends a metadata frame.
	c.handleEngineSwitch("engineB")
	// Drain the metadata frame.
	readMetadata(t, ws)

	// Send a live event to engineB — triggers streamState snapshot (b-1b-2)
	// then event frame for b-3.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-3"})
	// Read streamState snapshot + event frame.
	drainUntilEvent(t, ws, "b-3")

	// While engineB is active, main buffers events silently.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-3"})

	// Switch back to main. Sends metadata frame.
	c.handleEngineSwitch("main")
	readMetadata(t, ws)

	// Send live event to main — triggers streamState snapshot (main-1main-2main-3)
	// then event frame for main-4.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-4"})
	drainUntilEvent(t, ws, "main-4")
}

// drainUntilEvent reads WS messages until finding an event frame whose event
// text matches want. Skips metadata, streamState, and other non-event frames.
// Fails if it encounters an unexpected event with different text
// (cross-contamination from the wrong engine) or if not found within 20 frames.
func drainUntilEvent(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	for range 20 {
		msg := readWSMessage(t, ws)
		var env struct {
			Type  string `json:"type"`
			Event struct {
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event.Text != "" {
			if env.Event.Text == want {
				return
			}
			t.Fatalf("unexpected event text %q while looking for %q (cross-contamination)", env.Event.Text, want)
		}
	}
	t.Fatalf("did not find event %q within 20 frames", want)
}

// TestMultiEngine_SwitchToUnknownID verifies that engine_switch to a
// non-existent engine ID is a no-op (no crash).
func TestMultiEngine_SwitchToUnknownID(t *testing.T) {
	c := newTestConnector(t)
	c.handleEngineSwitch("nonexistent")

	active := c.ActiveID()

	if active != "main" {
		t.Errorf("active after switch to unknown = %q, want %q", active, "main")
	}
}

// TestMultiEngine_PerEngineBufferIsolation verifies that two engines'
// streamStates are independent — events to one do not appear in the other's
// buffer.
func TestMultiEngine_PerEngineBufferIsolation(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Send events to both engines.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "mainEvent"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "bEvent"})

	mainBuf := streamStateCount(c, "main")
	bBuf := streamStateCount(c, "engineB")

	if mainBuf == 0 {
		t.Error("main engine buffer should have events")
	}
	if bBuf == 0 {
		t.Error("engineB buffer should have events")
	}
}

// TestMultiEngine_QueryEndClearsOnlyTargetEngine verifies that clearing
// streamState for one engine does not affect another engine's buffer.
func TestMultiEngine_QueryEndClearsOnlyTargetEngine(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Buffer events on both engines.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "main"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b"})

	// Clear only engineB.
	c.slotsMu.RLock()
	slotB := c.slots["engineB"]
	c.slotsMu.RUnlock()
	if slotB != nil {
		slotB.streamState = streamState{}
		resetQueryStats(&slotB.queryStats)
		slotB.taskToolIDs = make(map[string]bool)
	}

	mainBuf := streamStateCount(c, "main")
	bBuf := streamStateCount(c, "engineB")

	if mainBuf == 0 {
		t.Error("main engine buffer should still have events after clearing engineB")
	}
	if bBuf != 0 {
		t.Errorf("engineB buffer should be empty after clear, got %d entries", bBuf)
	}
}

// TestMultiEngine_ConcurrentEventDelivery verifies that concurrent events
// to two different engines are safe under -race.
//
// New counting: 100 text_delta events accumulate into a single text string
// (count=1). So we verify the text is non-empty rather than checking count.
func TestMultiEngine_ConcurrentEventDelivery(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("A%d", i)})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 100 {
			hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("B%d", i)})
		}
	}()
	wg.Wait()

	// Wait for hub dispatch to complete (async).
	if !waitFor(time.Second, func() bool {
		return streamStateCount(c, "engineB") >= 1
	}) {
		t.Errorf("engineB buffer empty after concurrent dispatch")
	}

	// Verify both engines have buffered their events.
	// New counting: 100 text_deltas → 1 accumulated text entry.
	mainBuf := streamStateCount(c, "main")
	bBuf := streamStateCount(c, "engineB")

	if mainBuf != 1 {
		t.Errorf("expected 1 entry (accumulated text) in main buffer, got %d", mainBuf)
	}
	if bBuf != 1 {
		t.Errorf("expected 1 entry (accumulated text) in engineB buffer, got %d", bBuf)
	}
}

// TestMultiEngine_SwitchDuringActiveStreaming verifies that after
// handleEngineSwitch returns, c.ActiveID() is the new engine and events
// dispatched to the old engine are buffered (not written to WS).
func TestMultiEngine_SwitchDuringActiveStreaming(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, _ = addMockEngine(t, c, "engineB")

	ws := dialAndStore(t, c)

	// Switch to engineB.
	c.handleEngineSwitch("engineB")

	// c.ActiveID() must be engineB immediately after switch.
	active := c.ActiveID()
	if active != "engineB" {
		t.Fatalf("c.ActiveID() = %q, want engineB", active)
	}

	// Dispatch an event to main (now inactive) — should be buffered, not
	// written to WS.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "post-switch-main"})

	// Verify main's streamState has the event.
	if !waitFor(time.Second, func() bool {
		return streamStateCount(c, "main") >= 1
	}) {
		t.Error("main engine should have buffered the post-switch event")
	}

	// Read WS — should find metadata (from switch), NOT the post-switch
	// event from main. Drain frames until metadata or timeout.
	foundMetadata := false
	foundPostSwitchEvent := false
	for range 20 {
		msg := readWSMessage(t, ws)
		var env struct {
			Type  string `json:"type"`
			Event struct {
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "metadata" {
			foundMetadata = true
			break
		}
		if env.Type == "event" && env.Event.Text == "post-switch-main" {
			foundPostSwitchEvent = true
		}
	}
	if !foundMetadata {
		t.Error("expected metadata on WS after switch")
	}
	if foundPostSwitchEvent {
		t.Error("post-switch event from old (inactive) engine leaked to WS — onEngineEvent did not check c.ActiveID() before writing")
	}
}

// TestIntegration_SubAgentEventsSurviveEngineSwitch verifies the original bug:
// sub-agent events (text, tools) are correctly preserved in the Block tree
// during engine switch. When switching back, the snapshot contains the
// full nested structure — sub-agent text inside Agent tool's children.
//
// Full chain: hub dispatch → updateStreamState (tree) → snapshot serialize → WS frame.
func TestIntegration_SubAgentEventsSurviveEngineSwitch(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, _ = addMockEngine(t, c, "engineB")

	// Main agent starts streaming: text + Agent tool
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "Let me check"})
	hubMain.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
		ID: "agent1", Name: "Agent", Summary: "Reviewer",
	}})

	// Sub-agent events arrive (nested under agent1 via ParentToolUseID)
	hubMain.Dispatch(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "Reviewing code",
		Agent: &types.AgentMeta{ParentToolUseID: "agent1", AgentType: "Reviewer"},
	})
	hubMain.Dispatch(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "grep1", Name: "Grep", Summary: "streamState"},
		Agent:   &types.AgentMeta{ParentToolUseID: "agent1", AgentType: "Reviewer"},
	})
	hubMain.Dispatch(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "grep1", DisplayOutput: "found 5 matches"},
		Agent:      &types.AgentMeta{ParentToolUseID: "agent1", AgentType: "Reviewer"},
	})

	// Wait for events to be processed
	if !waitFor(time.Second, func() bool { return streamStateCount(c, "main") >= 2 }) {
		t.Fatal("main should have buffered text + Agent tool")
	}

	// Verify streamState tree structure BEFORE switch
	c.slotsMu.RLock()
	mainSlot := c.slots["main"]
	c.slotsMu.RUnlock()
	if mainSlot == nil {
		t.Fatal("main slot not found")
	}

	// Should have: [text:"Let me check", tool(agent1, children:[text, tool(grep1, done)])]
	if len(mainSlot.streamState.blocks) != 2 {
		t.Fatalf("expected 2 root blocks (text + Agent tool), got %d", len(mainSlot.streamState.blocks))
	}
	agentBlock := mainSlot.streamState.blocks[1]
	if agentBlock.Kind != "tool" || agentBlock.Name != "Agent" {
		t.Fatalf("expected Agent tool at index 1, got kind=%s name=%s", agentBlock.Kind, agentBlock.Name)
	}
	if len(agentBlock.Children) != 2 {
		t.Fatalf("expected 2 children in Agent tool, got %d", len(agentBlock.Children))
	}
	if agentBlock.Children[0].Kind != "text" || agentBlock.Children[0].Text != "Reviewing code" {
		t.Errorf("child[0] = %+v, want text 'Reviewing code'", agentBlock.Children[0])
	}
	if agentBlock.Children[1].Kind != "tool" || agentBlock.Children[1].Name != "Grep" {
		t.Errorf("child[1] = %+v, want Grep tool", agentBlock.Children[1])
	}
	if agentBlock.Children[1].State != "done" {
		t.Errorf("child[1].State = %s, want 'done'", agentBlock.Children[1].State)
	}

	// Connect WS + switch to engineB (main becomes inactive)
	ws := dialAndStore(t, c)
	c.handleEngineSwitch("engineB")
	readMetadata(t, ws)

	// More sub-agent events arrive while main is inactive
	hubMain.Dispatch(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "Found issues",
		Agent: &types.AgentMeta{ParentToolUseID: "agent1", AgentType: "Reviewer"},
	})
	if !waitFor(time.Second, func() bool {
		c.slotsMu.RLock()
		s := c.slots["main"]
		c.slotsMu.RUnlock()
		return s != nil && len(s.streamState.blocks) >= 2 && len(s.streamState.blocks[1].Children) >= 3
	}) {
		t.Fatal("sub-agent text should be buffered while inactive")
	}

	// Switch back to main — snapshot should contain nested children
	c.handleEngineSwitch("main")
	readMetadata(t, ws)

	// Trigger snapshot by sending a live event
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "done"})

	// Read snapshot frame
	snap := readStreamStatePayload(t, ws)
	// 3 root blocks: text "Let me check", Agent tool, text "done"
	// (the final text_delta "done" creates a new root text block)
	if len(snap.Blocks) != 3 {
		t.Fatalf("snapshot should have 3 root blocks, got %d", len(snap.Blocks))
	}
	// Find the Agent tool block (not at fixed index due to text block ordering)
	var snapAgent streamBlock
	for _, b := range snap.Blocks {
		if b.Kind == "tool" && b.Name == "Agent" {
			snapAgent = b
			break
		}
	}
	if snapAgent.Kind != "tool" {
		t.Fatal("snapshot missing Agent tool block")
	}
	if len(snapAgent.Children) != 3 {
		t.Fatalf("snapshot Agent tool should have 3 children, got %d", len(snapAgent.Children))
	}
	// Third child (added while inactive) should have "Found issues"
	found := false
	for _, child := range snapAgent.Children {
		if child.Kind == "text" && child.Text == "Found issues" {
			found = true
			break
		}
	}
	if !found {
		t.Error("snapshot missing sub-agent text 'Found issues' added while inactive")
	}
}

// TestIntegration_QueryEndClearsMidQuery verifies that query_end
// resets streamState at the end of a query. Mid-query (between turns),
// streamState persists until query_end clears it.
func TestIntegration_QueryEndClearsMidQuery(t *testing.T) {
	c := newTestConnector(t)

	// Turn 1: text + tool_start
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "Let me search"})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
		ID: "g1", Name: "Grep", Summary: "pattern",
	}})

	if streamStateCount(c, "main") != 2 {
		t.Fatalf("expected 2 blocks before query_end, got %d", streamStateCount(c, "main"))
	}

	// query_end clears streamState
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if streamStateCount(c, "main") != 0 {
		t.Fatalf("streamState should be empty after query_end, got %d blocks", streamStateCount(c, "main"))
	}

	// Tool results arrive — streamState is empty (query_end cleared it).
	c.Handle(types.QueryEvent{Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "g1", DisplayOutput: "results"}})

	if streamStateCount(c, "main") != 0 {
		t.Fatalf("streamState should still be empty after orphaned tool_end, got %d blocks", streamStateCount(c, "main"))
	}

	// query_end fires again for tool execution phase
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if streamStateCount(c, "main") != 0 {
		t.Fatalf("streamState should be empty after second query_end, got %d blocks", streamStateCount(c, "main"))
	}

	// Turn 2: fresh text — should start from empty
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "Based on results"})
	if streamStateCount(c, "main") != 1 {
		t.Fatalf("expected 1 block in turn 2, got %d", streamStateCount(c, "main"))
	}
	c.slotsMu.RLock()
	mainSlot := c.slots["main"]
	c.slotsMu.RUnlock()
	if mainSlot.streamState.blocks[0].Text != "Based on results" {
		t.Errorf("turn 2 text = %q, want 'Based on results'", mainSlot.streamState.blocks[0].Text)
	}
}

// TestIntegration_SnapshotHasCorrectNestedJSON verifies that buildPendingBlocks
// produces JSON with correct nesting structure matching client model.ts.
func TestIntegration_SnapshotHasCorrectNestedJSON(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "main text"})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
		ID: "a1", Name: "Agent", Summary: "Reviewer",
	}})
	c.Handle(types.QueryEvent{
		Type:  types.EventTextDelta,
		Text:  "sub text",
		Agent: &types.AgentMeta{ParentToolUseID: "a1", AgentType: "Reviewer"},
	})
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "r1", Name: "Read", Summary: "file.go"},
		Agent:   &types.AgentMeta{ParentToolUseID: "a1", AgentType: "Reviewer"},
	})

	payload := buildPendingBlocks(c.slots["main"].streamState)

	var snap struct {
		Type   string        `json:"type"`
		Blocks []streamBlock `json:"blocks"`
	}
	if err := json.Unmarshal(payload, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	if snap.Type != "streamState" {
		t.Fatalf("type = %q, want 'streamState'", snap.Type)
	}
	if len(snap.Blocks) != 2 {
		t.Fatalf("expected 2 root blocks, got %d", len(snap.Blocks))
	}
	if snap.Blocks[0].Kind != "text" || snap.Blocks[0].Text != "main text" {
		t.Errorf("block[0] = %+v, want text 'main text'", snap.Blocks[0])
	}
	if snap.Blocks[1].Kind != "tool" || snap.Blocks[1].Name != "Agent" {
		t.Errorf("block[1] = %+v, want Agent tool", snap.Blocks[1])
	}

	agent := snap.Blocks[1]
	if len(agent.Children) != 2 {
		t.Fatalf("Agent tool should have 2 children, got %d", len(agent.Children))
	}
	if agent.Children[0].Kind != "text" || agent.Children[0].Text != "sub text" {
		t.Errorf("child[0] = %+v, want text 'sub text'", agent.Children[0])
	}
	if agent.Children[1].Kind != "tool" || agent.Children[1].Name != "Read" {
		t.Errorf("child[1] = %+v, want Read tool", agent.Children[1])
	}

	// Verify JSON uses camelCase (matching client model.ts)
	raw := string(payload)
	if strings.Contains(raw, "is_search") || strings.Contains(raw, "is_read") {
		t.Error("JSON should use camelCase field names, not snake_case")
	}
}

// TestIntegration_LiveEventsAfterSnapshotWithChildren verifies recovery:
// after snapshot restores nested structure, live events correctly find
// and update the right block in the tree.
func TestIntegration_LiveEventsAfterSnapshotWithChildren(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, _ = addMockEngine(t, c, "engineB")

	// Main: Agent tool + sub-agent tool_start (running)
	hubMain.Dispatch(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{
		ID: "a1", Name: "Agent", Summary: "Reviewer",
	}})
	hubMain.Dispatch(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "r1", Name: "Read", Summary: "file.go"},
		Agent:   &types.AgentMeta{ParentToolUseID: "a1", AgentType: "Reviewer"},
	})

	if !waitFor(time.Second, func() bool { return streamStateCount(c, "main") >= 1 }) {
		t.Fatal("should have Agent tool in streamState")
	}

	// Switch away then back
	ws := dialAndStore(t, c)
	c.handleEngineSwitch("engineB")
	readMetadata(t, ws)
	c.handleEngineSwitch("main")
	readMetadata(t, ws)

	// Live event: sub-agent tool_end should find r1 in the nested tree
	hubMain.Dispatch(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "r1", DisplayOutput: "file contents"},
		Agent:      &types.AgentMeta{ParentToolUseID: "a1", AgentType: "Reviewer"},
	})

	// Verify the tree was updated
	c.slotsMu.RLock()
	mainSlot := c.slots["main"]
	c.slotsMu.RUnlock()

	agent := mainSlot.streamState.blocks[0]
	if agent.Kind != "tool" || agent.Name != "Agent" {
		t.Fatalf("expected Agent tool at root, got %+v", agent)
	}

	found := false
	for _, child := range agent.Children {
		if child.ID == "r1" && child.State == "done" && child.DisplayOutput == "file contents" {
			found = true
			break
		}
	}
	if !found {
		t.Error("live tool_end did not update nested tool r1 to done state with output")
	}
}

// readStreamStatePayload reads WS messages until finding a streamState frame.
func readStreamStatePayload(t *testing.T, ws *websocket.Conn) struct {
	Type   string        `json:"type"`
	Blocks []streamBlock `json:"blocks"`
} {
	t.Helper()
	type snapType struct {
		Type   string        `json:"type"`
		Blocks []streamBlock `json:"blocks"`
	}
	for range 20 {
		msg := readWSMessage(t, ws)
		var snap snapType
		if err := json.Unmarshal(msg, &snap); err != nil {
			continue
		}
		if snap.Type == "streamState" {
			return snap
		}
	}
	t.Fatal("did not find streamState frame within 20 frames")
	return snapType{}
}
