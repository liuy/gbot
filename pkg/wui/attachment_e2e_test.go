package wui

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
// an httptest server exposing /ws/chat and POST /api/upload.
func setupAttachmentServer(t *testing.T) (*WUIConnector, *httptest.Server) {
	t.Helper()
	// Sandbox the send-time parse cache (resolved from HOME).
	t.Setenv("HOME", t.TempDir())
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

// attachChatMux mounts the chat WS and the upload route on a fresh mux. Kept
// inline (not added to helpers_test.go) because the e2e tests in this file
// are the only callers.
func attachChatMux(c *WUIConnector) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterChatWS(mux, c)
	RegisterUploadRoute(mux, c)
	return mux
}

// postUpload uploads body as the named file through the mounted /api/upload
// route and decodes the 200 response; fatal on any other status.
func postUpload(t *testing.T, srv *httptest.Server, name string, body []byte) uploadResponse {
	t.Helper()
	ct, buf := multipartFileBody(t, name, body)
	resp, err := http.Post(srv.URL+"/api/upload", ct, buf)
	if err != nil {
		t.Fatalf("POST /api/upload: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/upload status = %d body = %s", resp.StatusCode, raw)
	}
	var out uploadResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out
}

var uploadImageID = regexp.MustCompile(`^i:[0-9a-f]{16}\.png$`)
var uploadDocID = regexp.MustCompile(`^d:[0-9a-f]{16}\.txt$`)
var uploadPDFID = regexp.MustCompile(`^d:[0-9a-f]{16}\.pdf$`)

// TestUpload_Image_RoundTrip drives the full flow over the mounted route:
// one multipart POST, then a WS commit frame referencing the returned id —
// mock.QueryWithContent fires with text + a base64 image block whose decoded
// bytes equal the fixture.
func TestUpload_Image_RoundTrip(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	resp := postUpload(t, srv, "photo.png", minimalPNGAttachment)
	if !uploadImageID.MatchString(resp.ID) {
		t.Fatalf("id = %q, want i:<16hex>.png", resp.ID)
	}
	if resp.Mime != "image/png" || resp.Kind != "image" || resp.Size != int64(len(minimalPNGAttachment)) {
		t.Fatalf("resp = %+v, want {image/png image %d}", resp, len(minimalPNGAttachment))
	}
	// The cache object exists under images/ and byte-equals the fixture.
	objPath := filepath.Join(c.mediaCache.RootDir, string(media.CategoryImage), strings.TrimPrefix(resp.ID, "i:"))
	got, err := os.ReadFile(objPath)
	if err != nil {
		t.Fatalf("read cached object: %v", err)
	}
	if !bytes.Equal(got, minimalPNGAttachment) {
		t.Errorf("cached object bytes diverge from the uploaded fixture")
	}

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "what is this",
		"attachments": []map[string]any{
			{"id": resp.ID, "name": "photo.png", "mime": resp.Mime, "size": resp.Size},
		},
	})
	if !waitFor(2*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		t.Fatalf("queryWithContentCalls = %d, want 1", len(mock.queryWithContentCalls))
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
}

