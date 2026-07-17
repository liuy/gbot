package wui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/types"
)

// TestTakeover_NewConnectionReceivesHistoryThenLiveStream verifies that when a
// new WS connection takes over mid-stream, it receives (a) history inside the
// metadata frame, (b) a streamState snapshot embedded in the same metadata
// frame (containing accumulated text from d0..d4), and (c) all subsequent live
// events. The old connection receives nothing after takeover.
func TestTakeover_NewConnectionReceivesHistoryThenLiveStream(t *testing.T) {
	c := newTestConnector(t)

	// Conn1 connects first, becomes active.
	ws1 := dialAndStore(t, c)

	// Set up engine history AFTER ws1 connects so ws1 doesn't get a history
	// frame (buildHistoryMessage returns nil for empty Messages).
	historyMsgs := []types.Message{
		{ID: "u1", Role: types.RoleUser, Timestamp: time.Unix(1000, 0),
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "prior q"}}},
		{ID: "a1", Role: types.RoleAssistant, Timestamp: time.Unix(1001, 0),
			Content: []types.ContentBlock{{Type: types.ContentTypeText, Text: "prior a"}}},
	}
	c.mock().SetMessagesFn(func() []types.Message { return historyMsgs })

	// Engine streams 5 text_delta events to ws1.
	for i := range 5 {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
	}

	// Assert ws1 received exactly d0..d4 (as 5 individual event frames).
	var got1 []string
	for i := range 5 {
		msg := readWSMessage(t, ws1)
		var env struct {
			Event struct {
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("ws1 delta %d unmarshal: %v", i, err)
		}
		got1 = append(got1, env.Event.Text)
	}
	want1 := []string{"d0", "d1", "d2", "d3", "d4"}
	if !reflect.DeepEqual(got1, want1) {
		t.Fatalf("ws1 deltas = %v, want %v", got1, want1)
	}

	// Conn2 connects → takeover. Drain the metadata frame which contains
	// connect, config, engines, history, stats.
	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")
	meta := readMetadata(t, ws2)

	// Verify history is inside the metadata frame.
	var hist struct {
		Type     string `json:"type"`
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(meta.History, &hist); err != nil {
		t.Fatalf("ws2 history unmarshal: %v", err)
	}
	if hist.Type != "history" {
		t.Fatalf("ws2 history msg type = %q, want \"history\"", hist.Type)
	}
	if len(hist.Messages) != 2 {
		t.Fatalf("ws2 history has %d messages, want 2", len(hist.Messages))
	}

	// Verify snapshot is embedded in the metadata frame — d0..d4 accumulated
	// into a single text block.
	snapBlocks := extractSnapshotFromMetadata(t, meta.Snapshot)
	if len(snapBlocks) != 1 {
		t.Fatalf("snapshot should have 1 text block (accumulated d0..d4), got %d", len(snapBlocks))
	}
	if snapBlocks[0].Kind != "text" || snapBlocks[0].Text != "d0d1d2d3d4" {
		t.Errorf("snapshot text = kind=%s text=%q, want text 'd0d1d2d3d4'", snapBlocks[0].Kind, snapBlocks[0].Text)
	}

	// Now send live events d5, d6, d7. These arrive as plain event frames
	// (snapshot was already in metadata).
	for i := 5; i < 8; i++ {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
	}

	// Read from ws2: 3 event frames (snapshot was in metadata, already drained).
	var got2 []string
	for range 3 {
		msg := readWSMessage(t, ws2)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(msg, &head) != nil {
			continue
		}
		if head.Type == "event" {
			var env struct {
				Event struct {
					Text string `json:"text"`
				} `json:"event"`
			}
			if err := json.Unmarshal(msg, &env); err != nil {
				continue
			}
			if env.Event.Text != "" {
				got2 = append(got2, env.Event.Text)
			}
		}
	}
	want2 := []string{"d5", "d6", "d7"}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("ws2 live deltas = %v, want %v", got2, want2)
	}

	// ws1 (invalidated) must see nothing more.
	_ = ws1.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) // REAL-TIME
	if _, _, err := ws1.ReadMessage(); err == nil {
		t.Error("ws1 received a message after takeover — invalidated conn should be silent")
	}
}

