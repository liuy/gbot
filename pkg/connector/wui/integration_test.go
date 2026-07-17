package wui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---- Section 1: New() constructor + registerEngine() ----

// TestNew_RegistersAllEngines verifies that New() iterates all engines in
// the manager and creates a slot for each one, plus sets the active engine.
func TestNew_RegistersAllEngines(t *testing.T) {
	mgr := engine.NewEngineManager()

	h1 := hub.NewHub()
	eng1 := engine.New(&engine.Params{EngineID: "main"})
	eng1.SetDispatcher(h1)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng1})

	h2 := hub.NewHub()
	eng2 := engine.New(&engine.Params{EngineID: "e2"})
	eng2.SetDispatcher(h2)
	mgr.Add(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: eng2})

	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	c.slotsMu.RLock()
	mainSlot, mainOk := c.slots["main"]
	e2Slot, e2Ok := c.slots["e2"]
	c.slotsMu.RUnlock()

	if !mainOk || mainSlot == nil {
		t.Fatal("slot for 'main' not created")
	}
	if !e2Ok || e2Slot == nil {
		t.Fatal("slot for 'e2' not created")
	}
	if mainSlot.engineID != "main" {
		t.Errorf("main slot engineID = %q, want 'main'", mainSlot.engineID)
	}
	if e2Slot.engineID != "e2" {
		t.Errorf("e2 slot engineID = %q, want 'e2'", e2Slot.engineID)
	}
	if mainSlot.hub == nil {
		t.Error("main slot hub is nil")
	}
	if e2Slot.hub == nil {
		t.Error("e2 slot hub is nil")
	}

	if c.ActiveID() != "main" {
		t.Errorf("ActiveID = %q, want 'main' (first engine is active)", c.ActiveID())
	}

	// Slots are created but not marked active until a WS connection triggers
	// serveChatWS → switchEngine. This is by design: the active flag gates
	// live event delivery, which only starts after a client connects.
	if e2Slot.active.Load() {
		t.Error("e2 slot should NOT be active (no WS connection)")
	}
}

