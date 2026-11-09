package app

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

// appLogHandler captures slog records for the app-package tests.
type appLogHandler struct {
	mu     sync.Mutex
	recs   []appLogRec
	notify chan struct{}
}

type appLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *appLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *appLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, appLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	select {
	case h.notify <- struct{}{}:
	default:
	}
	return nil
}

func (h *appLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *appLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *appLogHandler) find(msg string) []appLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []appLogRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func captureAppLogs(t *testing.T) *appLogHandler {
	t.Helper()
	h := &appLogHandler{notify: make(chan struct{}, 256)}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// waitForAppLog blocks until a record with msg lands, so the goroutine that
// serves pprof cannot race the assertion.
func waitForAppLog(t *testing.T, h *appLogHandler, msg string) appLogRec {
	t.Helper()
	for {
		if recs := h.find(msg); len(recs) == 1 {
			return recs[0]
		}
		select {
		case <-h.notify:
		case <-time.After(2 * time.Second):
			t.Fatalf("log %q never appeared", msg)
		}
	}
}

func TestStartPprofServer_AddressResolution(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:6060")
	if err != nil {
		t.Skipf("port 6060 unavailable: %v", err)
	}
	defer ln.Close()
	t.Setenv("GBOT_PPROF_ADDR", "")
	logs := captureAppLogs(t)
	startPprofServer("")
	rec := waitForAppLog(t, logs, "pprof:listen_failed")
	if rec.attrs["addr"] != "localhost:6060" {
		t.Errorf("listen failure addr = %v, want the localhost:6060 default", rec.attrs["addr"])
	}
	if rec.attrs["error"] == nil {
		t.Error("listen failure record carries no error attr")
	}
	if recs := logs.find("pprof:listen"); len(recs) != 0 {
		t.Errorf("pprof:listen logged %d times after a failed bind, want 0", len(recs))
	}
}

func TestStartPprofServer_EnvAndDisable(t *testing.T) {
	t.Setenv("GBOT_PPROF_ADDR", "256.256.256.256:1")
	logs := captureAppLogs(t)
	startPprofServer("localhost:6061")
	rec := waitForAppLog(t, logs, "pprof:listen_failed")
	if rec.attrs["addr"] != "256.256.256.256:1" {
		t.Errorf("addr = %v, want the GBOT_PPROF_ADDR override", rec.attrs["addr"])
	}
	for _, off := range []string{"off", "-"} {
		t.Setenv("GBOT_PPROF_ADDR", "")
		logs := captureAppLogs(t)
		startPprofServer(off)
		if recs := logs.find("pprof:disabled"); len(recs) != 1 {
			t.Errorf("addr %q logged %d disable records, want 1", off, len(recs))
		}
	}
}
