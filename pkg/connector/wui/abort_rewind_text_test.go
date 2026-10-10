package wui

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

// abortLogCapture records "wui:abort" log records' attrs so tests can assert
// the triage log names the rewound target (mutation coverage for the
// slog.Info call itself).
type abortLogCapture struct {
	mu      sync.Mutex
	records []string
}

func (h *abortLogCapture) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *abortLogCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "wui:abort" {
		var sb strings.Builder
		r.Attrs(func(a slog.Attr) bool {
			sb.WriteString(a.Key + "=" + a.Value.String() + " ")
			return true
		})
		h.mu.Lock()
		h.records = append(h.records, sb.String())
		h.mu.Unlock()
	}
	return nil
}

func (h *abortLogCapture) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *abortLogCapture) WithGroup(_ string) slog.Handler      { return h }

// decodeQueryEndEvent reads the next event frame off the WS and returns the
// query_end event object as a raw map so tests can assert exact key presence
// AND absence (typed structs cannot distinguish "absent" from "zero value").
func decodeQueryEndEvent(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var frame struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("event frame decode: %v (%s)", err, raw)
	}
	if frame.Event["type"] != "query_end" {
		t.Fatalf("event type = %v, want query_end (%s)", frame.Event["type"], raw)
	}
	return frame.Event
}

// dispatchAbortQueryEnd drives the production abort path: the hub delivers
// query_end carrying an AbortError, exactly as the engine does on user stop.
func dispatchAbortQueryEnd(t *testing.T, c *WUIConnector) {
	t.Helper()
	slot := c.activeSlot()
	if slot == nil || slot.hub == nil {
		t.Fatal("no active slot/hub")
	}
	slot.hub.Dispatch(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: &engine.AbortError{Err: context.Canceled},
	})
}

// TestAbortQueryEndCarriesRewoundText pins the server-authoritative rewind
// target: the aborted query_end frame must name the exact text of the prompt
// the server rewound, so clients never guess the target from a boolean. With
// a completed older turn in history, the rewind target is the just-appended
// prompt ("next question"), never userOld.
func TestAbortQueryEndCarriesRewoundText(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return false }
	msgs := []types.Message{
		{ID: "m1", Role: types.RoleUser, Timestamp: fixedTimestamp,
			Content: []types.ContentBlock{types.NewTextBlock("old question")}},
		{ID: "m2", Role: types.RoleAssistant, Timestamp: fixedTimestamp,
			Content: []types.ContentBlock{types.NewTextBlock("old answer")}},
		{ID: "m3", Role: types.RoleUser, Timestamp: fixedTimestamp,
			Content: []types.ContentBlock{types.NewTextBlock("next question")}},
	}
	mock.messagesFn = func() []types.Message {
		return append([]types.Message(nil), msgs...)
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// The abort triage log must name the rewound target (installed before
	// the dispatch so the record cannot slip past the capture).
	logCap := &abortLogCapture{}
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(logCap))
	defer slog.SetDefault(oldDefault)

	dispatchAbortQueryEnd(t, c)
	raw := readUntilType(t, ws, "event")
	ev := decodeQueryEndEvent(t, raw)
	if ev["aborted"] != true {
		t.Fatalf("query_end aborted = %v, want true (%s)", ev["aborted"], raw)
	}
	if ev["rewound"] != true {
		t.Fatalf("query_end rewound = %v, want true (%s)", ev["rewound"], raw)
	}
	got, ok := ev["rewound_text"]
	if !ok {
		t.Fatalf("query_end event missing rewound_text key (%s)", raw)
	}
	if got != "next question" {
		t.Fatalf("query_end rewound_text = %v, want %q", got, "next question")
	}

	// The frame read above was pushed AFTER the log fired, so the record is
	// already captured here.
	logCap.mu.Lock()
	defer logCap.mu.Unlock()
	if len(logCap.records) != 1 {
		t.Fatalf("wui:abort log records = %d, want exactly 1 (%v)", len(logCap.records), logCap.records)
	}
	// msgs=N is dynamic; assert the exact fixed-attr prefix instead.
	if !strings.Contains(logCap.records[0], "engine=main rewound=true rewound_text=next question ") {
		t.Fatalf("wui:abort log = %q, want rewound_text=next question", logCap.records[0])
	}

	// The server rewound the just-appended prompt (index 2), not the old turn.
	mock.mu.RLock()
	defer mock.mu.RUnlock()
	if len(mock.rewindCalls) != 1 || mock.rewindCalls[0] != 2 {
		t.Fatalf("rewindCalls = %v, want exactly [2] (the userNew index)", mock.rewindCalls)
	}
}

