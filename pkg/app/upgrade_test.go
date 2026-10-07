package app

import (
	"errors"
	"os"
	"strings"
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

// TestNewUpgrader_ScrubsTableflipMarkerFromEnv guards the leak that made every
// descendant of a hot-restarted daemon believe it was a tableflip child: the
// marker sits in the daemon's own env, and exec.Cmd inherits os.Environ() by
// default, so any child's tableflip.New tries to decode parent fds that do not
// exist there and dies with EBADF. Asserting on os.Environ() is asserting on
// exactly what a child would inherit.
//
// Unlike the rest of this file, the first subtest does reach tableflip.New, so it
// constructs an Upgrader whenever the process-wide slot is still free. That is
// benign today only because file-name ordering runs TestNewUpgrader_PinsArgv0
// first: by the time this subtest runs, the slot is claimed either by that test or
// by an earlier Start(), and newUpgrader returns nil here.
func TestNewUpgrader_ScrubsTableflipMarkerFromEnv(t *testing.T) {
	marker := tableflipEnvPrefix + "HAS_PARENT_7DIU3"

	t.Run("cold boot drops it before New", func(t *testing.T) {
		t.Setenv("GBOT_SUPERVISED", "0")
		t.Setenv(marker, "yes")
		if upg := newUpgrader(); upg != nil {
			upg.Stop()
		}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, tableflipEnvPrefix) {
				t.Errorf("marker survived into the process env, so every child inherits it: %s", kv)
			}
		}
	})

	// Supervised daemons never call tableflip.New, so nothing consumes the
	// marker here — it must still not be handed down.
	t.Run("supervised drops it without constructing", func(t *testing.T) {
		t.Setenv("GBOT_SUPERVISED", "1")
		t.Setenv(marker, "yes")
		if upg := newUpgrader(); upg != nil {
			t.Error("supervised mode must not construct an upgrader")
		}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, tableflipEnvPrefix) {
				t.Errorf("marker survived into the process env, so every child inherits it: %s", kv)
			}
		}
	})
}