// TestNew_HubEventsReachWS verifies that events dispatched via an engine's
// hub reach the WS through the engineHubShim subscription set up by New().
func TestNew_HubEventsReachWS(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng})

	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	ws := dialAndStore(t, c)

	h.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "from-hub"})

	msg := readWSMessage(t, ws)
	var env struct {
		Event struct {
			Text string `json:"text"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Text != "from-hub" {
		t.Errorf("event.text = %q, want 'from-hub'", env.Event.Text)
	}
}

// TestRegisterEngine_CreatesSlotAndSubscribes verifies registerEngine
// creates a slot and subscribes the connector to the engine's hub.
func TestRegisterEngine_CreatesSlotAndSubscribes(t *testing.T) {
	mgr := engine.NewEngineManager()
	h1 := hub.NewHub()
	eng1 := engine.New(&engine.Params{EngineID: "main"})
	eng1.SetDispatcher(h1)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng1})

	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	h2 := hub.NewHub()
	eng2 := engine.New(&engine.Params{EngineID: "e2"})
	eng2.SetDispatcher(h2)

	c.RegisterEngine(&engine.EngineViewState{ID: "e2", Name: "agent-2", Engine: eng2})

	c.slotsMu.RLock()
	e2Slot := c.slots["e2"]
	c.slotsMu.RUnlock()

	if e2Slot == nil {
		t.Fatal("slot for 'e2' not created after RegisterEngine")
	}
	if e2Slot.engineID != "e2" {
		t.Errorf("engineID = %q, want 'e2'", e2Slot.engineID)
	}
	if e2Slot.hub == nil {
		t.Error("hub is nil")
	}
	if e2Slot.unsubscribe == nil {
		t.Error("unsubscribe func is nil — not subscribed")
	}
}

// TestRegisterEngine_Idempotent verifies that registering the same engine
// ID twice is a no-op: only one slot exists and no double subscription.
func TestRegisterEngine_Idempotent(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng})

	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	// Register the same engine again.
	c.RegisterEngine(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng})

	c.slotsMu.RLock()
	slotCount := len(c.slots)
	c.slotsMu.RUnlock()

	if slotCount != 1 {
		t.Fatalf("slots count = %d, want 1 (duplicate registerEngine should be no-op)", slotCount)
	}
}

// ---- Section 2: engineAdapter delegates ----

// TestEngineAdapter_Delegates verifies that each engineAdapter method
// correctly forwards to the underlying *engine.Engine. This exercises the
// thin wrapper layer that is otherwise 0% covered (tests use mockEngine).
func TestEngineAdapter_Delegates(t *testing.T) {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		EngineID: "main",
		Model:    "test-model",
	})
	eng.SetDispatcher(h)

	a := &engineAdapter{eng: eng}

	// --- Read-only delegates ---

	if a.EngineID() != "main" {
		t.Errorf("EngineID() = %q, want 'main'", a.EngineID())
	}
	if a.Model() != "test-model" {
		t.Errorf("Model() = %q, want 'test-model'", a.Model())
	}

	// Messages() on a fresh engine returns nil — that's valid.
	a.Messages()

	tools := a.Tools()
	if tools == nil {
		t.Error("Tools() = nil, want empty map")
	}

	if a.IsBusy() {
		t.Error("IsBusy() = true, want false")
	}

	if a.SystemPrompt() != "" {
		t.Errorf("SystemPrompt() = %q, want empty", a.SystemPrompt())
	}

	tl := a.TaskList()
	if tl == nil {
		t.Error("TaskList() = nil, want non-nil")
	}

	if a.ProjectDir() != "" {
		t.Errorf("ProjectDir() = %q, want empty", a.ProjectDir())
	}

	// SessionID on a fresh engine is empty until NewSession is called.
	a.SessionID()

	// --- State-mutating delegates ---

	a.SetMaxTokens(8000)
	a.SetInputModalities([]string{"text", "image"})
	a.UpdateAutoCompactConfig(engine.AutoCompactConfig{ContextWindow: 100000})

	// Abort should not panic.
	a.Abort()
}

// TestEngineAdapter_RemainingDelegates covers the adapter methods that
// require heavier engine setup: Query, EnqueueAttachment, RemoveAttachment,
// ListSessions, SetProvider.
func TestEngineAdapter_RemainingDelegates(t *testing.T) {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		EngineID: "main",
		Model:    "test-model",
	})
	eng.SetDispatcher(h)
	a := &engineAdapter{eng: eng}

	// EnqueueAttachment + RemoveAttachment
	a.EnqueueAttachment(types.QueuedItem{
		Value: "test",
		UUID:  "uuid-1",
		Mode:  types.ItemModePrompt,
	})
	removed := a.RemoveAttachment("uuid-1")
	if !removed {
		t.Error("RemoveAttachment(uuid-1) = false, want true")
	}

	// ListSessions — engine without a store returns an error.
	_, err := a.ListSessions(50)
	if err == nil {
		t.Error("ListSessions on engine without store: error = nil, want non-nil")
	}

	// Query — fires on a background goroutine. Use a canceled context so it
	// exits immediately without calling the LLM.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.Query(ctx, "test message", "")

	// SetProvider — nil is safe (clears the provider).
	a.SetProvider(nil)
	if a.Provider() != nil {
		t.Error("Provider() != nil after SetProvider(nil)")
	}
}

// TestEngineAdapter_RewindTo verifies the adapter discards the result
// and returns only the error from engine.RewindTo.
func TestEngineAdapter_RewindTo(t *testing.T) {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	a := &engineAdapter{eng: eng}

	err := a.RewindTo(0)
	if err == nil {
		return
	}
	// RewindTo(0) on an empty engine may error (e.g. "index out of range")
	// or succeed — both are acceptable. The test verifies no panic.
	if err.Error() == "" {
		t.Error("RewindTo returned an error with empty message")
	}
}

// TestEngineAdapter_SetProviderSetModel verifies SetProvider and SetModel
// round-trip through Provider() and Model().
func TestEngineAdapter_SetProviderSetModel(t *testing.T) {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	a := &engineAdapter{eng: eng}

	if a.Provider() != nil {
		t.Error("Provider() should be nil before SetProvider")
	}

	a.SetModel("new-model")
	if a.Model() != "new-model" {
		t.Errorf("Model() = %q, want 'new-model'", a.Model())
	}
}

// ---- Section 3: handleAsk ----

// TestHandleAsk_NilAskNoOp verifies that handleAsk does not panic or
// produce a WS message when the Ask field is nil.
func TestHandleAsk_NilAskNoOp(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.handleAsk(types.QueryEvent{Type: types.EventAsk, Ask: nil})

	// No WS message should arrive: write something else and verify only
	// that arrives (handleAsk with nil Ask is a silent no-op).
	// We verify by checking that no pending asks were created.
	c.pendingMu.Lock()
	pendingCount := len(c.pendingAsks)
	c.pendingMu.Unlock()

	if pendingCount != 0 {
		t.Errorf("pendingAsks count = %d, want 0 (nil Ask should be no-op)", pendingCount)
	}

	// Verify the WS is still alive by sending a normal event.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "still alive"})
	msg := readWSMessage(t, ws)
	var env struct {
		Event struct {
			Text string `json:"text"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Event.Text != "still alive" {
		t.Errorf("event.text = %q, want 'still alive'", env.Event.Text)
	}
}

// TestHandleAsk_InputKind verifies that AskInput kind maps to "input"
// in the outbound payload.
func TestHandleAsk_InputKind(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.handleAsk(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:   types.AskInput,
			Prompt: "Enter a value:",
		},
	})

	msg := readWSMessage(t, ws)
	var got struct {
		Type   string `json:"type"`
		Kind   string `json:"kind"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(msg, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "ask" {
		t.Errorf("type = %q, want 'ask'", got.Type)
	}
	if got.Kind != "input" {
		t.Errorf("kind = %q, want 'input'", got.Kind)
	}
	if got.Prompt != "Enter a value:" {
		t.Errorf("prompt = %q, want 'Enter a value:'", got.Prompt)
	}
}

// ---- Section 4: onEngineEvent Ask for inactive engine ----

// TestOnEngineEvent_AskDroppedForInactiveEngine verifies that Ask events
// from inactive engines are silently dropped (no WS message, no pending ask).
func TestOnEngineEvent_AskDroppedForInactiveEngine(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// engineB is not the active engine.
	if c.ActiveID() != "main" {
		t.Fatalf("ActiveID = %q, want 'main'", c.ActiveID())
	}

	// Dispatch an Ask through engineB's hub — should be dropped.
	hubB.Dispatch(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:     types.AskPermission,
			ToolName: "Bash",
		},
	})

	// Wait briefly for the hub goroutine to process.
	if !waitFor(500*time.Millisecond, func() bool {
		c.pendingMu.Lock()
		count := len(c.pendingAsks)
		c.pendingMu.Unlock()
		return count == 0
	}) {
		c.pendingMu.Lock()
		count := len(c.pendingAsks)
		c.pendingMu.Unlock()
		t.Errorf("pendingAsks count = %d, want 0 (Ask from inactive engine should be dropped)", count)
	}
}

// ---- Section 5: updateStreamState nil edge cases ----

// TestUpdateStreamState_NilToolParamDelta verifies that a ToolParamDelta
// event with nil PartialInput is a no-op (no panic).
func TestUpdateStreamState_NilToolParamDelta(t *testing.T) {
	var ss streamState
	// First create a tool block to make it realistic.
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	// Now send a param delta with nil PartialInput.
	updateStreamState(&ss, types.QueryEvent{
		Type:         types.EventToolParamDelta,
		PartialInput: nil,
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (tool_start created one block)", len(ss.blocks))
	}
	if ss.blocks[0].State != "running" {
		t.Errorf("block state = %q, want 'running' (unchanged)", ss.blocks[0].State)
	}
}

// TestUpdateStreamState_NilToolResultOnEnd verifies that a ToolEnd event
// with nil ToolResult is a no-op (no panic, tool stays in current state).
func TestUpdateStreamState_NilToolResultOnEnd(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolEnd,
		ToolResult: nil,
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(ss.blocks))
	}
	if ss.blocks[0].State != "running" {
		t.Errorf("state = %q, want 'running' (nil ToolResult should not change state)", ss.blocks[0].State)
	}
}

// TestUpdateStreamState_NilToolResultOnOutputDelta verifies that a
// ToolOutputDelta event with nil ToolResult is a no-op.
func TestUpdateStreamState_NilToolResultOnOutputDelta(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "t1", Name: "Bash"},
	})
	updateStreamState(&ss, types.QueryEvent{
		Type:       types.EventToolOutputDelta,
		ToolResult: nil,
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(ss.blocks))
	}
	if ss.blocks[0].DisplayOutput != "" {
		t.Errorf("DisplayOutput = %q, want empty (nil ToolResult should not set output)", ss.blocks[0].DisplayOutput)
	}
}

// TestUpdateStreamState_NilThinkingOnDelta verifies that a ThinkingDelta
// event with nil Thinking is a no-op.
func TestUpdateStreamState_NilThinkingOnDelta(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingDelta,
		Thinking: nil,
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (thinking_start)", len(ss.blocks))
	}
	if ss.blocks[0].Text != "" {
		t.Errorf("Text = %q, want empty (nil Thinking should not append)", ss.blocks[0].Text)
	}
}

// TestUpdateStreamState_NilThinkingOnEnd verifies that a ThinkingEnd event
// with nil Thinking is a no-op.
func TestUpdateStreamState_NilThinkingOnEnd(t *testing.T) {
	var ss streamState
	updateStreamState(&ss, types.QueryEvent{Type: types.EventThinkingStart})
	updateStreamState(&ss, types.QueryEvent{
		Type:     types.EventThinkingEnd,
		Thinking: nil,
	})

	if len(ss.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(ss.blocks))
	}
	if ss.blocks[0].Active != true {
		t.Errorf("Active = %v, want true (nil Thinking should not finalize)", ss.blocks[0].Active)
	}
	if ss.blocks[0].DurationNs != 0 {
		t.Errorf("DurationNs = %d, want 0 (nil Thinking should not set duration)", ss.blocks[0].DurationNs)
	}
}

// ---- Section 6: Start / Send / SetCreateEngineFn ----

// TestStart_ReturnsNil verifies Start is a no-op returning nil.
func TestStart_ReturnsNil(t *testing.T) {
	c := newTestConnector(t)
	if err := c.Start(context.Background()); err != nil {
		t.Errorf("Start() error = %v, want nil", err)
	}
}

// TestSend_ReturnsNil verifies Send is a no-op returning nil.
func TestSend_ReturnsNil(t *testing.T) {
	c := newTestConnector(t)
	if err := c.Send("user-1", "hello"); err != nil {
		t.Errorf("Send() error = %v, want nil", err)
	}
}

// TestSetCreateEngineFn verifies SetCreateEngineFn stores the closure
// so handleEngineNew can invoke it.
func TestSetCreateEngineFn(t *testing.T) {
	c := newTestConnector(t)
	called := false
	c.SetCreateEngineFn(func(name string) (string, error) {
		called = true
		if name != "test" {
			t.Errorf("name = %q, want 'test'", name)
		}
		return "engine-x", nil
	})

	if c.createEngine == nil {
		t.Fatal("createEngine is nil after SetCreateEngineFn")
	}

	id, err := c.createEngine("test")
	if err != nil {
		t.Fatalf("createEngine error: %v", err)
	}
	if !called {
		t.Error("closure was not called")
	}
	if id != "engine-x" {
		t.Errorf("createEngine returned %q, want 'engine-x'", id)
	}
}

// ---- Section 7: ActiveID / activeEngine edge cases ----

// TestActiveID_EmptyConnector verifies ActiveID returns empty string when
// the active pointer is not set.
func TestActiveID_EmptyPointer(t *testing.T) {
	c := &WUIConnector{}
	// active is nil (zero-value atomic.Pointer)
	if c.ActiveID() != "" {
		t.Errorf("ActiveID() = %q, want empty (nil pointer)", c.ActiveID())
	}
}

// TestActiveEngine_NilWhenNoSlots verifies activeEngine returns nil when
// no slots exist.
func TestActiveEngine_NilWhenNoSlots(t *testing.T) {
	c := &WUIConnector{slots: make(map[string]*engineSlot)}
	emptyID := ""
	c.active.Store(&emptyID)
	if c.activeEngine() != nil {
		t.Error("activeEngine() != nil, want nil when no slots exist")
	}
}

// ---- Section 8: registerEngine nil Engine panics ----

// TestRegisterEngine_NilEnginePanics verifies that registerEngine panics
// when called with a nil Engine (programming error).
func TestRegisterEngine_NilEnginePanics(t *testing.T) {
	mgr := engine.NewEngineManager()
	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("registerEngine with nil Engine should panic")
		}
	}()

	c.registerEngine(&engine.EngineViewState{ID: "ghost", Engine: nil})
}

// ---- Section 9: RegisterChatWS upgrade error path ----

// TestRegisterChatWS_NonWSRequest verifies that a plain HTTP GET (not a WS
// upgrade) to the chat endpoint returns an error. This exercises the
// upgrade-failed branch in RegisterChatWS.
func TestRegisterChatWS_NonWSRequest(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/ws/chat")
	if err != nil {
		t.Fatalf("GET /ws/chat: %v", err)
	}
	defer resp.Body.Close()

	// The WS upgrader rejects plain HTTP with 400 Bad Request.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (non-WS request rejected)", resp.StatusCode, http.StatusBadRequest)
	}
}

// ---- Section 10: handleAskResponse unknown ID ----

// TestHandleAskResponse_UnknownID verifies that responding to an ask that
// doesn't exist is a safe no-op.
func TestHandleAskResponse_UnknownID(t *testing.T) {
	c := newTestConnector(t)
	// Should not panic.
	c.handleAskResponse("nonexistent-id", "allow", "", false)

	c.pendingMu.Lock()
	count := len(c.pendingAsks)
	c.pendingMu.Unlock()

	if count != 0 {
		t.Errorf("pendingAsks = %d, want 0", count)
	}
}

// ---- Section 11: onEngineEvent slot not found ----

// TestOnEngineEvent_UnknownEngineID verifies that events for an engine ID
// with no registered slot are silently dropped (no panic).
func TestOnEngineEvent_UnknownEngineID(t *testing.T) {
	c := newTestConnector(t)

	// Should not panic for unknown engine.
	c.onEngineEvent("nonexistent", hub.Event{Type: types.EventTextDelta, Text: "ghost"})

	// Verify no streamState was created.
	if streamStateCount(c, "nonexistent") != 0 {
		t.Error("streamState created for unknown engine ID")
	}
}

// ---- Section 12: handleMessageInbound nil engine ----

// TestHandleMessageInbound_NilEngine verifies that handleMessageInbound
// is a safe no-op when there is no active engine.
func TestHandleMessageInbound_NilEngine(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan []byte, 16),
		done:        make(chan struct{}),
	}
	emptyID := ""
	c.active.Store(&emptyID)
	go c.wsWriter()
	t.Cleanup(c.Stop)

	// Should not panic.
	c.handleMessageInbound("test message")
}

// ---- Section 13: handleStop nil engine ----

// TestHandleStop_NilEngine verifies handleStop does not panic when there
// is no active engine.
func TestHandleStop_NilEngine(t *testing.T) {
	c := &WUIConnector{
		slots: make(map[string]*engineSlot),
	}
	emptyID := ""
	c.active.Store(&emptyID)

	// Should not panic.
	c.handleStop()
}

// ---- Section 14: switchEngine with nil metadata slot ----

// TestSwitchEngine_FromNilOldSlot verifies switchEngine works correctly
// when there is no old slot (e.g., first-ever switch on a connector with
// an empty active ID).
func TestSwitchEngine_FromNilOldSlot(t *testing.T) {
	mgr := engine.NewEngineManager()
	h := hub.NewHub()
	eng := engine.New(&engine.Params{EngineID: "main"})
	eng.SetDispatcher(h)
	mgr.Add(&engine.EngineViewState{ID: "main", Name: "main", Engine: eng})

	c := New(mgr, nil, nil)
	t.Cleanup(c.Stop)

	// Clear active ID to simulate a state where the old slot lookup fails.
	emptyID := ""
	c.active.Store(&emptyID)

	// Switch to main — should work even with nil old slot.
	c.switchEngine("main")

	if c.ActiveID() != "main" {
		t.Errorf("ActiveID = %q, want 'main'", c.ActiveID())
	}
}

// ---- Section 15: buildEngineList with nil mgr ----

// TestBuildEngineList_NilMgr verifies buildEngineList handles a nil manager
// gracefully (returns empty list).
func TestBuildEngineList_NilMgr(t *testing.T) {
	c := &WUIConnector{mgr: nil}
	activeID := "test"
	c.active.Store(&activeID)

	payload := c.buildEngineList()

	var env struct {
		Type     string           `json:"type"`
		Engines  []engineListItem `json:"engines"`
		ActiveID string           `json:"activeID"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "engine_list" {
		t.Errorf("type = %q, want 'engine_list'", env.Type)
	}
	if len(env.Engines) != 0 {
		t.Errorf("engines count = %d, want 0", len(env.Engines))
	}
	if env.ActiveID != "test" {
		t.Errorf("activeID = %q, want 'test'", env.ActiveID)
	}
}

