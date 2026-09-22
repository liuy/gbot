//go:build !windows

package app

import (
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/connector/wui"
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

// The falsifiable pin that SIGUSR2 is not an ungated bypass: a busy daemon
// receives the signal and never calls Upgrade; an idle one does.
// TestNotifyUpgradeSignal_TUIModeRefuses: in TUI mode the signal handler
// must stay ARMED (a bare SIGUSR2 default-terminates the process) but never
// reach Upgrade — same gate as the admin endpoint.
func TestNotifyUpgradeSignal_TUIModeRefuses(t *testing.T) {
	signal.Reset(syscall.SIGUSR2)
	t.Cleanup(func() { signal.Reset(syscall.SIGUSR2) })
	fake := &fakeUpgrader{}
	notifyUpgradeSignal(fake, fake.Upgrade, func() wui.BusyReport {
		return wui.BusyReport{Busy: false, Items: []wui.BusyItem{}}
	}, true)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatalf("kill SIGUSR2: %v", err)
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
}

func TestNotifyUpgradeSignal_GatedByBusy(t *testing.T) {
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
}
