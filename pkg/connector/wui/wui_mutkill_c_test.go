package wui

// wui_mutkill_c_test.go kills the batch-C mutation escapes (artifacts.go,
// server.go, logs.go). Each test targets behavior a caller of the public
// surface can observe; comments state why a assertion exists only where the
// reason is not obvious from the test name.

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
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mutCLogFile writes raw content as the daemon log file so window/tail
// arithmetic can be pinned byte-exactly (writeLogLines only emits uniform
// "line-NNNN" fixtures).
func mutCLogFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gbot.log")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}
	return path
}

// mutCLogLines builds n lines of exactly size bytes each ("line-%04d" plus
// padding) so the 256KiB read window lands on a known byte offset.
func mutCLogLines(t *testing.T, n, size int) string {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		head := fmt.Sprintf("line-%04d", i)
		if len(head) > size-1 {
			t.Fatalf("fixture line size %d too small for header %q", size, head)
		}
		b.WriteString(head)
		b.WriteString(strings.Repeat("x", size-len(head)-1))
		b.WriteString("\n")
	}
	return mutCLogFile(t, b.String())
}

func mutCGetLogLines(t *testing.T, srv *httptest.Server, query string) []string {
	t.Helper()
	status, body := getLogs(t, srv, query)
	if status != http.StatusOK {
		t.Fatalf("GET /api/logs%s status = %d, want 200", query, status)
	}
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// mutCSlogRecorder captures records through an installed default logger.
// Several killed mutants differ only in which log line fires, so the record
// stream itself is the observable contract.
type mutCSlogRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *mutCSlogRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
	return nil
}

func (*mutCSlogRecorder) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (*mutCSlogRecorder) WithAttrs(attrs []slog.Attr) slog.Handler     { return nil }
func (*mutCSlogRecorder) WithGroup(name string) slog.Handler           { return nil }

func (r *mutCSlogRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.records))
	for i, rec := range r.records {
		out[i] = rec.Message
	}
	return out
}

func mutCCaptureSlog(t *testing.T) *mutCSlogRecorder {
	t.Helper()
	rec := &mutCSlogRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

func (r *mutCSlogRecorder) hasMessage(msg string) bool {
	for _, m := range r.messages() {
		if m == msg {
			return true
		}
	}
	return false
}

// mutCDeadline runs fn and fatals when it does not return within d. Mutants
// that remove an unlock or a select-default deadlock instead of returning a
// wrong value, so the hang itself must fail the test quickly.
func mutCDeadline(t *testing.T, d time.Duration, name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %s (deadlocked?)", name, d)
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

func mutCWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// logs.go
// ---------------------------------------------------------------------------

func TestMutC_LogsResponseHeaders(t *testing.T) {
	path := mutCLogFile(t, "line-a\nline-b\n")
	srv := newLogTestServer(t, path)

	resp, err := http.Get(srv.URL + "/api/logs?tail=5")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (log tail must never be cached)", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
	if string(body) != "line-a\nline-b" {
		t.Errorf("body = %q, want the tailed lines", string(body))
	}
}

func TestMutC_LogsTailOne(t *testing.T) {
	path := mutCLogLines(t, 10, 16)
	srv := newLogTestServer(t, path)

	got := mutCGetLogLines(t, srv, "?tail=1")
	if len(got) != 1 || !strings.HasPrefix(got[0], "line-0009") {
		t.Fatalf("tail=1 = %q, want exactly the last line", got)
	}
}

// TestMutC_LogsReadWindowBoundary pins the 256KiB window arithmetic: 300
// lines of 1024B put the window start exactly at line 44's boundary, so the
// first (complete) line is dropped and 255 remain. Any shift of the window
// size or of the drop offset changes the count or first line.
func TestMutC_LogsReadWindowBoundary(t *testing.T) {
	path := mutCLogLines(t, 300, 1024)
	srv := newLogTestServer(t, path)

	got := mutCGetLogLines(t, srv, "?tail=2000")
	if len(got) != 255 {
		t.Fatalf("got %d lines, want 255 (first %q last %q)", len(got), got[0], got[len(got)-1])
	}
	if !strings.HasPrefix(got[0], "line-0045") || !strings.HasPrefix(got[254], "line-0299") {
		t.Fatalf("window = [%q .. %q], want [line-0045 .. line-0299]", got[0], got[254])
	}
}

func TestMutC_LogsSmallFileKeepsFirstLine(t *testing.T) {
	path := mutCLogLines(t, 10, 16)
	srv := newLogTestServer(t, path)

	got := mutCGetLogLines(t, srv, "?tail=10")
	if len(got) != 10 || !strings.HasPrefix(got[0], "line-0000") {
		t.Fatalf("small file tail = %d lines first %q, want 10 starting at line-0000 (no partial drop below the window)", len(got), got[0])
	}
}

func TestMutC_LogsOneByteIntoWindow(t *testing.T) {
	// One stray byte pushes the window start to offset 1: the first element
	// after split is a complete line, but offset>0 still drops it.
	content := "A" + strings.Repeat(strings.Repeat("y", 1023)+"\n", 256)
	path := mutCLogFile(t, content)
	srv := newLogTestServer(t, path)

	got := mutCGetLogLines(t, srv, "?tail=2000")
	if len(got) != 255 {
		t.Fatalf("got %d lines, want 255 (offset=1 drops the first complete line)", len(got))
	}
	if got[0] != "line-0000" && !strings.HasPrefix(got[0], "yyy") {
		// first returned line is the second 1024B chunk, i.e. padding content
		t.Fatalf("first line after drop = %q", got[0])
	}
}

func TestMutC_LogsSingleOversizedLineDropped(t *testing.T) {
	// A single line larger than the window: the whole buffer is one partial
	// line, so the tail is empty — dropping vs keeping it is observable.
	path := mutCLogFile(t, strings.Repeat("z", 300*1024))
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=50")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body != "" {
		t.Fatalf("body = %d bytes, want empty (single partial line is dropped)", len(body))
	}
}

func TestMutC_LogsBlankLinesFileIsEmpty(t *testing.T) {
	path := mutCLogFile(t, "\n\n\n")
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=10")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty (all-blank file has no complete content lines)", body)
	}
}

