package wui

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

// TestRegisterChatWS_ConnectStatus asserts the server sends a metadata
// frame immediately after the WS upgrade, and the metadata's connect field
// carries connected=true.
func TestRegisterChatWS_ConnectStatus(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	data := readWSMessage(t, ws)

	var meta struct {
		Type    string          `json:"type"`
		Connect json.RawMessage `json:"connect"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.Type != "metadata" {
		t.Errorf("type = %q, want \"metadata\"", meta.Type)
	}
	var got struct {
		Connected bool `json:"connected"`
	}
	if err := json.Unmarshal(meta.Connect, &got); err != nil {
		t.Fatalf("unmarshal connect: %v", err)
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
// order. Replaces the prior single-UUID contract — wui now sends the
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
	drainInitialFrames(t, ws) // drain the single metadata frame

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

// requestHistory sends a history_request with the given cursor/limit and
// returns the raw envelope JSON for caller-side assertions.
func requestHistory(t *testing.T, ws *websocket.Conn, cursor string, limit int) []byte {
	t.Helper()
	req, err := json.Marshal(map[string]any{"type": "history_request", "cursor": cursor, "limit": limit})
	if err != nil {
		t.Fatalf("marshal history_request: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write history_request: %v", err)
	}
	return readWSMessage(t, ws)
}

// TestBuildHistory_PreCompactInitialPage verifies that when the in-memory
// page reaches the top AND a compact boundary exists, buildHistory sets
// hasMore=true and nextCursor="precompact:0" without emitting an envelope
// compactBoundary flag (the flag belongs only to the final pre-compact page).
func TestBuildHistory_PreCompactInitialPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		// 2 in-memory messages — full page returned, start==0.
		return []types.Message{
			{ID: "in-mem-0", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("hi")}},
			{ID: "in-mem-1", Role: types.RoleAssistant, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock("there")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]types.Message, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		// Only the probe call (limit=0) is expected for the initial page.
		t.Errorf("unexpected preCompactFn(%d, %d) on initial page", delivered, limit)
		return nil, 0, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "", 30)
	var env struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "history" {
		t.Errorf("type = %q, want \"history\"", env.Type)
	}
	if len(env.Messages) != 2 {
		t.Errorf("messages = %d, want 2", len(env.Messages))
	}
	if !env.HasMore {
		t.Error("HasMore = false, want true (boundary exists)")
	}
	if env.NextCursor != "precompact:0" {
		t.Errorf("NextCursor = %q, want \"precompact:0\"", env.NextCursor)
	}
	if env.CompactBoundary {
		t.Error("CompactBoundary = true on initial page, want false (intermediate page)")
	}
}

// TestBuildHistory_PreCompactFinalPage verifies the final pre-compact page
// sets the envelope-level compactBoundary=true with no per-message flag.
func TestBuildHistory_PreCompactFinalPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post-compact")}},
		}
	}
	var preMsgs []types.Message
	for i := range 3 {
		preMsgs = append(preMsgs, types.Message{
			ID:        fmt.Sprintf("pre-%d", i),
			Role:      types.RoleUser,
			Timestamp: time.Unix(int64(900+i), 0),
			Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("pre-%d", i))},
		})
	}
	mock.preCompactFn = func(delivered, limit int) ([]types.Message, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		if delivered != 0 {
			t.Errorf("delivered = %d, want 0", delivered)
		}
		return preMsgs[:], 3, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:0", 30)
	var env struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "history" {
		t.Errorf("type = %q, want \"history\"", env.Type)
	}
	if len(env.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(env.Messages))
	}
	for i, m := range env.Messages {
		if m.ID != fmt.Sprintf("pre-%d", i) {
			t.Errorf("messages[%d].ID = %q, want pre-%d", i, m.ID, i)
		}
	}
	if env.HasMore {
		t.Error("HasMore = true on final page, want false")
	}
	if env.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", env.NextCursor)
	}
	if !env.CompactBoundary {
		t.Error("CompactBoundary = false on final pre-compact page, want true")
	}
	// Per-message compactBoundary must NOT appear on any message.
	for i, raw := range mustUnmarshalMessages(t, data) {
		if _, ok := raw["compactBoundary"]; ok {
			t.Errorf("messages[%d] has per-message compactBoundary, want envelope-only", i)
		}
	}
}

// TestBuildHistory_PreCompactMultiPage verifies that an intermediate
// pre-compact page emits NO envelope flag and advances the cursor.
func TestBuildHistory_PreCompactMultiPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	// Pre-compact total = 50; page 1 returns 30 (limit=30), final returns 20.
	mock.preCompactFn = func(delivered, limit int) ([]types.Message, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		const total = 50
		out := make([]types.Message, 0, limit)
		end := min(delivered+limit, total)
		for i := delivered; i < end; i++ {
			out = append(out, types.Message{
				ID:        fmt.Sprintf("pre-%d", i),
				Role:      types.RoleUser,
				Timestamp: time.Unix(int64(i), 0),
				Content:   []types.ContentBlock{types.NewTextBlock(fmt.Sprintf("pre-%d", i))},
			})
		}
		return out, total, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// Page 1: 30 msgs, hasMore=true, nextCursor="precompact:30", NO envelope flag.
	d1 := requestHistory(t, ws, "precompact:0", 30)
	var env1 struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(d1, &env1); err != nil {
		t.Fatalf("unmarshal page 1: %v", err)
	}
	if len(env1.Messages) != 30 {
		t.Fatalf("page 1 len = %d, want 30", len(env1.Messages))
	}
	if !env1.HasMore {
		t.Error("page 1 hasMore = false, want true")
	}
	if env1.NextCursor != "precompact:30" {
		t.Errorf("page 1 nextCursor = %q, want \"precompact:30\"", env1.NextCursor)
	}
	if env1.CompactBoundary {
		t.Error("page 1 has CompactBoundary, want false (intermediate page)")
	}

	// Page 2: 20 msgs, hasMore=false, nextCursor="", envelope flag = true.
	d2 := requestHistory(t, ws, "precompact:30", 30)
	var env2 struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(d2, &env2); err != nil {
		t.Fatalf("unmarshal page 2: %v", err)
	}
	if len(env2.Messages) != 20 {
		t.Fatalf("page 2 len = %d, want 20", len(env2.Messages))
	}
	if env2.HasMore {
		t.Error("page 2 hasMore = true, want false")
	}
	if env2.NextCursor != "" {
		t.Errorf("page 2 nextCursor = %q, want empty", env2.NextCursor)
	}
	if !env2.CompactBoundary {
		t.Error("page 2 CompactBoundary = false, want true (final page)")
	}
}

// TestBuildHistory_PreCompactNoBoundary verifies current behavior is preserved
// when no compact boundary exists (preCompactFn returns false).
func TestBuildHistory_PreCompactNoBoundary(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem-0", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("only")}},
		}
	}
	// preCompactFn unset → returns (nil, 0, false). Same as production
	// sessions without a boundary.

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "", 30)
	var env struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.HasMore {
		t.Error("HasMore = true, want false (no boundary)")
	}
	if env.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty", env.NextCursor)
	}
	if env.CompactBoundary {
		t.Error("CompactBoundary = true, want false (no boundary)")
	}
}

// TestBuildHistory_PreCompactMalformedCursor verifies that a malformed cursor
// is treated as delivered=0 and returns page data rather than panicking.
func TestBuildHistory_PreCompactMalformedCursor(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]types.Message, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		if delivered != 0 {
			t.Errorf("delivered = %d, want 0 (malformed → 0)", delivered)
		}
		return []types.Message{
			{ID: "pre-0", Role: types.RoleUser, Timestamp: time.Unix(900, 0), Content: []types.ContentBlock{types.NewTextBlock("pre-0")}},
		}, 1, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:abc", 30)
	var env struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "history" {
		t.Errorf("type = %q, want \"history\"", env.Type)
	}
	if len(env.Messages) != 1 {
		t.Fatalf("messages = %d, want 1 (malformed treated as delivered=0)", len(env.Messages))
	}
	if env.HasMore {
		t.Error("HasMore = true, want false")
	}
	if !env.CompactBoundary {
		t.Error("CompactBoundary = false, want true (final page)")
	}
}

// TestBuildHistory_PreCompactEmptyPage verifies that a boundary with zero
// filtered messages still emits compactBoundary=true on the final page.
func TestBuildHistory_PreCompactEmptyPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]types.Message, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		return nil, 0, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:0", 30)
	var env struct {
		Type            string           `json:"type"`
		Messages        []historyChatMsg `json:"messages"`
		NextCursor      string           `json:"nextCursor"`
		HasMore         bool             `json:"hasMore"`
		CompactBoundary bool             `json:"compactBoundary"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Messages) != 0 {
		t.Errorf("messages = %d, want 0 (empty pre-compact)", len(env.Messages))
	}
	if env.HasMore {
		t.Error("HasMore = true, want false")
	}
	if !env.CompactBoundary {
		t.Error("CompactBoundary = false, want true (final page, even empty)")
	}
}

