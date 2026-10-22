package wui

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// makeTM is a test helper for buildPreCompactHistory tests: it builds a
// *short.TranscriptMessage from a minimal types.Message via the production
// EngineMessagesToStore round-trip so it matches real persisted rows.
func makeTM(id string, role types.Role) *short.TranscriptMessage {
	em := types.Message{
		ID:        id,
		Role:      role,
		Timestamp: time.Unix(900, 0),
		Content:   []types.ContentBlock{types.NewTextBlock(id)},
	}
	ts := short.EngineMessagesToStore([]types.Message{em})
	return ts[0]
}

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
	// Generous deadline: under full-suite parallelism tight windows (2s)
	// starved and flaked; 30s only matters when the server truly fails.
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second)) // REAL-TIME
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

	c.handleMessageInbound("queued text", nil)

	// Enqueue happens inside a goroutine launched by handleMessageInbound.
	// Poll until the recorded call lands.
	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.enqueueCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("enqueueCalls len = %d, want 1", len(mock.enqueueCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.queryCalls) != 0 {
		t.Errorf("query dispatched %d time(s), want 0", len(mock.queryCalls))
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
	// The handler pre-filters via PendingAttachments (it must snapshot each
	// item's content to build the restore payload) — feed the queue.
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{
			{UUID: "u-1", Value: "one", Mode: types.ItemModePrompt},
			{UUID: "u-2", Value: "two", Mode: types.ItemModePrompt},
		}
	}

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
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{
			{UUID: "u-1", Value: "one", Mode: types.ItemModePrompt},
			{UUID: "u-2", Value: "two", Mode: types.ItemModePrompt},
		}
	}

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

// TestBuildHistory_PreCompactTransitionPage verifies that when the in-memory
// page reaches the top AND a compact boundary exists, buildHistory sets
// hasMore=true and nextCursor="precompact:0" so the client can paginate
// into the pre-compact DB region.
func TestBuildHistory_PreCompactTransitionPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		// 2 in-memory messages — full page returned, start==0.
		return []types.Message{
			{ID: "in-mem-0", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("hi")}},
			{ID: "in-mem-1", Role: types.RoleAssistant, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock("there")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		// Only the probe call (limit=0) is expected for the transition page.
		t.Errorf("unexpected preCompactFn(%d, %d) on transition page", delivered, limit)
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
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
}

// TestBuildHistory_PreCompactFinalPage verifies the final pre-compact page
// returns messages with in-page compact_boundary markers (if any).
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
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		if delivered != 0 {
			t.Errorf("delivered = %d, want 0", delivered)
		}
		ts := short.EngineMessagesToStore(preMsgs[:])
		return ts, 3, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:0", 30)
	var env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
	// This page has no compact_boundary markers in the test data.
	for i, raw := range mustUnmarshalMessages(t, data) {
		if _, ok := raw["compactBoundary"]; ok {
			t.Errorf("messages[%d] has compactBoundary, want none on this page", i)
		}
	}
}

// TestBuildHistory_PreCompactMultiPage verifies pagination across multiple
// pre-compact pages.
func TestBuildHistory_PreCompactMultiPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	// Pre-compact total = 50; page 1 returns 30 (limit=30), final returns 20.
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
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
		ts := short.EngineMessagesToStore(out)
		return ts, total, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// Page 1: 30 msgs, hasMore=true, nextCursor="precompact:30".
	d1 := requestHistory(t, ws, "precompact:0", 30)
	var env1 struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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

	// Page 2: 20 msgs, hasMore=false, nextCursor="".
	d2 := requestHistory(t, ws, "precompact:30", 30)
	var env2 struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		if delivered != 0 {
			t.Errorf("delivered = %d, want 0 (malformed → 0)", delivered)
		}
		ts := short.EngineMessagesToStore([]types.Message{
			{ID: "pre-0", Role: types.RoleUser, Timestamp: time.Unix(900, 0), Content: []types.ContentBlock{types.NewTextBlock("pre-0")}},
		})
		return ts, 1, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:abc", 30)
	var env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
}

// TestBuildHistory_PreCompactEmptyPage verifies that a boundary with zero
// filtered messages returns an empty page with hasMore=false.
func TestBuildHistory_PreCompactEmptyPage(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
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
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
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
}

// TestBuildPreCompactHistory_InPageBoundaryMarker verifies that an in-page
// compact_boundary marker (Type=system, Subtype=compact_boundary) returned by
// preCompactMessages serializes as a historyChatMsg with role="system",
// compactBoundary=true, NO body — and that regular messages around it remain
// normal (compactBoundary=false, omitted via omitempty).
func TestBuildPreCompactHistory_InPageBoundaryMarker(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "in-mem", Role: types.RoleAssistant, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("post")}},
		}
	}
	makeBoundary := func(id string) *short.TranscriptMessage {
		return &short.TranscriptMessage{
			UUID:      id,
			Type:      string(types.RoleSystem),
			Subtype:   "compact_boundary",
			Content:   "[]", // empty content block array; marker has no body
			CreatedAt: time.Unix(950, 0),
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		if limit == 0 {
			return nil, 0, true
		}
		return []*short.TranscriptMessage{
			makeTM("pre-0", types.RoleUser),
			makeTM("pre-1", types.RoleUser),
			makeBoundary("boundary-A"),
			makeTM("pre-2", types.RoleUser),
		}, 4, true
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "precompact:0", 30)
	var env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(env.Messages))
	}
	if env.Messages[2].Role != "system" {
		t.Errorf("messages[2].Role = %q, want \"system\" (in-page marker)", env.Messages[2].Role)
	}
	if !env.Messages[2].CompactBoundary {
		t.Errorf("messages[2].CompactBoundary = false, want true (in-page marker)")
	}
	if env.Messages[2].ID != "boundary-A" {
		t.Errorf("messages[2].ID = %q, want \"boundary-A\"", env.Messages[2].ID)
	}
	for i, m := range env.Messages {
		if i == 2 {
			continue
		}
		if m.CompactBoundary {
			t.Errorf("messages[%d].CompactBoundary = true, want false (only markers flag)", i)
		}
	}

	// Raw map cross-check: marker serializes compactBoundary:true, others omit it.
	rawMsgs := mustUnmarshalMessages(t, data)
	markerRaw, ok := rawMsgs[2]["compactBoundary"].(bool)
	if !ok || !markerRaw {
		t.Errorf("raw messages[2] compactBoundary = %v, want true", rawMsgs[2]["compactBoundary"])
	}
	for i, raw := range rawMsgs {
		if i == 2 {
			continue
		}
		if _, present := raw["compactBoundary"]; present {
			t.Errorf("raw messages[%d] has compactBoundary key, want omitted (omitempty on false)", i)
		}
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

	// Prefill replay: queryStartMsgIdx=3 with empty blocks → snapshot absent
	// → a query_start event frame for msgs[3] must follow the metadata.
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, replayData, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("expected replayed query_start after metadata: %v", err)
	}
	var replayEnv struct {
		Type  string `json:"type"`
		Event struct {
			Type    string `json:"type"`
			Message *struct {
				ID string `json:"id"`
			} `json:"message"`
		} `json:"event"`
	}
	if err := json.Unmarshal(replayData, &replayEnv); err != nil {
		t.Fatalf("unmarshal replay: %v", err)
	}
	if replayEnv.Type != "event" || replayEnv.Event.Type != "query_start" ||
		replayEnv.Event.Message == nil || replayEnv.Event.Message.ID != "goal" {
		t.Fatalf("replay frame = %s, want event/query_start for message id \"goal\"", string(replayData))
	}

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
	for _, want := range []string{"p1", "pq", "pa", "goal"} {
		if !ids[want] {
			t.Errorf("history missing expected message %q (user query must be included so it shows in UI)", want)
		}
	}
	for _, banned := range []string{"skill-a", "tr", "next-a"} {
		if ids[banned] {
			t.Errorf("history must NOT include in-flight assistant/tool message %q (would overlap with snapshot)", banned)
		}
	}
}

