package webchat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "test-system-prompt" }

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	out, _ := json.Marshal(map[string]any{"type": "message", "text": "hello there"})
	if err := ws.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write: %v", err)
	}

	var gotUser, gotSysPrompt string
	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		for _, qc := range mock.queryCalls {
			gotUser = qc.userMessage
			gotSysPrompt = qc.systemPrompt
		}
		mock.mu.Unlock()
		return gotUser == "hello there"
	}) {
		t.Fatalf("query received text = %q, want \"hello there\"", gotUser)
	}
	if gotSysPrompt != "test-system-prompt" {
		t.Errorf("systemPrompt = %q, want \"test-system-prompt\"", gotSysPrompt)
	}
}

// TestHandleMessageInbound_BusyEnqueues verifies that when isBusyFn returns
// true, the message is enqueued via enqueueFn (not dispatched via queryFn).
func TestHandleMessageInbound_BusyEnqueues(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return true }

	c.handleMessageInbound("queued text")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.queryCalls) != 0 {
		t.Errorf("query dispatched %d time(s), want 0", len(mock.queryCalls))
	}
	if len(mock.enqueueCalls) != 1 {
		t.Fatalf("enqueueCalls len = %d, want 1", len(mock.enqueueCalls))
	}
	item := mock.enqueueCalls[0]
	if item.Value != "queued text" {
		t.Errorf("enqueued Value = %q, want \"queued text\"", item.Value)
	}
	if item.Priority != types.PriorityNext {
		t.Errorf("enqueued Priority = %v, want PriorityNext", item.Priority)
	}
	if item.Origin == nil || item.Origin.Kind != types.OriginHuman {
		t.Errorf("enqueued Origin = %+v, want OriginHuman", item.Origin)
	}
}

// TestHandleStop_CallsAbortFn verifies handleStop invokes abortFn.
func TestHandleStop_CallsAbortFn(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()

	c.handleStop()

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.abortCount != 1 {
		t.Fatalf("abortCount = %d, want 1", mock.abortCount)
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
	drainInitialFrames(t, ws)

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

// TestRegisterChatWS_CancelQueuedBatch verifies that a cancel_queued inbound
// message with a UUID array invokes engine.RemoveAttachment once per UUID in
// order. Replaces the prior single-UUID contract — webchat now sends the
// full queued UUID list for batch cancellation (TUI popAllQueuedToInput parity).
func TestRegisterChatWS_CancelQueuedBatch(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	out, _ := json.Marshal(map[string]any{"type": "cancel_queued", "uuids": []string{"u-1", "u-2"}})
	if err := ws.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write cancel_queued: %v", err)
	}

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.removeAttachment) >= 2
	}) {
		mock.mu.Lock()
		t.Fatalf("removeAttachment calls = %v, want 2", mock.removeAttachment)
		mock.mu.Unlock()
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.removeAttachment) != 2 {
		t.Fatalf("removeAttachment calls = %v, want exactly 2", mock.removeAttachment)
	}
	if mock.removeAttachment[0] != "u-1" || mock.removeAttachment[1] != "u-2" {
		t.Errorf("removeAttachment = %v, want [u-1 u-2]", mock.removeAttachment)
	}
}

// TestRegisterChatWS_CancelQueuedBatch_FiltersEmpty verifies that empty
// strings inside the uuids array are skipped — defensive belt matching
// Engine.RemoveAttachment's own empty-uuid guard.
func TestRegisterChatWS_CancelQueuedBatch_FiltersEmpty(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	out, _ := json.Marshal(map[string]any{"type": "cancel_queued", "uuids": []string{"u-1", "", "u-2"}})
	if err := ws.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("write cancel_queued: %v", err)
	}

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.removeAttachment) >= 2
	}) {
		mock.mu.Lock()
		t.Fatalf("removeAttachment calls = %v, want 2 (empty filtered)", mock.removeAttachment)
		mock.mu.Unlock()
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.removeAttachment) != 2 {
		t.Fatalf("removeAttachment calls = %v, want exactly 2", mock.removeAttachment)
	}
	if mock.removeAttachment[0] != "u-1" || mock.removeAttachment[1] != "u-2" {
		t.Errorf("removeAttachment = %v, want [u-1 u-2] (empty filtered)", mock.removeAttachment)
	}
}

// TestRegisterChatWS_HistoryRequest verifies the history_request inbound
// handler returns the correct page of older messages via the WS.
func TestRegisterChatWS_HistoryRequest(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		msgs := make([]types.Message, 65)
		for i := range 65 {
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
	_ = readWSMessage(t, ws) // drain config
	_ = readWSMessage(t, ws) // drain engine_list

	// Initial history page: latest 30 messages (now 4th frame after connect_status/config/engine_list)
	initData := readWSMessage(t, ws)
	_ = readWSMessage(t, ws) // drain stats
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
	if len(initEnv.Messages) != 30 {
		t.Errorf("initial messages = %d, want 30", len(initEnv.Messages))
	}
	if !initEnv.HasMore {
		t.Error("initial hasMore = false, want true")
	}
	if initEnv.NextCursor != "30" {
		t.Errorf("initial nextCursor = %q, want \"30\"", initEnv.NextCursor)
	}

	// Request next page
	req, _ := json.Marshal(map[string]any{"type": "history_request", "cursor": "30", "limit": 30})
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
	if len(p2Env.Messages) != 30 {
		t.Errorf("page 2 messages = %d, want 30", len(p2Env.Messages))
	}
	if !p2Env.HasMore {
		t.Error("page 2 hasMore = false, want true")
	}
	if p2Env.NextCursor != "60" {
		t.Errorf("page 2 nextCursor = %q, want \"60\"", p2Env.NextCursor)
	}
}

// TestRegisterChatWS_PingPong verifies that an inbound {"type":"ping"} gets
// an immediate {"type":"pong"} reply.
func TestRegisterChatWS_PingPong(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	// Drain connect_status + config + engine_list + stats sent on connect.
	for range 4 {
		readWSMessage(t, ws)
	}

	ping, _ := json.Marshal(map[string]string{"type": "ping"})
	if err := ws.WriteMessage(websocket.TextMessage, ping); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	data := readWSMessage(t, ws)
	var got struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "pong" {
		t.Errorf("type = %q, want \"pong\"", got.Type)
	}
}
