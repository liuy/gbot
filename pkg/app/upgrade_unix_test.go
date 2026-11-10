//go:build !windows

package app

import (
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/wui"
)

// fakeUpgrader records Upgrade() calls without touching tableflip — the
// one-upgrader-per-process constraint forbids a second tableflip.New here.
type fakeUpgrader struct {
	upgradeCalls atomic.Int32
	err          error  // returned by Upgrade when non-nil
	during       func() // invoked inside Upgrade — mid-call sampling
}

func (f *fakeUpgrader) Listen(network, addr string) (net.Listener, error) { return nil, nil }
func (f *fakeUpgrader) Ready() error                                      { return nil }
func (f *fakeUpgrader) Exit() <-chan struct{}                             { return nil }
func (f *fakeUpgrader) HasParent() bool                                   { return false }
func (f *fakeUpgrader) Stop()                                             {}
func (f *fakeUpgrader) Upgrade() error {
	f.upgradeCalls.Add(1)
	if f.during != nil {
		f.during()
	}
	return f.err
}

// waitForAppLogCount blocks until n records with msg have landed, so a
// signal-driven handler (which logs from its own goroutine) can be counted.
func waitForAppLogCount(t *testing.T, h *appLogHandler, msg string, n int) []appLogRec {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME — signal handler goroutine
	for time.Now().Before(deadline) {
		if recs := h.find(msg); len(recs) >= n {
			return recs
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	return h.find(msg)
}

// TestNotifyUpgradeSignal_TUIModeRefuses: in TUI mode the signal handler
// must stay ARMED (a bare SIGUSR2 default-terminates the process) but never
// reach Upgrade — same gate as the admin endpoint. The refusal must also be
// logged once PER SIGNAL: an operator poking SIGUSR2 at a TUI daemon gets
// nothing else back, and a `break` instead of `continue` would disarm the
// explanation after the first one.
func TestNotifyUpgradeSignal_TUIModeRefuses(t *testing.T) {
	logs := captureAppLogs(t)
	signal.Reset(syscall.SIGUSR2)
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR2) })
	fake := &fakeUpgrader{}
	notifyUpgradeSignal(fake, fake.Upgrade, func() wui.BusyReport {
		return wui.BusyReport{Busy: false, Items: []wui.BusyItem{}}
	}, true)
	const refusal = "upgrade: sigusr2 refused — TUI mode cannot hot-restart (two processes cannot share one tty); exit and restart manually"
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2: %v", err)
	}
	if recs := waitForAppLogCount(t, logs, refusal, 1); len(recs) != 1 {
		t.Errorf("TUI refusal logs after one signal = %d, want 1", len(recs))
	} else if recs[0].level != slog.LevelWarn {
		t.Errorf("TUI refusal level = %v, want WARN", recs[0].level)
	}
	deadline := time.Now().Add(500 * time.Millisecond) // REAL-TIME
	for time.Now().Before(deadline) {
		if fake.upgradeCalls.Load() != 0 {
			t.Fatal("Upgrade fired in TUI mode — signal gate missing")
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME — give the handler time to misbehave
	}
	if fake.upgradeCalls.Load() != 0 {
		t.Fatal("Upgrade fired in TUI mode — signal gate missing")
	}
	// Second signal: the loop must keep serving (continue, not break).
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2 (second): %v", err)
	}
	if recs := waitForAppLogCount(t, logs, refusal, 2); len(recs) != 2 {
		t.Errorf("TUI refusal logs after two signals = %d, want 2 — the handler loop stopped", len(recs))
	}
}

