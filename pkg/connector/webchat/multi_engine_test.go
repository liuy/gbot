package webchat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// addMockEngine registers a second mock engine on the connector. Returns
// the mock and the hub for injecting events to the engine.
func addMockEngine(t *testing.T, c *WebChatConnector, engineID string) (*mockEngine, *hub.Hub) {
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
	c.writeMu.Lock()
	c.slots[engineID] = slot
	c.writeMu.Unlock()
	t.Cleanup(func() {
		slot.unsubscribe()
	})
	return mock, h
}

// TestMultiEngine_InactiveEngineBuffersOnly verifies that events from a
// non-active engine are buffered but NOT forwarded to the Handle path.
// Only the active engine's events are dispatched through Handle → WS.
func TestMultiEngine_InactiveEngineBuffersOnly(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Send events from engineB (inactive) — should be buffered.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "fromB"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventToolStart})

	// Verify engineB's buffer has the events.
	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	bBuf := len(c.slots["engineB"].streamBuf)
	c.writeMu.Unlock()

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

	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	c.writeMu.Unlock()

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

	c.writeMu.Lock()
	active := c.active
	c.writeMu.Unlock()

	if active != "engineB" {
		t.Errorf("active engine after switch = %q, want %q", active, "engineB")
	}
}

// TestMultiEngine_DualStreamSwitch verifies that when two engines are both
// streaming, switching between them delivers the correct engine's live events
// and replayed buffer on each switch. The WS should never receive events from
// a non-active engine in real-time.
//
// Flow:
//  1. Both main and engineB stream concurrently
//  2. Switch main→engineB: WS gets engineB's replayed buffer + live events
//  3. Switch engineB→main: WS gets main's replayed buffer + live events
//  4. Verify no cross-contamination
func TestMultiEngine_DualStreamSwitch(t *testing.T) {
	// Create connector with an explicit hub for main engine events.
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, hubB := addMockEngine(t, c, "engineB")

	// Both engines buffer some events while main is active.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-1"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-1"})

	// Connect WS manually without draining replay frames.
	// dialAndStore would drain through stats (consuming replay), so we
	// drain metadata frames (connect_status, config, engine_list) then
	// read replay from subsequent frames.
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status
	_ = readWSMessage(t, ws) // drain config
	_ = readWSMessage(t, ws) // drain engine_list

	// main-1 should have been replayed during takeover.
	drainUntilTextDelta(t, ws, "main-1")

	// Send a live event to main — should arrive on WS.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-2"})
	drainUntilTextDelta(t, ws, "main-2")

	// While main is streaming, engineB also buffers events.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-2"})

	// Switch to engineB mid-stream.
	c.handleEngineSwitch("engineB")

	// First frame: connect_status.
	cs := readWSMessage(t, ws)
	var csType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(cs, &csType); err != nil {
		t.Fatalf("connect_status unmarshal: %v", err)
	}
	if csType.Type != "connect_status" {
		t.Errorf("first frame after switch = %q, want connect_status", csType.Type)
	}

	// Drain takeover frames, then read replayed engineB streamBuf (b-1 and b-2).
	drainUntilTextDelta(t, ws, "b-1")
	drainUntilTextDelta(t, ws, "b-2")

	// Now send a live event to engineB — should arrive on WS.
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b-3"})
	drainUntilTextDelta(t, ws, "b-3")

	// While engineB is active, main buffers events silently.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-3"})

	// Switch back to main. Buffer still holds main-1, main-2, main-3 (never
	// cleared — only query_end clears it). All three replay in order.
	c.handleEngineSwitch("main")

	// First frame: connect_status.
	cs2 := readWSMessage(t, ws)
	if err := json.Unmarshal(cs2, &csType); err != nil {
		t.Fatalf("connect_status 2 unmarshal: %v", err)
	}
	if csType.Type != "connect_status" {
		t.Errorf("first frame after switch back = %q, want connect_status", csType.Type)
	}

	// Drain takeover frames, then read full replay: main-1, main-2, main-3.
	drainUntilTextDelta(t, ws, "main-1")
	drainUntilTextDelta(t, ws, "main-2")
	drainUntilTextDelta(t, ws, "main-3")

	// Send live to main — should arrive.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "main-4"})
	drainUntilTextDelta(t, ws, "main-4")
}

// drainUntilTextDelta reads WS messages until finding a text_delta with the
// expected text, skipping connect_status/history/config/engine_list frames.
// Fails if it encounters an unexpected text_delta (cross-contamination from
// the wrong engine) or if the expected delta is not found within 20 frames.
func drainUntilTextDelta(t *testing.T, ws *websocket.Conn, want string) {
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
			t.Fatalf("unexpected text_delta %q while looking for %q (cross-contamination)", env.Event.Text, want)
		}
	}
	t.Fatalf("did not find text_delta %q within 20 frames", want)
}