// TestWSWriter_FailureMarksInactive verifies that when wsWriter's
// WriteMessage fails (closed TCP), it clears activeWS so subsequent writes
// are no-ops.
func TestWSWriter_FailureMarksInactive(t *testing.T) {
	c := newTestConnector(t)

	srvWSCh := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvWSCh <- ws
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	clientWS, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientWS.Close() })
	srvWS := <-srvWSCh
	c.activeWS.Store(srvWS)

	if uc := srvWS.UnderlyingConn(); uc != nil {
		_ = uc.Close()
	}

	c.sendWS([]byte(`{"type":"event"}`))

	if !waitFor(time.Second, func() bool { return c.activeWS.Load() == nil }) {
		t.Fatal("activeWS not nil after wsWriter write failure — wsWriter should Store(nil) on error")
	}
}

// TestSendWS_NoActiveWSIsNoOp verifies that sendWS with nil activeWS
// is a silent no-op — the payload goes into wsCh but wsWriter drops it
// because activeWS is nil.
func TestSendWS_NoActiveWSIsNoOp(t *testing.T) {
	c := newTestConnector(t)
	c.sendWS([]byte(`{"type":"event"}`))
	if c.activeStreamBufLen() != 0 {
		t.Fatalf("streamState should be empty (no activeWS, event not buffered), got %d", c.activeStreamBufLen())
	}
}