// TestBuildHistory_CompactBoundaryInlinePosition verifies that a
// compact_boundary system message in the MIDDLE of in-memory messages
// is rendered as an inline historyChatMsg{compactBoundary:true} at its
// correct position — NOT skipped and appended at the end.
func TestBuildHistory_CompactBoundaryInlinePosition(t *testing.T) {
	c := newTestConnector(t)
	mock := c.mock()
	// compact_boundary messages are RoleUser + FlagCompactSummary with
	// compact_boundary metadata embedded in the content text JSON.
	boundaryContent, _ := json.Marshal(map[string]any{
		"type":    "system",
		"subtype": "compact_boundary",
		"content": "Conversation compacted",
	})
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "pre-compact-msg", Role: types.RoleUser, Timestamp: time.Unix(1000, 0), Content: []types.ContentBlock{types.NewTextBlock("before compact")}},
			{ID: "boundary-1", Role: types.RoleUser, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock(string(boundaryContent))}, Flags: types.FlagCompactSummary},
			// Summary message: also FlagCompactSummary but NOT a boundary marker.
			// Must be skipped, NOT rendered as a second divider.
			{ID: "summary-1", Role: types.RoleUser, Timestamp: time.Unix(1001, 0), Content: []types.ContentBlock{types.NewTextBlock("This session is being continued from a previous conversation...")}, Flags: types.FlagCompactSummary},
			{ID: "post-compact-msg", Role: types.RoleAssistant, Timestamp: time.Unix(1002, 0), Content: []types.ContentBlock{types.NewTextBlock("after compact")}},
		}
	}
	mock.preCompactFn = func(delivered, limit int) ([]*short.TranscriptMessage, int, bool) {
		return nil, 0, false
	}

	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	data := requestHistory(t, ws, "", 30)
	var env struct {
		Type       string           `json:"type"`
		Messages   []historyChatMsg `json:"messages"`
		NextCursor string           `json:"nextCursor"`
		HasMore    bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Expect 3 entries: user, compactBoundary, assistant — in order.
	if len(env.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user + boundary + assistant):\n%+v", len(env.Messages), env.Messages)
	}

	// Position 0: user message
	if env.Messages[0].ID != "pre-compact-msg" {
		t.Errorf("messages[0].ID = %q, want pre-compact-msg", env.Messages[0].ID)
	}

	// Position 1: compact boundary (inline, not skipped)
	if !env.Messages[1].CompactBoundary {
		t.Error("messages[1].CompactBoundary = false, want true (boundary must be inline at its correct position)")
	}

	// Position 2: assistant message
	if env.Messages[2].ID != "post-compact-msg" {
		t.Errorf("messages[2].ID = %q, want post-compact-msg", env.Messages[2].ID)
	}
}

// mutCBarrier sends session_list_request and reads frames until its response
// arrives, failing if any frame of a banned type was emitted first. The
// readLoop processes frames serially, so a frame a mutant wrongly emits for
// the preceding message is guaranteed to arrive before this barrier response.
func mutCBarrier(t *testing.T, ws *websocket.Conn, banned ...string) []string {
	t.Helper()
	writeWSText(t, ws, map[string]any{"type": "session_list_request"})
	var saw []string
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		data := readWSMessage(t, ws)
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil {
			continue
		}
		if head.Type == "session_list" {
			for _, b := range banned {
				for _, s := range saw {
					if s == b {
						t.Fatalf("frame %q emitted but must not be (frames seen: %v)", b, saw)
					}
				}
			}
			return saw
		}
		saw = append(saw, head.Type)
	}
	t.Fatal("barrier session_list response never arrived")
	return nil
}

// mutCDialChat dials the connector's /ws/chat endpoint on a fresh server.
func mutCDialChat(t *testing.T, c *WUIConnector) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
}

// mutCEnableSessions makes buildSessionList emit a frame; with the default
// mock the list is empty and session_list_request would stay silent, which
// would break the barrier contract.
func mutCEnableSessions(c *WUIConnector) {
	c.mock().listSessionsFn = func(int) ([]*short.Session, error) {
		return []*short.Session{{SessionID: "s1"}}, nil
	}
}

// mutCPNG is a real 1x1 PNG so fileread's decode/resize path produces a
// genuine image block.
func mutCPNG(t *testing.T) []byte {
	t.Helper()
	// Pre-encoded 1x1 transparent PNG (fixed bytes, no image dependency).
	raw, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	return raw
}

// ---------------------------------------------------------------------------
// server.go — WS endpoint
// ---------------------------------------------------------------------------

func TestMutC_WSUpgradeAllowsCrossOrigin(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// The wui client may be served from any origin (tailscale phones etc.),
	// so CheckOrigin must accept a foreign Origin header.
	d := &websocket.Dialer{}
	header := http.Header{"Origin": []string{"http://evil.example"}}
	ws, resp, err := d.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat", header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("cross-origin dial failed (status %d): %v — CheckOrigin must stay permissive", status, err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	drainInitialFrames(t, ws)
}