// mustUnmarshalMessages returns the messages array from a history envelope
// as a slice of generic maps so the caller can inspect keys we don't model
// in historyChatMsg (e.g. detect stray compactBoundary fields).
func mustUnmarshalMessages(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var env struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal messages: %v", err)
	}
	return env.Messages
}

// TestBuildHistory_IsBusy_ExcludesQueryMessagesWithToolResult verifies
// buildHistory excludes in-flight query messages. queryStartMsgIdx=3 means
// msgs[:3] is pre-query; msgs[3:] (goal, skill-a, tr, next-a) is produced
// by the query and must be excluded (covered by snapshot + live events).
func TestBuildHistory_IsBusy_ExcludesQueryMessagesWithToolResult(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "p1", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0),
				Content: []types.ContentBlock{types.NewTextBlock("prior-1")}},
			{ID: "pq", Role: types.RoleUser, Timestamp: time.Unix(1001, 0),
				Content: []types.ContentBlock{types.NewTextBlock("prior-q")}},
			{ID: "pa", Role: types.RoleAssistant, Timestamp: time.Unix(1002, 0),
				Content: []types.ContentBlock{types.NewTextBlock("prior-a")}},
			{ID: "goal", Role: types.RoleUser, Timestamp: time.Unix(1003, 0),
				Content: []types.ContentBlock{types.NewTextBlock("/goal")}},
			{ID: "skill-a", Role: types.RoleAssistant, Timestamp: time.Unix(1004, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ID: "tu-skill", Name: "Skill", Input: json.RawMessage(`{}`)},
				}},
			{ID: "tr", Role: types.RoleUser, Timestamp: time.Unix(1005, 0),
				Content: []types.ContentBlock{
					types.NewTextBlock("skill done"),
					{Type: types.ContentTypeToolResult, ToolUseID: "tu-skill", Content: json.RawMessage(`"ok"`)},
				}},
			{ID: "next-a", Role: types.RoleAssistant, Timestamp: time.Unix(1006, 0),
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolUse, ID: "tu-agent", Name: "Agent", Input: json.RawMessage(`{}`)},
				}},
		}
	}
	mock.isBusyFn = func() bool { return true }
	mock.queryStartMsgIdxFn = func() int { return 3 } // index of "/goal"

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// Explicitly request page 1 so we observe exactly what buildHistory returns.
	req, err := json.Marshal(map[string]any{"type": "history_request", "cursor": "", "limit": 30})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	data := readWSMessage(t, ws)
	msgs := mustUnmarshalMessages(t, data)

	ids := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if id, ok := m["id"].(string); ok {
			ids[id] = true
		}
	}
	for _, want := range []string{"p1", "pq", "pa"} {
		if !ids[want] {
			t.Errorf("history missing expected pre-query message %q", want)
		}
	}
	for _, banned := range []string{"goal", "skill-a", "tr", "next-a"} {
		if ids[banned] {
			t.Errorf("history must NOT include current-query message %q (would overlap with snapshot)", banned)
		}
	}
}
