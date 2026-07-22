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
	cleanup, err := acquirePID(projectDir)
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

	cleanup, err := acquirePID(projectDir)
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

	_, err := acquirePID(projectDir)
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

	cleanup, err := acquirePID(projectDir)
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

	cleanup, err := acquirePID(projectDir)
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
