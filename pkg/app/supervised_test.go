package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/connector/wui"
)

func idleReport() wui.BusyReport { return wui.BusyReport{Busy: false} }
func busyReport() wui.BusyReport { return wui.BusyReport{Busy: true} }

func resetStepdown(t *testing.T) string {
	t.Helper()
	steppedDown.Store(false)
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "gbot.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}
	return pidPath
}

func shortReclaimTTL(t *testing.T) (restore func()) {
	t.Helper()
	old := pidReclaimTTL
	pidReclaimTTL = 60 * time.Millisecond
	return func() { pidReclaimTTL = old }
}

func TestSupervisedStepdown_BusyRefusedLockHeld(t *testing.T) {
	pidPath := resetStepdown(t)
	released := false
	busy, report := supervisedStepdown(busyReport, func() { released = true }, filepath.Dir(pidPath))
	if !busy || !report.Busy {
		t.Fatalf("busy=%v report.Busy=%v, want true/true", busy, report.Busy)
	}
	if released {
		t.Error("lock must stay held on busy refusal")
	}
	if steppedDown.Load() {
		t.Error("steppedDown must stay false on busy refusal")
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("PID file must survive a refusal: %v", err)
	}
}

func TestSupervisedStepdown_IdleReleasesAndFlipsFlag(t *testing.T) {
	pidPath := resetStepdown(t)
	defer shortReclaimTTL(t)()
	busy, _ := supervisedStepdown(idleReport, func() {
		if err := os.Remove(pidPath); err != nil {
			t.Fatal(err)
		}
	}, filepath.Dir(pidPath))
	if busy {
		t.Fatal("idle stepdown must not report busy")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("PID file must be released, stat err=%v", err)
	}
	if !steppedDown.Load() {
		t.Error("steppedDown must flip so TERM skips pidCleanup")
	}
}

func TestSupervisedStepdown_TTLReclaimsWhenNoSuccessor(t *testing.T) {
	pidPath := resetStepdown(t)
	defer shortReclaimTTL(t)()
	supervisedStepdown(idleReport, func() {}, filepath.Dir(pidPath))
	time.Sleep(200 * time.Millisecond)
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("TTL re-claim must restore the lock: %v", err)
	}
	if pid, _ := strconv.Atoi(string(data)); pid != os.Getpid() {
		t.Errorf("re-claimed PID = %s, want our %d", string(data), os.Getpid())
	}
}

// The existence guard: a replacement that claimed the file before the TTL
// must never be clobbered with the predecessor's pid.
func TestSupervisedStepdown_TTLNeverClobbersSuccessor(t *testing.T) {
	pidPath := resetStepdown(t)
	defer shortReclaimTTL(t)()
	supervisedStepdown(idleReport, func() {}, filepath.Dir(pidPath))
	// The replacement claims the lock during the overlap window.
	if err := os.WriteFile(pidPath, []byte("424242"), 0644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	data, _ := os.ReadFile(pidPath)
	if string(data) != "424242" {
		t.Errorf("successor's PID file clobbered: %q", string(data))
	}
}
