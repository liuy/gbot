package wui

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// newLogTestServer mounts the log route against logPath (which need not
// exist — the missing-file case is part of the contract).
func newLogTestServer(t *testing.T, logPath string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterLogRoutes(mux, logPath)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeLogLines creates the log file with lines "line-0000".."line-<n-1>",
// newline-terminated, so assertions can index into the tail precisely.
func writeLogLines(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "line-%04d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func getLogs(t *testing.T, srv *httptest.Server, query string) (int, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/logs" + query)
	if err != nil {
		t.Fatalf("GET /api/logs: %v", err)
	}
	defer resp.Body.Close()
	body := new(strings.Builder)
	if _, err := io.Copy(body, resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, body.String()
}

func TestRegisterLogRoutes_TailReturnsLastNInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gbot.log")
	writeLogLines(t, path, 10)
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=3")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	got := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	want := []string{"line-0007", "line-0008", "line-0009"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

func TestRegisterLogRoutes_DefaultTail300(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gbot.log")
	writeLogLines(t, path, 400)
	srv := newLogTestServer(t, path)

	for name, query := range map[string]string{"no param": "", "invalid": "?tail=abc", "zero": "?tail=0"} {
		_, body := getLogs(t, srv, query)
		got := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		if len(got) != 300 {
			t.Fatalf("%s: got %d lines, want 300 (first: %q last: %q)", name, len(got), got[0], got[len(got)-1])
		}
		if got[0] != "line-0100" || got[299] != "line-0399" {
			t.Fatalf("%s: wrong window: first %q last %q", name, got[0], got[299])
		}
	}
}

func TestRegisterLogRoutes_TailCappedAt2000(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gbot.log")
	writeLogLines(t, path, 2500)
	srv := newLogTestServer(t, path)

	_, body := getLogs(t, srv, "?tail=99999")
	got := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(got) != 2000 {
		t.Fatalf("got %d lines, want 2000 (capped)", len(got))
	}
	if got[0] != "line-0500" || got[1999] != "line-2499" {
		t.Fatalf("wrong window: first %q last %q", got[0], got[1999])
	}
}

func TestRegisterLogRoutes_MissingFileReturns200Empty(t *testing.T) {
	srv := newLogTestServer(t, filepath.Join(t.TempDir(), "absent.log"))

	status, body := getLogs(t, srv, "?tail=50")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

// A file larger than the read window must never leak a partial line: the
// chunk starts mid-line when size > window, so the first fragment is dropped.
func TestRegisterLogRoutes_LargeFileTailHasOnlyCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gbot.log")
	// 300 lines x 2KB = 600KB, well past the 256KB window.
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "line-%04d%s\n", i, strings.Repeat("x", 2000))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	srv := newLogTestServer(t, path)

	status, body := getLogs(t, srv, "?tail=2000")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no lines returned")
	}
	re := regexp.MustCompile(`^line-\d{4}x+$`)
	for i, line := range lines {
		if !re.MatchString(line) {
			t.Fatalf("line %d is not a complete log line: %.40q", i, line)
		}
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "line-0299") {
		t.Fatalf("last line = %.20q, want line-0299", last)
	}
}

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
