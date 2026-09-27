package wui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/types"
)

// writeTempFile writes data to a uniquely named file under dir. Restore tests
// need real on-disk bytes: the restored ids re-bind these paths and the
// re-send commit re-reads them (fileread for images, ParseDocument for docs).
func writeTempFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// restoreResult mirrors the restore_result wire shape for assertions.
type restoreResult struct {
	Attachments []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Mime string `json:"mime"`
		Size int    `json:"size"`
	} `json:"attachments"`
}

// sendRestoreAndRead sends a restore_attachments frame and returns the decoded
// restore_result plus the raw frame (for wire-shape assertions).
func sendRestoreAndRead(t *testing.T, ws *websocket.Conn) (restoreResult, json.RawMessage) {
	t.Helper()
	writeWSText(t, ws, map[string]any{"type": "restore_attachments"})
	raw := readUntilType(t, ws, "restore_result")
	var rr restoreResult
	if err := json.Unmarshal(raw, &rr); err != nil {
		t.Fatalf("restore_result decode: %v (%s)", err, raw)
	}
	return rr, raw
}

// TestRestoreAttachments_FileSourcedImageAndDocument pins the rewind-restore
// contract for engine-bound prompts: restore_attachments must re-stage the
// last user prompt's attachments under fresh upload ids (documents re-bind
// their cache path; file-sourced images re-bind Source.Path — engine-bound
// image blocks carry no base64), and those ids must round-trip through a
// commit re-send that reaches the engine as image + document contents.
func TestRestoreAttachments_FileSourcedImageAndDocument(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return false }

	dir := t.TempDir()
	pngPath := writeTempFile(t, dir, "shot.png", minimalPNGAttachment)
	docBody := []byte("hello notes")
	docPath := writeTempFile(t, dir, "notes.txt", docBody)
	mock.messagesFn = func() []types.Message {
		return []types.Message{{
			ID:        "m1",
			Role:      types.RoleUser,
			Timestamp: fixedTimestamp,
			Content: []types.ContentBlock{
				types.NewTextBlock("see this"),
				{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/png", Path: pngPath}},
				types.NewDocumentBlock("notes.txt", docPath, "text/plain", int64(len(docBody)), 0),
			},
		}}
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	rr, _ := sendRestoreAndRead(t, ws)
	if len(rr.Attachments) != 2 {
		t.Fatalf("restore_result attachments = %d, want 2 (image + document)", len(rr.Attachments))
	}
	img, doc := rr.Attachments[0], rr.Attachments[1]
	if img.ID == "" || doc.ID == "" || img.ID == doc.ID {
		t.Fatalf("attachment ids = (%q, %q), want two distinct non-empty ids", img.ID, doc.ID)
	}
	if img.Mime != "image/png" {
		t.Errorf("image mime = %q, want image/png", img.Mime)
	}
	if doc.Name != "notes.txt" || doc.Mime != "text/plain" || doc.Size != len(docBody) {
		t.Errorf("document attachment = %+v, want {notes.txt text/plain %d}", doc, len(docBody))
	}

	// The fresh ids must round-trip: a commit re-send while idle dispatches
	// both contents without any new byte upload.
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "retry",
		"attachments": []map[string]any{
			{"id": img.ID, "name": img.Name, "mime": img.Mime, "size": img.Size},
			{"id": doc.ID, "name": doc.Name, "mime": doc.Mime, "size": doc.Size},
		},
	})
	if !waitFor(10*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		t.Fatal("re-send with restored ids never dispatched")
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	hasImage, hasDoc := false, false
	for _, cb := range call.content {
		switch cb.Type {
		case types.ContentTypeImage:
			hasImage = true
			if cb.Source == nil || cb.Source.MediaType != "image/png" || cb.Source.Data == "" {
				t.Errorf("image block source = %+v, want base64 image/png data", cb.Source)
			}
		case types.ContentTypeDocument:
			hasDoc = true
			if cb.Name != "notes.txt" || cb.Path != docPath || cb.Size != int64(len(docBody)) {
				t.Errorf("document block = %+v, want {notes.txt %s %d}", cb, docPath, len(docBody))
			}
		}
	}
	if !hasImage || !hasDoc {
		t.Fatalf("re-send content missing attachment blocks: hasImage=%v hasDoc=%v (%+v)", hasImage, hasDoc, call.content)
	}
}

// TestRestoreAttachments_WalkPastToolResultMessages pins the ordering rule:
// tool_result messages also carry Role user, so the backwards walk must skip
// them and return the actual user PROMPT's attachment.
func TestRestoreAttachments_WalkPastToolResultMessages(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	dir := t.TempDir()
	docBody := []byte("prompt body")
	docPath := writeTempFile(t, dir, "prompt.txt", docBody)
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{
				ID: "m1", Role: types.RoleUser, Timestamp: fixedTimestamp,
				Content: []types.ContentBlock{
					types.NewTextBlock("with attachment"),
					types.NewDocumentBlock("prompt.txt", docPath, "text/plain", int64(len(docBody)), 0),
				},
			},
			{
				ID: "m2", Role: types.RoleAssistant, Timestamp: fixedTimestamp,
				Content: []types.ContentBlock{types.NewToolUseBlock("tu_1", "Read", json.RawMessage(`{}`))},
			},
			{
				ID: "m3", Role: types.RoleUser, Timestamp: fixedTimestamp,
				Content: []types.ContentBlock{
					{Type: types.ContentTypeToolResult, ToolUseID: "tu_1"},
				},
			},
		}
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	rr, _ := sendRestoreAndRead(t, ws)
	if len(rr.Attachments) != 1 {
		t.Fatalf("restore_result attachments = %d, want 1 (the prompt's document)", len(rr.Attachments))
	}
	if rr.Attachments[0].Name != "prompt.txt" || rr.Attachments[0].Mime != "text/plain" {
		t.Fatalf("attachment = %+v, want the prompt's prompt.txt document", rr.Attachments[0])
	}
}

