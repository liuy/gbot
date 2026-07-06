package webchat

import (
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
// new WS connection takes over mid-stream, it receives (a) history from
// engine.Messages(), (b) replay of in-flight streaming deltas from
// currentTurnBuf, and (c) all subsequent live events. The old connection
// receives nothing after takeover.
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
	c.mock().messagesFn = func() []types.Message { return historyMsgs }

	// Engine streams 5 text_delta events to ws1.
	for i := range 5 {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
	}

	// Assert ws1 received exactly d0..d4.
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

	// Conn2 connects → takeover. dialAndStore drains connect_status only.
	ws2 := dialAndStore(t, c)

	// Next frame on ws2 must be history (committed messages).
	histMsg := readWSMessage(t, ws2)
	var hist struct {
		Type     string `json:"type"`
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(histMsg, &hist); err != nil {
		t.Fatalf("ws2 history unmarshal: %v", err)
	}
	if hist.Type != "history" {
		t.Fatalf("ws2 first msg type = %q, want \"history\"", hist.Type)
	}
	if len(hist.Messages) != 2 {
		t.Fatalf("ws2 history has %d messages, want 2", len(hist.Messages))
	}

	// After history, ws2 must receive replay of currentTurnBuf (d0..d4) then
	// live d5..d7.
	for i := 5; i < 8; i++ {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: fmt.Sprintf("d%d", i)})
	}

	var got2 []string
	for i := range 8 {
		msg := readWSMessage(t, ws2)
		var env struct {
			Event struct {
				Text string `json:"text"`
			} `json:"event"`
		}
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("ws2 delta %d unmarshal: %v", i, err)
		}
		got2 = append(got2, env.Event.Text)
	}
	want2 := []string{"d0", "d1", "d2", "d3", "d4", "d5", "d6", "d7"}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("ws2 deltas = %v, want %v", got2, want2)
	}

	// ws1 (invalidated) must see nothing more.
	_ = ws1.SetReadDeadline(time.Now().Add(200 * time.Millisecond)) // REAL-TIME
	if _, _, err := ws1.ReadMessage(); err == nil {
		t.Error("ws1 received a message after takeover — invalidated conn should be silent")
	}
}

// TestWritePayload_FailureMarksInactive verifies the contract that
// writePayload clears activeWS when WriteMessage fails.
//
// We use a standalone httptest.Server (NOT going through serveChatWS, so
// no readLoop runs) to get a real *websocket.Conn. We close the underlying
// TCP directly so WriteMessage fails immediately.
//
// Falsifiability: if the Store(nil) on write-failure is removed, the test fails.
func TestWritePayload_FailureMarksInactive(t *testing.T) {
	c := newTestConnector(t)

	// Spin up a minimal server that just upgrades and signals when ws is ready.
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

	// Dial and wait for server-side ws.
	clientWS, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientWS.Close() })
	srvWS := <-srvWSCh
	c.activeWS.Store(srvWS)

	// Close underlying TCP from server side — WriteMessage will fail.
	if uc := srvWS.UnderlyingConn(); uc != nil {
		_ = uc.Close()
	}

	// First write should fail because TCP is closed.
	err = c.writePayload([]byte(`{"type":"event"}`))
	if err == nil {
		t.Fatal("writePayload returned nil on closed conn — expected error")
	}
	if c.activeWS.Load() != nil {
		t.Fatal("activeWS not nil after writePayload failure — Store(nil) on write error missing")
	}

	// After failure cleared activeWS, subsequent writes are no-ops (nil error).
	if err := c.writePayload([]byte(`{"type":"event"}`)); err != nil {
		t.Fatalf("writePayload after inactive: %v", err)
	}
}

// TestWritePayload_NoActiveWSIsNoOp verifies that writePayload with nil activeWS
// is a silent no-op (returns nil, not an error), so the engine can keep running
// even when no client is connected.
func TestWritePayload_NoActiveWSIsNoOp(t *testing.T) {
	c := newTestConnector(t)
	// activeWS is nil by default (never connected).
	err := c.writePayload([]byte(`{"type":"event"}`))
	if err != nil {
		t.Fatal("writePayload with nil activeWS should return nil (no-op)")
	}
	// Buffer captures event even when activeWS is nil — so events during
	// disconnect are preserved for takeover replay.
	if len(c.currentTurnBuf) != 1 {
		t.Fatalf("currentTurnBuf should have 1 frame (unconditional append), got %d", len(c.currentTurnBuf))
	}
}

