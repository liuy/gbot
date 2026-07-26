package wui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/types"
)

// writeWSText JSON-marshals payload and sends it as a TextMessage with a
// 2s write deadline so a stuck connection fails the test instead of hanging.
func writeWSText(t *testing.T, c *websocket.Conn, payload map[string]any) {
	t.Helper()
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	if err := c.WriteMessage(websocket.TextMessage, out); err != nil {
		t.Fatalf("ws.WriteMessage: %v", err)
	}
}

// writeWSBinary sends a binary frame with a deadline. Mirrors readWSMessage's
// 2s timeout so the test fails fast on a stalled write.
func writeWSBinary(t *testing.T, c *websocket.Conn, data []byte) {
	t.Helper()
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second)) // REAL-TIME
	if err := c.WriteMessage(websocket.BinaryMessage, data); err != nil {
		t.Fatalf("ws.WriteMessage(binary): %v", err)
	}
}

// readWSError polls for an error frame within the timeout; returns the
// message string. Fatal-fails if no error frame arrives in time.
func readWSError(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	var msg string
	if !waitFor(time.Second, func() bool {
		_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond)) // REAL-TIME
		_, data, err := c.ReadMessage()
		_ = c.SetReadDeadline(time.Time{})
		if err != nil {
			return false
		}
		var head struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &head) != nil {
			return false
		}
		if head.Type != "error" {
			return false
		}
		msg = head.Message
		return true
	}) {
		t.Fatal("no error frame received within timeout")
	}
	return msg
}

// setupAttachmentServer returns a connector with a real media store bound to
// an httptest server exposing only /ws/chat (the /upload route is gone).
func setupAttachmentServer(t *testing.T) (*WUIConnector, *httptest.Server) {
	t.Helper()
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)
	mux := attachChatMux(c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return c, srv
}

// attachChatMux mounts /ws/chat on a fresh mux. Kept inline (not added to
// helpers_test.go) because the e2e tests in this file are the only callers.
func attachChatMux(c *WUIConnector) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	return mux
}

// TestWSAttachment_RoundTrip_Image drives the two-phase commit flow:
//
//	attachment_start → binary chunks → attachment_end → user_message →
//	mock.QueryWithContent fires with text + image block.
//
// All attachments must land in saved BEFORE the user_message commit frame
// arrives — the server refuses dispatch otherwise.
func TestWSAttachment_RoundTrip_Image(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "photo.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "what is this",
		"attachments": []map[string]any{
			{"id": "a1", "name": "photo.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (text + image)", len(call.content))
	}
	if call.content[0].Type != types.ContentTypeText || call.content[0].Text != "what is this" {
		t.Errorf("content[0] = %+v, want text 'what is this'", call.content[0])
	}
	imgBlock := call.content[1]
	if imgBlock.Type != types.ContentTypeImage {
		t.Fatalf("content[1].Type = %v, want image", imgBlock.Type)
	}
	if imgBlock.Source == nil || imgBlock.Source.Type != "base64" {
		t.Fatalf("image source type = %+v, want base64", imgBlock.Source)
	}
	if imgBlock.Source.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", imgBlock.Source.MediaType)
	}
	decoded, err := base64.StdEncoding.DecodeString(imgBlock.Source.Data)
	if err != nil {
		t.Errorf("Source.Data is not valid base64: %v", err)
	}
	if !bytes.Equal(decoded, minimalPNGAttachment) {
		t.Errorf("decoded bytes len = %d, want %d (roundtrip corrupted)", len(decoded), len(minimalPNGAttachment))
	}
	if len(mock.queryCalls) != 0 {
		t.Errorf("queryCalls = %d, want 0 (QueryWithContent path)", len(mock.queryCalls))
	}
}

// TestWSAttachment_RoundTrip_Document exercises the document branch: a
// plain-text document is saved and the dispatched content's text block
// carries the [Document: name saved at path] header followed by the parsed
// body.
func TestWSAttachment_RoundTrip_Document(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	body := []byte("the quick brown fox jumps over the lazy dog")
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "d1", "name": "story.txt", "mime": "text/plain", "size": len(body),
	})
	writeWSBinary(t, ws, body)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "d1"})
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "summarize this",
		"attachments": []map[string]any{
			{"id": "d1", "name": "story.txt", "mime": "text/plain", "size": len(body)},
		},
	})

	if !waitFor(2*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (text + document)", len(call.content))
	}
	if call.content[0].Type != types.ContentTypeText || call.content[0].Text != "summarize this" {
		t.Errorf("content[0] = %+v, want text 'summarize this'", call.content[0])
	}
	doc := call.content[1]
	if doc.Type != types.ContentTypeText {
		t.Fatalf("content[1].Type = %v, want text (document header)", doc.Type)
	}
	if !strings.HasPrefix(doc.Text, "[Document: story.txt saved at ") {
		t.Errorf("doc.Text = %q, want prefix '[Document: story.txt saved at '", doc.Text)
	}
	if !strings.HasSuffix(doc.Text, "the quick brown fox jumps over the lazy dog") {
		t.Errorf("doc.Text = %q, want suffix body text", doc.Text)
	}
}

