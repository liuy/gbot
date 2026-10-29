package wui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
)

// withUploadCaps injects small cap values so boundary tests never stream tens
// of MiB. Mutates package state — callers must NOT use t.Parallel().
func withUploadCaps(t *testing.T, image, document int64) {
	t.Helper()
	oldImage, oldDoc := maxUploadImageSize, maxUploadDocSize
	maxUploadImageSize, maxUploadDocSize = image, document
	t.Cleanup(func() {
		maxUploadImageSize, maxUploadDocSize = oldImage, oldDoc
	})
}

// newUploadConnector returns a connector with a real media store, for direct
// handleUpload calls (no socket, no route).
func newUploadConnector(t *testing.T) (*WUIConnector, *media.Store) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)
	return c, store
}

// multipartFileBody builds a single-file multipart body named filename.
func multipartFileBody(t *testing.T, filename string, body []byte) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(uploadFormField, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(body); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return w.FormDataContentType(), &buf
}

// newUploadReq builds a POST /api/upload request; contentLength overrides the
// declared size (-1 keeps chunked semantics).
func newUploadReq(t *testing.T, contentType string, body io.Reader, contentLength int64) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = contentLength
	return req
}

// callUpload invokes handleUpload directly and returns the recorder.
func callUpload(t *testing.T, c *WUIConnector, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c.handleUpload(rec, req)
	return rec
}

func decodeUploadBody(t *testing.T, rec *httptest.ResponseRecorder) (uploadResponse, string) {
	t.Helper()
	var resp uploadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return resp, env.Error
}

// walkRegularFiles lists every regular file under dir, recursively.
func walkRegularFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

// cachedObjectCount counts regular files under images/ and documents/
// (documents/parse/ included via the recursive walk).
func cachedObjectCount(t *testing.T, store *media.Store) int {
	t.Helper()
	return len(walkRegularFiles(t, filepath.Join(store.RootDir, string(media.CategoryImage)))) +
		len(walkRegularFiles(t, filepath.Join(store.RootDir, string(media.CategoryDocument))))
}

// pngOfSize returns a real PNG (magic bytes from the fixture) padded to size.
func pngOfSize(size int) []byte {
	out := make([]byte, size)
	copy(out, minimalPNGAttachment)
	return out
}

// ---------------------------------------------------------------------------
// Per-kind caps (3, 3b, 3c, 3d, 3e) and the three distinct size bounds (4, 4b, 5)
// ---------------------------------------------------------------------------

func TestUpload_DocumentOverCap_Rejected(t *testing.T) {
	withUploadCaps(t, 4<<20, 1<<20)
	c, store := newUploadConnector(t)
	ct, body := multipartFileBody(t, "big.bin", bytes.Repeat([]byte("x"), 1<<20+1))
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "upload exceeds 1.0MB limit" {
		t.Errorf("error = %q, want the injected document cap's own label", errMsg)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0 (nothing landed)", n)
	}
}