func TestMutC_LogsTrailingNewlineTrimmed(t *testing.T) {
	path := mutCLogFile(t, "a\nb\n")
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=10")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body != "a\nb" {
		t.Fatalf("body = %q, want %q (trailing empty element dropped, content kept)", body, "a\nb")
	}
}

func TestMutC_LogsWindowMinusOneReadFromStart(t *testing.T) {
	// size = window-1 keeps offset clamped to 0 and returns every line; a
	// mutant comparing offset < -1 leaves offset negative and ReadAt fails.
	var b strings.Builder
	for i := 0; i < 255; i++ {
		b.WriteString(strings.Repeat("q", 1023) + "\n")
	}
	b.WriteString(strings.Repeat("q", 1022) + "\n")
	path := mutCLogFile(t, b.String())
	srv := newLogTestServer(t, path)

	got := mutCGetLogLines(t, srv, "?tail=2000")
	if len(got) != 256 {
		t.Fatalf("got %d lines, want 256 (window-1 file read whole)", len(got))
	}
}

func TestMutC_LogsOneByteFile(t *testing.T) {
	path := mutCLogFile(t, "x")
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=10")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body != "x" {
		t.Fatalf("body = %q, want x (1-byte file is a complete line)", body)
	}
}

// ---------------------------------------------------------------------------
// artifacts.go — HTTP surface
// ---------------------------------------------------------------------------

// TestMutC_ArtifactOpenPermErrorIs404 pins the "unexplainable open error is
// a plain 404" branch: only a missing file may fall through to the bundled
// game fallback.
func TestMutC_ArtifactOpenPermErrorIs404(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	resp, err := http.Get(srv.URL + "/artifacts/chess")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (EACCES must not fall through to the bundled page)", resp.StatusCode)
	}
	if got := strings.TrimSpace(string(body)); got != "not found" {
		t.Errorf("body = %q, want the handler's not-found text", got)
	}
}

func TestMutC_ArtifactBundledFallback(t *testing.T) {
	srv := newArtifactTestServer(t, t.TempDir())

	status, header, body := getArtifactFull(t, srv.URL+"/artifacts/chess")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (registry name serves the bundled page)", status)
	}
	if !strings.Contains(string(body), "var ZhChess") {
		t.Errorf("body lacks the bundled chess template (%d bytes)", len(body))
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on the fallback", got)
	}
}

func TestMutC_ArtifactListNoStoreHeader(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "a.html", "a")
	srv := newArtifactTestServer(t, dir)

	status, header, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (listing must reflect new writes)", got)
	}
	if string(body) == "" || string(body) == "null" {
		t.Fatalf("body = %q, want a JSON array", string(body))
	}
}