// TestMultiEngine_SwitchToUnknownID verifies that engine_switch to a
// non-existent engine ID is a no-op (no crash).
func TestMultiEngine_SwitchToUnknownID(t *testing.T) {
	c := newTestConnector(t)
	c.handleEngineSwitch("nonexistent")

	c.writeMu.Lock()
	active := c.active
	c.writeMu.Unlock()

	if active != "main" {
		t.Errorf("active after switch to unknown = %q, want %q", active, "main")
	}
}

// TestMultiEngine_PerEngineBufferIsolation verifies that two engines'
// streamBufs are independent — events to one do not appear in the other's
// buffer.
func TestMultiEngine_PerEngineBufferIsolation(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Send events to both engines.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "mainEvent"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "bEvent"})

	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	bBuf := len(c.slots["engineB"].streamBuf)
	c.writeMu.Unlock()

	if mainBuf == 0 {
		t.Error("main engine buffer should have events")
	}
	if bBuf == 0 {
		t.Error("engineB buffer should have events")
	}
}

// TestMultiEngine_OnStreamDoneClearsOnlyTargetEngine verifies that clearing
// streamBuf for one engine does not affect another engine's buffer.
func TestMultiEngine_OnStreamDoneClearsOnlyTargetEngine(t *testing.T) {
	c := newTestConnector(t)
	_, hubB := addMockEngine(t, c, "engineB")

	// Buffer events on both engines.
	c.Handle(types.QueryEvent{Type: types.EventTextDelta, Text: "main"})
	hubB.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "b"})

	// Clear only engineB.
	c.clearStreamBuf("engineB")

	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	bBuf := len(c.slots["engineB"].streamBuf)
	c.writeMu.Unlock()

	if mainBuf == 0 {
		t.Error("main engine buffer should still have events after clearing engineB")
	}
	if bBuf != 0 {
		t.Errorf("engineB buffer should be empty after clear, got %d entries", bBuf)
	}
}

// TestMultiEngine_ConcurrentEventDelivery verifies that concurrent events
// to two different engines are safe under -race.
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

	// Verify both engines have buffered their events.
	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	bBuf := len(c.slots["engineB"].streamBuf)
	c.writeMu.Unlock()

	if mainBuf < 90 {
		t.Errorf("expected ~100 events in main buffer, got %d", mainBuf)
	}
	if bBuf < 90 {
		t.Errorf("expected ~100 events in engineB buffer, got %d", bBuf)
	}
}

// TestMultiEngine_SwitchDuringActiveStreaming verifies that after
// handleEngineSwitch returns, c.active is the new engine and events
// dispatched to the old engine are buffered (not written to WS).
func TestMultiEngine_SwitchDuringActiveStreaming(t *testing.T) {
	hubMain := hub.NewHub()
	c := newTestConnectorWithHub(t, hubMain)
	_, _ = addMockEngine(t, c, "engineB")

	ws := dialAndStore(t, c)

	// Switch to engineB.
	c.handleEngineSwitch("engineB")

	// c.active must be engineB immediately after switch.
	c.writeMu.Lock()
	active := c.active
	c.writeMu.Unlock()
	if active != "engineB" {
		t.Fatalf("c.active = %q, want engineB", active)
	}

	// Dispatch an event to main (now inactive) — should be buffered, not
	// written to WS.
	hubMain.Dispatch(types.QueryEvent{Type: types.EventTextDelta, Text: "post-switch-main"})

	// Verify main's streamBuf has the event.
	c.writeMu.Lock()
	mainBuf := len(c.slots["main"].streamBuf)
	c.writeMu.Unlock()
	if mainBuf == 0 {
		t.Error("main engine should have buffered the post-switch event")
	}

	// Read WS — should find connect_status (from switch), NOT the post-switch
	// event from main. Drain frames until connect_status or timeout.
	foundConnectStatus := false
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
		if env.Type == "connect_status" {
			foundConnectStatus = true
			break
		}
		if env.Type == "event" && env.Event.Text == "post-switch-main" {
			foundPostSwitchEvent = true
		}
	}
	if !foundConnectStatus {
		t.Error("expected connect_status on WS after switch")
	}
	if foundPostSwitchEvent {
		t.Error("post-switch event from old (inactive) engine leaked to WS — writePayloadTo did not check c.active before writing")
	}
}