// TestServeChatWS_StaleReadLoopDoesNotClobberNewTakeover verifies that a stale
// readLoop goroutine (from an older connection exiting after a newer takeover)
// does NOT clear activeWS when its cleanup runs with the old ws.
func TestServeChatWS_StaleReadLoopDoesNotClobberNewTakeover(t *testing.T) {
	c := newTestConnector(t)
	ws1 := dialAndStore(t, c)

	// ws2 connects → takeover → activeWS now points to ws2.
	ws2 := dialAndStore(t, c)
	// serveChatWS runs takeover asynchronously. Wait for ws2 to become
	// active before simulating ws1's exit — otherwise CompareAndSwap(ws1)
	// races with ws2's pending Store and clears the nil left by ws2's takeover.
	waitFor(2*time.Second, func() bool { return c.activeWS.Load() == ws2 })
	if c.activeWS.Load() == nil {
		t.Fatal("activeWS nil after ws2 takeover")
	}

	// Simulate ws1's readLoop finally exiting. Its cleanup must NOT clear
	// activeWS because ws1 != ws2.
	c.activeWS.CompareAndSwap(ws1, nil)
	if c.activeWS.Load() == nil {
		t.Fatal("ws1 stale exit cleared activeWS — should only clear if it equals the exiting ws")
	}

	// ws2 still receives events after the stale cleanup.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "post-stale"})
	msg := readWSMessage(t, ws2)
	var env struct {
		Event struct {
			Text string `json:"text"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("ws2 post-stale unmarshal: %v", err)
	}
	if env.Event.Text != "post-stale" {
		t.Fatalf("ws2 post-stale text = %q, want \"post-stale\"", env.Event.Text)
	}
}

// TestWritePayloadAndClear_ClearsBufferOnDisconnect verifies that
// query_end clears streamState even when activeWS is nil
// (client disconnected during the turn). Without this, a takeover replay
// would re-send events from a turn that's already committed to
// engine.Messages(), causing duplication on the client.
func TestWritePayloadAndClear_ClearsBufferOnDisconnect(t *testing.T) {
	c := newTestConnector(t)

	// Simulate streaming events into buffer (no active WS — disconnected).
	// 2 text_deltas accumulate into a single text block (count=1).
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should have 1 entry (accumulated text), got %d", c.activeStreamBufLen())
	}

	// query_end arrives while disconnected. Resets streamState.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if c.activeStreamBufLen() != 0 {
		t.Fatalf("buffer should be empty after query_end, got %d frames — replay would duplicate committed turn", c.activeStreamBufLen())
	}
}

// TestSubAgentTurnEnd_DoesNotClearBuffer verifies that a sub-agent's
// turn_end does NOT clear streamState. Sub-agent turns are nested
// inside a parent Agent tool call — clearing the buffer on sub-agent
// turn_end would wipe the parent's setup events (turn_start, tool_start)
// that are still needed for takeover replay.
//
// Without this fix, a reconnect during sub-agent execution produces a
// replay buffer with only sub-agent events (no parent setup), so the
// client can't render the sub-agent's output inside the parent tool.
//
// New counting: text deltas accumulate (count=1), each tool_start = 1,
// thinking_start = 1. No text_delta in these events, so count =
// tools(1) + thinking(1) = 2.
func TestSubAgentTurnEnd_DoesNotClearBuffer(t *testing.T) {
	c := newTestConnector(t)

	// Main agent setup events — these MUST survive sub-agent turn_end.
	c.Handle(types.QueryEvent{Type: types.EventTurnStart})
	c.Handle(types.QueryEvent{Type: types.EventThinkingStart, Thinking: &types.ThinkingEvent{}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1 * time.Millisecond}})
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Agent"},
	})

	// Sub-agent turn — has Agent set (parent_tool_use_id).
	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	c.Handle(types.QueryEvent{Type: types.EventTurnStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventThinkingStart, Agent: agent, Thinking: &types.ThinkingEvent{}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: agent, Thinking: &types.ThinkingEvent{Duration: 1 * time.Millisecond}})
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd, Agent: agent})

	// Buffer must still contain state — sub-agent turn_end must NOT have
	// cleared it. Root blocks: [thinking, Agent-tool] = 2.
	if c.activeStreamBufLen() != 2 {
		t.Fatalf("buffer should have 2 entries after sub-agent turn_end (must not clear), got %d — sub-agent turn_end cleared parent setup events", c.activeStreamBufLen())
	}

	// Now main agent commits the assistant response — query_end resets streamState.
	c.slotsMu.RLock()
	slot := c.slots["main"]
	c.slotsMu.RUnlock()
	if slot != nil {
		c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	}
	if c.activeStreamBufLen() != 0 {
		t.Fatalf("buffer should be empty after query_end, got %d", c.activeStreamBufLen())
	}
}

// TestTakeoverConcurrent_NoRaceOnBufferLen verifies that reading
// c.activeStreamBufLen() during takeover doesn't race with concurrent
// Handle appends. The takeover log line reads buffer length outside
// slotsMu — race detector catches this when Handle and serveChatWS
// run concurrently.
//
// Run with: go test -race -run TestTakeoverConcurrent
func TestTakeoverConcurrent_NoRaceOnBufferLen(t *testing.T) {
	c := newTestConnector(t)
	_ = dialAndStore(t, c)

	// Concurrently pump Handle events while repeatedly dialing new WS
	// connections (triggering takeover). If c.activeStreamBufLen() is read
	// outside slotsMu, the race detector fires.
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer goroutine: continuously append to buffer via Handle.
	wg.Go(func() {
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
				i++
			}
		}
	})

	// Takeover goroutine: repeatedly dial new WS to trigger serveChatWS.
	wg.Go(func() {
		mux := http.NewServeMux()
		RegisterChatWS(mux, c)
		srv := httptest.NewServer(mux)
		defer srv.Close()
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/chat"
		for range 20 {
			conn, _, err := websocket.DefaultDialer.Dial(url, nil)
			if err != nil {
				continue
			}
			time.Sleep(2 * time.Millisecond) // REAL-TIME
			_ = conn.Close()
		}
	})

	time.Sleep(100 * time.Millisecond) // REAL-TIME — let race window open
	close(stop)
	wg.Wait()
}

// TestWritePayloadAndClear_ClearsBufferWithActiveWS verifies that
// updateStreamState is called on the active path (events go to both
// streamState and WS), and query_end clears it.
func TestWritePayloadAndClear_ClearsBufferWithActiveWS(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	// When the engine is active and WS is connected, events go to both
	// streamState and WS (design decision 2: both paths call updateStreamState).
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	// query_end clears streamState.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if c.activeStreamBufLen() != 0 {
		t.Fatalf("buffer should be empty after query_end, got %d", c.activeStreamBufLen())
	}

	// Drain: streamState snapshot (with accumulated text), 2 event frames, query_end.
	// The snapshot has 1 text block with "delta1delta2".
	// Then event frames for delta1 and delta2, then query_end.
	for {
		msg := readWSMessage(t, ws)
		var env struct {
			Event struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Event.Type == "query_end" {
			break
		}
	}
}

// TestWritePayloadAndClear_WriteFailureStillClearsBuffer verifies that
// query_end clears the buffer even when the write fails
// (e.g. broken pipe). The turn is committed regardless of delivery.
func TestWritePayloadAndClear_WriteFailureStillClearsBuffer(t *testing.T) {
	c := newTestConnector(t)
	_ = dialAndStore(t, c)

	// Break the WS so writes fail and activeWS becomes nil. After that,
	// events go to streamState (since activeWS is nil).
	srvWS := c.activeWS.Load()
	if srvWS == nil {
		t.Fatal("activeWS nil")
	}
	_ = srvWS.UnderlyingConn().Close()

	// Pump until write fails (kernel buffer drains).
	for range 10 {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "fill"})
	}
	// activeWS should now be nil (wsWriter marked inactive on failure).
	if c.activeWS.Load() != nil {
		// Keep pumping until it clears.
		for i := 0; i < 50 && c.activeWS.Load() != nil; i++ {
			c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "fill"})
			time.Sleep(10 * time.Millisecond) // REAL-TIME
		}
	}

	// Now activeWS is nil, so events go to streamState. Send some events
	// to populate the buffer.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	// 2 text_deltas accumulate into one text block (count=1).
	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should have 1 entry (accumulated text), got %d", c.activeStreamBufLen())
	}

	// query_end clears streamState.
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	if c.activeStreamBufLen() != 0 {
		t.Fatalf("buffer should be empty after query_end even on write failure, got %d", c.activeStreamBufLen())
	}
}

// TestTakeover_DoesNotClearBuffer verifies that takeover replay does NOT
// clear streamState. The buffer must persist across multiple takeovers
// (reconnects) until query_end clears it.
//
// Scenario: sub-agent (Reviewer) running inside Agent tool. User
// reconnects multiple times during sub-agent execution. Each reconnect
// must see the full sub-agent content (including tool results). After
// sub-agent query_end, reconnect must see the tool result summary via
// history replay.
//
// New counting: root blocks = [tool(Agent)] = 1. Sub-agent events nest
// inside Agent's children, so root count stays at 1.
func TestTakeover_DoesNotClearBuffer(t *testing.T) {
	c := newTestConnector(t)

	c.Handle(types.QueryEvent{Type: types.EventTurnStart})
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Agent"},
	})
	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	c.Handle(types.QueryEvent{Type: types.EventTurnStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "sub-agent text", Agent: agent})
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu2", Name: "Grep"},
		Agent:   agent,
	})
	c.Handle(types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{ToolUseID: "tu2", DisplayOutput: "grep result"},
		Agent:      agent,
	})

	// Root has 1 block: Agent tool. Sub-agent events nest in children.
	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should have 1 root block (Agent), got %d", c.activeStreamBufLen())
	}

	// ── First takeover ──
	ws1 := dialAndStore(t, c)
	defer ws1.Close()

	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should still have 1 root block after first takeover, got %d", c.activeStreamBufLen())
	}

	// Send a live event — nests in children.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "more text", Agent: agent})

	// Root block count unchanged (live event nests in children).
	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should still have 1 root block after new event, got %d", c.activeStreamBufLen())
	}

	// ws1 receives event frame (more text). Snapshot was in metadata (drained).
	ws1GotMoreText := false
	msg := readWSMessage(t, ws1)
	if strings.Contains(string(msg), "more text") {
		ws1GotMoreText = true
	}
	if !ws1GotMoreText {
		t.Error("ws1 did not receive 'more text' in event frame")
	}

	// ── Second takeover ──
	_ = ws1.Close()
	time.Sleep(100 * time.Millisecond) // REAL-TIME

	ws2 := dialAndStore(t, c)
	defer ws2.Close()

	if c.activeStreamBufLen() != 1 {
		t.Fatalf("buffer should still have 1 root block after second takeover, got %d — repeated reconnects must not shrink buffer", c.activeStreamBufLen())
	}

	// Send a live event.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "final", Agent: agent})

	// ws2 receives event frame (final). Snapshot (with grep result) was in metadata.
	ws2GotFinal := false
	msg = readWSMessage(t, ws2)
	if strings.Contains(string(msg), "final") {
		ws2GotFinal = true
	}
	if !ws2GotFinal {
		t.Error("ws2 did not receive 'final' in event frame")
	}
}

// TestBufferClearedOnQueryEnd verifies that takeover replay does not
// duplicate events already reflected in engine history.
//
// Full integration test through WS connections — no internal buffer checks,
// no direct query_end calls. Tests observable behavior only.
func TestBufferClearedOnQueryEnd(t *testing.T) {
	c := newTestConnector(t)

	// History: main agent's assistant response (committed to engine.Messages).
	agentResponse := types.Message{
		ID: "a1", Role: types.RoleAssistant, Timestamp: time.Unix(1001, 0),
		Content: []types.ContentBlock{{
			Type: types.ContentTypeText, Text: "committed response",
		}},
	}
	c.mock().messagesFn = func() []types.Message {
		return []types.Message{agentResponse}
	}

	// Mock engine: when Query runs, it dispatches streaming events,
	// then query_end clears streamState. After commit, tool execution
	// dispatches sub-agent events. This mirrors the real engine lifecycle.
	c.mock().queryFn = func(ctx context.Context, userMessage, systemPrompt string) {
		// LLM streaming: main agent response events.
		c.Handle(types.QueryEvent{Type: types.EventTurnStart})
		c.Handle(types.QueryEvent{Type: types.EventThinkingStart})
		c.Handle(types.QueryEvent{Type: types.EventThinkingEnd})
		c.Handle(types.QueryEvent{
			Type:    types.EventToolStart,
			ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Agent"},
		})
		// query_end clears streamState after the LLM turn finishes.
		c.Handle(types.QueryEvent{Type: types.EventQueryEnd})
	}

	// Trigger the full lifecycle: query → streaming → query_end (streamState cleared).
	c.mock().Query(context.Background(), "test", "")

	// Sub-agent events arrive AFTER query_end (tool execution phase).
	// With the new tree model, tu1 was cleared by query_end.
	// These sub-agent events target tu1's children but tu1 doesn't exist,
	// so they are dropped (matching the "unknown parent" behavior).
	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	c.Handle(types.QueryEvent{Type: types.EventTurnStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventThinkingStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: agent})

	// Connect WS2 → takeover → read metadata frame.
	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")
	// The metadata frame contains history (with the committed response).
	meta := readMetadata(t, ws2)
	var hist struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(meta.History, &hist); err != nil {
		t.Fatalf("ws2 history unmarshal: %v", err)
	}
	if hist.Type != "history" {
		t.Fatalf("ws2 history msg type = %q, want \"history\"", hist.Type)
	}

	// Send a live event. Since tu1 was cleared, the event targets a
	// non-existent parent and is dropped. No snapshot is sent.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "sub output", Agent: agent})

	// Read from ws2: just the event frame (no snapshot since blocks is empty).
	var gotEvents []replayEvent
	for range 1 {
		_ = ws2.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // REAL-TIME
		_, payload, err := ws2.ReadMessage()
		if err != nil {
			break
		}
		var env struct {
			Event replayEvent `json:"event"`
		}
		if json.Unmarshal(payload, &env) == nil && env.Event.Type != "" {
			gotEvents = append(gotEvents, env.Event)
		}
	}

	// ws2 should see NO main agent events — they were committed to history
	// and cleared from streamState by query_end.
	for _, ev := range gotEvents {
		if ev.Agent == nil {
			t.Errorf("unexpected main agent event %q after snapshot — leaked into buffer after commit", ev.Type)
		}
	}
}

type replayEvent struct {
	Type  string `json:"type"`
	Agent any    `json:"agent"`
}

// TestTakeover_StatsMessageSentAfterReplay verifies that:
//  1. The metadata frame contains a stats field with accumulated values.
//  2. The connect field (connect_status) does NOT carry stats fields
//     (usage/queryStartMs/toolCount/thinkingMs).
//  3. The stats field carries the accumulated values from slot.queryStats.
func TestTakeover_StatsMessageSentAfterReplay(t *testing.T) {
	c := newTestConnector(t)

	ws1 := dialAndStore(t, c)
	t.Cleanup(func() { _ = ws1.Close() })

	// Accumulate stats on the server.
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 5000, OutputTokens: 300, CacheReadInputTokens: 200,
	}})
	c.Handle(types.QueryEvent{Type: types.EventToolStart, ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"}})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Thinking: &types.ThinkingEvent{Duration: 1500 * time.Millisecond}})

	// Dial ws2 → takeover.
	mux2 := http.NewServeMux()
	RegisterChatWS(mux2, c)
	srv2 := httptest.NewServer(mux2)
	t.Cleanup(srv2.Close)
	ws2 := dialChatWS(t, "ws"+strings.TrimPrefix(srv2.URL, "http")+"/ws/chat")

	// The server sends a single metadata frame.
	meta := readMetadata(t, ws2)

	// 2. connect_status must NOT contain stats fields.
	for _, field := range []string{`"usage"`, `"queryStartMs"`, `"toolCount"`, `"thinkingMs"`} {
		if strings.Contains(string(meta.Connect), field) {
			t.Errorf("connect_status must NOT contain %s; got: %s", field, string(meta.Connect))
		}
	}

	// 3. stats field must carry the accumulated values.
	var stats struct {
		Usage *struct {
			InputTokens          int `json:"input_tokens"`
			OutputTokens         int `json:"output_tokens"`
			CacheReadInputTokens int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		QueryStartMs int64 `json:"queryStartMs"`
		ToolCount    int   `json:"toolCount"`
		ThinkingMs   int64 `json:"thinkingMs"`
	}
	if err := json.Unmarshal(meta.Stats, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v\nraw: %s", err, meta.Stats)
	}
	if stats.Usage == nil {
		t.Fatal("stats.usage is nil")
	}
	if stats.Usage.InputTokens != 5000 {
		t.Errorf("stats.usage.input_tokens = %d, want 5000", stats.Usage.InputTokens)
	}
	if stats.Usage.OutputTokens != 300 {
		t.Errorf("stats.usage.output_tokens = %d, want 300", stats.Usage.OutputTokens)
	}
	if stats.Usage.CacheReadInputTokens != 200 {
		t.Errorf("stats.usage.cache_read_input_tokens = %d, want 200", stats.Usage.CacheReadInputTokens)
	}
	if stats.QueryStartMs == 0 {
		t.Error("stats.queryStartMs = 0, want non-zero (query_start was emitted)")
	}
	if stats.ToolCount != 1 {
		t.Errorf("stats.toolCount = %d, want 1", stats.ToolCount)
	}
	if stats.ThinkingMs != 1500 {
		t.Errorf("stats.thinkingMs = %d, want 1500", stats.ThinkingMs)
	}
}