// ---- Section 16: errors.AsType coverage ----

// TestHandle_QueryEndAbortErrorWithPartialContent verifies that an abort
// error that does NOT auto-rewind (partial content present) still sends
// the interrupt message to the client.
func TestHandle_QueryEndAbortErrorWithPartialContent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	abortErr := &engine.AbortError{Phase: "streaming", Err: ctx.Err()}

	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID:        "u1",
				Role:      types.RoleUser,
				Timestamp: time.Unix(1000, 0),
				Content:   []types.ContentBlock{{Type: types.ContentTypeText, Text: "hello"}},
			},
			{
				ID:        "a1",
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeText, Text: "partial"},
				},
			},
		}
	}
	ws := dialAndStore(t, c)

	c.Handle(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})

	// First message: interrupt text_delta (sent before query_end when
	// auto-rewind is skipped because partial content exists).
	msg1 := readWSMessage(t, ws)
	if !strings.Contains(string(msg1), "interrupted") && !strings.Contains(string(msg1), types.InterruptMessage) {
		t.Errorf("first message should be interrupt text_delta: %s", string(msg1))
	}
	// Second message: query_end event with aborted=true.
	msg2 := readWSMessage(t, ws)
	if !strings.Contains(string(msg2), "query_end") {
		t.Errorf("second message should be query_end: %s", string(msg2))
	}
}