// TestMutC_ArtifactWalkErrorsSurface500 uses an unreadable artifacts dir to
// pin that walk failures (other than a missing dir) answer 500 on both the
// list and the clear route.
func TestMutC_ArtifactWalkErrorsSurface500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	status, _, _ := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusInternalServerError {
		t.Fatalf("list status = %d, want 500 (walk error must surface)", status)
	}
	if delStatus := deleteArtifact(t, srv.URL+"/api/artifacts"); delStatus != http.StatusInternalServerError {
		t.Fatalf("delete status = %d, want 500 (walk error must surface)", delStatus)
	}
}

// TestMutC_ArtifactDeleteRemoveFailure500 keeps the dir readable (r+x) but
// not writable so readdir works and os.Remove fails with EACCES: a half-done
// delete must answer 500, not 204.
func TestMutC_ArtifactDeleteRemoveFailure500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit fixtures do not fail as root")
	}
	dir := t.TempDir()
	writeArtifactFile(t, dir, "a.html", "a")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := newArtifactTestServer(t, dir)

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a failing file remove must abort the clear)", status)
	}
}

// TestMutC_ArtifactGzipSizeFloor pins the exact eligibility floor around
// 1 MiB: below it identity, at it gzip.
func TestMutC_ArtifactGzipSizeFloor(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "sub-threshold.html", strings.Repeat("a", 600*1024))
	writeArtifactFile(t, dir, "one-under.html", strings.Repeat("a", 1<<20-1))
	writeArtifactFile(t, dir, "exact.html", strings.Repeat("a", 1<<20))
	srv := newArtifactTestServer(t, dir)

	for _, name := range []string{"sub-threshold.html", "one-under.html"} {
		status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", name, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s Content-Encoding = %q, want identity below the 1MiB floor", name, got)
		}
		if len(body) < 600*1024 {
			t.Errorf("%s body = %d bytes, want the full identity body", name, len(body))
		}
	}
	status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/exact.html", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("exact.html status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Errorf("exact.html Content-Encoding = %q, want gzip (size == floor is eligible)", got)
	}
	if plain := gunzipAll(t, body); len(plain) != 1<<20 {
		t.Errorf("exact.html gunzipped = %d bytes, want %d", len(plain), 1<<20)
	}
}

// TestMutC_ArtifactGzipEligibleText serves a >1MiB text artifact: the
// eligibility sniff must let plain text through and the octet-stream
// Content-Type fallback must be applied on the compressed representation.
func TestMutC_ArtifactGzipEligibleText(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("wui-gzip-text-payload\n", 80000) // > 1.5 MiB
	writeArtifactFile(t, dir, "big.txt", content)
	srv := newArtifactTestServer(t, dir)

	status, h, body, contentLength := probeArtifact(t, srv.URL+"/artifacts/big.txt", "gzip", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := h.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip for compressible text over the floor", got)
	}
	if got := h.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want the explicit octet-stream fallback", got)
	}
	if contentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d (exact compressed size on the wire)", contentLength, len(body))
	}
	if plain := gunzipAll(t, body); string(plain) != content {
		t.Errorf("gunzipped body = %d bytes, want the exact content (%d)", len(plain), len(content))
	}
}

// TestMutC_ArtifactGzipSkipsCompressedMedia pins the sniff blocklist: zip,
// gzip and video payloads must serve identity even when the client accepts
// gzip.
func TestMutC_ArtifactGzipSkipsCompressedMedia(t *testing.T) {
	dir := t.TempDir()
	zipBytes := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 1<<20+512)...)
	gzBytes := append([]byte{0x1f, 0x8b, 0x08}, bytes.Repeat([]byte{0}, 1<<20+512)...)
	// A valid ftyp box (size 32, brands padded) is required for the mp4
	// sniff; the bytes inside the box must all be brand strings.
	mp4Bytes := append([]byte{
		0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		'm', 'p', '4', '2', 'm', 'p', '4', '2', 'm', 'p', '4', '2',
		'm', 'p', '4', '2', 'm', 'p', '4', '2',
	}, bytes.Repeat([]byte{0}, 1<<20+512)...)
	writeArtifactFile(t, dir, "a.zip", string(zipBytes))
	writeArtifactFile(t, dir, "a.gz", string(gzBytes))
	writeArtifactFile(t, dir, "a.mp4", string(mp4Bytes))
	srv := newArtifactTestServer(t, dir)

	for _, name := range []string{"a.zip", "a.gz", "a.mp4"} {
		status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/"+name, "gzip", nil)
		if status != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", name, status)
		}
		if got := h.Get("Content-Encoding"); got != "" {
			t.Errorf("%s Content-Encoding = %q, want identity (already-compressed medium)", name, got)
		}
		if len(body) < 1<<20 {
			t.Errorf("%s body = %d bytes, want the full identity body", name, len(body))
		}
	}
}

