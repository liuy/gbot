package toolresult

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupSession(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	sessionID := "test-cleanup"
	// Create tool-results directory with a file
	dir, _ := GetToolResultsDir(sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := CleanupSession(sessionID)
	if err != nil {
		t.Fatalf("CleanupSession: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Error("directory should be removed")
	}
}

func TestCleanupSession_Nonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	err := CleanupSession("nonexistent-session")
	if err != nil {
		t.Errorf("CleanupSession on nonexistent dir should return nil, got: %v", err)
	}
}

func TestCleanupSession_InvalidID(t *testing.T) {
	err := CleanupSession("../../../etc")
	if err == nil {
		t.Fatal("expected error for invalid sessionID")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid, got: %v", err)
	}
}

func TestCleanupOldSessions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create an old session with tool-results
	oldDir := filepath.Join(tmpDir, ".gbot", "sessions", "old-session", ToolResultsSubdir)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "test.txt"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Set mtime to past
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Create a recent session that should not be cleaned
	recentDir := filepath.Join(tmpDir, ".gbot", "sessions", "recent-session", ToolResultsSubdir)
	if err := os.MkdirAll(recentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recentDir, "test.txt"), []byte("recent"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	cleaned, err := CleanupOldSessions(cutoff)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}
	// Old dir should be gone
	if _, statErr := os.Stat(oldDir); !os.IsNotExist(statErr) {
		t.Error("old session tool-results should be removed")
	}
	// Recent dir should still exist
	if _, statErr := os.Stat(recentDir); os.IsNotExist(statErr) {
		t.Error("recent session tool-results should not be removed")
	}
}

func TestCleanupOldSessions_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	cleaned, err := CleanupOldSessions(time.Now())
	if err != nil {
		t.Fatalf("CleanupOldSessions with no sessions dir: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

func TestCleanupOldSessions_WithNonDirEntry(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create a file (not directory) in sessions — should be skipped
	sessionsDir := filepath.Join(tmpDir, ".gbot", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "not-a-dir.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cleaned, err := CleanupOldSessions(time.Now())
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (no dirs), got %d", cleaned)
	}
}

func TestCleanupOldSessions_SessionWithoutToolResults(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Session dir exists but no tool-results subdir
	sessionsDir := filepath.Join(tmpDir, ".gbot", "sessions")
	if err := os.MkdirAll(filepath.Join(sessionsDir, "empty-session"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cleaned, err := CleanupOldSessions(time.Now())
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (no tool-results), got %d", cleaned)
	}
}
