package dream

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestReadLastConsolidatedAt_Absent(t *testing.T) {
	tmpDir := t.TempDir()
	// No lock file → 0
	got, err := ReadLastConsolidatedAt(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("ReadLastConsolidatedAt = %d, want 0 for absent file", got)
	}
}

func TestReadLastConsolidatedAt_Existing(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Create lock file with known content
	if err := os.WriteFile(lockPath, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set mtime to a known time
	want := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	if err := os.Chtimes(lockPath, want, want); err != nil {
		t.Fatal(err)
	}

	got, err := ReadLastConsolidatedAt(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantMs := want.UnixMilli()
	// Allow 1ms tolerance for filesystem precision
	if got < wantMs-1 || got > wantMs+1 {
		t.Errorf("ReadLastConsolidatedAt = %d, want ~%d", got, wantMs)
	}
}

func TestTryAcquire_NoPrior(t *testing.T) {
	tmpDir := t.TempDir()

	priorMtime, acquired, err := TryAcquireConsolidationLock(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !acquired {
		t.Error("TryAcquire should succeed with no prior lock")
	}
	if priorMtime != 0 {
		t.Errorf("priorMtime = %d, want 0 for no prior lock", priorMtime)
	}

	// Verify PID was written
	data, err := os.ReadFile(filepath.Join(tmpDir, lockFileName))
	if err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
	if string(data) != pidString() {
		t.Errorf("lock file content = %q, want %q", string(data), pidString())
	}
}

func TestTryAcquire_StaleReclaim(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Create stale lock (older than 1 hour)
	oldTime := time.Now().Add(-2 * time.Hour) // REALTIME: testing stale lock reclamation
	if err := os.WriteFile(lockPath, []byte("99999"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(lockPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	priorMtime, acquired, err := TryAcquireConsolidationLock(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !acquired {
		t.Error("TryAcquire should reclaim stale lock")
	}
	if priorMtime == 0 {
		t.Error("priorMtime should be non-zero for stale lock")
	}
}

func TestTryAcquire_RaceCondition(t *testing.T) {
	tmpDir := t.TempDir()

	// First, acquire the lock ourselves
	priorMtime, acquired, err := TryAcquireConsolidationLock(tmpDir)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if !acquired {
		t.Fatal("first acquire should succeed")
	}

	// Now try to acquire from "another process" — should fail because
	// the lock is fresh (mtime is now) and held by our PID (which is alive).
	_, acquired2, err := TryAcquireConsolidationLock(tmpDir)
	if err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	if acquired2 {
		t.Error("second acquire should fail — lock is held by live PID")
	}

	// Clean up
	RollbackConsolidationLock(tmpDir, priorMtime)
}

func TestRollback_ZeroMtime(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Create lock file
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	RollbackConsolidationLock(tmpDir, 0)

	// File should be unlinked
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be unlinked after rollback with priorMtime=0")
	}
}

func TestRollback_NonZeroMtime(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	// Create lock file
	if err := os.WriteFile(lockPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	wantMtime := time.Date(2025, 1, 10, 12, 0, 0, 0, time.Local)
	RollbackConsolidationLock(tmpDir, wantMtime.UnixMilli())

	// File should still exist with rewound mtime
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("lock file should exist after rollback: %v", err)
	}
	gotMtime := info.ModTime()
	diff := gotMtime.Sub(wantMtime)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("mtime = %v, want ~%v", gotMtime, wantMtime)
	}

	// Body should be empty
	data, _ := os.ReadFile(lockPath)
	if len(data) != 0 {
		t.Errorf("lock file body = %q, want empty", string(data))
	}
}

func TestRecordConsolidation(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockFileName)

	RecordConsolidation(tmpDir)

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
	if string(data) != pidString() {
		t.Errorf("lock file content = %q, want %q", string(data), pidString())
	}

	// mtime should be recent
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > 5*time.Second {
		t.Errorf("mtime too old: %v", info.ModTime())
	}
}

func TestRecordConsolidation_CreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "memory", "nested")

	RecordConsolidation(nestedDir)

	lockPath := filepath.Join(nestedDir, lockFileName)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock file not created in nested dir: %v", err)
	}
	if string(data) != pidString() {
		t.Errorf("lock file content = %q, want %q", string(data), pidString())
	}
}

// pidString returns the current process PID as a string.
func pidString() string {
	return strconv.Itoa(os.Getpid())
}
