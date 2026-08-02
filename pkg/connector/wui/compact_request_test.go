package wui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func TestCompactRequest_Success(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	c.mock().manualCompactFn = func(_ context.Context, _ types.Message, _ string) (*short.CompactResult, error) {
		return &short.CompactResult{BeforeTokens: 10000, AfterTokens: 2000}, nil
	}

	sendJSON(t, ws, map[string]string{"type": "compact_request"})

	if !waitFor(2*time.Second, func() bool {
		c.mock().mu.RLock()
		defer c.mock().mu.RUnlock()
		return c.mock().manualCompactCalls == 1
	}) {
		c.mock().mu.RLock()
		n := c.mock().manualCompactCalls
		c.mock().mu.RUnlock()
		t.Fatalf("manualCompactCalls = %d, want 1", n)
	}

	c.mock().mu.RLock()
	defer c.mock().mu.RUnlock()
	msgs := c.mock().manualCompactMsgs
	if len(msgs) != 1 {
		t.Fatalf("manualCompactMsgs len = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Role != types.RoleUser {
		t.Errorf("userMsg.Role = %v, want user", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].Text != "/compact" {
		t.Errorf("userMsg content = %+v, want /compact text", m.Content)
	}
	if m.ID == "" {
		t.Error("userMsg.ID is empty, want uuid")
	}
	if m.Timestamp.IsZero() {
		t.Error("userMsg.Timestamp is zero")
	}
}

func TestCompactRequest_Error_NoErrorFrame(t *testing.T) {
	c := newTestConnector(t)
	ws := dialAndStore(t, c)
	defer ws.Close()

	c.mock().manualCompactFn = func(_ context.Context, _ types.Message, _ string) (*short.CompactResult, error) {
		return nil, errors.New("summarization failed")
	}

	sendJSON(t, ws, map[string]string{"type": "compact_request"})

	if !waitFor(2*time.Second, func() bool {
		c.mock().mu.RLock()
		defer c.mock().mu.RUnlock()
		return c.mock().manualCompactCalls == 1
	}) {
		c.mock().mu.RLock()
		n := c.mock().manualCompactCalls
		c.mock().mu.RUnlock()
		t.Fatalf("manualCompactCalls = %d, want 1", n)
	}

	// ManualCompact error is emitted via EventQueryEnd by the real engine,
	// not by the connector. The connector only logs. So no error frame
	// should arrive on the WS from handleCompactRequest itself.
	type wsRead struct{ err error }
	ch := make(chan wsRead, 1)
	go func() {
		_, _, err := ws.ReadMessage()
		ch <- wsRead{err}
	}()
	select {
	case <-time.After(300 * time.Millisecond):
		// timeout — no frame arrived, correct
	case r := <-ch:
		if r.err == nil {
			t.Error("expected no WS frame, got a message — connector sent an error frame it shouldn't have")
		}
	}
}

func TestCompactRequest_Lifecycle_EventsRoutedToFrontend(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	defer ws.Close()

	compactID := "compact-manual-test1234"
	c.mock().manualCompactFn = func(_ context.Context, _ types.Message, _ string) (*short.CompactResult, error) {
		h.Dispatch(types.QueryEvent{Type: types.EventQueryStart})
		h.Dispatch(types.QueryEvent{
			Type:    types.EventToolStart,
			ToolUse: &types.ToolUseEvent{ID: compactID, Name: "Compact", Summary: "Compacting..."},
		})
		h.Dispatch(types.QueryEvent{
			Type:    types.EventToolRun,
			ToolUse: &types.ToolUseEvent{ID: compactID, Name: "Compact"},
		})
		h.Dispatch(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     compactID,
				DisplayOutput: "Compacted: 80k → 15k",
			},
		})
		h.Dispatch(types.QueryEvent{Type: types.EventQueryEnd})
		return &short.CompactResult{BeforeTokens: 80000, AfterTokens: 15000}, nil
	}

	sendJSON(t, ws, map[string]string{"type": "compact_request"})

	wantTypes := []string{"query_start", "tool_start", "tool_run", "tool_end", "query_end"}
	for i, want := range wantTypes {
		data := readWSMessage(t, ws)
		var env struct {
			Event struct {
				Type string `json:"type"`
			} `json:"event"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatalf("frame %d unmarshal: %v", i, err)
		}
		if env.Event.Type != want {
			t.Errorf("frame %d event.type = %q, want %q", i, env.Event.Type, want)
		}
	}
}

func TestCompactRequest_Lifecycle_ErrorPath_SingleErrorFrame(t *testing.T) {
	h := hub.NewHub()
	c := newTestConnectorWithHub(t, h)
	ws := dialAndStore(t, c)
	defer ws.Close()

	c.mock().manualCompactFn = func(_ context.Context, _ types.Message, _ string) (*short.CompactResult, error) {
		h.Dispatch(types.QueryEvent{Type: types.EventQueryStart})
		h.Dispatch(types.QueryEvent{
			Type:    types.EventToolStart,
			ToolUse: &types.ToolUseEvent{ID: "compact-manual-err", Name: "Compact"},
		})
		h.Dispatch(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     "compact-manual-err",
				IsError:       true,
				DisplayOutput: "Compact failed: summarization unavailable",
			},
		})
		h.Dispatch(types.QueryEvent{Type: types.EventQueryEnd, Error: errors.New("summarization failed")})
		return nil, errors.New("summarization failed")
	}

	sendJSON(t, ws, map[string]string{"type": "compact_request"})

	// Read 5 frames — 4 event frames + 1 error frame from query_end.
	// The error comes from onEngineEvent's buildError(event.Error) for
	// EventQueryEnd with Error set. handleCompactRequest must NOT send
	// a second error frame.
	errorCount := 0
	for range 5 {
		data := readWSMessage(t, ws)
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg["type"] == "error" {
			errorCount++
			errMsg, _ := msg["message"].(string)
			if !strings.Contains(errMsg, "summarization") {
				t.Errorf("error message = %q, want substring \"summarization\"", errMsg)
			}
		}
	}
	if errorCount != 1 {
		t.Errorf("error frame count = %d, want exactly 1 (from EventQueryEnd, not from handleCompactRequest)", errorCount)
	}
}
