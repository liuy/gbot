package mcp

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type captureHandler struct {
	mu    sync.Mutex
	attrs []map[string]string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(string) slog.Handler { return h }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.attrs = append(h.attrs, m)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) lines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.attrs {
		if m["msg"] == "mcp: server stderr" {
			out = append(out, m["line"])
		}
	}
	return out
}

func withCapturedStderr(t *testing.T, fn func()) []string {
	t.Helper()
	h := &captureHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	fn()
	return h.lines()
}

func TestStderrLineLoggerSplitsAndTrims(t *testing.T) {
	got := withCapturedStderr(t, func() {
		l := &stderrLineLogger{server: "srv"}
		if n, err := l.Write([]byte("first\r\nsecond\n")); err != nil || n != 14 {
			t.Fatalf("write: n=%d err=%v", n, err)
		}
	})
	want := []string{"first", "second"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func TestStderrLineLoggerLineSplitAcrossWrites(t *testing.T) {
	got := withCapturedStderr(t, func() {
		l := &stderrLineLogger{server: "srv"}
		_, _ = l.Write([]byte("partial"))
		_, _ = l.Write([]byte(" line\n"))
	})
	if len(got) != 1 || got[0] != "partial line" {
		t.Fatalf("entries = %v, want [partial line]", got)
	}
}

func TestStderrLineLoggerSkipsEmptyLines(t *testing.T) {
	got := withCapturedStderr(t, func() {
		l := &stderrLineLogger{server: "srv"}
		_, _ = l.Write([]byte("\n\nreal\n\n"))
	})
	if len(got) != 1 || got[0] != "real" {
		t.Fatalf("entries = %v, want [real]", got)
	}
}

func TestStderrLineLoggerTruncatesNewlineFreeBlob(t *testing.T) {
	got := withCapturedStderr(t, func() {
		l := &stderrLineLogger{server: "srv"}
		_, _ = l.Write([]byte(strings.Repeat("x", stderrLineMax+5000)))
	})
	if len(got) != 1 {
		t.Fatalf("entries = %d, want 1", len(got))
	}
	// The truncation suffix "…" is three UTF-8 bytes.
	if ln := len(got[0]); ln != stderrLineMax+3 {
		t.Fatalf("truncated length = %d, want %d", ln, stderrLineMax+3)
	}
}

// The real-process path: os/exec's copy goroutine delivers stderr through the
// writer before Wait (inside Output) returns, so the log line is already
// captured when fn returns — no polling needed.
func TestStderrLineLoggerWiredToRealProcess(t *testing.T) {
	got := withCapturedStderr(t, func() {
		cmd := exec.Command("sh", "-c", "echo crash-reason-here >&2")
		cmd.Stderr = &stderrLineLogger{server: "helper"}
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if len(got) != 1 || got[0] != "crash-reason-here" {
		t.Fatalf("entries = %v, want [crash-reason-here]", got)
	}
}

// The overflow flush must reset the buffer: content logged during truncation
// must not leak into the next line.
func TestStderrLineLoggerResetsAfterOverflowFlush(t *testing.T) {
	got := withCapturedStderr(t, func() {
		l := &stderrLineLogger{server: "srv"}
		_, _ = l.Write([]byte(strings.Repeat("x", stderrLineMax+100)))
		_, _ = l.Write([]byte("next\n"))
	})
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2 (flush + next): %v", len(got), got)
	}
	if got[1] != "next" {
		t.Fatalf("post-flush entry = %q, want exactly %q", got[1], "next")
	}
}

// Every spawned stdio server must have the stderr logger wired — the whole
// point of the feature. A missing wiring silently discards crash reasons.
func TestNewStdioTransportWiresStderrLogger(t *testing.T) {
	tr, err := newStdioTransport("srv", &StdioConfig{Command: "true", Args: nil}, ScopeUser, true)
	if err != nil {
		t.Fatalf("newStdioTransport: %v", err)
	}
	ct, ok := tr.(*mcp.CommandTransport)
	if !ok {
		t.Fatalf("transport type = %T, want *mcp.CommandTransport", tr)
	}
	if _, ok := ct.Command.Stderr.(*stderrLineLogger); !ok {
		t.Fatalf("Command.Stderr = %T, want *stderrLineLogger", ct.Command.Stderr)
	}
	if ct.Command.SysProcAttr == nil || !ct.Command.SysProcAttr.Setpgid {
		t.Fatal("Command must start its own process group (Setpgid) so cleanup can reap the whole tree")
	}
}
