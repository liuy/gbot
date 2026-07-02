package webchat

import (
	"context"
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

// TestHandleMessageInbound_BusyEnqueues verifies that when isBusyFn returns
// true, the message is enqueued via enqueueFn (not dispatched via queryFn).
func TestHandleMessageInbound_BusyEnqueues(t *testing.T) {
	c := newTestConnector(t)
	dispatched := 0
	c.queryFn = func(context.Context, string, string) { dispatched++ }
	c.isBusyFn = func() bool { return true }
	var enqueued *types.QueuedItem
	c.enqueueFn = func(item types.QueuedItem) { enqueued = &item }

	c.handleMessageInbound("queued text")

	if dispatched != 0 {
		t.Errorf("queryFn dispatched %d time(s), want 0", dispatched)
	}
	if enqueued == nil {
		t.Fatal("enqueueFn not called")
	}
	if enqueued.Value != "queued text" {
		t.Errorf("enqueued Value = %q, want \"queued text\"", enqueued.Value)
	}
	if enqueued.Priority != types.PriorityNext {
		t.Errorf("enqueued Priority = %v, want PriorityNext", enqueued.Priority)
	}
	if enqueued.Origin == nil || enqueued.Origin.Kind != types.OriginHuman {
		t.Errorf("enqueued Origin = %+v, want OriginHuman", enqueued.Origin)
	}
}

// TestHandleStop_CallsAbortFn verifies handleStop invokes abortFn.
func TestHandleStop_CallsAbortFn(t *testing.T) {
	c := newTestConnector(t)
	aborted := false
	c.abortFn = func() { aborted = true }

	c.handleStop()

	if !aborted {
		t.Fatal("abortFn not called by handleStop")
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

// TestRegisterChatWS_HistoryRequest verifies the history_request inbound
// handler returns the correct page of older messages via the WS.
func TestRegisterChatWS_HistoryRequest(t *testing.T) {
	c := newTestConnector(t)
	c.messagesFn = func() []types.Message {
		msgs := make([]types.Message, 25)
		for i := range 25 {
			msgs[i] = types.Message{
				ID:        fmt.Sprintf("msg-%d", i),
				Role:      types.RoleAssistant,
				Timestamp: time.Unix(int64(1000+i), 0),
				Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("content-%d", i))},
			}
		}
		return msgs
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	_ = readWSMessage(t, ws) // drain connect_status

	// Initial history page: latest 10 messages
	initData := readWSMessage(t, ws)
	var initEnv struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(initData, &initEnv); err != nil {
		t.Fatalf("unmarshal initial history: %v", err)
	}
	if initEnv.Type != "history" {
		t.Errorf("initial type = %q, want \"history\"", initEnv.Type)
	}
	if len(initEnv.Messages) != 10 {
		t.Errorf("initial messages = %d, want 10", len(initEnv.Messages))
	}
	if !initEnv.HasMore {
		t.Error("initial hasMore = false, want true")
	}
	if initEnv.NextCursor != "10" {
		t.Errorf("initial nextCursor = %q, want \"10\"", initEnv.NextCursor)
	}

	// Request next page
	req, _ := json.Marshal(map[string]any{"type": "history_request", "cursor": "10", "limit": 10})
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write history_request: %v", err)
	}

	p2Data := readWSMessage(t, ws)
	var p2Env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(p2Data, &p2Env); err != nil {
		t.Fatalf("unmarshal page 2: %v", err)
	}
	if p2Env.Type != "history" {
		t.Errorf("page 2 type = %q, want \"history\"", p2Env.Type)
	}
	if len(p2Env.Messages) != 10 {
		t.Errorf("page 2 messages = %d, want 10", len(p2Env.Messages))
	}
	if !p2Env.HasMore {
		t.Error("page 2 hasMore = false, want true")
	}
	if p2Env.NextCursor != "20" {
		t.Errorf("page 2 nextCursor = %q, want \"20\"", p2Env.NextCursor)
	}
}