// TestMutC_TakeoverCloseFrame pins the takeover close frame: the old client
// receives code 1000 + reason "taken_over" (the React onclose handler
// matches both to suppress auto-reconnect).
func TestMutC_TakeoverCloseFrame(t *testing.T) {
	c := newTestConnector(t)
	ws1 := mutCDialChat(t, c)
	drainInitialFrames(t, ws1)

	ws2 := mutCDialChat(t, c)
	drainInitialFrames(t, ws2)

	_ = ws1.SetReadDeadline(time.Now().Add(5 * time.Second)) // REAL-TIME
	var closeErr *websocket.CloseError
	for closeErr == nil {
		_, _, err := ws1.ReadMessage()
		if err == nil {
			continue
		}
		if !errors.As(err, &closeErr) {
			t.Fatalf("read error = %v, want a close frame", err)
		}
	}
	if closeErr.Code != 1000 {
		t.Errorf("close code = %d, want 1000 (normal closure suppresses reconnect)", closeErr.Code)
	}
	if closeErr.Text != "taken_over" {
		t.Errorf("close text = %q, want taken_over", closeErr.Text)
	}
}

// TestMutC_WSFrameCompressed asserts the negotiated permessage-deflate
// stream actually compresses (compression level >= 1). Level 0 stores bytes
// and inflates the frame past the decompressed size, which this catches.
// The compression-level incrementer (1 -> 2) is not tested on purpose:
// level 1 vs 2 output sizes are not a stable ratio across stdlib versions,
// so any bound is brittle across stdlib versions, exempted.
func TestMutC_WSFrameCompressed(t *testing.T) {
	c := newTestConnector(t)
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // REAL-TIME

	// The key must decode to exactly 16 bytes (RFC 6455), hence 16 raw chars.
	key := base64.StdEncoding.EncodeToString([]byte("mutc-sample-key0"))
	req := "GET /ws/chat HTTP/1.1\r\nHost: " + strings.TrimPrefix(srv.URL, "http://") +
		"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key +
		"\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Extensions: permessage-deflate; client_max_window_bits\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	hdr := make([]byte, 0, 2048)
	buf := make([]byte, 512)
	for !bytes.Contains(hdr, []byte("\r\n\r\n")) {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read handshake: %v (response %q)", err, hdr)
		}
		hdr = append(hdr, buf[:n]...)
	}
	if !bytes.Contains(hdr, []byte("101")) {
		t.Fatalf("handshake = %q, want 101 Switching Protocols", hdr)
	}
	if !bytes.Contains(hdr, []byte("permessage-deflate")) {
		t.Fatalf("handshake = %q, want permessage-deflate negotiated", hdr)
	}

	// Read one data frame (the metadata push is the first and largest).
	b := make([]byte, 2)
	if _, err := io.ReadFull(conn, b); err != nil {
		t.Fatal(err)
	}
	if b[0]&0x40 == 0 {
		t.Fatalf("frame RSV1 unset (0x%02x): server sent an uncompressed frame", b[0])
	}
	length := int64(b[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(conn, ext); err != nil {
			t.Fatal(err)
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(conn, ext); err != nil {
			t.Fatal(err)
		}
		length = int64(binary.BigEndian.Uint64(ext))
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		t.Fatal(err)
	}
	// Permessage-deflate terminates each message with a raw empty block; the
	// appended tail lets flate finalize the stream for measurement.
	zr := flate.NewReader(bytes.NewReader(append(payload, 0x00, 0x00, 0xff, 0xff)))
	plain, _ := io.ReadAll(zr)
	if len(plain) < 512 {
		t.Fatalf("decompressed frame = %d bytes, want the full metadata payload (compressed %d)", len(plain), len(payload))
	}
	if int64(len(payload)) > int64(len(plain))*9/10 {
		t.Fatalf("frame payload %d bytes vs decompressed %d: stored, not deflated (compression level dropped to 0?)", len(payload), len(plain))
	}
	if !bytes.Contains(plain, []byte(`"metadata"`)) && !bytes.Contains(plain, []byte(`"connect"`)) {
		t.Fatalf("decompressed frame is not the metadata payload: %.80q", plain)
	}
}

// TestMutC_ReadLimitBoundary pins the read limit at exactly
// maxAttachmentSize+64KiB: a frame at the limit is accepted, and one past it
// kills the read loop.
func TestMutC_ReadLimitBoundary(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	limit := maxAttachmentSize + 64*1024
	buf := make([]byte, limit+1024)
	for i := range buf {
		buf[i] = byte('A' + i%26)
	}

	// Exactly at the limit: gorilla rejects only strictly larger messages,
	// so the frame is delivered and answered with the no-active-upload
	// error. The 0x00 attachment prefix is part of the wire frame, so the
	// payload is one byte shorter to keep the boundary property.
	if err := ws.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil { // REAL-TIME
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte{0x00}, buf[:limit-1]...)); err != nil {
		t.Fatal(err)
	}
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "no active attachment") {
		t.Fatalf("error message = %q, want the accumulator rejection (frame at the limit must be read)", e.Message)
	}
	// The loop must still be alive afterwards.
	mutCBarrier(t, ws)

	// One KiB past the limit: the read fails and the loop exits, so nothing
	// answers afterwards.
	if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		t.Fatal(err)
	}
	writeWSText(t, ws, map[string]any{"type": "session_list_request"})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	_, _, err := ws.ReadMessage()
	if err == nil {
		t.Fatal("received a response after an over-limit frame, want the read loop dead")
	}
}

// TestMutC_ReadLimitPlus64Rejected probes one KiB granularity below the
// +1024 frame: a limit raised by only 64 bytes must still reject it.
func TestMutC_ReadLimitPlus64Rejected(t *testing.T) {
	c := newTestConnector(t)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	limit := maxAttachmentSize + 64*1024
	buf := make([]byte, limit+64)
	for i := range buf {
		buf[i] = byte('A' + i%26)
	}
	if err := ws.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil { // REAL-TIME
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		t.Fatal(err)
	}
	writeWSText(t, ws, map[string]any{"type": "session_list_request"})
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME // REAL-TIME
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("received a response after a limit+64 frame, want the read loop dead")
	}
}

func TestMutC_ReadLoopInvalidJSONIgnored(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	if err := ws.WriteMessage(websocket.TextMessage, []byte("not json {{")); err != nil {
		t.Fatal(err)
	}
	// A malformed frame must be skipped, not exit the loop.
	mutCBarrier(t, ws)
}

func TestMutC_BinaryWithoutUploadErrors(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte{0x00}, []byte("chunk")...)); err != nil {
		t.Fatal(err)
	}
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "no active attachment") {
		t.Fatalf("error message = %q, want the accumulator rejection", e.Message)
	}
	mutCBarrier(t, ws)
}

func TestMutC_CommitDuringUploadRejectedAndAlive(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mutCEnableSessions(c)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "a.bin", "mime": "application/octet-stream", "size": 4,
	})
	if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte{0x00}, []byte("data")...)); err != nil {
		t.Fatal(err)
	}
	// Committing mid-stream must be rejected, and the rejection must not
	// tear down the connection.
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "too early",
		"attachments": []map[string]any{{"id": "a1", "name": "a.bin", "mime": "application/octet-stream", "size": 4}},
	})
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "upload in progress") {
		t.Fatalf("error message = %q, want the mid-stream rejection", e.Message)
	}
	mutCBarrier(t, ws)
}