// TestServeChatWS_StaleReadLoopDoesNotClobberNewTakeover verifies that a stale
// readLoop goroutine (from an older connection exiting after a newer takeover)
// does NOT clear activeWS when it calls clearActiveIfCurrent with the old ws.
func TestServeChatWS_StaleReadLoopDoesNotClobberNewTakeover(t *testing.T) {
	c := newTestConnector(t)
	ws1 := dialAndStore(t, c)

	// ws2 connects → takeover → activeWS now points to ws2.
	ws2 := dialAndStore(t, c)
	if c.activeWS.Load() == nil {
		t.Fatal("activeWS nil after ws2 takeover")
	}

	// Simulate ws1's readLoop finally exiting. Its cleanup must NOT clear
	// activeWS because ws1 != ws2.
	c.clearActiveIfCurrent(ws1)
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
// turn_end/query_end clears currentTurnBuf even when activeWS is nil
// (client disconnected during the turn). Without this, a takeover replay
// would re-send events from a turn that's already committed to
// engine.Messages(), causing duplication on the client.
//
// Falsifiability: if the unconditional buffer clear in
// writePayloadAndClear is moved back after the ws==nil check, this test
// fails because buffer still has frames after turn_end.
func TestWritePayloadAndClear_ClearsBufferOnDisconnect(t *testing.T) {
	c := newTestConnector(t)

	// Simulate streaming events into buffer (no active WS — disconnected).
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	if len(c.currentTurnBuf) != 2 {
		t.Fatalf("buffer should have 2 frames after 2 deltas, got %d", len(c.currentTurnBuf))
	}

	// turn_end arrives while disconnected. Buffer MUST be cleared.
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd})

	if len(c.currentTurnBuf) != 0 {
		t.Fatalf("buffer should be empty after turn_end (even with nil activeWS), got %d frames — replay would duplicate committed turn", len(c.currentTurnBuf))
	}
}

// TestSubAgentTurnEnd_DoesNotClearBuffer verifies that a sub-agent's
// turn_end does NOT clear currentTurnBuf. Sub-agent turns are nested
// inside a parent Agent tool call — clearing the buffer on sub-agent
// turn_end would wipe the parent's setup events (turn_start, tool_start)
// that are still needed for takeover replay.
//
// Without this fix, a reconnect during sub-agent execution produces a
// replay buffer with only sub-agent events (no parent setup), so the
// client can't render the sub-agent's output inside the parent tool.
func TestSubAgentTurnEnd_DoesNotClearBuffer(t *testing.T) {
	c := newTestConnector(t)

	// Main agent setup events — these MUST survive sub-agent turn_end.
	c.Handle(types.QueryEvent{Type: types.EventTurnStart})
	c.Handle(types.QueryEvent{Type: types.EventThinkingStart})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd})
	c.Handle(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: &types.ToolUseEvent{ID: "tu1", Name: "Agent"},
	})

	// Sub-agent turn — has Agent set (parent_tool_use_id).
	agent := &types.AgentMeta{ParentToolUseID: "tu1", AgentType: "Reviewer"}
	c.Handle(types.QueryEvent{Type: types.EventTurnStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventThinkingStart, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventThinkingEnd, Agent: agent})
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd, Agent: agent})

	// Buffer must still contain ALL events — sub-agent turn_end must
	// NOT have cleared it. Main agent's turn is still in progress.
	// Events: turn_start(main) + thinking_start + thinking_end + tool_start +
	//         turn_start(sub) + thinking_start + thinking_end + turn_end(sub) = 8
	if len(c.currentTurnBuf) != 8 {
		t.Fatalf("buffer should have 8 frames after sub-agent turn_end (must not clear), got %d — sub-agent turn_end cleared parent setup events", len(c.currentTurnBuf))
	}

	// Now main agent's turn_end arrives — THIS should clear the buffer.
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd})
	if len(c.currentTurnBuf) != 0 {
		t.Fatalf("buffer should be empty after main agent turn_end, got %d", len(c.currentTurnBuf))
	}
}