// ---- Section 17: handleModelSwitch error paths ----

// TestHandleModelSwitch_NilEngine verifies handleModelSwitch is a safe
// no-op when there is no active engine.
func TestHandleModelSwitch_NilEngine(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan []byte, 16),
		done:        make(chan struct{}),
		providers:   make(map[string]llm.Provider),
	}
	emptyID := ""
	c.active.Store(&emptyID)
	go c.wsWriter()
	t.Cleanup(c.Stop)

	c.handleModelSwitch("provider", "model")
}

// TestHandleModelSwitch_UnknownProvider verifies handleModelSwitch sends
// an error for an unknown provider.
func TestHandleModelSwitch_UnknownProvider(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	c.handleModelSwitch("nonexistent", "model")

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want 'error'", env.Type)
	}
	if !strings.Contains(env.Message, "nonexistent") {
		t.Errorf("message = %q, want it to contain 'nonexistent'", env.Message)
	}
}

// TestHandleModelSwitch_Busy verifies handleModelSwitch rejects when busy.
func TestHandleModelSwitch_Busy(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }
	ws := dialAndStore(t, c)

	c.handleModelSwitch("p", "m")

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(env.Message, "busy") {
		t.Errorf("message = %q, want it to contain 'busy'", env.Message)
	}
}