func TestMutC_CommitMissingIDRejectedAndAlive(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mutCEnableSessions(c)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "ghost file",
		"attachments": []map[string]any{{"id": "ghost", "name": "g.bin", "mime": "application/octet-stream", "size": 1}},
	})
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "missing attachment uploads: ghost") {
		t.Fatalf("error message = %q, want the missing-id rejection", e.Message)
	}
	mutCBarrier(t, ws)
}

func TestMutC_AttachmentCommitDispatches(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	// queryWithContentFn runs on the inbound dispatch goroutine; mu hands the
	// captured values (and the ctx chain they point into) to the test body.
	var mu sync.Mutex
	var gotBlocks []types.ContentBlock
	var gotCtx context.Context
	mock.queryWithContentFn = func(ctx context.Context, blocks []types.ContentBlock, _ string) {
		mu.Lock()
		gotCtx = ctx
		gotBlocks = blocks
		mu.Unlock()
	}
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	png := mutCPNG(t)
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "img1", "name": "dot.png", "mime": "image/png", "size": len(png),
	})
	if err := ws.WriteMessage(websocket.BinaryMessage, append([]byte{0x00}, png...)); err != nil {
		t.Fatal(err)
	}
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "img1"})
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "see the dot",
		"attachments": []map[string]any{{"id": "img1", "name": "dot.png", "mime": "image/png", "size": len(png)}},
	})

	if !waitFor(5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotBlocks != nil
	}) {
		t.Fatal("QueryWithContent never fired for the committed attachment")
	}
	mu.Lock()
	blocks, qctx := gotBlocks, gotCtx
	mu.Unlock()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + image)", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText || blocks[0].Text != "see the dot" {
		t.Fatalf("text block = %+v, want the message text \"see the dot\"", blocks[0])
	}
	if blocks[1].Type != types.ContentTypeImage {
		t.Fatalf("block[1] type = %v, want image", blocks[1].Type)
	}
	if qctx == nil || engine.SourceFromContext(qctx) != "wui" {
		t.Errorf("query ctx source = %q, want wui", engine.SourceFromContext(qctx))
	}
	// The commit must retire the staged upload: a second message reusing the
	// same id is rejected (the frontend re-uploads instead).
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "again",
		"attachments": []map[string]any{{"id": "img1", "name": "dot.png", "mime": "image/png", "size": len(png)}},
	})
	frame := readUntilType(t, ws, "error")
	e2 := struct {
		Message string `json:"message"`
	}{}
	if err := json.Unmarshal(frame, &e2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e2.Message, "missing attachment uploads: img1") {
		t.Fatalf("second commit error = %q, want the missing-id rejection (staged uploads are one-shot)", e2.Message)
	}
	// The commit must not end the loop: later requests still answer.
	mutCBarrier(t, ws)
}

func TestMutC_StopCallsAbort(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "stop"})
	if !waitFor(5*time.Second, func() bool {
		c.mock().mu.RLock()
		defer c.mock().mu.RUnlock()
		return c.mock().abortCount == 1
	}) {
		c.mock().mu.RLock()
		n := c.mock().abortCount
		c.mock().mu.RUnlock()
		t.Fatalf("abortCount = %d, want 1 for a stop frame", n)
	}
}

func TestMutC_CancelQueuedMalformedSkipped(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return true }
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{{UUID: "u-orig", Value: "queued text", Mode: types.ItemModePrompt}}
	}
	mock.removeAttachmentFn = func(id string) bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		mock.removeAttachment = append(mock.removeAttachment, id)
		return true
	}
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "queued text"})
	readUntilType(t, ws, "queued")

	// Malformed cancel (uuids not an array): unmarshal fails and nothing
	// must be popped — no cancel_result frame and no removals.
	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": "not-an-array"})
	mutCBarrier(t, ws, "cancel_result")
	mock.mu.RLock()
	removed := len(mock.removeAttachment)
	mock.mu.RUnlock()
	if removed != 0 {
		t.Fatalf("removeAttachment calls = %d, want 0 for a malformed cancel frame", removed)
	}

	// The queue survived: a well-formed cancel still pops the item.
	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": []string{}})
	frame := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(frame, &cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Removed) != 1 || cr.Removed[0] != "u-orig" {
		t.Fatalf("removed = %v, want [u-orig] (queue must survive the malformed frame)", cr.Removed)
	}
}

// TestMutC_CancelEpochViaFrameBumps drives the mid-parse cancel through the
// real cancel_queued frame: the epoch bump there must make the in-flight
// assemble goroutine restore instead of enqueue. The message carries a
// document so the restore must also re-stage the attachment chip.
func TestMutC_CancelEpochViaFrameBumps(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return true }
	mock.pendingAttachmentsFn = func() []types.QueuedItem { return nil }

	doc := filepath.Join(t.TempDir(), "mid.txt")
	mutCWriteFile(t, doc, []byte("mid parse document body"))

	entered := make(chan struct{})
	release := make(chan struct{})
	prevGate := assembleGate
	assembleGate = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { assembleGate = prevGate })

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "mid parse",
		"content": []map[string]any{
			{"type": "document", "source": map[string]any{"type": "file", "path": doc, "name": "mid.txt", "mime": "text/plain", "size": 21}},
		},
	})
	<-entered
	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": []string{}})
	readUntilType(t, ws, "cancel_result") // the frame-driven bump itself
	close(release)

	late := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Restored []struct {
			Text        string                 `json:"text"`
			Attachments []queuedAttachmentJSON `json:"attachments"`
		} `json:"restored"`
	}
	if err := json.Unmarshal(late, &cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Restored) != 1 || cr.Restored[0].Text != "mid parse" {
		t.Fatalf("late restore = %+v, want text [mid parse] (cancel frame bumps the epoch)", cr.Restored)
	}
	if len(cr.Restored[0].Attachments) != 1 {
		t.Fatalf("late restore attachments = %+v, want the document chip re-staged", cr.Restored[0].Attachments)
	}
	mock.mu.RLock()
	enqueued := len(mock.enqueueCalls)
	mock.mu.RUnlock()
	if enqueued != 0 {
		t.Fatalf("enqueueCalls = %d, want 0 (bumped epoch must restore, not enqueue)", enqueued)
	}
}