// TestRestoreAttachments_NoCandidatesEmpty pins the empty contract: no user
// message at all, and a user prompt without attachment blocks, both answer
// restore_result with an EMPTY array (never null, never stale entries).
func TestRestoreAttachments_NoCandidatesEmpty(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.messagesFn = func() []types.Message {
		return []types.Message{
			{ID: "m1", Role: types.RoleAssistant, Timestamp: fixedTimestamp,
				Content: []types.ContentBlock{types.NewTextBlock("answer")}},
		}
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	rr, raw := sendRestoreAndRead(t, ws)
	if len(rr.Attachments) != 0 {
		t.Fatalf("attachments = %d, want 0 (no user message)", len(rr.Attachments))
	}
	if !strings.Contains(string(raw), `"attachments":[]`) {
		t.Fatalf("empty restore_result must serialize attachments as [], got %s", raw)
	}

	// A user prompt with text only is not an attachment candidate either.
	mock.SetMessagesFn(func() []types.Message {
		return []types.Message{
			{ID: "m2", Role: types.RoleUser, Timestamp: fixedTimestamp,
				Content: []types.ContentBlock{types.NewTextBlock("no attachments")}},
		}
	})
	rr2, _ := sendRestoreAndRead(t, ws)
	if len(rr2.Attachments) != 0 {
		t.Fatalf("attachments = %d, want 0 (text-only prompt)", len(rr2.Attachments))
	}
}

// TestRestoreAttachments_AfterAbortRewindServesStashedPrompt pins the
// production timing: the connector rewinds the empty-aborted prompt out of
// engine history BEFORE the client's query_end frame is sent, so a walk over
// Messages() alone would find nothing. The restore must serve the prompt
// captured at rewind time (one-shot), then return empty on a second ask.
func TestRestoreAttachments_AfterAbortRewindServesStashedPrompt(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	mock.isBusyFn = func() bool { return false }

	dir := t.TempDir()
	pngPath := writeTempFile(t, dir, "shot.png", minimalPNGAttachment)
	docBody := []byte("hello notes")
	docPath := writeTempFile(t, dir, "notes.txt", docBody)

	// msgs mirrors engine state: messagesFn serves it, rewindToFn truncates
	// it exactly like Engine.RewindTo(lastUserIdx) drops the prompt itself.
	msgs := []types.Message{{
		ID: "m1", Role: types.RoleUser, Timestamp: fixedTimestamp,
		Content: []types.ContentBlock{
			types.NewTextBlock("see this"),
			{Type: types.ContentTypeImage, Source: &types.ImageSource{Type: "file", MediaType: "image/png", Path: pngPath}},
			types.NewDocumentBlock("notes.txt", docPath, "text/plain", int64(len(docBody)), 0),
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
			return fmt.Errorf("rewind index %d out of range [0, %d]", idx, len(msgs))
		}
		msgs = msgs[:idx]
		return nil
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	// Production abort path: the hub delivers query_end with an AbortError;
	// the connector auto-rewinds before the client can ask for a restore.
	slot := c.activeSlot()
	if slot == nil || slot.hub == nil {
		t.Fatal("no active slot/hub")
	}
	slot.hub.Dispatch(types.QueryEvent{
		Type:  types.EventQueryEnd,
		Error: &engine.AbortError{Err: context.Canceled},
	})

	var rewindCount atomic.Int32
	if !waitFor(5*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		rewindCount.Store(int32(len(mock.rewindCalls)))
		return len(mock.rewindCalls) == 1
	}) {
		t.Fatalf("auto-rewind never ran (rewindCalls = %d)", rewindCount.Load())
	}
	mock.mu.Lock()
	remaining := len(msgs)
	mock.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("rewind left %d messages, want 0 (prompt removed)", remaining)
	}

	// Messages() is now empty — the restore must still return the stashed
	// prompt's two attachments.
	rr, _ := sendRestoreAndRead(t, ws)
	if len(rr.Attachments) != 2 {
		t.Fatalf("restore_result attachments = %d, want 2 (stashed prompt)", len(rr.Attachments))
	}
	if rr.Attachments[0].Mime != "image/png" || rr.Attachments[1].Name != "notes.txt" {
		t.Fatalf("stashed attachments = %+v, want image + notes.txt document", rr.Attachments)
	}

	// One-shot: the stash is consumed, a second ask walks the (empty)
	// history and returns zero.
	rr2, raw2 := sendRestoreAndRead(t, ws)
	if len(rr2.Attachments) != 0 {
		t.Fatalf("second restore attachments = %d, want 0 (stash consumed)", len(rr2.Attachments))
	}
	if !strings.Contains(string(raw2), `"attachments":[]`) {
		t.Fatalf("second restore_result must serialize attachments as [], got %s", raw2)
	}
}
