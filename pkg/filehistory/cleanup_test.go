package filehistory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupOldBackups_RemovesOldDirectories(t *testing.T) {
	parent := t.TempDir()
	fhDir := filepath.Join(parent, "file-history")

	// Create two session directories
	oldDir := filepath.Join(fhDir, "old-session")
	newDir := filepath.Join(fhDir, "new-session")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write content to verify it gets deleted
	if err := os.WriteFile(filepath.Join(oldDir, "backup.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set old directory's mtime to 31 days ago
	oldTime := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	cleaned, err := CleanupOldBackups(fhDir, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned directory, got %d", cleaned)
	}

	// Old directory should be gone
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Error("old directory should be removed")
	}
	// New directory should still exist
	if _, err := os.Stat(newDir); err != nil {
		t.Error("new directory should still exist")
	}
}

func TestCleanupOldBackups_NonexistentDir(t *testing.T) {
	cleaned, err := CleanupOldBackups("/nonexistent/path", DefaultCleanupAge)
	if err != nil {
		t.Fatalf("nonexistent dir should not error, got: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

func TestCleanupOldBackups_NothingToRemove(t *testing.T) {
	fhDir := t.TempDir()

	// Create a recent directory
	recentDir := filepath.Join(fhDir, "recent-session")
	if err := os.MkdirAll(recentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cleaned, err := CleanupOldBackups(fhDir, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
	if _, err := os.Stat(recentDir); err != nil {
		t.Error("recent directory should still exist")
	}
}