// TestMutC_CancelQueuedSelective pins the cancel filters with two rounds:
// (a) cancelling [job, meta, wanted] must remove only the wanted prompt —
// the mode/meta filter must reject the wanted-but-wrong-kind ids; (b) a
// single-uuid cancel must still filter, not pop everything.
func TestMutC_CancelQueuedSelective(t *testing.T) {
	pending := func() []types.QueuedItem {
		return []types.QueuedItem{
			{UUID: "u-job", Value: "job", Mode: types.ItemModeJob},
			{UUID: "u-skip", Value: "other prompt", Mode: types.ItemModePrompt},
			{UUID: "u-meta", Value: "meta", Mode: types.ItemModePrompt, IsMeta: true},
			{UUID: "u-want", Value: "wanted prompt", Mode: types.ItemModePrompt},
		}
	}
	cancel := func(t *testing.T, uuids []string) []string {
		t.Helper()
		c := newTestConnector(t)
		ws := mutCDialChat(t, c)
		drainInitialFrames(t, ws)
		mock := c.mock()
		mock.pendingAttachmentsFn = pending

		writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": uuids})
		frame := readUntilType(t, ws, "cancel_result")
		var cr struct {
			Removed []string `json:"removed"`
		}
		if err := json.Unmarshal(frame, &cr); err != nil {
			t.Fatal(err)
		}
		mock.mu.RLock()
		calls := append([]string(nil), mock.removeAttachment...)
		mock.mu.RUnlock()
		if len(calls) != len(cr.Removed) {
			t.Fatalf("removeAttachment calls %v != removed %v", calls, cr.Removed)
		}
		for i := range calls {
			if calls[i] != cr.Removed[i] {
				t.Fatalf("removeAttachment calls %v != removed %v", calls, cr.Removed)
			}
		}
		return cr.Removed
	}

	removed := cancel(t, []string{"u-job", "u-meta", "u-want"})
	if len(removed) != 1 || removed[0] != "u-want" {
		t.Fatalf("round a removed = %v, want exactly [u-want] (job/meta ids must be filtered)", removed)
	}
	removed = cancel(t, []string{"u-want"})
	if len(removed) != 1 || removed[0] != "u-want" {
		t.Fatalf("round b removed = %v, want exactly [u-want] (a one-id list still filters)", removed)
	}
}

func TestMutC_SessionSwitchGuards(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	// Malformed payload and empty sessionID must both be skipped: neither
	// may reach SwitchSession.
	writeWSText(t, ws, map[string]any{"type": "session_switch", "sessionID": 42})
	writeWSText(t, ws, map[string]any{"type": "session_switch", "sessionID": ""})
	mutCBarrier(t, ws, "error")
	mock := c.mock()
	mock.mu.RLock()
	switches := len(mock.switchSessionCalls)
	mock.mu.RUnlock()
	if switches != 0 {
		t.Fatalf("switchSessionCalls = %v, want none for malformed/empty frames", mock.switchSessionCalls)
	}
}

func TestMutC_EngineSwitchGuards(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	// Both malformed and empty engineID frames must be dropped before
	// switchEngine, which would emit an "unknown engine" error frame.
	writeWSText(t, ws, map[string]any{"type": "engine_switch", "engineID": 42})
	writeWSText(t, ws, map[string]any{"type": "engine_switch", "engineID": ""})
	mutCBarrier(t, ws, "error")
}

func TestMutC_EngineNewGuards(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	// Valid frame: createEngine is nil in the test connector, so the guard
	// answers with the not-configured error.
	writeWSText(t, ws, map[string]any{"type": "engine_new", "name": "scratch"})
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "engine creation not configured") {
		t.Fatalf("error message = %q, want the not-configured rejection", e.Message)
	}

	// Malformed frame: nothing may be dispatched.
	writeWSText(t, ws, map[string]any{"type": "engine_new", "name": 42})
	mutCBarrier(t, ws, "error")
}

func TestMutC_ContextRequestFrame(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "context_request"})
	frame := readUntilType(t, ws, "context_breakdown")
	var cb struct {
		TotalTokens int `json:"totalTokens"`
	}
	if err := json.Unmarshal(frame, &cb); err != nil {
		t.Fatal(err)
	}
	if cb.TotalTokens != 0 {
		t.Errorf("totalTokens = %d, want 0 from the nil breakdown", cb.TotalTokens)
	}
}

// TestMutC_CompactRequestDoesNotBlockReadLoop pins that compact handling is
// asynchronous: a slow ManualCompact must not stall subsequent frames.
func TestMutC_CompactRequestDoesNotBlockReadLoop(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	blockCompact := make(chan struct{})
	mock.manualCompactFn = func(context.Context, types.Message, string) (*short.CompactResult, error) {
		<-blockCompact
		return nil, nil
	}
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "compact_request"})
	if !waitFor(5*time.Second, func() bool {
		mock.mu.RLock()
		defer mock.mu.RUnlock()
		return mock.manualCompactCalls == 1
	}) {
		t.Fatal("ManualCompact never started")
	}
	// While compact is parked the read loop must still answer.
	writeWSText(t, ws, map[string]any{"type": "session_list_request"})
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second)) // REAL-TIME
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("no answer while compact parked: %v (readLoop blocked?)", err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "session_list" {
			break
		}
	}
	close(blockCompact)
}

// mutCZhipuConfig wires a zhipu provider config at url into the connector so
// buildQuota's fetch path runs against a controllable upstream.
func mutCZhipuConfig(url string) map[string]*config.Provider {
	return map[string]*config.Provider{
		"zhipu": {Name: "zhipu", URL: url, Keys: []string{"k"}},
	}
}

// TestMutC_QuotaRequestFrame pins the quota_request frame branch: the read
// loop must answer it with a quota_result frame carrying the fetched entry.
func TestMutC_QuotaRequestFrame(t *testing.T) {
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":40,"nextResetTime":4102444800000}]},"msg":""}`))
	})
	c := newTestConnector(t)
	c.providerConfigs = mutCZhipuConfig(up.URL)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "quota_request"})
	frame := readUntilType(t, ws, "quota_result")
	var qr struct {
		Entries []struct {
			Provider string `json:"provider"`
			Quota    string `json:"quota"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(frame, &qr); err != nil {
		t.Fatal(err)
	}
	if len(qr.Entries) != 1 {
		t.Fatalf("entries = %+v, want exactly the zhipu entry", qr.Entries)
	}
	if qr.Entries[0].Provider != "zhipu" {
		t.Errorf("provider = %q, want zhipu", qr.Entries[0].Provider)
	}
	// Fixture reports 40% used, so the display string must lead with the
	// remaining 60%; the reset suffix is wall-clock derived and unpinned.
	if !strings.HasPrefix(qr.Entries[0].Quota, "60%/") {
		t.Errorf("quota = %q, want the 60%% remaining prefix", qr.Entries[0].Quota)
	}
}

// TestMutC_QuotaRequestDoesNotBlockReadLoop pins that quota fetching is
// asynchronous: a slow upstream quota response must not stall the read loop.
func TestMutC_QuotaRequestDoesNotBlockReadLoop(t *testing.T) {
	blockQuota := make(chan struct{})
	up, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-blockQuota
		_, _ = w.Write([]byte(`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":40,"nextResetTime":4102444800000}]},"msg":""}`))
	})
	c := newTestConnector(t)
	c.providerConfigs = mutCZhipuConfig(up.URL)
	mutCEnableSessions(c)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "quota_request"})
	// While the quota fetch is parked the read loop must still answer.
	writeWSText(t, ws, map[string]any{"type": "session_list_request"})
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second)) // REAL-TIME
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("no answer while quota fetch parked: %v (readLoop blocked?)", err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "session_list" {
			break
		}
	}
	close(blockQuota)
}