// TestMutC_ArtifactGzipValidators pins If-None-Match: "*" (a one-byte
// header value) matching any representation, and the 304 header diet.
func TestMutC_ArtifactGzipValidators(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	srv := newArtifactTestServer(t, dir)

	status, h, body, _ := probeArtifact(t, srv.URL+"/artifacts/big.glb", "gzip",
		map[string]string{"If-None-Match": "*"})
	if status != http.StatusNotModified {
		t.Fatalf("INM * status = %d, want 304", status)
	}
	if len(body) != 0 {
		t.Errorf("304 body = %d bytes, want 0", len(body))
	}
	if got := h.Get("Content-Type"); got != "" {
		t.Errorf("304 Content-Type = %q, want empty (writeNotModified drops representation headers)", got)
	}
	if got := h.Get("Content-Encoding"); got != "" {
		t.Errorf("304 Content-Encoding = %q, want empty", got)
	}
	if !gzipETagRe.MatchString(h.Get("ETag")) {
		t.Errorf("304 ETag = %q, want the gz- validator", h.Get("ETag"))
	}
}

func TestMutC_ArtifactGzipHEADNoContentLength(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "big.glb", bigGLB())
	srv := newArtifactTestServer(t, dir)

	req, err := http.NewRequest(http.MethodHead, srv.URL+"/artifacts/big.glb", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if resp.ContentLength != -1 {
		t.Errorf("HEAD ContentLength = %d, want -1 (HEAD returns before the compressed body is sized)", resp.ContentLength)
	}
	if got := resp.Header.Get("ETag"); !gzipETagRe.MatchString(got) {
		t.Errorf("HEAD ETag = %q, want the gz- validator", got)
	}
}

func TestMutC_ETagMatchesDirect(t *testing.T) {
	tests := []struct {
		name string
		inm  string
		etag string
		want bool
	}{
		{"star matches anything", "*", `"abc"`, true},
		{"exact", `"abc"`, `"abc"`, true},
		{"weak prefix counts", `W/"abc"`, `"abc"`, true},
		{"list with spaces", `"zzz", "abc" `, `"abc"`, true},
		{"no match", `"zzz"`, `"abc"`, false},
		{"empty header", ``, `"abc"`, false},
	}
	for _, tt := range tests {
		if got := etagMatches(tt.inm, tt.etag); got != tt.want {
			t.Errorf("%s: etagMatches(%q, %q) = %v, want %v", tt.name, tt.inm, tt.etag, got, tt.want)
		}
	}
}

func TestMutC_NotModifiedSinceDirect(t *testing.T) {
	mtime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	fresh := mtime.UTC().Format(http.TimeFormat)
	stale := mtime.Add(-time.Hour).UTC().Format(http.TimeFormat)
	tests := []struct {
		name    string
		ims     string
		modtime time.Time
		want    bool
	}{
		{"empty header", "", mtime, false},
		// A zero modtime is the bundled-template case; the guard must reject
		// before ParseTime runs, and a mutated guard flips this to true.
		{"zero modtime with valid ims", fresh, time.Time{}, false},
		{"equal second is fresh", fresh, mtime, true},
		{"older validator is fresh", stale, mtime, false},
		{"malformed ims", "not-a-date", mtime, false},
	}
	for _, tt := range tests {
		if got := notModifiedSince(tt.ims, tt.modtime); got != tt.want {
			t.Errorf("%s: notModifiedSince(%q, %v) = %v, want %v", tt.name, tt.ims, tt.modtime, got, tt.want)
		}
	}
}

func TestMutC_ArtifactContentTypeGltf(t *testing.T) {
	if got := artifactContentType("scene.GLTF"); got != "model/gltf+json" {
		t.Errorf("gltf = %q, want model/gltf+json", got)
	}
	if got := artifactContentType("scene.glb"); got != "model/gltf-binary" {
		t.Errorf("glb = %q, want model/gltf-binary", got)
	}
	if got := artifactContentType("scene.html"); got != "" {
		t.Errorf("html = %q, want empty (no explicit map)", got)
	}
}