// TestAbortQueryEndOmitsRewoundTextWhenNotRewound pins the wire omission
// contract: when a queued prompt keeps the interrupted turn (no rewind), the
// frame carries neither rewound nor rewound_text — legacy wire shape intact.
func TestAbortQueryEndOmitsRewoundTextWhenNotRewound(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return false }
	msgs := []types.Message{
		{ID: "m1", Role: types.RoleUser, Timestamp: fixedTimestamp,
			Content: []types.ContentBlock{types.NewTextBlock("query A")}},
	}
	mock.messagesFn = func() []types.Message {
		return append([]types.Message(nil), msgs...)
	}
	mock.pendingAttachmentsFn = func() []types.QueuedItem {
		return []types.QueuedItem{{
			UUID:    "q-b",
			Mode:    types.ItemModePrompt,
			Value:   "queued B",
			Content: []types.ContentBlock{types.NewTextBlock("queued B")},
		}}
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	dispatchAbortQueryEnd(t, c)
	raw := readUntilType(t, ws, "event")
	ev := decodeQueryEndEvent(t, raw)
	if ev["aborted"] != true {
		t.Fatalf("query_end aborted = %v, want true (%s)", ev["aborted"], raw)
	}
	for _, key := range []string{"rewound", "rewound_text"} {
		if _, ok := ev[key]; ok {
			t.Fatalf("query_end event must omit %q when no rewind ran (%s)", key, raw)
		}
	}

	mock.mu.RLock()
	defer mock.mu.RUnlock()
	if len(mock.rewindCalls) != 0 {
		t.Fatalf("rewindCalls = %v, want none (queued prompt keeps the turn)", mock.rewindCalls)
	}
}

// TestAbortRewindTextWithAttachmentPrompt pins rewound_text extraction for
// mixed-content prompts (first text block wins) AND that the attachment
// stash flow is untouched: restore_attachments still serves the rewound
// prompt's image after the rewind dropped it from engine history.
func TestAbortRewindTextWithAttachmentPrompt(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return false }

	dir := t.TempDir()
	pngPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(pngPath, minimalPNGAttachment, 0o644); err != nil {
		t.Fatalf("write shot.png: %v", err)
	}
	msgs := []types.Message{{
		ID: "m1", Role: types.RoleUser, Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.NewTextBlock("see this"),
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/png", Path: pngPath}},
		},
	}}
	mock.messagesFn = func() []types.Message {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return append([]types.Message(nil), msgs...)
	}
	mock.rewindToFn = func(idx int) error {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		if idx < 0 || idx > len(msgs) {
			t.Fatalf("rewind index %d out of range [0, %d]", idx, len(msgs))
		}
		msgs = msgs[:idx]
		return nil
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	dispatchAbortQueryEnd(t, c)
	raw := readUntilType(t, ws, "event")
	ev := decodeQueryEndEvent(t, raw)
	if got, ok := ev["rewound_text"]; !ok || got != "see this" {
		t.Fatalf("query_end rewound_text = %v (present=%v), want %q", got, ok, "see this")
	}

	// The rewind dropped the prompt from Messages(); the stash must still
	// serve its image under a fresh re-sendable id.
	rr, _ := sendRestoreAndRead(t, ws)
	if len(rr.Attachments) != 1 {
		t.Fatalf("restore_result attachments = %d, want 1 (stashed image)", len(rr.Attachments))
	}
	if rr.Attachments[0].Mime != "image/png" {
		t.Fatalf("stashed attachment mime = %q, want image/png", rr.Attachments[0].Mime)
	}
}