// TestTakeoverConcurrent_NoRaceOnBufferLen verifies that reading
// len(c.currentTurnBuf) during takeover doesn't race with concurrent
// Handle appends. The takeover log line reads buffer length outside
// writeMu — race detector catches this when Handle and serveChatWS
// run concurrently.
//
// Run with: go test -race -run TestTakeoverConcurrent
func TestTakeoverConcurrent_NoRaceOnBufferLen(t *testing.T) {
	c := newTestConnector(t)
	_ = dialAndStore(t, c)

	// Concurrently pump Handle events while repeatedly dialing new WS
	// connections (triggering takeover). If len(c.currentTurnBuf) is read
	// outside writeMu, the race detector fires.
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
// writePayloadAndClear clears currentTurnBuf when activeWS is connected
// and the write succeeds. The turn's events are committed to
// engine.Messages() so replay must not include them.
func TestWritePayloadAndClear_ClearsBufferWithActiveWS(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)

	// Stream some events into buffer.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	if len(c.currentTurnBuf) != 2 {
		t.Fatalf("buffer should have 2 frames, got %d", len(c.currentTurnBuf))
	}

	// turn_end arrives while connected. Buffer MUST be cleared.
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd})

	if len(c.currentTurnBuf) != 0 {
		t.Fatalf("buffer should be empty after turn_end with active WS, got %d", len(c.currentTurnBuf))
	}

	// Drain delta1, delta2, then read turn_end from the WS.
	_ = readWSMessage(t, ws)    // delta1
	_ = readWSMessage(t, ws)    // delta2
	msg := readWSMessage(t, ws) // turn_end
	var env struct {
		Event struct {
			Type string `json:"type"`
		} `json:"event"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal turn_end: %v", err)
	}
	if env.Event.Type != "turn_end" {
		t.Errorf("turn_end event type = %q, want \"turn_end\"", env.Event.Type)
	}
}

// TestWritePayloadAndClear_WriteFailureStillClearsBuffer verifies that
// writePayloadAndClear clears the buffer even when the write fails
// (e.g. broken pipe). The turn is committed regardless of delivery.
func TestWritePayloadAndClear_WriteFailureStillClearsBuffer(t *testing.T) {
	c := newTestConnector(t)
	_ = dialAndStore(t, c)

	// Stream events into buffer.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta1"})
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "delta2"})

	if len(c.currentTurnBuf) != 2 {
		t.Fatalf("buffer should have 2 frames, got %d", len(c.currentTurnBuf))
	}

	// Break the WS so turn_end write fails.
	srvWS := c.activeWS.Load()
	if srvWS == nil {
		t.Fatal("activeWS nil")
	}
	_ = srvWS.UnderlyingConn().Close()

	// Pump until write fails (kernel buffer drains).
	for range 10 {
		c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "fill"})
	}
	// activeWS should now be nil (writePayload marked inactive on failure).
	if c.activeWS.Load() != nil {
		// Keep pumping until it clears.
		for i := 0; i < 50 && c.activeWS.Load() != nil; i++ {
			c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "fill"})
			time.Sleep(10 * time.Millisecond) // REAL-TIME
		}
	}

	// Even though write failed, turn_end must clear the buffer.
	c.Handle(types.QueryEvent{Type: types.EventTurnEnd})

	if len(c.currentTurnBuf) != 0 {
		t.Fatalf("buffer should be empty after turn_end even on write failure, got %d", len(c.currentTurnBuf))
	}
}

// TestTakeover_DoesNotClearBuffer verifies that takeover replay does NOT
// clear currentTurnBuf. The buffer must persist across multiple takeovers
// (reconnects) until turn_end/query_end clears it.
//
// Scenario: sub-agent (Reviewer) running inside Agent tool. User
// reconnects multiple times during sub-agent execution. Each reconnect
// must see the full sub-agent content (including tool results). After
// sub-agent query_end, reconnect must see the tool result summary via
// history replay.
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
		Type:       types.EventToolStart,
		ToolUse:    &types.ToolUseEvent{ID: "tu2", Name: "Grep"},
		Agent:      agent,
	})
	c.Handle(types.QueryEvent{
		Type:         types.EventToolEnd,
		ToolResult:   &types.ToolResultEvent{ToolUseID: "tu2", DisplayOutput: "grep result"},
		Agent:        agent,
	})

	if len(c.currentTurnBuf) != 6 {
		t.Fatalf("buffer should have 6 frames, got %d", len(c.currentTurnBuf))
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/chat"

	// ── First takeover ──
	ws1, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws1 dial: %v", err)
	}
	defer ws1.Close()
	_ = readWSMessage(t, ws1) // connect_status

	if len(c.currentTurnBuf) != 6 {
		t.Fatalf("buffer should still have 6 frames after first takeover, got %d", len(c.currentTurnBuf))
	}

	// Verify ws1 received the grep result during replay.
	ws1GotResult := false
	for i := 0; i < 6; i++ {
		msg := readWSMessage(t, ws1)
		if strings.Contains(string(msg), "grep result") {
			ws1GotResult = true
		}
	}
	if !ws1GotResult {
		t.Error("ws1 did not receive grep result in first replay")
	}

	// More sub-agent events arrive after first takeover.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "more text", Agent: agent})

	if len(c.currentTurnBuf) != 7 {
		t.Fatalf("buffer should have 7 frames after new event, got %d", len(c.currentTurnBuf))
	}

	// ── Second takeover ──
	ws1.Close()
	time.Sleep(100 * time.Millisecond) // REAL-TIME

	ws2, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws2 dial: %v", err)
	}
	defer ws2.Close()
	_ = readWSMessage(t, ws2) // connect_status

	if len(c.currentTurnBuf) != 7 {
		t.Fatalf("buffer should still have 7 frames after second takeover, got %d — repeated reconnects must not shrink buffer", len(c.currentTurnBuf))
	}

	// Verify ws2 received ALL 7 frames including grep result + more text.
	ws2GotResult := false
	ws2GotMoreText := false
	for i := 0; i < 7; i++ {
		msg := readWSMessage(t, ws2)
		if strings.Contains(string(msg), "grep result") {
			ws2GotResult = true
		}
		if strings.Contains(string(msg), "more text") {
			ws2GotMoreText = true
		}
	}
	if !ws2GotResult {
		t.Error("ws2 did not receive grep result in second replay")
	}
	if !ws2GotMoreText {
		t.Error("ws2 did not receive 'more text' in second replay")
	}
}
