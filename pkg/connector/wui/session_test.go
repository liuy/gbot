package wui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func TestSessionListRequest(t *testing.T) {
	c := newTestConnector(t)
	c.mock().listSessionsFn = func(limit int) ([]*short.Session, error) {
		if limit != 50 {
			t.Fatalf("expected limit 50, got %d", limit)
		}
		return []*short.Session{
			{SessionID: "s1", Title: "Session One", UpdatedAt: time.UnixMilli(1700000000000)},
			{SessionID: "s2", Title: "Session Two", UpdatedAt: time.UnixMilli(1700000001000)},
		}, nil
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_list_request"})

	data := readWSMessage(t, ws)
	var msg struct {
		Type     string `json:"type"`
		Sessions []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			UpdatedAt int64  `json:"updatedAt"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "session_list" {
		t.Fatalf("expected type session_list, got %s", msg.Type)
	}
	if len(msg.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(msg.Sessions))
	}
	if msg.Sessions[0].ID != "s1" || msg.Sessions[0].Title != "Session One" {
		t.Fatalf("unexpected session[0]: %+v", msg.Sessions[0])
	}
	if msg.Sessions[0].UpdatedAt != 1700000000000 {
		t.Fatalf("unexpected updatedAt: %d", msg.Sessions[0].UpdatedAt)
	}
	if msg.Sessions[1].ID != "s2" || msg.Sessions[1].Title != "Session Two" {
		t.Fatalf("unexpected session[1]: %+v", msg.Sessions[1])
	}
}

func TestSessionListRequestEmpty(t *testing.T) {
	c := newTestConnector(t)
	c.mock().listSessionsFn = func(limit int) ([]*short.Session, error) {
		return nil, nil
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_list_request"})

	// No frame expected — wait briefly and confirm no response.
	done := make(chan struct{})
	go func() {
		_, _, _ = ws.ReadMessage()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("expected no response for empty session list")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestSessionSwitch(t *testing.T) {
	c := newTestConnector(t)
	c.mock().switchSessionFn = func(sessionID string) error {
		return nil
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": "target-123"})

	// Session switch sends a metadata frame. Parse it and extract connect.
	meta := readMetadata(t, ws)
	var cs struct {
		Type      string `json:"type"`
		Connected bool   `json:"connected"`
		Agent     string `json:"agent"`
		Model     string `json:"model"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(meta.Connect, &cs); err != nil {
		t.Fatalf("unmarshal connect_status: %v", err)
	}
	if cs.Type != "connect_status" {
		t.Fatalf("expected connect_status, got %s", cs.Type)
	}
	if cs.Agent != "main" {
		t.Fatalf("expected agent main, got %s", cs.Agent)
	}
	if cs.Model != "glm-5.2" {
		t.Fatalf("expected model glm-5.2, got %s", cs.Model)
	}

	c.mock().mu.Lock()
	calls := c.mock().switchSessionCalls
	c.mock().mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected 1 switchSession call, got %d", len(calls))
	}
	if calls[0] != "target-123" {
		t.Fatalf("expected switchSession called with target-123, got %q", calls[0])
	}
}

func TestSessionSwitchError(t *testing.T) {
	c := newTestConnector(t)
	c.mock().switchSessionFn = func(sessionID string) error {
		return errSessionNotFound
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": "bad"})

	data := readWSMessage(t, ws)
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" {
		t.Fatalf("expected error, got %s", msg.Type)
	}
	if msg.Message == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestSessionRename(t *testing.T) {
	c := newTestConnector(t)
	c.mock().listSessionsFn = func(limit int) ([]*short.Session, error) {
		return []*short.Session{
			{SessionID: "s1", Title: "New Title", UpdatedAt: time.UnixMilli(1700000000000)},
		}, nil
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{
		"type":      "session_rename",
		"sessionID": "s1",
		"title":     "New Title",
	})

	// Should receive session_list after rename
	data := readWSMessage(t, ws)
	var msg struct {
		Type     string `json:"type"`
		Sessions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "session_list" {
		t.Fatalf("expected session_list, got %s", msg.Type)
	}
	if len(msg.Sessions) != 1 || msg.Sessions[0].Title != "New Title" {
		t.Fatalf("unexpected sessions: %+v", msg.Sessions)
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if len(c.mock().updateTitleCalls) != 1 {
		t.Fatalf("expected 1 updateTitle call, got %d", len(c.mock().updateTitleCalls))
	}
	if c.mock().updateTitleCalls[0].ID != "s1" || c.mock().updateTitleCalls[0].Title != "New Title" {
		t.Fatalf("unexpected updateTitle call: %+v", c.mock().updateTitleCalls[0])
	}
}

func TestSessionNew(t *testing.T) {
	c := newTestConnector(t)
	c.mock().newSessionFn = func() (string, error) {
		return "fresh-session-id", nil
	}

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_new"})

	// Session new sends a metadata frame. Parse it and extract connect.
	meta := readMetadata(t, ws)
	var cs struct {
		Type      string `json:"type"`
		Connected bool   `json:"connected"`
		SessionID string `json:"sessionID"`
	}
	if err := json.Unmarshal(meta.Connect, &cs); err != nil {
		t.Fatalf("unmarshal connect_status: %v", err)
	}
	if cs.Type != "connect_status" {
		t.Fatalf("expected connect_status, got %s", cs.Type)
	}
	if !cs.Connected {
		t.Fatal("expected connected true")
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if c.mock().newSessionCalls != 1 {
		t.Fatalf("expected 1 newSession call, got %d", c.mock().newSessionCalls)
	}
}

func TestSessionSwitchBusy(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": "target"})

	data := readWSMessage(t, ws)
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" {
		t.Fatalf("expected type error, got %s", msg.Type)
	}
	if msg.Message != "Session is busy — please wait for the current request to finish, then try again" {
		t.Fatalf("expected session busy message, got %q", msg.Message)
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if len(c.mock().switchSessionCalls) != 0 {
		t.Fatalf("expected 0 switchSession calls (engine busy), got %d", len(c.mock().switchSessionCalls))
	}
}

func TestSessionNewBusy(t *testing.T) {
	c := newTestConnector(t)
	c.mock().isBusyFn = func() bool { return true }

	ws := dialAndStore(t, c)

	sendJSON(t, ws, map[string]string{"type": "session_new"})

	data := readWSMessage(t, ws)
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "error" {
		t.Fatalf("expected type error, got %s", msg.Type)
	}
	if msg.Message != "Session is busy — please wait for the current request to finish, then try again" {
		t.Fatalf("expected session busy message, got %q", msg.Message)
	}

	c.mock().mu.Lock()
	defer c.mock().mu.Unlock()
	if c.mock().newSessionCalls != 0 {
		t.Fatalf("expected 0 newSession calls (engine busy), got %d", c.mock().newSessionCalls)
	}
}

// sendJSON marshals v and writes it as a WS text message.
func sendJSON(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := ws.WriteMessage(1, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestSessionSwitch_SendsStatsMessage verifies that session switch sends a
// stats field (inside the metadata frame) with zero values (no in-flight query
// after switch), resetting the client's accumulated stats.
func TestSessionSwitch_SendsStatsMessage(t *testing.T) {
	c := newTestConnector(t)
	c.mock().switchSessionFn = func(sessionID string) error { return nil }

	// Simulate a completed query (query_start → usage → query_end resets queryStats).
	c.Handle(types.QueryEvent{Type: types.EventQueryStart})
	c.Handle(types.QueryEvent{Type: types.EventUsage, Usage: &types.UsageEvent{
		InputTokens: 5000, OutputTokens: 300,
	}})
	c.Handle(types.QueryEvent{Type: types.EventQueryEnd})

	ws := dialAndStore(t, c)
	defer ws.Close()

	// Switch session.
	sendJSON(t, ws, map[string]string{"type": "session_switch", "sessionID": "target-123"})

	// Session switch sends a metadata frame with stats inside.
	meta := readMetadata(t, ws)
	var stats struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(meta.Stats, &stats); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	if stats.Usage == nil {
		t.Fatal("stats.usage is nil, want zero-value object")
	}
	// After session switch, queryStats is reset — values must be zero.
	if stats.Usage.InputTokens != 0 {
		t.Errorf("stats.usage.input_tokens = %d, want 0", stats.Usage.InputTokens)
	}
	if stats.Usage.OutputTokens != 0 {
		t.Errorf("stats.usage.output_tokens = %d, want 0", stats.Usage.OutputTokens)
	}
}

var errSessionNotFound = &testError{"session not found"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
