package filehistory

import (
	"os"
	"path/filepath"
	"time"

	"testing"
)

// testTime is a fixed timestamp for deterministic tests.
var testTime = time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func TestTakeSnapshot_CapturesFiles(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.txt"), []byte("hello"))
	mustWriteFile(t, filepath.Join(root, "b.txt"), []byte("world"))
	mustMkdirAll(t, filepath.Join(root, "sub"))
	mustWriteFile(t, filepath.Join(root, "sub", "c.txt"), []byte("nested"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Should have 3 files
	if len(snap) != 3 {
		t.Fatalf("expected 3 files, got %d", len(snap))
	}

	for _, path := range []string{
		filepath.Join(root, "a.txt"),
		filepath.Join(root, "b.txt"),
		filepath.Join(root, "sub", "c.txt"),
	} {
		s, ok := snap[path]
		if !ok {
			t.Errorf("missing snapshot for %s", path)
			continue
		}
		if s.modTime.IsZero() {
			t.Errorf("snapshot for %s has zero modTime", path)
		}
		if s.size == 0 {
			t.Errorf("snapshot for %s has zero size", path)
		}
	}
}

func TestTakeSnapshot_SkipsLargeDirs(t *testing.T) {
	root := t.TempDir()
	// Create files in .git and node_modules — these should be skipped
	mustMkdirAll(t, filepath.Join(root, ".git", "objects"))
	mustWriteFile(t, filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main"))
	mustMkdirAll(t, filepath.Join(root, "node_modules", "pkg"))
	mustWriteFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("module.exports = {}"))
	// Create a normal file
	mustWriteFile(t, filepath.Join(root, "main.go"), []byte("package main"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	if len(snap) != 1 {
		t.Fatalf("expected 1 file (skipping .git and node_modules), got %d", len(snap))
	}
	if _, ok := snap[filepath.Join(root, "main.go")]; !ok {
		t.Error("expected main.go in snapshot")
	}
}

func TestDetectChanges_ModifiedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "data.txt")
	mustWriteFile(t, file, []byte("before"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Set original file mtime to past to guarantee mtime change on rewrite
	past := testTime.Add(-1 * time.Hour)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	mustWriteFile(t, file, []byte("after content change"))

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != file {
		t.Errorf("expected path %s, got %s", file, changes[0].Path)
	}
	// BeforeContent is nil in lazy snapshot mode — content not stored in snapshot
	if changes[0].BeforeContent != nil {
		t.Errorf("expected nil BeforeContent (lazy snapshot), got %q", string(changes[0].BeforeContent))
	}
	if string(changes[0].AfterContent) != "after content change" {
		t.Errorf("expected after content 'after content change', got %q", string(changes[0].AfterContent))
	}
}

func TestDetectChanges_NewFile(t *testing.T) {
	root := t.TempDir()
	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Create new file
	mustWriteFile(t, filepath.Join(root, "new.txt"), []byte("new file"))

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != filepath.Join(root, "new.txt") {
		t.Errorf("expected new.txt path, got %s", changes[0].Path)
	}
	if changes[0].BeforeContent != nil {
		t.Errorf("expected nil BeforeContent for new file, got %q", string(changes[0].BeforeContent))
	}
	// New files have nil AfterContent per design (we don't need after-content for new files)
	if changes[0].AfterContent != nil {
		t.Errorf("expected nil AfterContent for new file, got %q", string(changes[0].AfterContent))
	}
}

func TestDetectChanges_DeletedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "gone.txt")
	mustWriteFile(t, file, []byte("will be deleted"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Delete the file
	if err := os.Remove(file); err != nil {
		t.Fatalf("remove: %v", err)
	}

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}

	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != file {
		t.Errorf("expected deleted file path, got %s", changes[0].Path)
	}
	// BeforeContent is nil in lazy snapshot mode
	if changes[0].BeforeContent != nil {
		t.Errorf("expected nil BeforeContent (lazy snapshot), got %q", string(changes[0].BeforeContent))
	}
	if changes[0].AfterContent != nil {
		t.Errorf("expected nil AfterContent for deleted file, got %q", string(changes[0].AfterContent))
	}
}

func TestDetectChanges_NoChanges(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "stable.txt"), []byte("unchanged"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// No changes made
	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}

	if len(changes) != 0 {
		t.Fatalf("expected 0 changes, got %d: %+v", len(changes), changes)
	}
}

func TestDetectChanges_MtimeChangedOnly(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "same.txt")
	mustWriteFile(t, file, []byte("content"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Touch the file — same content, new mtime
	now := testTime.Add(1 * time.Hour)
	if err := os.Chtimes(file, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}

	// Content unchanged — should NOT report as a change
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes (content identical), got %d: %+v", len(changes), changes)
	}
}

func TestTakeSnapshot_StatFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	root := t.TempDir()
	// Create a broken symlink — WalkDir lists it, but os.Stat follows the symlink and fails
	target := filepath.Join(root, "nonexistent")
	link := filepath.Join(root, "broken")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	// Broken symlink should be skipped (Stat failed)
	if _, ok := snap[link]; ok {
		t.Error("broken symlink should not be in snapshot")
	}
}

func TestDetectChanges_StatFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	root := t.TempDir()
	file := filepath.Join(root, "data.txt")
	mustWriteFile(t, file, []byte("before"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Add a broken symlink — WalkDir lists it, but os.Stat fails
	target := filepath.Join(root, "nonexistent")
	link := filepath.Join(root, "broken")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	// Broken symlink should be skipped, only the new-file symlink itself not reported
	for _, c := range changes {
		if c.Path == link {
			t.Errorf("broken symlink should not appear in changes, got path %s", c.Path)
		}
	}
}

func TestTakeSnapshot_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	root := t.TempDir()
	file := filepath.Join(root, "secret.txt")
	mustWriteFile(t, file, []byte("secret"))
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}
	// TakeSnapshot reads file content for hash computation — unreadable files are skipped.
	if _, ok := snap[file]; ok {
		t.Error("unreadable file should not be in snapshot")
	}
}

func TestDetectChanges_UnreadableModifiedFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	root := t.TempDir()
	file := filepath.Join(root, "data.txt")
	mustWriteFile(t, file, []byte("before"))

	snap, err := TakeSnapshot(root)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// Modify file (change mtime + content)
	past := testTime.Add(-1 * time.Hour)
	if err := os.Chtimes(file, past, past); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, file, []byte("after"))

	// Make file unreadable so ReadFile in DetectChanges fails
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	changes, err := DetectChanges(root, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	// Modified but unreadable → skipped (not reported as change)
	if len(changes) != 0 {
		t.Errorf("expected 0 changes (unreadable modified file skipped), got %d", len(changes))
	}
}