func TestMutC_NilEngineSlotNoPanic(t *testing.T) {
	c := &WUIConnector{
		slots:  make(map[string]*engineSlot),
		done:   make(chan struct{}),
		thumbs: newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	active := "main"
	c.active.Store(&active)
	c.slots["main"] = &engineSlot{engineID: "main"} // engine deliberately nil
	t.Cleanup(c.Stop)

	// A slot whose engine is not (yet) wired must be ignored quietly.
	c.handleMessageInbound("hello", nil)

	// A connector with no slot at all is the same contract.
	noSlots := &WUIConnector{
		slots:  make(map[string]*engineSlot),
		done:   make(chan struct{}),
		thumbs: newThumbCache(),
	}
	noSlots.outQ.Store(newOutQueue(outQueueCapacity))
	t.Cleanup(noSlots.Stop)
	noSlots.handleMessageInbound("hello", nil)
}

// TestMutC_CancelQueuedWithoutSlotsSurvives pins the cancel-path nil guard:
// a connector with no slots at all answers cancel_queued normally.
func TestMutC_CancelQueuedWithoutSlotsSurvives(t *testing.T) {
	c := &WUIConnector{
		slots:       make(map[string]*engineSlot),
		pendingAsks: make(map[string]*types.AskEvent),
		done:        make(chan struct{}),
		thumbs:      newThumbCache(),
	}
	c.outQ.Store(newOutQueue(outQueueCapacity))
	go c.wsWriter()
	t.Cleanup(c.Stop)

	ws := mutCDialChat(t, c)
	// This connector has no engine, so the dial produces an "unknown engine"
	// error frame instead of metadata; skip frames until the loop is live.
	writeWSText(t, ws, map[string]any{"type": "cancel_queued", "uuids": []string{}})
	frame := readUntilType(t, ws, "cancel_result")
	var cr struct {
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(frame, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Removed != nil {
		t.Errorf("removed = %v, want nil (nothing pending without slots)", cr.Removed)
	}
}

func TestMutC_HandleStopLogsAndAbort(t *testing.T) {
	t.Run("active engine", func(t *testing.T) {
		c := newTestConnector(t)
		mock := c.mock()
		rec := mutCCaptureSlog(t)

		c.handleStop()

		mock.mu.RLock()
		aborts := mock.abortCount
		mock.mu.RUnlock()
		if aborts != 1 {
			t.Fatalf("abortCount = %d, want 1", aborts)
		}
		for _, want := range []string{
			"wui:handle_stop_enter", "wui:handle_stop_calling_abort", "wui:handle_stop_abort_returned",
		} {
			if !rec.hasMessage(want) {
				t.Errorf("log %q missing (messages: %v) — the stop path is observable through its trace", want, rec.messages())
			}
		}
	})
	t.Run("no engine", func(t *testing.T) {
		c := &WUIConnector{
			slots:  make(map[string]*engineSlot),
			done:   make(chan struct{}),
			thumbs: newThumbCache(),
		}
		c.outQ.Store(newOutQueue(outQueueCapacity))
		t.Cleanup(c.Stop)
		rec := mutCCaptureSlog(t)

		c.handleStop()

		if !rec.hasMessage("wui:handle_stop_no_active_engine") {
			t.Fatalf("messages = %v, want the no-active-engine branch to log", rec.messages())
		}
	})
}

func TestMutC_DisconnectLogs(t *testing.T) {
	c := newTestConnector(t)
	rec := mutCCaptureSlog(t)
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	if err := ws.Close(); err != nil && !errors.Is(err, websocket.ErrCloseSent) {
		t.Logf("client close: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		return rec.hasMessage("wui:readLoop exit") && rec.hasMessage("wui:disconnect")
	}) {
		t.Fatalf("disconnect trace missing (messages: %v)", rec.messages())
	}
}

func TestMutC_CleanupConnClearsActiveWS(t *testing.T) {
	c := newTestConnector(t)
	fake := &websocket.Conn{}
	c.activeWS.Store(fake)

	c.cleanupConn()

	if got := c.activeWS.Load(); got != nil {
		t.Fatalf("activeWS = %v after cleanup, want nil (a stale pointer keeps the writer writing to a dead conn)", got)
	}
}

func TestMutC_AbortAsksClearsMapDeliversAndLogs(t *testing.T) {
	c := newTestConnector(t)
	deliverable := make(chan types.AskResponse, 1)
	stuck := make(chan types.AskResponse) // unbuffered, nobody reading
	c.pendingAsks["d1"] = &types.AskEvent{ResponseCh: deliverable}
	c.pendingAsks["s1"] = &types.AskEvent{ResponseCh: stuck}
	rec := mutCCaptureSlog(t)

	mutCDeadline(t, 5*time.Second, "abortPendingAsksOnDisconnect", c.abortPendingAsksOnDisconnect)

	if len(c.pendingAsks) != 0 {
		t.Fatalf("pendingAsks = %d entries after disconnect abort, want 0", len(c.pendingAsks))
	}
	select {
	case resp := <-deliverable:
		if !resp.Aborted {
			t.Errorf("delivered resp = %+v, want Aborted", resp)
		}
	default:
		t.Error("abort response never delivered to the readable channel")
	}
	if !rec.hasMessage("wui: asks aborted on disconnect") {
		t.Errorf("messages = %v, want the debug trace", rec.messages())
	}
}

func TestMutC_AskResponseNonblockingWhenFull(t *testing.T) {
	c := newTestConnector(t)
	full := make(chan types.AskResponse, 1)
	full <- types.AskResponse{Text: "occupied"}
	c.pendingAsks["q1"] = &types.AskEvent{Kind: types.AskInput, ResponseCh: full}

	mutCDeadline(t, 5*time.Second, "handleAskResponse on a full channel", func() {
		c.handleAskResponse("q1", "", "", false, false)
	})
	if _, still := c.pendingAsks["q1"]; still {
		t.Error("ask not removed from pendingAsks after response")
	}
	if got := <-full; got.Text != "occupied" {
		t.Errorf("channel content = %+v, want the original occupant (drop, not block)", got)
	}
}

// TestMutC_AssembleRunsAfterReadLoop pins that block assembly runs on its
// own goroutine: a slow parse must not stop the loop from draining frames.
func TestMutC_AssembleRunsAfterReadLoop(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	entered := make(chan struct{})
	release := make(chan struct{})
	prevGate := assembleGate
	assembleGate = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { assembleGate = prevGate })

	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "slow parse"})
	<-entered
	writeWSText(t, ws, map[string]any{"type": "stop"})
	if !waitFor(5*time.Second, func() bool {
		mock.mu.RLock()
		defer mock.mu.RUnlock()
		return mock.abortCount == 1
	}) {
		t.Fatal("stop frame not processed while the parse goroutine was parked — assembly blocked the read loop")
	}
	close(release)
}