// ---- Section 18: handleSessionSwitch error paths ----

// TestHandleSessionSwitch_NilEngine verifies handleSessionSwitch is safe
// when there is no active engine.
func TestHandleSessionSwitch_NilEngine(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		wsCh:        make(chan []byte, 16),
		done:        make(chan struct{}),
	}
	emptyID := ""
	c.active.Store(&emptyID)
	go c.wsWriter()
	t.Cleanup(c.Stop)

	c.handleSessionSwitch("session-1")
}

// TestHandleSessionSwitch_SwitchError verifies handleSessionSwitch sends
// an error when SwitchSession fails.
func TestHandleSessionSwitch_SwitchError(t *testing.T) {
	c := newTestConnector(t)
	c.mock().switchSessionFn = func(sessionID string) error {
		return errors.New("session not found")
	}
	ws := dialAndStore(t, c)

	c.handleSessionSwitch("bad-session")

	msg := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want 'error'", env.Type)
	}
	if env.Message != "session not found" {
		t.Errorf("message = %q, want 'session not found'", env.Message)
	}
}

// TestHandleSessionSwitch_Busy verifies handleSessionSwitch rejects when busy.
func TestHandleSessionSwitch_Busy(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }
	ws := dialAndStore(t, c)

	c.handleSessionSwitch("session-1")

	msg := readWSMessage(t, ws)
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want 'error'", env.Type)
	}
}

// ---- Section 19: buildSessionList edge cases ----

// TestBuildSessionList_NilEngine verifies buildSessionList returns nil
// when there is no active engine.
func TestBuildSessionList_NilEngine(t *testing.T) {
	c := &WUIConnector{slots: make(map[string]*engineSlot)}
	emptyID := ""
	c.active.Store(&emptyID)

	if payload := c.buildSessionList(); payload != nil {
		t.Errorf("buildSessionList() = %v, want nil", payload)
	}
}

// TestBuildSessionList_EmptySessions verifies buildSessionList returns nil
// when the engine returns no sessions.
func TestBuildSessionList_EmptySessions(t *testing.T) {
	c := newTestConnector(t)
	c.mock().listSessionsFn = func(limit int) ([]*short.Session, error) {
		return nil, nil
	}

	if payload := c.buildSessionList(); payload != nil {
		t.Errorf("buildSessionList() = %v, want nil when no sessions", payload)
	}
}