func TestMutC_ArtifactListNewestFirstOrder(t *testing.T) {
	dir := t.TempDir()
	writeArtifactFile(t, dir, "old.html", "old")
	writeArtifactFile(t, dir, "new.html", "newer")
	writeArtifactFile(t, dir, "mid.html", "mid")
	setArtifactMtime(t, filepath.Join(dir, "old.html"), time.UnixMilli(1000))
	setArtifactMtime(t, filepath.Join(dir, "mid.html"), time.UnixMilli(2000))
	setArtifactMtime(t, filepath.Join(dir, "new.html"), time.UnixMilli(3000))
	srv := newArtifactTestServer(t, dir)

	status, _, body := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].Name != "new.html" || items[1].Name != "mid.html" || items[2].Name != "old.html" {
		t.Fatalf("order = [%s %s %s], want newest first [new mid old]", items[0].Name, items[1].Name, items[2].Name)
	}

	// Equal mtimes must keep the walk order (a "<=" comparator reverses
	// equal-key items under SliceStable's insertion pass).
	writeArtifactFile(t, dir, "t1.html", "1")
	writeArtifactFile(t, dir, "t2.html", "2")
	eq := time.UnixMilli(5000)
	setArtifactMtime(t, filepath.Join(dir, "t1.html"), eq)
	setArtifactMtime(t, filepath.Join(dir, "t2.html"), eq)
	status, _, body = getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusOK {
		t.Fatalf("second list status = %d, want 200", status)
	}
	items = nil
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %d, want 5", len(items))
	}
	if items[0].Name != "t1.html" || items[1].Name != "t2.html" {
		t.Fatalf("equal-mtime order = [%s %s], want the stable walk order [t1 t2]", items[0].Name, items[1].Name)
	}
}

// TestMutC_ArtifactClearENOTDIR500 registers the routes under a path whose
// parent is a regular file: stat fails with ENOTDIR (not IsNotExist), which
// must surface as 500 rather than the nothing-to-clear 204.
func TestMutC_ArtifactClearENOTDIR500(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	mutCWriteFile(t, blocker, []byte("file"))
	bad := filepath.Join(blocker, "artifacts")
	mux := http.NewServeMux()
	RegisterArtifactRoutes(mux, bad, observeStubFn(nil, "", false))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if status := deleteArtifact(t, srv.URL+"/api/artifacts"); status != http.StatusInternalServerError {
		t.Fatalf("delete status = %d, want 500 (ENOTDIR is not the nothing-to-clear case)", status)
	}
	status, _, _ := getArtifactList(t, srv.URL+"/api/artifacts")
	if status != http.StatusInternalServerError {
		t.Fatalf("list status = %d, want 500 (ENOTDIR walk error surfaces)", status)
	}
}

func TestMutC_ArtifactFilePathDirect(t *testing.T) {
	dir := t.TempDir()
	if got, status := artifactFilePath(dir, ""); got != "" || status != http.StatusBadRequest {
		t.Errorf("empty name = (%q, %d), want (\"\", 400)", got, status)
	}
	full, status := artifactFilePath(dir, "sub/game.html")
	if status != 0 || full != filepath.Join(dir, "sub", "game.html") {
		t.Errorf("nested = (%q, %d), want the joined path", full, status)
	}
	if _, status := artifactFilePath(dir, "../secret"); status != http.StatusBadRequest {
		t.Errorf("traversal status = %d, want 400", status)
	}
}

// ---------------------------------------------------------------------------
// artifacts.go — gzip cache
// ---------------------------------------------------------------------------

// mutCCacheCounts wraps a deterministic payload producer that records calls.
type mutCCacheCounts struct {
	mu    sync.Mutex
	calls []string
}

func (cc *mutCCacheCounts) compress(key string, payload []byte) func() ([]byte, error) {
	return func() ([]byte, error) {
		cc.mu.Lock()
		cc.calls = append(cc.calls, key)
		cc.mu.Unlock()
		return payload, nil
	}
}

func (cc *mutCCacheCounts) count(key string) int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	n := 0
	for _, k := range cc.calls {
		if k == key {
			n++
		}
	}
	return n
}

func mutCCacheGet(t *testing.T, c *gzipCache, path string, mtime, size int64, compress func() ([]byte, error)) []byte {
	t.Helper()
	// A mutant that drops an unlock deadlocks here; fail fast instead of
	// stalling the whole suite.
	var gz []byte
	mutCDeadline(t, 5*time.Second, "gzipCache.get "+path, func() {
		out, err := c.get(path, mtime, size, compress)
		if err != nil {
			t.Errorf("get %s: %v", path, err)
			return
		}
		gz = out
	})
	return gz
}

