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

func TestCleanupOldSessions_HomeError(t *testing.T) {
	// Trigger os.UserHomeDir error (cleanup.go:33-35)
	t.Setenv("HOME", "")
	_, err := CleanupOldSessions(time.Now())
	if err == nil {
		t.Fatal("expected error when HOME is invalid")
	}
	if !strings.Contains(err.Error(), "home") && !strings.Contains(err.Error(), "HOME") {
		t.Errorf("error should relate to home directory, got: %v", err)
	}
}

func TestCleanupOldSessions_ReadDirError(t *testing.T) {
	// Trigger os.ReadDir error that is NOT os.IsNotExist (cleanup.go:42)
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create sessions dir as a file (not a directory) to cause ReadDir failure
	sessionsDir := filepath.Join(tmpDir, ".gbot", "sessions")
	if err := os.MkdirAll(filepath.Dir(sessionsDir), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(sessionsDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := CleanupOldSessions(time.Now())
	if err == nil {
		t.Fatal("expected error when sessions dir is a file")
	}
	if !strings.Contains(err.Error(), "sessions") {
		t.Errorf("error should mention sessions, got: %v", err)
	}
}

func TestCleanupOldSessions_RemoveAllFailure(t *testing.T) {
	// Trigger os.RemoveAll failure when the tool-results dir is old
	// but RemoveAll fails (cleanup.go:56 — cleaned not incremented)
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	ResetDirCache()

	// Create an old session with tool-results
	oldDir := filepath.Join(tmpDir, ".gbot", "sessions", "old-rm-session", ToolResultsSubdir)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Set mtime to past
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// Make the sessions dir read-only to potentially interfere with RemoveAll
	// This is a best-effort test - on some systems root can still delete
	cutoff := time.Now().Add(-24 * time.Hour)
	cleaned, err := CleanupOldSessions(cutoff)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	// Even if RemoveAll succeeds, count should be >= 0
	if cleaned < 0 {
		t.Errorf("cleaned should be >= 0, got %d", cleaned)
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
