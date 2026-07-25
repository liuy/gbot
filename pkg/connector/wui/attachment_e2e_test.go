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

// TestWSAttachment_RoundTrip_Image drives the full B-plan flow:
//
//	user_message (attachments:[...]) → attachment_start → binary chunks →
//	attachment_end → mock.QueryWithContent fires with text + image block.
//
// Asserts the dispatched content has the expected shape (text first, image
// with valid base64 data second) and that no stray mock.Query fires.
func TestWSAttachment_RoundTrip_Image(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "what is this",
		"attachments": []map[string]any{
			{"id": "a1", "name": "photo.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "photo.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

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
// body. (fileread parses .txt reliably; the .pdf path is covered by the
// inbound_test.go parseDocument unit tests.)
func TestWSAttachment_RoundTrip_Document(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	body := []byte("the quick brown fox jumps over the lazy dog")
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "summarize this",
		"attachments": []map[string]any{
			{"id": "d1", "name": "story.txt", "mime": "text/plain", "size": len(body)},
		},
	})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "d1", "name": "story.txt", "mime": "text/plain", "size": len(body),
	})
	writeWSBinary(t, ws, body)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "d1"})

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

	// Build a deterministic JPEG (300 KiB) so the engine path decodes it
	// successfully. We use the same magic bytes that SniffImageMime
	// recognizes so the file is classified as image.
	totalSize := 300 * 1024
	// JPEG SOI marker + content bytes (the resize path only needs valid
	// magic; ReadAsImageBlock will fail on this synthetic body, but the
	// image category is what we are asserting on via the saved file).
	body := make([]byte, totalSize)
	body[0] = 0xFF
	body[1] = 0xD8
	body[2] = 0xFF
	for i := 3; i < totalSize; i++ {
		body[i] = byte(i % 256)
	}

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "see this",
		"attachments": []map[string]any{
			{"id": "big1", "name": "big.jpg", "mime": "image/jpeg", "size": totalSize},
		},
	})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "big1", "name": "big.jpg", "mime": "image/jpeg", "size": totalSize,
	})
	// Split into 256 KiB + 44 KiB chunks (mirrors SendFile's chunking).
	chunkSize := 256 * 1024
	for offset := 0; offset < totalSize; offset += chunkSize {
		end := min(offset+chunkSize, totalSize)
		writeWSBinary(t, ws, body[offset:end])
	}
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "big1"})

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

// TestWSAttachment_SizeMismatch_SendsErrorAndDropsAttachment verifies the
// size-mismatch branch: attachment_start declares size=N but client sends
// fewer bytes. Server must emit a WS error frame, drop that attachment, and
// still dispatch the (text-only) turn so the engine sees the user's message.
// When ALL attachments drop, buildContents returns [] and handleMessageInbound
// takes the len(content)==0 path (engine.Query, not QueryWithContent).
func TestWSAttachment_SizeMismatch_SendsErrorAndDropsAttachment(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "still send this",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": 100},
		},
	})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": 100,
	})
	// Send 50 bytes — less than declared 100.
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

	// Engine still receives the text via Query (text-only fallback path —
	// all-attachments-dropped produces an empty contents slice).
	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryCalls = %d, want 1 (text dispatched after drop)", len(mock.queryCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryCalls[0]
	if call.userMessage != "still send this" {
		t.Errorf("Query.userMessage = %q, want 'still send this'", call.userMessage)
	}
	if len(mock.queryWithContentCalls) != 0 {
		t.Errorf("queryWithContentCalls = %d, want 0 (text-only fallback)", len(mock.queryWithContentCalls))
	}
}

// TestWSAttachment_NestedStart_SendsError verifies a second attachment_start
// arriving before the first attachment_end produces an "in progress" error
// and preserves the first attachment's active state — subsequent binary
// chunks for the first id still land in the first buffer.
func TestWSAttachment_NestedStart_SendsError(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "two",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
			{"id": "a2", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
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

	// Complete both attachments — bytes for a1 still land in the first
	// buffer (nested-start did NOT clear it). The dispatch fires with both
	// saved attachments once readyToDispatch goes true.
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a2", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a2"})

	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryWithContentCalls = %d, want 1 after both attachments complete", len(mock.queryWithContentCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	// Dispatch fires with text + a1 image + a2 image (3 blocks: text + 2 imgs).
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

// TestWSAttachment_SecondUserMessageWhileWaiting_SendsError verifies a
// user_message with attachments arriving while a batch is still in flight
// produces an "uploads already in progress" error.
func TestWSAttachment_SecondUserMessageWhileWaiting_SendsError(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "first",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	// Second user_message with attachments while a1 batch still in flight.
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "second",
		"attachments": []map[string]any{
			{"id": "b1", "name": "y.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "uploads already in progress") {
		t.Errorf("error = %q, want substring 'uploads already in progress'", errMsg)
	}

	// Complete the first batch — engine dispatches the first message only.
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

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
	if got := mock.queryWithContentCalls[0].content[0].Text; got != "first" {
		t.Errorf("dispatched text = %q, want 'first' (second message rejected)", got)
	}
}

// TestWSAttachment_NoMediaCache_SendsErrorAndDropsAttachment verifies the
// nil-cache branch: handleEnd returns "no media cache" error and the
// attachment is dropped. If text was provided, the turn still dispatches
// via the Query path (buildContents returns [] when all attachments drop).
func TestWSAttachment_NoMediaCache_SendsErrorAndDropsAttachment(t *testing.T) {
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
		"type": "message",
		"text": "fallback text",
		"attachments": []map[string]any{
			{"id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment)},
		},
	})
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "a1", "name": "x.png", "mime": "image/png", "size": len(minimalPNGAttachment),
	})
	writeWSBinary(t, ws, minimalPNGAttachment)
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "a1"})

	errMsg := readWSError(t, ws)
	if !strings.Contains(errMsg, "no media cache") {
		t.Errorf("error = %q, want substring 'no media cache'", errMsg)
	}

	// Text fallback dispatches via Query (text-only path — contents empty).
	if !waitFor(time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls) == 1
	}) {
		mock.mu.Lock()
		t.Fatalf("queryCalls = %d, want 1 (text dispatched after drop)", len(mock.queryCalls))
		mock.mu.Unlock()
		return
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryCalls[0]
	if call.userMessage != "fallback text" {
		t.Errorf("Query.userMessage = %q, want 'fallback text'", call.userMessage)
	}
	if len(mock.queryWithContentCalls) != 0 {
		t.Errorf("queryWithContentCalls = %d, want 0 (text-only fallback)", len(mock.queryWithContentCalls))
	}
}
