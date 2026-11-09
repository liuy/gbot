package filehistory

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// fhLogHandler captures slog records for the filehistory tests.
type fhLogHandler struct {
	mu   sync.Mutex
	recs []fhLogRec
}

type fhLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *fhLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *fhLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, fhLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *fhLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *fhLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *fhLogHandler) find(msg string) []fhLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []fhLogRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func captureFhLogs(t *testing.T) *fhLogHandler {
	t.Helper()
	h := &fhLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestCleanupOldBackups_RemovesOnlyStaleSessions(t *testing.T) {
	root := t.TempDir()
	old1 := filepath.Join(root, "sess-old1")
	old2 := filepath.Join(root, "sess-old2")
	fresh := filepath.Join(root, "sess-fresh")
	for _, d := range []string{old1, old2, fresh} {
		if err := os.MkdirAll(filepath.Join(d, "inner"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, "dangling")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	stale := time.Unix(1, 0) // 1970: unambiguously older than any maxAge under test
	for _, d := range []string{old1, old2} {
		if err := os.Chtimes(d, stale, stale); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	cleaned, err := CleanupOldBackups(root, 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldBackups: %v", err)
	}
	if cleaned != 2 {
		t.Errorf("cleaned = %d, want 2 (both stale sessions, nothing else)", cleaned)
	}
	for _, d := range []string{old1, old2} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s still present after cleanup", d)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the fresh session was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "stray.txt")); err != nil {
		t.Errorf("a stray file in the history root was removed: %v", err)
	}
}

func TestCleanupOldBackups_RemoveFailureIsLogged(t *testing.T) {
	logs := captureFhLogs(t)
	root := t.TempDir()
	parent := filepath.Join(root, "hist")
	session := filepath.Join(parent, "sess")
	if err := os.MkdirAll(filepath.Join(session, "inner"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := time.Unix(1, 0) // 1970: unambiguously older than any maxAge under test
	if err := os.Chtimes(session, stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	cleaned, err := CleanupOldBackups(parent, 24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldBackups: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("cleaned = %d when removal fails, want 0", cleaned)
	}
	recs := logs.find("filehistory:cleanup:remove_failed")
	if len(recs) != 1 {
		t.Fatalf("remove failure logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["dir"] != session {
		t.Errorf("dir attr = %v, want the session directory", recs[0].attrs["dir"])
	}
}