func TestNotifyUpgradeSignal_GatedByBusy(t *testing.T) {
	logs := captureAppLogs(t)
	// Reset FIRST: Start()-based tests in this package arm the REAL daemon
	// handler (notifyUpgradeSignal inside Start). A live SIGUSR2 here would
	// fork-exec the test binary via tableflip.
	signal.Reset(syscall.SIGUSR2)
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR2) })

	// Busy case: signal arrives, upgrade must NOT fire.
	busyFake := &fakeUpgrader{}
	notifyUpgradeSignal(busyFake, busyFake.Upgrade, func() wui.BusyReport {
		return wui.BusyReport{Busy: true, Items: []wui.BusyItem{{Kind: "query"}}}
	}, false)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2: %v", err)
	}
	// The refusal must name the activity: a bare "no" is undiagnosable.
	if recs := waitForAppLogCount(t, logs, "upgrade: sigusr2 refused — busy", 1); len(recs) != 1 {
		t.Errorf("busy refusal logs = %d, want 1", len(recs))
	} else if v, ok := recs[0].attrs["items"].(int64); !ok || v != 1 {
		t.Errorf("busy refusal items attr = %#v, want int64(1)", recs[0].attrs["items"])
	}
	deadline := time.Now().Add(500 * time.Millisecond) // REAL-TIME — give the handler time to misbehave
	for time.Now().Before(deadline) {
		if busyFake.upgradeCalls.Load() != 0 {
			t.Fatalf("Upgrade called %d times while busy — SIGUSR2 bypassed the busy gate", busyFake.upgradeCalls.Load())
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if busyFake.upgradeCalls.Load() != 0 {
		t.Fatalf("Upgrade called while busy — SIGUSR2 bypassed the busy gate")
	}

	// Idle case: signal arrives, upgrade MUST fire.
	idleFake := &fakeUpgrader{}
	notifyUpgradeSignal(idleFake, idleFake.Upgrade, func() wui.BusyReport {
		return wui.BusyReport{Busy: false, Items: []wui.BusyItem{}}
	}, false)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2 (idle): %v", err)
	}
	ok := false
	deadline = time.Now().Add(2 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {
		if idleFake.upgradeCalls.Load() > 0 {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME
	}
	if !ok {
		t.Fatal("Upgrade never called after idle SIGUSR2 within 2s")
	}
	// The accepted outcome is the only confirmation the operator gets that the
	// restart actually started.
	if recs := waitForAppLogCount(t, logs, "upgrade: sigusr2 accepted", 1); len(recs) != 1 {
		t.Errorf("accepted logs = %d, want 1", len(recs))
	} else if recs[0].level != slog.LevelInfo {
		t.Errorf("accepted level = %v, want INFO", recs[0].level)
	}
}

// TestNotifyUpgradeSignal_UnsupportedUpgraderLogs pins the third outcome: with no
// tableflip trigger wired (upgrade == nil), RequestRestart reports Unsupported
// and the signal must say so rather than fall through silently.
func TestNotifyUpgradeSignal_UnsupportedUpgraderLogs(t *testing.T) {
	logs := captureAppLogs(t)
	signal.Reset(syscall.SIGUSR2)
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR2) })

	fake := &fakeUpgrader{}
	notifyUpgradeSignal(fake, nil, func() wui.BusyReport {
		return wui.BusyReport{Busy: false, Items: []wui.BusyItem{}}
	}, false)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2: %v", err)
	}
	if recs := waitForAppLogCount(t, logs, "upgrade: sigusr2 refused — unsupported", 1); len(recs) != 1 {
		t.Errorf("unsupported refusal logs = %d, want 1", len(recs))
	} else if recs[0].level != slog.LevelWarn {
		t.Errorf("unsupported refusal level = %v, want WARN", recs[0].level)
	}
	if n := fake.upgradeCalls.Load(); n != 0 {
		t.Errorf("Upgrade called %d times with no trigger wired, want 0", n)
	}
}

// TestNotifyUpgradeSignal_NilUpgraderArmsNothing pins the nil gate: with no
// upgrader there is no restart to trigger, so the handler must not be armed at
// all. Go ignores an unhandled SIGUSR2, so the in-process probe is safe — the
// signal simply goes nowhere, and any refusal line proves a handler was armed.
func TestNotifyUpgradeSignal_NilUpgraderArmsNothing(t *testing.T) {
	logs := captureAppLogs(t)
	signal.Reset(syscall.SIGUSR2)
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR2) })

	notifyUpgradeSignal(nil, nil, func() wui.BusyReport {
		return wui.BusyReport{Busy: false, Items: []wui.BusyItem{}}
	}, false)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2: %v", err)
	}
	// Give a wrongly-armed handler time to log through its goroutine.
	time.Sleep(300 * time.Millisecond) // REAL-TIME
	for _, msg := range []string{
		"upgrade: sigusr2 refused — unsupported",
		"upgrade: sigusr2 refused — busy",
		"upgrade: sigusr2 accepted",
	} {
		if recs := logs.find(msg); len(recs) != 0 {
			t.Errorf("nil upgrader armed a handler: %d x %q", len(recs), msg)
		}
	}
}
