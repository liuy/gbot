package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRestartBaseURL(t *testing.T) {
	t.Setenv("GBOT_WS_ADDR", "")
	if got := restartBaseURL("18765"); got != "http://127.0.0.1:18765" {
		t.Errorf("restartBaseURL(18765) = %q, want http://127.0.0.1:18765", got)
	}
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:9999")
	if got := restartBaseURL("18765"); got != "http://127.0.0.1:9999" {
		t.Errorf("with GBOT_WS_ADDR = %q, want http://127.0.0.1:9999", got)
	}
	t.Setenv("GBOT_WS_ADDR", "http://10.0.0.5:8765")
	if got := restartBaseURL("18765"); got != "http://10.0.0.5:8765" {
		t.Errorf("with scheme already present = %q, want unchanged", got)
	}
}

// Degenerate GBOT_WS_ADDR values (shorter than a scheme prefix, no port)
// must resolve without panicking — the scheme detection is prefix-based
// today, and this test keeps it that way under any future refactor.
func TestRestartBaseURL_ShortAddrNoPanic(t *testing.T) {
	t.Setenv("GBOT_WS_ADDR", "x.io:80")
	if got := restartBaseURL("18765"); got != "http://x.io:80" {
		t.Errorf("restartBaseURL(18765) = %q, want http://x.io:80", got)
	}
}

func TestRunRestart_202(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/admin/restart" {
			t.Errorf("request = %s %s, want POST /api/admin/restart", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"upgrading"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if code := runRestart(srv.URL, &buf); code != 0 {
		t.Fatalf("exit code = %d, want 0 for 202", code)
	}
	if !strings.Contains(buf.String(), "Upgrading") {
		t.Errorf("output = %q, want it to contain \"Upgrading\"", buf.String())
	}
}

func TestRunRestart_409Busy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"busy":true,"items":[` +
			`{"kind":"query","engine":"Main","engineId":"main","session":"debugging","detail":"fix the scanner bug"},` +
			`{"kind":"job","engine":"Side","engineId":"side","session":"","detail":"npm run build"}]}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if code := runRestart(srv.URL, &buf); code != 2 {
		t.Fatalf("exit code = %d, want 2 for 409", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Restart deferred — 2 active:") {
		t.Errorf("output = %q, want summary line with count 2", out)
	}
	if !strings.Contains(out, "- [query] Main (debugging): fix the scanner bug") {
		t.Errorf("output = %q, want query line with engine, session, detail", out)
	}
	if !strings.Contains(out, "- [job] Side: npm run build") {
		t.Errorf("output = %q, want job line with engine and command", out)
	}
}

func TestRunRestart_501(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"restart not supported on this platform"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if code := runRestart(srv.URL, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for 501", code)
	}
	if !strings.Contains(buf.String(), "restart not supported on this platform") {
		t.Errorf("output = %q, want platform message", buf.String())
	}
}

// TestRunRestart_501CodedRefusalMessage pins the CLI half of the refusal
// contract: the 501 body's human message must print, not the generic
// platform default (the wui gets the code for i18n; the CLI gets words).
func TestRunRestart_501CodedRefusalMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"TUI mode cannot hot-restart — exit and start the new binary manually","reason":"tui_mode"}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	if code := runRestart(srv.URL, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for 501", code)
	}
	if !strings.Contains(buf.String(), "TUI mode cannot hot-restart") {
		t.Errorf("output = %q, want the server's refusal sentence", buf.String())
	}
	if strings.Contains(buf.String(), "not supported on this platform") {
		t.Errorf("output = %q, generic default must not mask the specific refusal", buf.String())
	}
}

func TestRunRestart_Unreachable(t *testing.T) {
	var buf bytes.Buffer
	if code := runRestart("http://127.0.0.1:1", &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for unreachable daemon", code)
	}
	if !strings.Contains(buf.String(), "cannot reach gbot daemon at http://127.0.0.1:1") {
		t.Errorf("output = %q, want \"cannot reach\" with the URL", buf.String())
	}
}

func TestRunRestart_OtherStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	if code := runRestart(srv.URL, &buf); code != 1 {
		t.Fatalf("exit code = %d, want 1 for unexpected status", code)
	}
	if !strings.Contains(buf.String(), "unexpected status 500") {
		t.Errorf("output = %q, want \"unexpected status 500\"", buf.String())
	}
}
