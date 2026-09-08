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
