package app

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/project"
)

func TestAcquirePID_Success(t *testing.T) {
	projectDir := t.TempDir()
	cleanup, err := acquirePID(projectDir, false)
	if err != nil {
		t.Fatalf("acquirePID failed: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(project.PIDFile(projectDir))
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file contains non-integer: %q", string(data))
	}
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquirePID_StalePID(t *testing.T) {
	projectDir := t.TempDir()
	pidPath := project.PIDFile(projectDir)

	// Write a dead PID (999999999 is unlikely to exist)
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := acquirePID(projectDir, false)
	if err != nil {
		t.Fatalf("acquirePID should succeed for stale PID, got: %v", err)
	}
	defer cleanup()

	// Verify current PID was written
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file contains non-integer: %q", string(data))
	}
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquirePID_LivePID(t *testing.T) {
	projectDir := t.TempDir()
	pidPath := project.PIDFile(projectDir)

	// Write our own PID (which is definitely alive). Shrink the grace window
	// so the wait-out doesn't slow the suite.
	oldTotal, oldStep := pidWaitTotal, pidWaitStep
	pidWaitTotal, pidWaitStep = 30*time.Millisecond, 10*time.Millisecond
	defer func() { pidWaitTotal, pidWaitStep = oldTotal, oldStep }()

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := acquirePID(projectDir, false)
	if err == nil {
		t.Fatal("expected error for live PID, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error should mention 'already running', got: %v", err)
	}
}

// The app stops the old daemon and respawns immediately: the respawn must
// wait out the dying predecessor (which still holds the PID file for a
// moment) instead of failing — that race once left the phone daemon-less
// for minutes. Simulate with a real short-lived child process.
func TestAcquirePID_WaitsOutDyingPredecessor(t *testing.T) {
	projectDir := t.TempDir()

	cmd := exec.Command("sleep", "0.4")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	// Reap promptly: an unreaped child is a zombie, and zombies still "exist"
	// for isProcessAlive, which would defeat the dying-predecessor simulation.
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	defer func() { _ = cmd.Process.Kill(); <-done }()

	if err := os.WriteFile(project.PIDFile(projectDir), []byte(strconv.Itoa(cmd.Process.Pid)), 0644); err != nil {
		t.Fatal(err)
	}

	oldTotal, oldStep := pidWaitTotal, pidWaitStep
	pidWaitTotal, pidWaitStep = 3*time.Second, 50*time.Millisecond
	defer func() { pidWaitTotal, pidWaitStep = oldTotal, oldStep }()

	cleanup, err := acquirePID(projectDir, false)
	if err != nil {
		t.Fatalf("acquirePID should wait out the dying predecessor, got: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(project.PIDFile(projectDir))
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err != nil || pid != os.Getpid() {
		t.Errorf("PID file = %q, want our PID %d", string(data), os.Getpid())
	}
}

func TestAcquirePID_Cleanup(t *testing.T) {
	projectDir := t.TempDir()
	pidPath := project.PIDFile(projectDir)

	cleanup, err := acquirePID(projectDir, false)
	if err != nil {
		t.Fatalf("acquirePID failed: %v", err)
	}

	// PID file should exist
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Fatal("PID file should exist before cleanup")
	}

	cleanup()

	// PID file should be gone
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file should be removed after cleanup")
	}
}

func TestIsProcessAlive_Self(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
}

// A tableflip child boots while its parent still holds the PID file: the
// guard must be skippable (file still overwritten with our PID) while the
// default path keeps refusing.
func TestAcquirePID_SkipsLiveGuard(t *testing.T) {
	projectDir := t.TempDir()
	pidPath := project.PIDFile(projectDir)

	// Occupy with our own PID (always alive).
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	cleanup, err := acquirePID(projectDir, true)
	if err != nil {
		t.Fatalf("acquirePID(dir, true) with live holder failed: %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read PID file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file contains non-integer: %q", string(data))
	}
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d (file must be overwritten even when the guard is skipped)", pid, os.Getpid())
	}

	// Re-occupy (cleanup above removed it only at defer time — rewrite).
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatalf("rewrite pid file: %v", err)
	}
	// Shrink the grace window: the default-path refusal now waits out the
	// window before erroring.
	oldTotal, oldStep := pidWaitTotal, pidWaitStep
	pidWaitTotal, pidWaitStep = 30*time.Millisecond, 10*time.Millisecond
	defer func() { pidWaitTotal, pidWaitStep = oldTotal, oldStep }()
	if _, err := acquirePID(projectDir, false); err == nil {
		t.Fatal("acquirePID(dir, false) with live holder succeeded, want 'already running' error")
	}
}

func TestIsProcessAlive_Nonexistent(t *testing.T) {
	// PID 999999999 is very unlikely to exist
	if isProcessAlive(999999999) {
		t.Error("PID 999999999 should not be alive")
	}
}

func TestAcquirePID_CorruptPIDFile(t *testing.T) {
	projectDir := t.TempDir()
	pidPath := project.PIDFile(projectDir)

	// Write corrupted PID file
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0644); err != nil {
		t.Fatal(err)
	}

	cleanup, err := acquirePID(projectDir, false)
	if err != nil {
		t.Fatalf("acquirePID should succeed for corrupt PID file, got: %v", err)
	}
	defer cleanup()

	// Should have overwritten with current PID
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("PID file not created: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("PID file contains non-integer: %q", string(data))
	}
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}
}
