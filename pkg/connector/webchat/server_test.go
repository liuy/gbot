package webchat

import (
	"context"
	"encoding/json"
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

// dialChatWS dials the chat WS endpoint exposed by RegisterChatWS and returns
// the connected client.
func dialChatWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// readWSMessage reads one text message with a timeout.
func readWSMessage(t *testing.T, c *websocket.Conn) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}
	return data
}

// TestRegisterChatWS_ConnectStatus asserts the server sends connect_status
// immediately after the WS upgrade.
func TestRegisterChatWS_ConnectStatus(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	data := readWSMessage(t, ws)

	var got struct {
		Type      string `json:"type"`
		Connected bool   `json:"connected"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "connect_status" {
		t.Errorf("type = %q, want \"connect_status\"", got.Type)
	}
	if !got.Connected {
		t.Error("connected = false, want true")
	}
}

// TestRegisterChatWS_MessageDispatchesQuery verifies that an inbound
// {"type":"message","text":"hi"} triggers the engine Query seam with the
// user's text.
func TestRegisterChatWS_MessageDispatchesQuery(t *testing.T) {
	c := newTestConnector(t)
	var mu sync.Mutex
	var gotText string
	c.queryFn = func(_ context.Context, msg, _ string) {
		mu.Lock()
		gotText = msg
		mu.Unlock()
	}
	c.isBusyFn = func() bool { return false }

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status

	out, _ := json.Marshal(map[string]any{"type": "message", "text": "hello there"})
	if err := ws.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write: %v", err)
	}

	var snapshot string
	if !waitFor(time.Second, func() bool {
		mu.Lock()
		snapshot = gotText
		mu.Unlock()
		return snapshot == "hello there"
	}) {
		t.Fatalf("queryFn received text = %q, want \"hello there\"", snapshot)
	}
}

// TestRegisterChatWS_MessageWhileBusyReturnsError verifies that sending a
// second message while a query is active returns an error envelope instead of
// dispatching another query.
func TestRegisterChatWS_MessageWhileBusyReturnsError(t *testing.T) {
	c := newTestConnector(t)
	dispatched := 0
	c.queryFn = func(context.Context, string, string) { dispatched++ }
	c.isBusyFn = func() bool { return true } // always busy

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status

	out, _ := json.Marshal(map[string]any{"type": "message", "text": "second"})
	if err := ws.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write: %v", err)
	}

	data := readWSMessage(t, ws)
	var env struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "error" {
		t.Errorf("type = %q, want \"error\"", env.Type)
	}
	if !strings.Contains(env.Message, "active") && !strings.Contains(env.Message, "busy") {
		t.Errorf("message = %q, want it to contain \"active\" or \"busy\"", env.Message)
	}
	if dispatched != 0 {
		t.Errorf("queryFn dispatched %d time(s), want 0 (busy gate)", dispatched)
	}
}

// TestRegisterChatWS_AskRoundTrip verifies the full Ask flow: server emits
// the ask outbound, client sends ask_response, engine's ResponseCh unblocks.
func TestRegisterChatWS_AskRoundTrip(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status

	engineCh := make(chan types.AskResponse, 1)
	// Dispatch an Ask through the hub — same path the engine takes.
	h.Dispatch(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   "Bash",
			ResponseCh: engineCh,
		},
	})

	askMsg := readWSMessage(t, ws)
	var ask struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(askMsg, &ask); err != nil {
		t.Fatalf("unmarshal ask: %v", err)
	}
	if ask.Type != "ask" {
		t.Errorf("ask type = %q, want \"ask\"", ask.Type)
	}

	resp, _ := json.Marshal(map[string]any{
		"type":     "ask_response",
		"id":       ask.ID,
		"decision": "allow_always",
	})
	if err := ws.WriteMessage(websocket.TextMessage, resp); err != nil {
		t.Fatalf("write response: %v", err)
	}

	select {
	case got := <-engineCh:
		if got.Decision != types.DecisionAllowAlways {
			t.Errorf("engine received decision = %q, want %q", got.Decision, types.DecisionAllowAlways)
		}
	case <-time.After(time.Second):
		t.Fatal("engine ResponseCh never received the decision")
	}
}

// TestRegisterChatWS_StopCancelsQuery verifies the stop inbound message
// cancels the active query via the stored CancelFunc.
func TestRegisterChatWS_StopCancelsQuery(t *testing.T) {
	c := newTestConnector(t)
	cancelled := make(chan struct{})
	c.queryFn = func(ctx context.Context, _, _ string) {
		<-ctx.Done()
		close(cancelled)
	}
	c.isBusyFn = func() bool { return false }

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status

	msgOut, _ := json.Marshal(map[string]any{"type": "message", "text": "long"})
	if err := ws.WriteMessage(websocket.TextMessage, msgOut); err != nil {
		t.Fatalf("write message: %v", err)
	}
	// Wait for the query to be dispatched and the cancel to be stored.
	if !waitFor(2*time.Second, func() bool { return c.queryCancelActiveTest() }) {
		t.Fatal("queryCancel not stored after message dispatch")
	}

	stopOut, _ := json.Marshal(map[string]any{"type": "stop"})
	if err := ws.WriteMessage(websocket.TextMessage, stopOut); err != nil {
		t.Fatalf("write stop: %v", err)
	}

	select {
	case <-cancelled:
		// pass
	case <-time.After(time.Second):
		t.Fatal("query context not cancelled by stop message")
	}
}