// TestMutC_GzipCacheIdentity pins that a cached entry is keyed by the full
// (mtime, size) identity and that replacing an entry updates the byte
// accounting instead of accumulating.
func TestMutC_GzipCacheIdentity(t *testing.T) {
	c := newGzipCache(4, 1<<30)
	cc := &mutCCacheCounts{}

	gz1 := []byte(strings.Repeat("1", 10))
	gz2 := []byte(strings.Repeat("2", 30))
	if out := mutCCacheGet(t, c, "p", 100, 1, cc.compress("p", gz1)); !bytes.Equal(out, gz1) {
		t.Fatalf("first get = %q, want the compressed payload", out)
	}
	c.mu.Lock()
	bytes1, tick1 := c.bytes, c.tick
	c.mu.Unlock()
	if bytes1 != 10 {
		t.Fatalf("bytes after insert = %d, want 10", bytes1)
	}

	// Same path, new mtime: identity mismatch must re-compress.
	out := mutCCacheGet(t, c, "p", 200, 1, cc.compress("p", gz2))
	if cc.count("p") != 2 {
		t.Fatalf("compress calls = %d, want 2 (changed mtime invalidates)", cc.count("p"))
	}
	if !bytes.Equal(out, gz2) {
		t.Fatalf("second get = %q, want the fresh payload (stale identity must not be served)", out)
	}
	// Same path, new size: same rule.
	mutCCacheGet(t, c, "p", 200, 2, cc.compress("p", gz1))
	if cc.count("p") != 3 {
		t.Fatalf("compress calls = %d, want 3 (changed size invalidates)", cc.count("p"))
	}
	c.mu.Lock()
	bytes2, tick2 := c.bytes, c.tick
	entry := c.entries["p"]
	c.mu.Unlock()
	if bytes2 != 10 {
		t.Fatalf("bytes after replacement = %d, want 10 (old entry subtracted, new added)", bytes2)
	}
	if entry == nil || entry.size != 2 || entry.mtimeNano != 200 {
		t.Fatalf("entry = %+v, want the newest identity", entry)
	}
	if tick2 <= tick1 {
		t.Fatalf("tick = %d after more gets, must advance (was %d)", tick2, tick1)
	}
}

// TestMutC_GzipCacheJoinError pins single-flight error propagation: a joiner
// must receive the leader's error, not start its own compression.
func TestMutC_GzipCacheJoinError(t *testing.T) {
	c := newGzipCache(2, 1<<30)
	leaderStarted := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	compress := func() ([]byte, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(leaderStarted)
			<-release
			return nil, errors.New("leader boom")
		}
		return nil, errors.New("joiner boom")
	}

	errs := make(chan error, 2)
	go func() {
		_, err := c.get("p", 1, 1, compress)
		errs <- err
	}()
	<-leaderStarted
	go func() {
		_, err := c.get("p", 1, 1, compress)
		errs <- err
	}()
	// Give the joiner a moment to observe the in-flight call before releasing.
	time.Sleep(50 * time.Millisecond) // REAL-TIME: joins must observe the in-flight call before release
	close(release)

	var joinerErr error
	mutCDeadline(t, 5*time.Second, "joined get", func() {
		e1, e2 := <-errs, <-errs
		if e1 == nil || e2 == nil {
			t.Errorf("errors = %v / %v, want both non-nil", e1, e2)
		}
		if e1.Error() != "leader boom" {
			t.Errorf("first error = %v, want leader boom", e1)
		}
		joinerErr = e2
	})
	if joinerErr == nil || joinerErr.Error() != "leader boom" {
		t.Fatalf("joiner error = %v, want the leader's error (single flight)", joinerErr)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("compress calls = %d, want 1 (joiner must not re-compress)", got)
	}
}

