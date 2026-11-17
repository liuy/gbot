package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
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
	StartPprofServer("", nil)
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
	StartPprofServer("localhost:6061", nil)
	rec := waitForAppLog(t, logs, "pprof:listen_failed")
	if rec.attrs["addr"] != "256.256.256.256:1" {
		t.Errorf("addr = %v, want the GBOT_PPROF_ADDR override", rec.attrs["addr"])
	}
	for _, off := range []string{"off", "-"} {
		t.Setenv("GBOT_PPROF_ADDR", "")
		logs := captureAppLogs(t)
		StartPprofServer(off, nil)
		if recs := logs.find("pprof:disabled"); len(recs) != 1 {
			t.Errorf("addr %q logged %d disable records, want 1", off, len(recs))
		}
	}
}

// pprofUpgrader supplies a real listener from Listen, standing in for an
// inherited fd; records the requested network/addr.
type pprofUpgrader struct {
	gotNetwork, gotAddr string
	ln                  net.Listener
}

func (f *pprofUpgrader) Listen(network, addr string) (net.Listener, error) {
	f.gotNetwork, f.gotAddr = network, addr
	return f.ln, nil
}
func (f *pprofUpgrader) Ready() error          { return nil }
func (f *pprofUpgrader) Upgrade() error        { return nil }
func (f *pprofUpgrader) Exit() <-chan struct{} { return nil }
func (f *pprofUpgrader) HasParent() bool       { return false }
func (f *pprofUpgrader) Stop()                 {}

// The test itself holds the requested addr the way a draining parent holds
// the port across an upgrade: a plain net.Listen would EADDRINUSE, so the
// only way to a serving pprof endpoint is the listener the upgrader hands
// over (the inherited fd).
func TestStartPprofServer_UpgraderListenerIsUsed(t *testing.T) {
	heldAddr := "127.0.0.1:" + freeTCPPort(t)
	heldLn, err := net.Listen("tcp", heldAddr)
	if err != nil {
		t.Fatalf("pre-hold %s: %v", heldAddr, err)
	}
	defer heldLn.Close()
	t.Setenv("GBOT_PPROF_ADDR", "")

	inheritedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener stand-in for the inherited fd: %v", err)
	}
	defer inheritedLn.Close()
	f := &pprofUpgrader{ln: inheritedLn}

	logs := captureAppLogs(t)
	StartPprofServer(heldAddr, f)

	rec := waitForAppLog(t, logs, "pprof:listen")
	if rec.attrs["addr"] != inheritedLn.Addr().String() {
		t.Errorf("listen addr = %v, want the upgrader-supplied listener's %s", rec.attrs["addr"], inheritedLn.Addr().String())
	}
	if recs := logs.find("pprof:listen_failed"); len(recs) != 0 {
		t.Errorf("listen_failed logged %d times, want 0 (the upgrader path must not touch the held port)", len(recs))
	}
	if f.gotNetwork != "tcp" || f.gotAddr != heldAddr {
		t.Errorf("upgrader Listen(%q, %q), want (%q, %q)", f.gotNetwork, f.gotAddr, "tcp", heldAddr)
	}
	// The served endpoint must be the upgrader's listener itself, not a
	// second bind somewhere else.
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME — bounded wait for the serve goroutine
	for {
		resp, err := http.Get("http://" + inheritedLn.Addr().String() + "/debug/pprof/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
			t.Fatalf("GET /debug/pprof/ on inherited listener: status %d, want 200", resp.StatusCode)
		}
		if time.Now().After(deadline) { // REAL-TIME — poll loop bound
			t.Fatalf("inherited listener never served pprof: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME — real HTTP poll cadence
	}
}

// The requested addr is FREE here, so any silent fallback to net.Listen
// would bind successfully and expose itself as a pprof:listen record; the
// contract is WARN-and-give-up with the upgrader error passed through.
func TestStartPprofServer_UpgraderFailureWarnsWithoutFallback(t *testing.T) {
	addr := "127.0.0.1:" + freeTCPPort(t)
	t.Setenv("GBOT_PPROF_ADDR", "")

	logs := captureAppLogs(t)
	StartPprofServer(addr, &exitUpgrader{})

	rec := waitForAppLog(t, logs, "pprof:listen_failed")
	// The handler stores the raw slog value (an error, not a string);
	// fmt.Sprint normalizes either shape for the containment check.
	if got := fmt.Sprint(rec.attrs["error"]); !strings.Contains(got, net.ErrClosed.Error()) {
		t.Errorf("listen_failed error = %q, want the upgrader error %q passed through", got, net.ErrClosed.Error())
	}
	if recs := logs.find("pprof:listen"); len(recs) != 0 {
		t.Errorf("pprof:listen logged %d times after an upgrader failure, want 0 (no fallback bind)", len(recs))
	}
}