// TestUpload_Document_RoundTrip covers both document shapes: plain text and
// the pdf fixture. Documents save as reference blocks (no parse artifacts).
func TestUpload_Document_RoundTrip(t *testing.T) {
	c, srv := setupAttachmentServer(t)

	txt := postUpload(t, srv, "notes.txt", []byte("hello notes"))
	if !uploadDocID.MatchString(txt.ID) {
		t.Fatalf("txt id = %q, want d:<16hex>.txt", txt.ID)
	}
	if txt.Kind != "document" || txt.Mime != "text/plain" || txt.Size != 11 {
		t.Fatalf("txt resp = %+v, want {text/plain document 11}", txt)
	}

	pdf := postUpload(t, srv, "report.pdf", pdfFixture)
	if !uploadPDFID.MatchString(pdf.ID) {
		t.Fatalf("pdf id = %q, want d:<16hex>.pdf", pdf.ID)
	}
	if pdf.Mime != "application/pdf" || pdf.Kind != "document" {
		t.Fatalf("pdf resp = %+v, want {application/pdf document}", pdf)
	}

	// Both objects live under documents/; documents/parse/ is untouched and
	// no temp file survived.
	for _, id := range []string{txt.ID, pdf.ID} {
		objPath := filepath.Join(c.mediaCache.RootDir, string(media.CategoryDocument), strings.TrimPrefix(id, "d:"))
		if _, err := os.Stat(objPath); err != nil {
			t.Errorf("cached document %s missing: %v", objPath, err)
		}
	}
	parseDir := filepath.Join(c.mediaCache.RootDir, string(media.CategoryParse))
	if entries, err := os.ReadDir(parseDir); err != nil {
		t.Fatalf("ReadDir parse: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("documents/parse/ = %v, want empty (no parse artifacts at upload time)", entries)
	}
	for _, f := range walkRegularFiles(t, c.mediaCache.RootDir) {
		if strings.HasPrefix(filepath.Base(f), "save-") {
			t.Errorf("leftover temp %s after a clean upload", f)
		}
	}
}

// TestUploadCommit_SurvivesReconnect is the incident regression: the upload
// and the commit ride DIFFERENT WS connections. With content-addressed ids
// there is no connection- or session-scoped state to lose — the commit
// resolves purely from the cache.
func TestUploadCommit_SurvivesReconnect(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	resp := postUpload(t, srv, "photo.png", minimalPNGAttachment)

	wsA := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, wsA)
	if err := wsA.Close(); err != nil {
		t.Fatalf("close conn A: %v", err)
	}
	// Give the server a moment to retire conn A before dialing B.
	time.Sleep(100 * time.Millisecond) // REAL-TIME

	wsB := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, wsB)
	writeWSText(t, wsB, map[string]any{
		"type": "message",
		"text": "committed from a fresh connection",
		"attachments": []map[string]any{
			{"id": resp.ID, "name": "photo.png", "mime": resp.Mime, "size": resp.Size},
		},
	})
	if !waitFor(2*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryWithContentCalls) == 1
	}) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		t.Fatalf("queryWithContentCalls = %d, want 1 (ids are connection-independent)", len(mock.queryWithContentCalls))
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	call := mock.queryWithContentCalls[0]
	found := false
	for _, cb := range call.content {
		if cb.Type != types.ContentTypeImage || cb.Source == nil {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(cb.Source.Data)
		if err != nil {
			t.Fatalf("image data not base64: %v", err)
		}
		if bytes.Equal(decoded, minimalPNGAttachment) {
			found = true
		}
	}
	if !found {
		t.Fatalf("dispatch missing an image block with the fixture bytes: %+v", call.content)
	}
}

// TestUploadCommit_SameIDTwice pins idempotence: a repeated commit with the
// same id dispatches again (the cache object outlives any single commit).
func TestUploadCommit_SameIDTwice(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	resp := postUpload(t, srv, "notes.txt", []byte("hello notes"))

	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	for i := 0; i < 2; i++ {
		writeWSText(t, ws, map[string]any{
			"type": "message",
			"text": "send",
			"attachments": []map[string]any{
				{"id": resp.ID, "name": "notes.txt", "mime": resp.Mime, "size": resp.Size},
			},
		})
	}
	want := 0
	if !waitFor(5*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		want = len(mock.queryWithContentCalls)
		return want == 2
	}) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		t.Fatalf("queryWithContentCalls = %d, want 2 (repeated ids dispatch again)", want)
	}
	// No error frame may appear between the two dispatches.
	if raw, ok := readWSFrameDeadlineA3(t, ws, 300*time.Millisecond); ok {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &head) == nil && head.Type == "error" {
			t.Fatalf("unexpected error frame between idempotent commits: %s", raw)
		}
	}
}