// TestWSAttachment_MultiChunk_256KiBBoundary drives a 300 KiB attachment so
// the client must split it into a 256 KiB full chunk + a 44 KiB tail. The
// accumulator should reconstruct the full file and dispatch a single image
// block whose saved file size equals the sum of the chunks.
func TestWSAttachment_MultiChunk_256KiBBoundary(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	totalSize := 300 * 1024
	body := make([]byte, totalSize)
	body[0] = 0xFF
	body[1] = 0xD8
	body[2] = 0xFF
	for i := 3; i < totalSize; i++ {
		body[i] = byte(i % 256)
	}

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "big1", "name": "big.jpg", "mime": "image/jpeg", "size": totalSize,
	})
	chunkSize := 256 * 1024
	for offset := 0; offset < totalSize; offset += chunkSize {
		end := min(offset+chunkSize, totalSize)
		writeWSBinary(t, ws, body[offset:end])
	}
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "big1"})
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "see this",
		"attachments": []map[string]any{
			{"id": "big1", "name": "big.jpg", "mime": "image/jpeg", "size": totalSize},
		},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}

	// Saved file exists and its size matches the declared total.
	imagesDir := filepath.Join(c.mediaCache.RootDir, string(media.CategoryImage))
	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", imagesDir, err)
	}
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() == int64(totalSize) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected one image file of size %d under %s/", totalSize, imagesDir)
	}
}

// TestWSAttachment_SizeMismatch_SendsError_NotSaved verifies the size-mismatch
// branch: attachment_start declares size=N but client sends fewer bytes.
// handleEnd returns a "size mismatch" error and the attachment never lands
// in saved — so a subsequent user_message commit would also fail with
// "missing attachment". The new model has no text-only fallback: a failed
// upload means the user must retry the attachment before committing.
func TestWSAttachment_SizeMismatch_SendsError_NotSaved(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": 100,
	})
	writeWSBinary(t, ws, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A,
		0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14,
		0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E,
		0x1F, 0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
		0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F, 0x30, 0x31, 0x32})
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "size mismatch") {
		t.Errorf("error = %q, want substring 'size mismatch'", errMsg)
	}

	// New model: no text-only fallback dispatch — engine receives nothing.
	if waitFor(300*time.Millisecond, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls)+len(mock.queryWithContentCalls) != 0
	}) {
		mock.mu.Lock()
		t.Fatalf("dispatched despite size mismatch: queryCalls=%d queryWithContentCalls=%d",
			len(mock.queryCalls), len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
}

// TestWSAttachment_NestedStart_SendsError verifies a second attachment_start
// arriving before the first attachment_end produces an "in progress" error
// and preserves the first attachment's active state.
func TestWSAttachment_NestedStart_SendsError(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	// Second start before a1 ends — must be rejected.
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a2", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "in progress") {
		t.Errorf("error = %q, want substring 'in progress'", errMsg)
	}

	// Complete both attachments then commit.
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a2", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a2"})
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "two",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1 after commit", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	if len(call.content) != 3 {
		t.Fatalf("content blocks = %d, want 3 (text + a1 + a2)", len(call.content))
	}
	if call.content[0].Type != types.ContentTypeText || call.content[0].Text != "two" {
		t.Errorf("content[0] = %+v, want text 'two'", call.content[0])
	}
	if call.content[1].Type != types.ContentTypeImage {
		t.Errorf("content[1] = %+v, want image", call.content[1])
	}
	if call.content[2].Type != types.ContentTypeImage {
		t.Errorf("content[2] = %+v, want image", call.content[2])
	}
}

// TestWSAttachment_UserMessageDuringUpload_SendsError verifies that a
// user_message commit arriving while an attachment_start is still mid-stream
// (binary/end not yet sent) is rejected with an "upload in progress" error.
// The user must wait for attachment_end before committing.
func TestWSAttachment_UserMessageDuringUpload_SendsError(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	// Start a1 upload (activeID=a1, no binary/end yet).
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	// Commit while upload is in flight — server must refuse.
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "first attempt rejected",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "upload in progress") {
		t.Errorf("error = %q, want substring 'upload in progress'", errMsg)
	}

	// Finish a1 then commit again — now it dispatches.
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "second attempt commits",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1 after commit", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if got := mock.queryWithContentCalls[0].content[0].Text; got != "second attempt commits" {
		t.Errorf("dispatched text = %q, want 'second attempt commits' (first user_message rejected)", got)
	}
	if len(mock.queryWithContentCalls[0].content) != 2 {
		t.Errorf("content blocks = %d, want 2 (text + 1 image)", len(mock.queryWithContentCalls[0].content))
	}
}

// TestWSAttachment_NoMediaCache_UploadFails_CommitFails verifies the nil-cache
// branch produces two distinct error frames: handleEnd's "no media cache"
// (upload phase), and the commit-time "missing attachment uploads" because
// the failed id never landed in saved. The new model rejects the entire
// user_message when any attachment is missing — no text-only fallback.
func TestWSAttachment_NoMediaCache_UploadFails_CommitFails(t *testing.T) {
	c := newTestConnector(t) // no SetMediaCache — mediaCache stays nil
	mux := attachChatMux(c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	uploadErr := readWSError(t, ws)
	if !strings.Contains(uploadErr, "no media cache") {
		t.Errorf("first error = %q, want substring 'no media cache'", uploadErr)
	}

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "this text is also rejected",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	commitErr := readWSError(t, ws)
	if !strings.Contains(commitErr, "missing attachment uploads") || !strings.Contains(commitErr, "a1") {
		t.Errorf("second error = %q, want 'missing attachment uploads' and 'a1'", commitErr)
	}

	// No dispatch — text is also rejected (atomic two-phase commit).
	if waitFor(300*time.Millisecond, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls)+len(mock.queryWithContentCalls) != 0
	}) {
		mock.mu.Lock()
		t.Fatalf("dispatched despite upload+commit failure: queryCalls=%d queryWithContentCalls=%d",
			len(mock.queryCalls), len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
}