// TestMutC_GzipCacheLRUTickAndVictim pins the LRU clock: inserts and hits
// advance tick, lastUsed reflects recency, and eviction picks the true LRU.
func TestMutC_GzipCacheLRUTickAndVictim(t *testing.T) {
	c := newGzipCache(2, 1<<30)
	cc := &mutCCacheCounts{}

	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", []byte("aaaaaa")))
	mutCCacheGet(t, c, "b", 1, 1, cc.compress("b", []byte("bbbbbb")))
	c.mu.Lock()
	usedA, usedB := c.entries["a"].lastUsed, c.entries["b"].lastUsed
	c.mu.Unlock()
	if usedA != 1 || usedB != 2 {
		t.Fatalf("lastUsed = a:%d b:%d, want 1 and 2 (insert stamps recency)", usedA, usedB)
	}
	// Touch a: it becomes most recent, b the victim.
	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", nil))
	mutCCacheGet(t, c, "c", 1, 1, cc.compress("c", []byte("cccccc")))

	c.mu.Lock()
	tick := c.tick
	_, hasB := c.entries["b"]
	_, hasA := c.entries["a"]
	_, hasC := c.entries["c"]
	c.mu.Unlock()
	if !hasA || hasB || !hasC {
		t.Fatalf("entries after eviction: a=%v b=%v c=%v, want b evicted (LRU) and a,c resident", hasA, hasB, hasC)
	}
	// a-insert + b-insert + a-hit + c-insert = 4 ticks.
	if tick != 4 {
		t.Fatalf("tick = %d, want 4 (every insert and hit advances the LRU clock)", tick)
	}
	if cc.count("b") != 1 {
		t.Fatalf("b compress calls = %d, want 1", cc.count("b"))
	}
}

// TestMutC_GzipCacheOversizedEntryKept pins the documented cap escape: a
// single entry larger than maxBytes stays servable instead of evicting
// itself.
func TestMutC_GzipCacheOversizedEntryKept(t *testing.T) {
	c := newGzipCache(3, 16)
	cc := &mutCCacheCounts{}
	payload := []byte(strings.Repeat("z", 40))

	mutCCacheGet(t, c, "huge", 1, 1, cc.compress("huge", payload))
	c.mu.Lock()
	bytes, entry := c.bytes, c.entries["huge"]
	c.mu.Unlock()
	if entry == nil {
		t.Fatal("oversized entry was evicted, want it servable (documented cap escape)")
	}
	if bytes != 40 {
		t.Fatalf("bytes = %d, want 40", bytes)
	}
	mutCCacheGet(t, c, "huge", 1, 1, cc.compress("huge", payload))
	if cc.count("huge") != 1 {
		t.Fatalf("compress calls = %d, want 1 (oversized entry is cached)", cc.count("huge"))
	}
}

// TestMutC_GzipCacheEvictionBytesAccounting pins that evicting a victim
// subtracts (not assigns) its size.
func TestMutC_GzipCacheEvictionBytesAccounting(t *testing.T) {
	c := newGzipCache(3, 1<<30)
	cc := &mutCCacheCounts{}
	mutCCacheGet(t, c, "a", 1, 1, cc.compress("a", bytes.Repeat([]byte{1}, 10)))
	mutCCacheGet(t, c, "b", 1, 1, cc.compress("b", bytes.Repeat([]byte{2}, 20)))
	mutCCacheGet(t, c, "c", 1, 1, cc.compress("c", bytes.Repeat([]byte{3}, 30)))
	mutCCacheGet(t, c, "d", 1, 1, cc.compress("d", bytes.Repeat([]byte{4}, 40)))

	c.mu.Lock()
	bytes := c.bytes
	_, hasA := c.entries["a"]
	c.mu.Unlock()
	if hasA {
		t.Fatal("a was not evicted at the entry cap")
	}
	if bytes != 20+30+40 {
		t.Fatalf("bytes after eviction = %d, want %d (victim size subtracted)", bytes, 20+30+40)
	}
}

// TestMutC_GzipCacheBudgetConstants pins the documented daemon-wide memory
// budget. The byte cap is only observable with ~400MiB of resident entries,
// which no hermetic test can produce; the constant itself is the contract.
func TestMutC_GzipCacheBudgetConstants(t *testing.T) {
	if artifactGzipCacheMaxEntries != 3 {
		t.Errorf("artifactGzipCacheMaxEntries = %d, want 3 (documented resident-model budget)", artifactGzipCacheMaxEntries)
	}
	if artifactGzipCacheMaxBytes != 400<<20 {
		t.Errorf("artifactGzipCacheMaxBytes = %d, want %d (documented pinned-memory budget)", artifactGzipCacheMaxBytes, 400<<20)
	}
	if artifactGzipMinSize != 1<<20 {
		t.Errorf("artifactGzipMinSize = %d, want %d (floor under which gzip costs more than it saves)", artifactGzipMinSize, 1<<20)
	}
}

// mutCFailWriter is an http.ResponseWriter whose Write always fails, so the
// gzip write-error path fires deterministically.
type mutCFailWriter struct {
	hdr http.Header
}

func (w *mutCFailWriter) Header() http.Header       { return w.hdr }
func (w *mutCFailWriter) Write([]byte) (int, error) { return 0, errors.New("peer went away") }
func (w *mutCFailWriter) WriteHeader(int)           {}

