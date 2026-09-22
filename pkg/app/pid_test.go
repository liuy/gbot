package app

import (
	"os"
	"strconv"
	"strings"
	"testing"

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

	// Write our own PID (which is definitely alive)
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
