package app

import (
	"errors"
	"os"
	"testing"
)

// First test in the package process to touch tableflip. tableflip allows
// only one Upgrader per process; no other unit test may construct one (the
// daemon-level e2e uses a separate helper binary). The assertion only holds
// when run solo (go test -run TestNewUpgrader): in a full package run an
// earlier test's Start() may have consumed the one upgrader, so this test
// degrades to a skip via t.Skip.
// TestUpgradeGate_TUIModeRefuses pins the 2026-09-22 incident lesson: a TUI
// process must never hot-restart. The exec'd child inherits the parent's
// tty and both processes contend for the same termios — the child's raw
// mode entry fails with EIO AFTER Ready(), so the parent has already
// exited and neither process serves. Daemon mode (headless) upgrades fine.
func TestUpgradeGate_TUIModeRefuses(t *testing.T) {
	fake := &fakeUpgrader{}
	if got := upgradeGate(fake, false); got != nil {
		t.Error("TUI mode must gate upgrade to nil — hot-restart is daemon-only")
	}
	if got := upgradeGate(fake, true); got == nil {
		t.Error("daemon mode must wire the upgrade trigger")
	}
	if got := upgradeGate(nil, true); got != nil {
		t.Error("nil upgrader must stay nil")
	}
}

// TestUpgradeGate_HandedOverFlipsBeforeFork guards the PID-orphan window:
// the child rewrites the PID file during its multi-second boot, BEFORE the
// parent observes Exit(). handedOver must already be set when Upgrade is
// in flight, and reset when Upgrade fails (rollback: parent keeps serving
// and must not skip its own cleanup).
func TestUpgradeGate_HandedOverFlipsBeforeFork(t *testing.T) {
	handedOver.Store(false)
	t.Cleanup(func() { handedOver.Store(false) })

	failing := &fakeUpgrader{err: errors.New("exec failed")}
	if err := upgradeGate(failing, true)(); err == nil {
		t.Fatal("gate must propagate the Upgrade error")
	}
	if handedOver.Load() {
		t.Error("failed upgrade must reset handedOver — parent still owns the PID file")
	}

	ok := &fakeUpgrader{}
	// The in-flight moment cannot be sampled deterministically without
	// hooking inside Upgrade; the fake records the flag DURING the call.
	ok.during = func() {
		if !handedOver.Load() {
			t.Error("handedOver must be set BEFORE Upgrade runs — a mid-boot SIGINT would delete the child's PID file")
		}
	}
	if err := upgradeGate(ok, true)(); err != nil {
		t.Fatalf("gate: %v", err)
	}
}

func TestNewUpgrader_PinsArgv0(t *testing.T) {
	upg := newUpgrader()
	if upg == nil {
		t.Skip("upgrader unsupported on this platform or already constructed")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if os.Args[0] != exe {
		t.Errorf("os.Args[0] = %q, want os.Executable() %q", os.Args[0], exe)
	}
}