func TestUpload_ImageOverImageCap_Rejected(t *testing.T) {
	withUploadCaps(t, 1<<10, 4<<20)
	c, store := newUploadConnector(t)
	ct, body := multipartFileBody(t, "photo.png", pngOfSize(1025))
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "upload exceeds 1.0KB limit" {
		t.Errorf("error = %q, want the image cap's label (the image bytes were accepted as an image)", errMsg)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_ImageAboveDocCap_Accepted(t *testing.T) {
	// The document cap is tiny and the image is larger — only correct
	// per-kind cap selection keeps this at 200 with kind=image.
	withUploadCaps(t, 4<<20, 1<<10)
	c, store := newUploadConnector(t)
	ct, body := multipartFileBody(t, "photo.png", pngOfSize(2<<10))
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	resp, _ := decodeUploadBody(t, rec)
	if resp.Kind != "image" {
		t.Errorf("kind = %q, want image", resp.Kind)
	}
	if resp.Size != 2<<10 {
		t.Errorf("size = %d, want %d", resp.Size, 2<<10)
	}
	if got := cachedObjectCount(t, store); got != 1 {
		t.Errorf("cached files = %d, want 1", got)
	}
}

func TestUpload_DocumentExactlyAtCap_Accepted(t *testing.T) {
	withUploadCaps(t, 4<<20, 1<<20)
	c, _ := newUploadConnector(t)
	ct, body := multipartFileBody(t, "exact.bin", bytes.Repeat([]byte("y"), 1<<20))
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 (exactly at cap is accepted)", rec.Code, rec.Body.String())
	}
	resp, _ := decodeUploadBody(t, rec)
	if resp.Size != 1<<20 {
		t.Errorf("size = %d, want %d", resp.Size, 1<<20)
	}
}

// Literal sizes, not the constant symbols, so a mutated cap cannot
// self-fulfill the comparison (the attachment_test.go:466 discipline).
func TestUploadCapsExactly500MiBImageAnd50MiBDoc(t *testing.T) {
	if maxUploadImageSize != int64(500)<<20 {
		t.Errorf("maxUploadImageSize = %d, want %d (500 MiB, Anthropic Files API parity)", maxUploadImageSize, int64(500)<<20)
	}
	if maxUploadDocSize != int64(50)<<20 {
		t.Errorf("maxUploadDocSize = %d, want %d (50 MiB, the old WS protocol cap)", maxUploadDocSize, int64(50)<<20)
	}
}

// countingBody counts the bytes the handler actually pulled from the body.
type countingBody struct {
	r io.Reader
	n int64
}

func (cb *countingBody) Read(p []byte) (int, error) {
	n, err := cb.r.Read(p)
	cb.n += int64(n)
	return n, err
}

func (cb *countingBody) Close() error { return nil }

func TestUpload_DeclaredOverCapFastReject(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, store := newUploadConnector(t)
	body := &countingBody{r: strings.NewReader("")}
	req := newUploadReq(t, "multipart/form-data; boundary=x", body, int64(500)<<20+8<<10+1)
	rec := callUpload(t, c, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "upload exceeds 500.0MB limit" {
		t.Errorf("error = %q, want the image-cap label", errMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=413 reason=upload exceeds 500.0MB limit bytes=0 name=")
	if body.n != 0 {
		t.Errorf("body bytes read = %d, want 0 — the declared-size check must fire before any read", body.n)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_DeclaredAtCapNotFastRejected(t *testing.T) {
	c, _ := newUploadConnector(t)
	// A 9-byte junk body with a lying declared size of exactly the bound: the
	// handler must proceed past the header check (not a 413) and read the body.
	// (An empty body could not falsify "the body was read" — the counter would
	// stay at 0 either way.)
	body := &countingBody{r: strings.NewReader("junk-data")}
	req := newUploadReq(t, "multipart/form-data; boundary=x", body, uploadBodyBound())
	rec := callUpload(t, c, req)
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("status = 413 at exactly the bound, want the request to proceed (kills >= vs >)")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400 from the later parse", rec.Code, rec.Body.String())
	}
	if body.n != 9 {
		t.Errorf("body bytes read = %d, want 9 — the body must be consumed by the parse", body.n)
	}
}

func TestUpload_BodyOverTransportBound_MaxBytesError(t *testing.T) {
	logs := installUploadLogCapture(t)
	withUploadCaps(t, 1<<10, 1<<10) // uploadBodyBound() = 1<<10 + 8<<10 = 9216
	c, store := newUploadConnector(t)
	if bound := uploadBodyBound(); bound != 9216 {
		t.Fatalf("uploadBodyBound() = %d, want 9216", bound)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormField("name")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	if _, err := fw.Write(bytes.Repeat([]byte("j"), 1<<20)); err != nil {
		t.Fatalf("write junk field: %v", err)
	}
	ff, err := w.CreateFormFile(uploadFormField, "x.bin")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := ff.Write(bytes.Repeat([]byte("z"), 1<<10)); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	// ContentLength -1: the declared fast-reject must not fire (chunked shape).
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, -1))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "upload body exceeds 9.0KB" {
		t.Errorf("error = %q, want the transport-bound text (distinct from the per-kind 413 text)", errMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=413 reason=upload body exceeds 9.0KB bytes=0 name=")
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_SaveFailure_Returns500(t *testing.T) {
	c, store := newUploadConnector(t)
	// Park a directory at the exact destination so the atomic Rename fails.
	if err := os.Mkdir(store.Path(media.CategoryDocument, pdfFixture, ".pdf"), 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	ct, body := multipartFileBody(t, "report.pdf", pdfFixture)
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); !strings.HasPrefix(errMsg, "save failed") {
		t.Errorf("error = %q, want it starting with \"save failed\"", errMsg)
	}
	dst := store.Path(media.CategoryDocument, pdfFixture, ".pdf")
	if info, err := os.Stat(dst); err != nil || !info.IsDir() {
		t.Errorf("dst %s must still be the parked directory, got info=%v err=%v", dst, info, err)
	}
	for _, f := range walkRegularFiles(t, filepath.Join(store.RootDir, string(media.CategoryDocument))) {
		if strings.HasPrefix(filepath.Base(f), "save-") {
			t.Errorf("leftover temp %s after rename failure", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Classification / envelope shape (6, 7, 19, 20, 21, 23)
// ---------------------------------------------------------------------------

// prepUploadCommitConn dials the WS side and arms the mock for commit tests.
func prepUploadCommitConn(t *testing.T, c *WUIConnector, srv *httptest.Server) {
	t.Helper()
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }
}

// commitUploadedId sends the commit frame for id and waits for one dispatch,
// returning the dispatched blocks.
func commitUploadedId(t *testing.T, c *WUIConnector, ws *websocket.Conn, text, id, name, mime string, size int64) []types.ContentBlock {
	t.Helper()
	mock := c.mock()
	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": text,
		"attachments": []map[string]any{
			{"id": id, "name": name, "mime": mime, "size": size},
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
	return mock.queryWithContentCalls[0].content
}

func TestUpload_GarbageBytes_ClassifiedAsDocument(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	prepUploadCommitConn(t, c, srv)

	resp := postUpload(t, srv, "photo.png", []byte("not an image at all"))
	if resp.Kind != "document" {
		t.Fatalf("kind = %q, want document (sniff failure stays a document)", resp.Kind)
	}

	blocks := commitUploadedId(t, c, ws, "what is this", resp.ID, "photo.png", resp.Mime, resp.Size)
	if len(blocks) != 2 {
		t.Fatalf("content blocks = %d, want 2 (text + attachment)", len(blocks))
	}
	if blocks[1].Type != types.ContentTypeDocument {
		t.Errorf("blocks[1].Type = %v, want document (garbage must NOT become an image)", blocks[1].Type)
	}
}

func TestUpload_NotMultipart(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, store := newUploadConnector(t)
	rec := callUpload(t, c, newUploadReq(t, "application/octet-stream", bytes.NewReader([]byte("raw bytes")), 9))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "expected multipart/form-data" {
		t.Errorf("error = %q, want the exact 415 message", errMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=415 reason=expected multipart/form-data bytes=0 name=")
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_NoFileField(t *testing.T) {
	c, store := newUploadConnector(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormField("name")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	if _, err := fw.Write([]byte("x")); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, int64(buf.Len())))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); !strings.Contains(errMsg, "file") {
		t.Errorf("error = %q, want it naming the file field", errMsg)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_TruncatedMultipart(t *testing.T) {
	c, store := newUploadConnector(t)
	// File part present but the terminating boundary missing: the part read
	// dies with unexpected EOF mid-stream.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(uploadFormField, "notes.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte("hello notes")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	// Deliberately no w.Close(): the final boundary never lands.
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, int64(buf.Len())))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); !strings.Contains(errMsg, "read failed") {
		t.Errorf("error = %q, want it containing \"read failed\" (client died mid-body)", errMsg)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

func TestUpload_ZeroByteFile_CommitsEmptyDocument(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	prepUploadCommitConn(t, c, srv)

	resp := postUpload(t, srv, "empty.bin", []byte{})
	if resp.Size != 0 {
		t.Errorf("size = %d, want 0", resp.Size)
	}
	if !regexp.MustCompile(`^d:[0-9a-f]{16}\.bin$`).MatchString(resp.ID) {
		t.Errorf("id = %q, want d:<16hex>.bin", resp.ID)
	}

	blocks := commitUploadedId(t, c, ws, "empty doc", resp.ID, "empty.bin", resp.Mime, resp.Size)
	if len(blocks) != 2 || blocks[1].Type != types.ContentTypeDocument {
		t.Fatalf("blocks = %v, want [text, document] for a zero-byte upload", blocks)
	}
	if blocks[1].Size != 0 {
		t.Errorf("document Size = %d, want 0", blocks[1].Size)
	}
}

func TestSanitizeUploadExt_Table(t *testing.T) {
	cases := []struct{ in, want string }{
		{".备份", ""},
		{".PDF", ".pdf"},
		{".PPTX", ".pptx"},
		{".pptx", ".pptx"},
		{".txt", ".txt"},
		{"", ""},
		{".", ""}, // a bare dot has no extension characters
		{"txt", ""},
		{".a", ".a"},                   // the shortest accepted ext: "." plus 1 char
		{".aaaaaaaaaa", ".aaaaaaaaaa"}, // the longest: "." plus 10 chars
		{".aaaaaaaaaaa", ""},           // 11 chars after the dot
		{".png/../x", ""},
		{".a.b", ""},
		{".png", ".png"},
	}
	for _, tc := range cases {
		if got := sanitizeUploadExt(tc.in); got != tc.want {
			t.Errorf("sanitizeUploadExt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUpload_NonASCIIFilename_RoundTrips(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	prepUploadCommitConn(t, c, srv)

	resp := postUpload(t, srv, "报告.备份", []byte("report body"))
	if !regexp.MustCompile(`^d:[0-9a-f]{16}\.bin$`).MatchString(resp.ID) {
		t.Fatalf("id = %q, want d:<16hex>.bin (the unsanitized ext would make the id unparseable)", resp.ID)
	}
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: resp.ID, Name: "报告.备份"}})
	if missing != "" {
		t.Fatalf("resolveAttachments missing = %q, want empty", missing)
	}
	got, err := os.ReadFile(contents[0].Source.Path)
	if err != nil {
		t.Fatalf("read resolved path: %v", err)
	}
	if string(got) != "report body" {
		t.Errorf("resolved bytes = %q, want %q", got, "report body")
	}

	blocks := commitUploadedId(t, c, ws, "report attached", resp.ID, "报告.备份", resp.Mime, resp.Size)
	if len(blocks) != 2 || blocks[1].Type != types.ContentTypeDocument {
		t.Fatalf("blocks = %v, want [text, document] — the full round trip this bug broke", blocks)
	}
}

func TestUpload_JunkPartBeforeFileField_UsesFileField(t *testing.T) {
	c, store := newUploadConnector(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormField("name")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	if _, err := fw.Write([]byte("x")); err != nil {
		t.Fatalf("write field: %v", err)
	}
	ff, err := w.CreateFormFile(uploadFormField, "notes.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := ff.Write([]byte("hello notes")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, int64(buf.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	resp, _ := decodeUploadBody(t, rec)
	if resp.Kind != "document" || resp.Size != 11 {
		t.Errorf("resp = %+v, want the FILE part (document, 11 bytes of \"hello notes\"), not the junk field", resp)
	}
	wantPath := store.Path(media.CategoryDocument, []byte("hello notes"), ".txt")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected object at %s: %v", wantPath, err)
	}
	if got := cachedObjectCount(t, store); got != 1 {
		t.Errorf("cached files = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// ID codec and commit resolution (8, 9, 12, 22)
// ---------------------------------------------------------------------------

func TestParseAttachmentID_Table(t *testing.T) {
	accepts := []struct {
		id   string
		cat  media.Category
		name string
	}{
		{"i:0123456789abcdef", media.CategoryImage, "0123456789abcdef"},
		{"i:0123456789abcdef.png", media.CategoryImage, "0123456789abcdef.png"},
		{"d:0123456789abcdef.bin", media.CategoryDocument, "0123456789abcdef.bin"},
		{"d:ffffffffffffffff", media.CategoryDocument, "ffffffffffffffff"},
	}
	for _, tc := range accepts {
		cat, name, ok := parseAttachmentID(tc.id)
		if !ok {
			t.Errorf("parseAttachmentID(%q) ok=false, want true", tc.id)
			continue
		}
		if cat != tc.cat || name != tc.name {
			t.Errorf("parseAttachmentID(%q) = (%q, %q), want (%q, %q)", tc.id, cat, name, tc.cat, tc.name)
		}
	}
	rejects := []string{
		"",
		"d:../../etc/passwd",
		"i:0123456789abcdef.PNG/../../x",
		"x:0123456789abcdef.png",
		"i:0123456789abcdefA.png", // 17 hex chars
		"d:/abs/path",
		"i:0123456789ABCDEF.png", // uppercase hex
		"i:0123456789abcd.png",   // 15 hex chars
		"d:0123456789abcdef.png.bak",
	}
	for _, id := range rejects {
		if _, _, ok := parseAttachmentID(id); ok {
			t.Errorf("parseAttachmentID(%q) ok=true, want false", id)
		}
	}
}

func TestResolveAttachments_MissingAndMalformed(t *testing.T) {
	c, _ := newUploadConnector(t)
	ct, body := multipartFileBody(t, "notes.txt", []byte("hello notes"))
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	real, _ := decodeUploadBody(t, rec)

	atts := []inboundAttachment{
		{ID: real.ID, Name: "notes.txt"},
		{ID: "d:ffffffffffffffff.txt", Name: "absent.txt"},
		{ID: "junk", Name: "junk.bin"},
	}
	contents, missing := c.resolveAttachments(atts)
	if contents != nil {
		t.Fatalf("contents = %+v, want nil when ids are missing", contents)
	}
	if missing != "d:ffffffffffffffff.txt, junk" {
		t.Fatalf("missing = %q, want %q (absent then malformed, in order, comma-joined)", missing, "d:ffffffffffffffff.txt, junk")
	}

	// nil mediaCache: every id is a miss.
	c2 := newTestConnector(t)
	contents2, missing2 := c2.resolveAttachments([]inboundAttachment{{ID: "a"}, {ID: "b"}})
	if contents2 != nil {
		t.Fatalf("contents = %+v, want nil with nil mediaCache", contents2)
	}
	if missing2 != "a, b" {
		t.Fatalf("missing = %q, want \"a, b\"", missing2)
	}
}

func TestUploadCommit_UnknownID_ErrorsAndLives(t *testing.T) {
	c, srv := setupAttachmentServer(t)
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	mock := c.mock()
	mock.isBusyFn = func() bool { return false }
	mock.systemPromptFn = func() string { return "" }

	writeWSText(t, ws, map[string]any{
		"type": "message",
		"text": "doomed",
		"attachments": []map[string]any{
			{"id": "d:ffffffffffffffff.txt", "name": "ghost.txt", "mime": "text/plain", "size": 3},
		},
	})
	errMsg := readWSError(t, ws)
	if errMsg != "attachment not found: d:ffffffffffffffff.txt" {
		t.Errorf("error = %q, want the exact not-found line", errMsg)
	}

	// Barrier: the connection still answers a plain text message.
	writeWSText(t, ws, map[string]any{"type": "message", "text": "still alive"})
	if !waitFor(2*time.Second, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return len(mock.queryCalls) == 1
	}) {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		t.Fatalf("queryCalls = %d, want 1 after the error (connection alive)", len(mock.queryCalls))
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.queryCalls[0].userMessage != "still alive" {
		t.Errorf("dispatched text = %q, want \"still alive\"", mock.queryCalls[0].userMessage)
	}
	if len(mock.queryWithContentCalls) != 0 {
		t.Errorf("queryWithContentCalls = %d, want 0 (the unknown-id message never dispatched)", len(mock.queryWithContentCalls))
	}
}

func TestUpload_NoMediaCache(t *testing.T) {
	logs := installUploadLogCapture(t)
	c := newTestConnector(t) // no SetMediaCache
	mux := http.NewServeMux()
	RegisterUploadRoute(mux, c)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ct, body := multipartFileBody(t, "photo.png", minimalPNGAttachment)
	resp, err := http.Post(srv.URL+"/api/upload", ct, body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if env.Error != "upload disabled: no media cache" {
		t.Errorf("error = %q, want the exact no-cache message", env.Error)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=503 reason=upload disabled: no media cache bytes=0 name=")
}

func TestResolveAttachments_UsesCacheExtNotClientName(t *testing.T) {
	c, _ := newUploadConnector(t)
	ct, body := multipartFileBody(t, "report.pdf", pdfFixture)
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	uploaded, _ := decodeUploadBody(t, rec)

	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: uploaded.ID, Name: "notes.zip"}})
	if missing != "" {
		t.Fatalf("missing = %q, want empty", missing)
	}
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(contents))
	}
	src := contents[0].Source
	if src.Mime != "application/pdf" {
		t.Errorf("Mime = %q, want application/pdf (the cache basename drives classification)", src.Mime)
	}
	if src.Name != "notes.zip" {
		t.Errorf("Name = %q, want notes.zip (display name still from the client)", src.Name)
	}
	if contents[0].Type != "document" {
		t.Errorf("Type = %q, want document", contents[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Route, cachedID (13, 14, 15, 15b)
// ---------------------------------------------------------------------------

func TestUpload_RouteMountedOnMux(t *testing.T) {
	_, srv := setupAttachmentServer(t)
	resp := postUpload(t, srv, "photo.png", minimalPNGAttachment)
	if resp.Kind != "image" {
		t.Errorf("kind = %q, want image", resp.Kind)
	}
}

func TestCachedID_InCacheNoCopy(t *testing.T) {
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)

	path, err := store.Save(media.CategoryDocument, []byte("doc-bytes"), ".txt")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	before := cachedObjectCount(t, store)
	id := cachedID(store, path, "document")
	if id != "d:"+filepath.Base(path) {
		t.Fatalf("id = %q, want d:%s (no copy, the cache path binds directly)", id, filepath.Base(path))
	}
	if after := cachedObjectCount(t, store); after != before {
		t.Errorf("file count %d → %d, want unchanged (in-cache paths bind without IO)", before, after)
	}
}

func TestCachedID_OutsideCacheCopies(t *testing.T) {
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)

	src := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(src, []byte("outside bytes"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	id := cachedID(store, src, "document")
	if id == "" {
		t.Fatal("cachedID = \"\", want an id for a copyable outside file")
	}
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: id, Name: "outside.txt"}})
	if missing != "" {
		t.Fatalf("resolveAttachments missing = %q, want empty", missing)
	}
	got, err := os.ReadFile(contents[0].Source.Path)
	if err != nil {
		t.Fatalf("read resolved: %v", err)
	}
	if string(got) != "outside bytes" {
		t.Errorf("resolved bytes = %q, want %q", got, "outside bytes")
	}
	if !strings.HasPrefix(contents[0].Source.Path, filepath.Join(store.RootDir, string(media.CategoryDocument))) {
		t.Errorf("resolved path = %q, want it under the documents/ cache dir", contents[0].Source.Path)
	}
}

func TestCachedID_ParseSnapshotNotResolvable(t *testing.T) {
	c := newTestConnector(t)
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	c.SetMediaCache(store)

	// A parse snapshot: same <sha16> naming but under documents/parse/, which
	// attachment ids must never resolve.
	parsePath, err := store.Save(media.CategoryParse, []byte("# derived"), ".md")
	if err != nil {
		t.Fatalf("Save parse: %v", err)
	}
	id := cachedID(store, parsePath, "document")
	if id == "" {
		t.Fatal("cachedID = \"\", want the copy-fallback id")
	}
	if _, _, ok := parseAttachmentID(id); !ok {
		t.Fatalf("parseAttachmentID(%q) ok=false — cachedID minted an unparseable id", id)
	}
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: id, Name: "snap.md"}})
	if missing != "" {
		t.Fatalf("missing = %q, want empty", missing)
	}
	if !strings.HasPrefix(contents[0].Source.Path, filepath.Join(store.RootDir, string(media.CategoryDocument))+string(os.PathSeparator)) {
		t.Errorf("resolved path = %q, want the documents/ copy, never documents/parse/", contents[0].Source.Path)
	}
	got, err := os.ReadFile(contents[0].Source.Path)
	if err != nil {
		t.Fatalf("read resolved: %v", err)
	}
	if string(got) != "# derived" {
		t.Errorf("resolved bytes = %q, want %q", got, "# derived")
	}
}

// ---------------------------------------------------------------------------
// Concurrency, abort (16, 17, 18)
// ---------------------------------------------------------------------------

// uploadLogCapture records "msg k=v" strings for every slog record.
type uploadLogCapture struct {
	mu      sync.Mutex
	records []string
}

func (h *uploadLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *uploadLogCapture) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, sb.String())
	h.mu.Unlock()
	return nil
}

func (h *uploadLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *uploadLogCapture) WithGroup(string) slog.Handler { return h }

func (h *uploadLogCapture) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.records...)
}

func installUploadLogCapture(t *testing.T) *uploadLogCapture {
	t.Helper()
	h := &uploadLogCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return h
}

func countLogRecords(recs []string, msg string) int {
	n := 0
	for _, r := range recs {
		if strings.HasPrefix(r, msg+" ") || r == msg {
			n++
		}
	}
	return n
}

func hasRecordWith(recs []string, msg, attr string) bool {
	for _, r := range recs {
		if (strings.HasPrefix(r, msg+" ") || r == msg) && strings.Contains(r, attr) {
			return true
		}
	}
	return false
}

func TestUpload_MidBodyAbort_CleansUpAndServerLives(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, srv := setupAttachmentServer(t)

	// The incident shape: the client abandons the request after a few KiB of
	// a declared-huge body. EOF mid-part, no terminating boundary.
	pr, pw := io.Pipe()
	w := multipart.NewWriter(pw)
	ct := w.FormDataContentType()
	go func() {
		fw, err := w.CreateFormFile(uploadFormField, "huge.bin")
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := fw.Write(bytes.Repeat([]byte("h"), 4096)); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		// Abandon: close without the final boundary.
		_ = pw.Close()
	}()
	resp, err := http.Post(srv.URL+"/api/upload", ct, pr)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body) // response content irrelevant by design
		_ = resp.Body.Close()
	} else {
		t.Logf("aborted request transport error (acceptable): %v", err)
	}

	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		if countLogRecords(logs.snapshot(), "wui:upload rejected") == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	recs := logs.snapshot()
	if n := countLogRecords(recs, "wui:upload rejected"); n != 1 {
		t.Fatalf("wui:upload rejected records = %d, want exactly 1: %v", n, recs)
	}
	if n := countLogRecords(recs, "wui:upload ok"); n != 0 {
		t.Errorf("wui:upload ok records = %d, want 0", n)
	}
	if !hasRecordWith(recs, "wui:upload rejected", "status=400") {
		t.Errorf("rejected record missing status=400: %v", recs)
	}
	if n := cachedObjectCount(t, c.mediaCache); n != 0 {
		t.Errorf("cached files = %d, want 0 (no object, no temp)", n)
	}

	// The daemon survives: a normal upload then commit both succeed.
	uploaded := postUpload(t, srv, "photo.png", minimalPNGAttachment)
	if uploaded.Kind != "image" {
		t.Fatalf("post-abort upload kind = %q, want image", uploaded.Kind)
	}
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)
	prepUploadCommitConn(t, c, srv)
	blocks := commitUploadedId(t, c, ws, "after the abort", uploaded.ID, "photo.png", uploaded.Mime, uploaded.Size)
	if len(blocks) != 2 || blocks[1].Type != types.ContentTypeImage {
		t.Fatalf("blocks = %v, want [text, image] (server lives)", blocks)
	}
}

func TestUpload_ConcurrentIdentical_SingleIntactObject(t *testing.T) {
	// Same package-var seam as withUploadCaps: the real cap-2 semaphore would
	// make 4-simultaneous-200 scheduling-dependent; test 18 owns the 503 path.
	oldSem := uploadSem
	uploadSem = make(chan struct{}, 4)
	t.Cleanup(func() { uploadSem = oldSem })

	c, srv := setupAttachmentServer(t)

	var wg sync.WaitGroup
	responses := make([]uploadResponse, 4)
	errs := make([]error, 4)
	for i := range responses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ct, body := multipartFileBody(t, "photo.png", minimalPNGAttachment)
			resp, err := http.Post(srv.URL+"/api/upload", ct, body)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs[i] = fmt.Errorf("status %d", resp.StatusCode)
				return
			}
			if err := json.NewDecoder(resp.Body).Decode(&responses[i]); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("upload %d: %v", i, err)
		}
	}
	for i := 1; i < len(responses); i++ {
		if responses[i].ID != responses[0].ID {
			t.Errorf("upload %d id = %q, want %q (content-addressed ids are equal)", i, responses[i].ID, responses[0].ID)
		}
	}

	imagesDir := filepath.Join(c.mediaCache.RootDir, string(media.CategoryImage))
	files := walkRegularFiles(t, imagesDir)
	if len(files) != 1 {
		t.Fatalf("images/ files = %v, want exactly one object", files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(got, minimalPNGAttachment) {
		t.Errorf("object bytes diverge from the fixture (corruption under concurrency)")
	}
}

// heldPipeUpload parks the handler inside the sniff read by holding the body
// pipe open after the part header + first bytes.
type heldPipeUpload struct {
	pw   *io.PipeWriter
	mw   *multipart.Writer
	rest []byte
	resp chan heldResult
}

type heldResult struct {
	status int
	body   string
}

func startHeldUpload(t *testing.T, srv *httptest.Server, filename string, payload []byte) *heldPipeUpload {
	t.Helper()
	pr, pw := io.Pipe()
	h := &heldPipeUpload{pw: pw, resp: make(chan heldResult, 1)}
	h.mw = multipart.NewWriter(pw)
	// The request must be in flight before any pipe write — writes into an
	// io.Pipe block until the server side reads them, so issuing them before
	// http.Post starts would deadlock.
	go func() {
		resp, err := http.Post(srv.URL+"/api/upload", h.mw.FormDataContentType(), pr)
		if err != nil {
			h.resp <- heldResult{status: -1, body: err.Error()}
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		h.resp <- heldResult{status: resp.StatusCode, body: string(body)}
	}()
	fw, err := h.mw.CreateFormFile(uploadFormField, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	// Write half the payload, then park: the handler's sniff read blocks on
	// the open pipe with the semaphore held.
	if _, err := fw.Write(payload[:len(payload)/2]); err != nil {
		t.Fatalf("write half payload: %v", err)
	}
	h.rest = payload[len(payload)/2:]
	return h
}

func (h *heldPipeUpload) release() {
	if _, err := h.pw.Write(h.rest); err != nil {
		return
	}
	_ = h.mw.Close()
	_ = h.pw.Close()
}

func TestUpload_ConcurrencyLimit_Busy503(t *testing.T) {
	logs := installUploadLogCapture(t)
	_, srv := setupAttachmentServer(t)

	// Two uploads held mid-body by open pipes.
	held1 := startHeldUpload(t, srv, "a.png", minimalPNGAttachment)
	held2 := startHeldUpload(t, srv, "b.png", minimalPNGAttachment)

	// Wait until both handlers have started (the semaphore is acquired before
	// the start log line, so two starts prove both slots are held).
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		if countLogRecords(logs.snapshot(), "wui:upload start") == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if n := countLogRecords(logs.snapshot(), "wui:upload start"); n != 2 {
		t.Fatalf("wui:upload start records = %d, want 2 (both holders parked): %v", n, logs.snapshot())
	}

	ct, body := multipartFileBody(t, "third.png", minimalPNGAttachment)
	resp, err := http.Post(srv.URL+"/api/upload", ct, body)
	if err != nil {
		t.Fatalf("third POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third POST status = %d, want 503", resp.StatusCode)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != "upload busy" {
		t.Errorf("error = %q, want \"upload busy\"", env.Error)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=503 reason=upload busy bytes=0 name=")

	// Release both held uploads; they complete normally.
	held1.release()
	held2.release()
	for _, h := range []*heldPipeUpload{held1, held2} {
		res := <-h.resp
		if res.status != http.StatusOK {
			t.Errorf("held upload completed with status %d (%s), want 200", res.status, res.body)
		}
	}
}

// ---------------------------------------------------------------------------
// Rejection log contract, error mapping, and the cachedID/resolve boundaries
// ---------------------------------------------------------------------------

// wantRejectedRecord asserts the single wui:upload rejected record matches want
// byte-for-byte. status/reason/bytes/name are part of the protocol contract:
// the incident was only debuggable because every rejection logged one line.
func wantRejectedRecord(t *testing.T, logs *uploadLogCapture, want string) {
	t.Helper()
	var got []string
	for _, r := range logs.snapshot() {
		if strings.HasPrefix(r, "wui:upload rejected ") {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("rejected records = %v, want exactly one %q", got, want)
	}
	if got[0] != want {
		t.Errorf("rejected record = %q, want %q", got[0], want)
	}
}

// Literal pins: these numbers have no behavioural fingerprint of their own (any
// sniff head above the magic-byte length classifies the same, any slack keeps
// the body bound sane), so only the constants themselves can be asserted.
func TestUploadConstantsExact(t *testing.T) {
	if uploadSniffHead != 512 {
		t.Errorf("uploadSniffHead = %d, want 512", uploadSniffHead)
	}
	if uploadBodySlack != 8<<10 {
		t.Errorf("uploadBodySlack = %d, want %d", uploadBodySlack, 8<<10)
	}
	if uploadMaxConcurrent != 2 {
		t.Errorf("uploadMaxConcurrent = %d, want 2", uploadMaxConcurrent)
	}
	if uploadFormField != "file" {
		t.Errorf("uploadFormField = %q, want \"file\"", uploadFormField)
	}
}

// The body bound follows the LARGER of the two caps: with the document cap
// above the image cap, a bound derived from the image cap would cut a legal
// document upload off at the transport layer.
func TestUploadBodyBound_UsesTheLargerCap(t *testing.T) {
	withUploadCaps(t, 1<<10, 4<<20)
	if got := uploadBodyBound(); got != 4<<20+8<<10 {
		t.Errorf("uploadBodyBound() with doc>image = %d, want %d", got, 4<<20+8<<10)
	}
	withUploadCaps(t, 4<<20, 1<<10)
	if got := uploadBodyBound(); got != 4<<20+8<<10 {
		t.Errorf("uploadBodyBound() with image>doc = %d, want %d", got, 4<<20+8<<10)
	}
}

// uploadStreamErrStatus is the whole client-vs-store split. Each PathError Op
// is asserted separately: "open"/"close"/"write" are the store's own failures
// (500), every other error is the client's body (400).
func TestUploadStreamErrStatus_Table(t *testing.T) {
	logs := installUploadLogCapture(t)
	// Literal bound, not uploadBodyBound(): a mutated cap must not
	// self-fulfill the expected text.
	boundText := "upload body exceeds " + toolresult.FormatFileSize(int(500<<20+8<<10))
	cases := []struct {
		name   string
		err    error
		status int
		msg    string
	}{
		{"maxbytes", &http.MaxBytesError{}, http.StatusRequestEntityTooLarge, boundText},
		{"link-rename", &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EEXIST},
			http.StatusInternalServerError, "save failed: rename a b: file exists"},
		{"path-open", &fs.PathError{Op: "open", Path: "p", Err: syscall.ENOENT},
			http.StatusInternalServerError, "save failed: open p: no such file or directory"},
		{"path-close", &fs.PathError{Op: "close", Path: "p", Err: syscall.EIO},
			http.StatusInternalServerError, "save failed: close p: input/output error"},
		{"path-write", &fs.PathError{Op: "write", Path: "p", Err: syscall.ENOSPC},
			http.StatusInternalServerError, "save failed: write p: no space left on device"},
		{"path-read", &fs.PathError{Op: "read", Path: "p", Err: syscall.EISDIR},
			http.StatusBadRequest, "read failed: read p: is a directory"},
		{"plain-read-error", io.ErrUnexpectedEOF, http.StatusBadRequest, "read failed: unexpected EOF"},
		{"wrapped-open", fmt.Errorf("media: create temp in d: %w", &fs.PathError{Op: "open", Path: "p", Err: syscall.ENOENT}),
			http.StatusInternalServerError, "save failed: media: create temp in d: open p: no such file or directory"},
	}
	for _, tc := range cases {
		status, msg := uploadStreamErrStatus(tc.err)
		if status != tc.status || msg != tc.msg {
			t.Errorf("%s: uploadStreamErrStatus(%v) = (%d, %q), want (%d, %q)",
				tc.name, tc.err, status, msg, tc.status, tc.msg)
		}
	}
	if n := countLogRecords(logs.snapshot(), "wui:upload save failed"); n != 5 {
		t.Errorf("wui:upload save failed records = %d, want 5 (one per 500 arm)", n)
	}
}

// A Content-Type that parses to multipart/form-data but carries a broken
// parameter must still be a 415: the parse error, not just the media type,
// decides. (Without the err check the request would fall through to the
// multipart reader and answer 400.)
func TestUpload_BadMediaTypeParameter_Rejects415(t *testing.T) {
	c, store := newUploadConnector(t)
	rec := callUpload(t, c, newUploadReq(t, "multipart/form-data; boundary", strings.NewReader("x"), 1))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d (%s), want 415", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "expected multipart/form-data" {
		t.Errorf("error = %q, want the exact 415 message", errMsg)
	}
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

// multipart/form-data with no boundary parameter: r.MultipartReader() fails
// before any part is read, and its message must reach the client verbatim.
func TestUpload_NoBoundaryParam_MalformedMultipart(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, store := newUploadConnector(t)
	rec := callUpload(t, c, newUploadReq(t, "multipart/form-data", strings.NewReader("x"), 1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	wantMsg := "malformed multipart: no multipart boundary param in Content-Type"
	if _, errMsg := decodeUploadBody(t, rec); errMsg != wantMsg {
		t.Errorf("error = %q, want %q", errMsg, wantMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=400 reason="+wantMsg+" bytes=0 name=")
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

// A plain form field named "file" is not an upload: only a part with BOTH the
// field name and a filename qualifies.
func TestUpload_FileFieldWithoutFilename_MissingFileField(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, store := newUploadConnector(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormField(uploadFormField)
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	if _, err := fw.Write([]byte("just a value")); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, int64(buf.Len())))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "missing file field" {
		t.Errorf("error = %q, want \"missing file field\"", errMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=400 reason=missing file field bytes=0 name=")
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

// A body that fails with a non-EOF error inside the sniff read must surface
// that error unwrapped: the store was never reached, so the text is exactly
// what the client's stream reported.
func TestUpload_BodyReadError_RejectedBeforeSave(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, store := newUploadConnector(t)
	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	fw, err := w.CreateFormFile(uploadFormField, "notes.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte("partial")); err != nil {
		t.Fatalf("write part: %v", err)
	}
	// No w.Close(): the body ends in a hard error, not a clean EOF.
	body := io.MultiReader(&head, iotest.ErrReader(errors.New("boom")))
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), body, -1))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if _, errMsg := decodeUploadBody(t, rec); errMsg != "read failed: boom" {
		t.Errorf("error = %q, want \"read failed: boom\" (the sniff read's own error, unwrapped)", errMsg)
	}
	wantRejectedRecord(t, logs, "wui:upload rejected status=400 reason=read failed: boom bytes=0 name=notes.txt")
	if n := cachedObjectCount(t, store); n != 0 {
		t.Errorf("cached files = %d, want 0", n)
	}
}

// The FIELD NAME picks the part, not the presence of a filename: a file-named
// part under a different field name must be skipped, not uploaded.
func TestUpload_NamedPartUnderWrongField_Skipped(t *testing.T) {
	c, store := newUploadConnector(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	other, err := w.CreateFormFile("attachment", "evil.txt")
	if err != nil {
		t.Fatalf("CreateFormFile(other): %v", err)
	}
	if _, err := other.Write([]byte("WRONG")); err != nil {
		t.Fatalf("write other: %v", err)
	}
	ff, err := w.CreateFormFile(uploadFormField, "notes.txt")
	if err != nil {
		t.Fatalf("CreateFormFile(file): %v", err)
	}
	if _, err := ff.Write([]byte("hello notes")); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	rec := callUpload(t, c, newUploadReq(t, w.FormDataContentType(), &buf, int64(buf.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	resp, _ := decodeUploadBody(t, rec)
	if resp.Kind != "document" || resp.Size != 11 {
		t.Errorf("resp = %+v, want the \"file\" field part (11 bytes), not the file-named part under another field", resp)
	}
	if _, err := os.Stat(store.Path(media.CategoryDocument, []byte("hello notes"), ".txt")); err != nil {
		t.Errorf("expected the file-field object in the cache: %v", err)
	}
	if _, err := os.Stat(store.Path(media.CategoryDocument, []byte("WRONG"), ".txt")); !os.IsNotExist(err) {
		t.Errorf("the wrong-field part was cached (stat err = %v, want not-exist)", err)
	}
	if got := cachedObjectCount(t, store); got != 1 {
		t.Errorf("cached files = %d, want 1", got)
	}
}

// The success line is the other half of the protocol log: it must carry the
// minted id, the client name, the server-counted byte total and the kind.
func TestUpload_SuccessLogRecord(t *testing.T) {
	logs := installUploadLogCapture(t)
	c, _ := newUploadConnector(t)
	ct, body := multipartFileBody(t, "photo.png", minimalPNGAttachment)
	rec := callUpload(t, c, newUploadReq(t, ct, body, int64(body.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	resp, _ := decodeUploadBody(t, rec)
	wantPrefix := fmt.Sprintf("wui:upload ok id=%s name=photo.png bytes=%d kind=image dur=",
		resp.ID, len(minimalPNGAttachment))
	var got []string
	for _, r := range logs.snapshot() {
		if strings.HasPrefix(r, "wui:upload ok ") {
			got = append(got, r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("wui:upload ok records = %v, want exactly one %q", got, wantPrefix+"…")
	}
	if !strings.HasPrefix(got[0], wantPrefix) {
		t.Errorf("ok record = %q, want it to start with %q", got[0], wantPrefix)
	}
	if n := countLogRecords(logs.snapshot(), "wui:upload rejected"); n != 0 {
		t.Errorf("rejected records = %d, want 0 for a successful upload", n)
	}
}

// ---------------------------------------------------------------------------
// resolveAttachments / classifyCachedObject boundaries
// ---------------------------------------------------------------------------

// A nil media cache must report every id as a miss — including a well-formed
// one, which is the case that would otherwise dereference the nil store.
func TestResolveAttachments_NilCacheWellFormedID(t *testing.T) {
	c := newTestConnector(t)
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: "d:ffffffffffffffff.txt", Name: "x.txt"}})
	if contents != nil {
		t.Errorf("contents = %+v, want nil with a nil mediaCache", contents)
	}
	if missing != "d:ffffffffffffffff.txt" {
		t.Errorf("missing = %q, want the id itself", missing)
	}
}

// A directory parked at the id's path is a miss, never a content item.
func TestResolveAttachments_DirectoryAtIDPath_Miss(t *testing.T) {
	c, store := newUploadConnector(t)
	dir := filepath.Join(store.RootDir, string(media.CategoryDocument), "ffffffffffffffff.txt")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: "d:ffffffffffffffff.txt", Name: "dir.txt"}})
	if contents != nil {
		t.Errorf("contents = %+v, want nil for a directory at the id path", contents)
	}
	if missing != "d:ffffffffffffffff.txt" {
		t.Errorf("missing = %q, want the id", missing)
	}
}

// An object larger than the sniff head takes the other branch of the head-read
// check (ReadFull returns nil, not ErrUnexpectedEOF), and everything the commit
// needs must still come from the cache: type, mime, path, size.
func TestResolveAttachments_ObjectLargerThanSniffHead(t *testing.T) {
	c, _ := newUploadConnector(t)
	body := pngOfSize(4 << 10)
	ct, buf := multipartFileBody(t, "photo.png", body)
	rec := callUpload(t, c, newUploadReq(t, ct, buf, int64(buf.Len())))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	uploaded, _ := decodeUploadBody(t, rec)
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: uploaded.ID, Name: "photo.png"}})
	if missing != "" {
		t.Fatalf("missing = %q, want empty (a 4 KiB object resolves through the bounded head read)", missing)
	}
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(contents))
	}
	src := contents[0].Source
	if contents[0].Type != "image" || src.Type != "file" || src.Mime != "image/png" {
		t.Errorf("resolved content = %+v, want an image file source with mime image/png", contents[0])
	}
	if src.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d (from the stat, not the head read)", src.Size, len(body))
	}
	got, err := os.ReadFile(src.Path)
	if err != nil {
		t.Fatalf("read resolved path: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("resolved bytes = %d bytes, want the full %d-byte object", len(got), len(body))
	}
}

func TestClassifyCachedObject_Table(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	locked := filepath.Join(dir, "locked.txt")
	if err := os.WriteFile(locked, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write locked: %v", err)
	}
	empty := filepath.Join(dir, "0123456789abcdef.bin")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	img := filepath.Join(dir, "0123456789abcdef.png")
	if err := os.WriteFile(img, minimalPNGAttachment, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	// A directory opens fine but read() fails: the bounded head read must
	// treat that as a miss, not as an empty object.
	if got, ok := classifyCachedObject(sub, "ffffffffffffffff.txt", 4096, "d.txt"); ok {
		t.Errorf("classifyCachedObject(directory) = %+v, want ok=false", got)
	}
	if got, ok := classifyCachedObject(locked, "locked.txt", 6, "locked.txt"); ok {
		t.Errorf("classifyCachedObject(unreadable file) = %+v, want ok=false", got)
	}
	got, ok := classifyCachedObject(empty, "0123456789abcdef.bin", 0, "empty.bin")
	if !ok {
		t.Fatal("classifyCachedObject(empty file) ok=false, want true")
	}
	if got.Type != "document" || got.Source.Path != empty || got.Source.Name != "empty.bin" || got.Source.Size != 0 {
		t.Errorf("empty object content = %+v, want a document over the cache path", got)
	}
	got, ok = classifyCachedObject(img, "0123456789abcdef.png", int64(len(minimalPNGAttachment)), "photo.png")
	if !ok {
		t.Fatal("classifyCachedObject(png) ok=false, want true")
	}
	if got.Type != "image" || got.Source.Mime != "image/png" {
		t.Errorf("png content = %+v, want an image with mime image/png", got)
	}
}

// ---------------------------------------------------------------------------
// cachedID: the in-cache bind and every drop path
// ---------------------------------------------------------------------------

func newCacheStore(t *testing.T) *media.Store {
	t.Helper()
	store, err := media.NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("media.NewAt: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestCachedID_NilStore(t *testing.T) {
	if got := cachedID(nil, "/anywhere/x.txt", "document"); got != "" {
		t.Errorf("cachedID(nil store) = %q, want \"\"", got)
	}
}

// The in-cache bind is name-based: it must return the object's own name without
// re-hashing or copying. A name that does NOT match its content hash is the
// only input that separates "bind the existing object" from "copy it in under
// its content hash" — the copy would mint a second object and a different id.
func TestCachedID_InCacheNameNotContentHash_BindsAsIs(t *testing.T) {
	store := newCacheStore(t)
	docName := "0123456789abcdef.txt"
	docPath := filepath.Join(store.RootDir, string(media.CategoryDocument), docName)
	if err := os.WriteFile(docPath, []byte("not-the-hash"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	imgName := "fedcba9876543210.png"
	imgPath := filepath.Join(store.RootDir, string(media.CategoryImage), imgName)
	if err := os.WriteFile(imgPath, []byte("also-not-the-hash"), 0o644); err != nil {
		t.Fatalf("write img: %v", err)
	}
	before := cachedObjectCount(t, store)
	if got := cachedID(store, docPath, "document"); got != "d:"+docName {
		t.Errorf("cachedID(in-cache doc) = %q, want %q (bind by name, no re-hash)", got, "d:"+docName)
	}
	if got := cachedID(store, imgPath, "image"); got != "i:"+imgName {
		t.Errorf("cachedID(in-cache image) = %q, want %q (the images/ dir drives the i: prefix)", got, "i:"+imgName)
	}
	// The category dir, not the caller's kind string, picks the prefix.
	if got := cachedID(store, imgPath, "document"); got != "i:"+imgName {
		t.Errorf("cachedID(in-cache image, kind=document) = %q, want %q", got, "i:"+imgName)
	}
	if after := cachedObjectCount(t, store); after != before {
		t.Errorf("cached files %d → %d, want unchanged (an in-cache bind writes nothing)", before, after)
	}
}

// A path inside the cache root but outside a category dir must be copied in
// under its content hash, never bound by name: the name-based bind is only
// sound for the two category dirs.
func TestCachedID_OutsideCategoryDirCopies(t *testing.T) {
	store := newCacheStore(t)
	dir := filepath.Join(store.RootDir, "scratch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}
	p := filepath.Join(dir, "0123456789abcdef.txt")
	if err := os.WriteFile(p, []byte("scratch bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	id := cachedID(store, p, "document")
	want := "d:" + filepath.Base(store.Path(media.CategoryDocument, []byte("scratch bytes"), ".txt"))
	if id != want {
		t.Errorf("cachedID(scratch path) = %q, want %q (copied under the content hash, not bound by name)", id, want)
	}
}

// A path that IS a category dir has a single-component rel: the bind must not
// index parts[1] of it.
func TestCachedID_CategoryDirPathDropped(t *testing.T) {
	logs := installUploadLogCapture(t)
	store := newCacheStore(t)
	id := cachedID(store, filepath.Join(store.RootDir, string(media.CategoryDocument)), "document")
	if id != "" {
		t.Errorf("cachedID(category dir) = %q, want \"\"", id)
	}
	if !hasRecordWith(logs.snapshot(), "wui:restore attachment copy failed, dropping attachment", "is not a cacheable file") {
		t.Errorf("no not-cacheable warn for the category dir: %v", logs.snapshot())
	}
}

// A directory is not a cacheable file: the drop must name the path, not the
// failed save.
func TestCachedID_DirectoryDropped(t *testing.T) {
	logs := installUploadLogCapture(t)
	store := newCacheStore(t)
	dir := filepath.Join(t.TempDir(), "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := cachedID(store, dir, "document"); got != "" {
		t.Errorf("cachedID(directory) = %q, want \"\"", got)
	}
	if !hasRecordWith(logs.snapshot(), "wui:restore attachment copy failed, dropping attachment", "is not a cacheable file (dir=true") {
		t.Errorf("warn records = %v, want the not-cacheable(dir=true) line", logs.snapshot())
	}
}

// The per-kind cap is inclusive on restore too: exactly cap bytes copies, one
// byte more is dropped with the not-cacheable line (not a save error).
func TestCachedID_SizeBoundary(t *testing.T) {
	logs := installUploadLogCapture(t)
	withUploadCaps(t, 1<<20, 10)
	c := newTestConnector(t)
	store := newCacheStore(t)
	c.SetMediaCache(store)

	srcDir := t.TempDir()
	at := filepath.Join(srcDir, "at.txt")
	over := filepath.Join(srcDir, "over.txt")
	if err := os.WriteFile(at, bytes.Repeat([]byte("a"), 10), 0o644); err != nil {
		t.Fatalf("write at: %v", err)
	}
	if err := os.WriteFile(over, bytes.Repeat([]byte("a"), 11), 0o644); err != nil {
		t.Fatalf("write over: %v", err)
	}
	id := cachedID(store, at, "document")
	if !regexp.MustCompile(`^d:[0-9a-f]{16}\.txt$`).MatchString(id) {
		t.Fatalf("cachedID(10 bytes at a 10-byte cap) = %q, want d:<16hex>.txt", id)
	}
	contents, missing := c.resolveAttachments([]inboundAttachment{{ID: id, Name: "at.txt"}})
	if missing != "" || len(contents) != 1 {
		t.Fatalf("resolve(%q) = (%+v, %q), want one resolved item", id, contents, missing)
	}
	got, err := os.ReadFile(contents[0].Source.Path)
	if err != nil {
		t.Fatalf("read resolved: %v", err)
	}
	if !bytes.Equal(got, bytes.Repeat([]byte("a"), 10)) {
		t.Errorf("resolved bytes = %q, want the 10-byte input", got)
	}
	if id2 := cachedID(store, over, "document"); id2 != "" {
		t.Errorf("cachedID(11 bytes at a 10-byte cap) = %q, want \"\"", id2)
	}
	if !hasRecordWith(logs.snapshot(), "wui:restore attachment copy failed, dropping attachment", "is not a cacheable file (dir=false size=11 cap=10)") {
		t.Errorf("warn records = %v, want the size=11 cap=10 not-cacheable line", logs.snapshot())
	}
}

// stat succeeds but open fails: the drop must carry the open error, not a
// downstream save failure from a nil handle.
func TestCachedID_OpenFailureDrops(t *testing.T) {
	logs := installUploadLogCapture(t)
	store := newCacheStore(t)
	src := filepath.Join(t.TempDir(), "locked.txt")
	if err := os.WriteFile(src, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write locked: %v", err)
	}
	if got := cachedID(store, src, "document"); got != "" {
		t.Errorf("cachedID(unreadable file) = %q, want \"\"", got)
	}
	if !hasRecordWith(logs.snapshot(), "wui:restore attachment copy failed, dropping attachment", "permission denied") {
		t.Errorf("warn records = %v, want the open failure (permission denied)", logs.snapshot())
	}
}

// The copy itself failing (the category dir is gone) must drop the attachment
// rather than mint an id from the empty path SaveReader returns.
func TestCachedID_SaveFailureDrops(t *testing.T) {
	logs := installUploadLogCapture(t)
	store := newCacheStore(t)
	src := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(src, []byte("outside bytes"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(store.RootDir, string(media.CategoryDocument))); err != nil {
		t.Fatalf("remove category dir: %v", err)
	}
	if got := cachedID(store, src, "document"); got != "" {
		t.Errorf("cachedID(save failure) = %q, want \"\"", got)
	}
	if !hasRecordWith(logs.snapshot(), "wui:restore attachment copy failed, dropping attachment", "create temp") {
		t.Errorf("warn records = %v, want the temp-create failure line", logs.snapshot())
	}
}