func TestMutC_EmptyMessageNoErrorFrame(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	// The positive case first (a read deadline expiry would poison the conn
	// for later reads): one content item that is rejected must produce the
	// error (an unknown type yields no block).
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "",
		"content": []map[string]any{
			{"type": "banana", "source": map[string]any{"type": "file", "path": "/dev/null"}},
		},
	})
	frame := readUntilType(t, ws, "error")
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(frame, &e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.Message, "attachments rejected") {
		t.Fatalf("error message = %q, want the attachments-rejected rejection", e.Message)
	}

	// Text-less, attachment-less message: nothing to assemble, nothing to
	// complain about — the "attachments rejected" error is only for content
	// that was actually rejected.
	writeWSText(t, ws, map[string]any{"type": "message", "text": ""})
	mutCBarrier(t, ws, "error")
	// The rejection would come from the async assemble goroutine, which can
	// land after the barrier response — keep reading briefly and fail on any
	// error frame. (The expiring deadline ends this conn's reads; the test
	// ends here too.)
	_ = ws.SetReadDeadline(time.Now().Add(400 * time.Millisecond)) // REAL-TIME
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			break // deadline: no error frame arrived
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) == nil && head.Type == "error" {
			t.Fatal("error frame emitted for an empty message — the rejection is only for rejected content")
		}
	}
	mock.mu.RLock()
	queries := len(mock.queryCalls)
	mock.mu.RUnlock()
	if queries != 0 {
		t.Fatalf("queryCalls = %d, want 0 for an empty message", queries)
	}
}

// TestMutC_BusyEnqueueItemFields pins the queued item's wire contract:
// every field the client depends on must survive the enqueue.
func TestMutC_BusyEnqueueItemFields(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return true }
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)
	before := time.Now() // REAL-TIME

	writeWSText(t, ws, map[string]any{"type": "message", "text": "queue me"})
	frame := readUntilType(t, ws, "queued")
	var q struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(frame, &q); err != nil {
		t.Fatal(err)
	}
	if q.UUID == "" {
		t.Fatal("queued frame carries an empty uuid")
	}

	mock.mu.RLock()
	item := mock.enqueueCalls[len(mock.enqueueCalls)-1]
	mock.mu.RUnlock()
	if item.Value != "queue me" {
		t.Errorf("item.Value = %q, want the message text", item.Value)
	}
	if item.UUID != q.UUID {
		t.Errorf("item.UUID = %q, want the uuid stamped on the queued frame (%q)", item.UUID, q.UUID)
	}
	if item.Mode != types.ItemModePrompt {
		t.Errorf("item.Mode = %q, want prompt", item.Mode)
	}
	if item.Priority != types.PriorityNext {
		t.Errorf("item.Priority = %v, want next", item.Priority)
	}
	if item.Origin == nil || item.Origin.Kind != types.OriginHuman {
		t.Errorf("item.Origin = %+v, want human origin", item.Origin)
	}
	if item.Timestamp.Before(before.Add(-time.Second)) || item.Timestamp.After(time.Now().Add(time.Second)) { // REAL-TIME
		t.Errorf("item.Timestamp = %v, want ~now", item.Timestamp)
	}
	if len(item.Content) != 1 || item.Content[0].Text != "queue me" {
		t.Errorf("item.Content = %+v, want the assembled text block", item.Content)
	}
}

// TestMutC_QuerySourceTaggedWUI pins that dispatched queries carry the "wui"
// source tag. WUI no longer registers a FileSender (file delivery goes through
// artifacts), so the tag is kept for parity with other connectors and for any
// future source-keyed routing.
func TestMutC_QuerySourceTaggedWUI(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	// Both callbacks run on inbound dispatch goroutines; mu hands the captured
	// ctx (and the value chain it points into) to the test body.
	var mu sync.Mutex
	var queryCtx context.Context
	var contentCtx context.Context
	mock.queryFn = func(ctx context.Context, _, _ string) {
		mu.Lock()
		queryCtx = ctx
		mu.Unlock()
	}
	mock.queryWithContentFn = func(ctx context.Context, _ []types.ContentBlock, _ string) {
		mu.Lock()
		contentCtx = ctx
		mu.Unlock()
	}
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "plain"})
	if !waitFor(5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return queryCtx != nil
	}) {
		t.Fatal("Query never fired")
	}
	mu.Lock()
	qctx := queryCtx
	mu.Unlock()
	if engine.SourceFromContext(qctx) != "wui" {
		t.Errorf("Query ctx source = %q, want wui", engine.SourceFromContext(qctx))
	}

	// Unknown content types are skipped, so the message still dispatches via
	// QueryWithContent with the text block.
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "with content",
		"content": []map[string]any{
			{"type": "banana", "source": map[string]any{"type": "file", "path": "/dev/null"}},
		},
	})
	if !waitFor(5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return contentCtx != nil
	}) {
		t.Fatal("QueryWithContent never fired")
	}
	mu.Lock()
	cctx := contentCtx
	mu.Unlock()
	if engine.SourceFromContext(cctx) != "wui" {
		t.Errorf("QueryWithContent ctx source = %q, want wui", engine.SourceFromContext(cctx))
	}
}

// ---------------------------------------------------------------------------
// server.go — assembleContentBlocks
// ---------------------------------------------------------------------------