// TestMutC_ArtifactGzipWriteErrorWarns pins that a failed body write logs
// instead of passing silently (the only externally visible effect).
func TestMutC_ArtifactGzipWriteErrorWarns(t *testing.T) {
	rec := mutCCaptureSlog(t)
	resetArtifactGzipCache(t)
	path := filepath.Join(t.TempDir(), "warn.txt")
	mutCWriteFile(t, path, []byte(strings.Repeat("w", 2048)))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	mutCDeadline(t, 5*time.Second, "serveArtifactGzipped", func() {
		serveArtifactGzipped(&mutCFailWriter{hdr: http.Header{}}, r, f, st)
	})
	if !rec.hasMessage("artifact gzip write") {
		t.Fatalf("log messages = %v, want an \"artifact gzip write\" warn", rec.messages())
	}
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
	// so the frame is delivered and answered with the no-active-upload error.
	if err := ws.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil { // REAL-TIME
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, buf[:limit]); err != nil {
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

	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("chunk")); err != nil {
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
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("data")); err != nil {
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
	var gotBlocks []types.ContentBlock
	var gotCtx context.Context
	mock.queryWithContentFn = func(ctx context.Context, blocks []types.ContentBlock, _ string) {
		gotCtx = ctx
		gotBlocks = blocks
	}
	ws := dialChatWS(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/chat")
	drainInitialFrames(t, ws)

	png := mutCPNG(t)
	writeWSText(t, ws, map[string]any{
		"type": "attachment_start", "id": "img1", "name": "dot.png", "mime": "image/png", "size": len(png),
	})
	if err := ws.WriteMessage(websocket.BinaryMessage, png); err != nil {
		t.Fatal(err)
	}
	writeWSText(t, ws, map[string]any{"type": "attachment_end", "id": "img1"})
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "see the dot",
		"attachments": []map[string]any{{"id": "img1", "name": "dot.png", "mime": "image/png", "size": len(png)}},
	})

	if !waitFor(5*time.Second, func() bool { return gotBlocks != nil }) {
		t.Fatal("QueryWithContent never fired for the committed attachment")
	}
	if len(gotBlocks) != 2 {
		t.Fatalf("blocks = %d, want 2 (text + image)", len(gotBlocks))
	}
	if gotBlocks[0].Type != types.ContentTypeText || gotBlocks[0].Text != "see the dot" {
		t.Fatalf("text block = %+v, want the message text \"see the dot\"", gotBlocks[0])
	}
	if gotBlocks[1].Type != types.ContentTypeImage {
		t.Fatalf("block[1] type = %v, want image", gotBlocks[1].Type)
	}
	if gotCtx == nil || engine.SourceFromContext(gotCtx) != "wui" {
		t.Errorf("query ctx source = %q, want wui", engine.SourceFromContext(gotCtx))
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

// TestMutC_QuerySourceTaggedWUI pins that dispatched queries carry the
// source tag the engine routes SendFile by.
func TestMutC_QuerySourceTaggedWUI(t *testing.T) {
	c := newTestConnector(t)
	mutCEnableSessions(c)
	mock := c.mock()
	mock.systemPromptFn = func() string { return "" }
	var queryCtx context.Context
	var contentCtx context.Context
	mock.queryFn = func(ctx context.Context, _, _ string) { queryCtx = ctx }
	mock.queryWithContentFn = func(ctx context.Context, _ []types.ContentBlock, _ string) { contentCtx = ctx }
	ws := mutCDialChat(t, c)
	drainInitialFrames(t, ws)

	writeWSText(t, ws, map[string]any{"type": "message", "text": "plain"})
	if !waitFor(5*time.Second, func() bool { return queryCtx != nil }) {
		t.Fatal("Query never fired")
	}
	if engine.SourceFromContext(queryCtx) != "wui" {
		t.Errorf("Query ctx source = %q, want wui", engine.SourceFromContext(queryCtx))
	}

	// Unknown content types are skipped, so the message still dispatches via
	// QueryWithContent with the text block.
	writeWSText(t, ws, map[string]any{
		"type": "message", "text": "with content",
		"content": []map[string]any{
			{"type": "banana", "source": map[string]any{"type": "file", "path": "/dev/null"}},
		},
	})
	if !waitFor(5*time.Second, func() bool { return contentCtx != nil }) {
		t.Fatal("QueryWithContent never fired")
	}
	if engine.SourceFromContext(contentCtx) != "wui" {
		t.Errorf("QueryWithContent ctx source = %q, want wui", engine.SourceFromContext(contentCtx))
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