// TestMutC_MediaCacheNilSkipsAttachments pins that a connector without a
// media store skips attachment content entirely instead of touching the fs.
func TestMutC_MediaCacheNilSkipsAttachments(t *testing.T) {
	c := newTestConnector(t) // mediaCache deliberately nil
	rec := mutCCaptureSlog(t)
	pngPath := filepath.Join(t.TempDir(), "dot.png")
	mutCWriteFile(t, pngPath, mutCPNG(t))

	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{{
		Type:   "image",
		Source: inboundSource{Type: "file", Path: pngPath, Mime: "image/png", Name: "dot.png", Size: 100},
	}})
	if len(blocks) != 0 {
		t.Fatalf("blocks = %d, want 0 (no media cache means attachments are skipped, not read)", len(blocks))
	}
	if !rec.hasMessage("wui: media cache not configured, skipping attachment") {
		t.Errorf("messages = %v, want the skip trace", rec.messages())
	}
}

func TestMutC_MultiContentAllProcessed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, _ := setupAttachmentServer(t)
	d1 := filepath.Join(t.TempDir(), "d1.txt")
	d2 := filepath.Join(t.TempDir(), "d2.txt")
	mutCWriteFile(t, d1, []byte("first document body"))
	mutCWriteFile(t, d2, []byte("second document body"))

	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "document", Source: inboundSource{Type: "file", Path: d1, Mime: "text/plain", Name: "d1.txt", Size: 19}},
		{Type: "document", Source: inboundSource{Type: "file", Path: d2, Mime: "text/plain", Name: "d2.txt", Size: 20}},
	})
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (every content item is processed)", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeDocument || blocks[0].Name != "d1.txt" || blocks[0].Path != d1 {
		t.Errorf("blocks[0] = %+v, want the d1 document reference", blocks[0])
	}
	if blocks[1].Type != types.ContentTypeDocument || blocks[1].Name != "d2.txt" || blocks[1].Path != d2 {
		t.Errorf("blocks[1] = %+v, want the d2 document reference", blocks[1])
	}
}

// TestMutC_ImageFailuresSkipOnlyTheItem pins that a failed image item does
// not abort the remaining content: the open-failure and sniff-failure paths
// skip exactly that item.
func TestMutC_ImageFailuresSkipOnlyTheItem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, _ := setupAttachmentServer(t)
	rec := mutCCaptureSlog(t)
	doc := filepath.Join(t.TempDir(), "note.txt")
	mutCWriteFile(t, doc, []byte("document body"))
	garbage := filepath.Join(t.TempDir(), "garbage.bin")
	mutCWriteFile(t, garbage, bytes.Repeat([]byte("not an image at all"), 64))

	// Missing path: the open failure is logged and only the image is
	// dropped; the document still becomes a block.
	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: "/nonexistent/img.png"}},
		{Type: "document", Source: inboundSource{Type: "file", Path: doc, Mime: "text/plain", Name: "note.txt"}},
	})
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeDocument {
		t.Fatalf("blocks = %+v, want only the document (open failure skips just the image)", blocks)
	}
	if !rec.hasMessage("wui: open image failed") {
		t.Errorf("messages = %v, want the open-failure trace", rec.messages())
	}

	// Existing but unrecognized image bytes: sniff rejects, image skipped,
	// document kept.
	blocks = c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: garbage}},
		{Type: "document", Source: inboundSource{Type: "file", Path: doc, Mime: "text/plain", Name: "note.txt"}},
	})
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeDocument {
		t.Fatalf("blocks = %+v, want only the document (sniff failure skips just the image)", blocks)
	}
	if !rec.hasMessage("wui: image bytes not a recognized format") {
		t.Errorf("messages = %v, want the sniff-rejection trace", rec.messages())
	}
}

// TestMutC_ImageDecodeFailureWarns pins the else-branch trace when the
// bytes sniff as an image but cannot be decoded.
func TestMutC_ImageDecodeFailureWarns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, _ := setupAttachmentServer(t)
	rec := mutCCaptureSlog(t)
	truncated := append(mutCPNG(t), bytes.Repeat([]byte{0}, 64)...)
	truncated = truncated[:len(truncated)/2] // keep the magic, break the stream
	badPng := filepath.Join(t.TempDir(), "broken.png")
	mutCWriteFile(t, badPng, truncated)

	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "image", Source: inboundSource{Type: "file", Path: badPng, Mime: "image/png"}},
	})
	if len(blocks) != 0 {
		t.Fatalf("blocks = %d, want 0 (undecodable image is skipped)", len(blocks))
	}
	if !rec.hasMessage("wui: image resize/encode failed, skipping") {
		t.Fatalf("messages = %v, want the resize/encode failure trace", rec.messages())
	}
}

func TestMutC_UnknownContentTypeWarns(t *testing.T) {
	// The media-cache guard runs before the type switch, so this needs a
	// connector with a store attached to reach the default case.
	c, _ := setupAttachmentServer(t)
	rec := mutCCaptureSlog(t)

	blocks := c.assembleContentBlocks(context.Background(), "hello", []inboundContent{
		{Type: "banana", Source: inboundSource{Type: "file", Path: "/dev/null"}},
	})
	if len(blocks) != 1 || blocks[0].Text != "hello" {
		t.Fatalf("blocks = %+v, want the text block only", blocks)
	}
	if !rec.hasMessage("wui: unknown content type, skipping") {
		t.Fatalf("messages = %v, want the unknown-type trace", rec.messages())
	}
}

// TestMutC_DocumentEstTokensZeroOnParseFail pins the est default when the
// document cannot be parsed (the success path computes a real estimate).
func TestMutC_DocumentEstTokensZeroOnParseFail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c, _ := setupAttachmentServer(t)
	// A nonexistent path fails the parse deterministically (the block is a
	// reference, so no bytes are needed on disk).
	blocks := c.assembleContentBlocks(context.Background(), "", []inboundContent{
		{Type: "document", Source: inboundSource{Type: "file", Path: "/nonexistent/broken.pdf", Mime: "application/pdf", Name: "broken.pdf"}},
	})
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want the reference block even on parse failure", len(blocks))
	}
	if blocks[0].EstTokens != 0 {
		t.Errorf("EstTokens = %d, want 0 (parse failure keeps the zero estimate)", blocks[0].EstTokens)
	}
}

// TestMutC_CommitClearsRewindStash pins that a new commit retires any
// pending auto-rewind stash: later restores must not resurrect the aborted
// prompt's attachments.
func TestMutC_CommitClearsRewindStash(t *testing.T) {
	c := newTestConnector(t)
	docBlock := types.NewDocumentBlock("old.pdf", "/cache/old.pdf", "application/pdf", 10, 0)
	c.restoreMu.Lock()
	c.rewindStash = &rewindStash{engineID: "main", blocks: []types.ContentBlock{docBlock}}
	c.restoreMu.Unlock()

	c.handleMessageInbound("moving on", nil)

	c.restoreMu.Lock()
	stash := c.rewindStash
	c.restoreMu.Unlock()
	if stash != nil {
		t.Fatal("rewindStash survived a new commit — a later restore would resurrect the aborted prompt's chips")
	}
}
