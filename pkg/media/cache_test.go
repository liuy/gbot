package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewAt_CreatesSubdirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewAt(dir)
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	if s.RootDir != dir {
		t.Errorf("RootDir = %q, want %q", s.RootDir, dir)
	}
	for _, cat := range []Category{CategoryImage, CategoryDocument} {
		info, err := os.Stat(filepath.Join(dir, string(cat)))
		if err != nil {
			t.Fatalf("subdir %s not created: %v", cat, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", cat)
		}
	}
}

func TestSave_NewFile(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	data := []byte("png-bytes-here")
	path, err := s.Save(CategoryImage, data, ".png")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Ext(path) != ".png" {
		t.Errorf("path ext = %q, want .png", filepath.Ext(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("file content = %q, want %q", got, data)
	}
}

func TestSave_Dedup_SamePathSameMtime(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	data := []byte("identical-content")
	path1, err := s.Save(CategoryImage, data, ".png")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Backdate mtime so we can detect a rewrite would change it.
	backdated := time.Now().Add(-2 * time.Hour) // REAL-TIME
	if err := os.Chtimes(path1, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	path2, err := s.Save(CategoryImage, data, ".png")
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if path1 != path2 {
		t.Errorf("dedup path mismatch: %q != %q", path1, path2)
	}
	info2, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat second: %v", err)
	}
	// mtime must be unchanged — the file was NOT rewritten.
	if !info2.ModTime().Equal(backdated) {
		t.Errorf("dedup rewrote file: mtime = %v, want %v", info2.ModTime(), backdated)
	}
}

func TestSave_DifferentContent_DifferentPaths(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	p1, err := s.Save(CategoryImage, []byte("aaa"), ".png")
	if err != nil {
		t.Fatalf("Save aaa: %v", err)
	}
	p2, err := s.Save(CategoryImage, []byte("bbb"), ".png")
	if err != nil {
		t.Fatalf("Save bbb: %v", err)
	}
	if p1 == p2 {
		t.Errorf("different content should map to different paths, both = %q", p1)
	}
}

func TestSave_DifferentCategory_DifferentDirs(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	data := []byte("same-bytes")
	pImg, err := s.Save(CategoryImage, data, ".bin")
	if err != nil {
		t.Fatalf("Save image: %v", err)
	}
	pDoc, err := s.Save(CategoryDocument, data, ".bin")
	if err != nil {
		t.Fatalf("Save document: %v", err)
	}
	if filepath.Dir(pImg) == filepath.Dir(pDoc) {
		t.Errorf("same content under different categories must land in different dirs: %s", pImg)
	}
}

func TestSave_EmptyExt_DefaultsBin(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	path, err := s.Save(CategoryImage, []byte("x"), "")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Ext(path) != ".bin" {
		t.Errorf("empty ext should fall back to .bin, got path = %q", path)
	}
}

func TestPath_NoWrite(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	path := s.Path(CategoryImage, []byte("never-written"), ".png")
	if filepath.Ext(path) != ".png" {
		t.Errorf("Path ext = %q, want .png", filepath.Ext(path))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Path must not write a file, stat err = %v", err)
	}
}

func TestCleanup_RemovesOldFiles(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	stalePath, err := s.Save(CategoryImage, []byte("stale"), ".png")
	if err != nil {
		t.Fatalf("Save stale: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	removed := s.Cleanup(CategoryImage, 30*24*time.Hour)
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Errorf("stale file should be removed, stat err = %v", err)
	}
}

func TestCleanup_KeepsRecentFiles(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	freshPath, err := s.Save(CategoryImage, []byte("fresh"), ".png")
	if err != nil {
		t.Fatalf("Save fresh: %v", err)
	}
	removed := s.Cleanup(CategoryImage, 30*24*time.Hour)
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (file is recent)", removed)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh file should remain, stat err = %v", err)
	}
}

func TestCleanup_MissingDir_NotError(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	// documents dir exists (created by NewAt), but point at a category with no dir.
	removed := s.Cleanup(Category("nonexistent"), time.Hour)
	if removed != 0 {
		t.Errorf("removed = %d, want 0 for missing dir", removed)
	}
}

func TestCleanupAll_BothCategories(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	imgPath, _ := s.Save(CategoryImage, []byte("img-stale"), ".png")
	docPath, _ := s.Save(CategoryDocument, []byte("doc-stale"), ".pdf")
	old := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(imgPath, old, old); err != nil {
		t.Fatalf("chtimes img: %v", err)
	}
	if err := os.Chtimes(docPath, old, old); err != nil {
		t.Fatalf("chtimes doc: %v", err)
	}
	removed := s.CleanupAll(30 * 24 * time.Hour)
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (one per category)", removed)
	}
}

func TestClose_NoOpWithoutCleanup(t *testing.T) {
	t.Parallel()
	// NewAt does not start cleanup — Close must be a safe no-op.
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	s.Close() // must not panic or block
	s.Close() // idempotent
}

func TestClose_StopsCleanupLoop(t *testing.T) {
	dir := t.TempDir()
	// Use New() with a HOME override so os.UserHomeDir() resolves into the temp tree.
	t.Setenv("HOME", dir)
	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Add a stale file AFTER closing — it must survive, proving the loop stopped.
	s.Close()
	stalePath, err := s.Save(CategoryImage, []byte("post-close-stale"), ".png")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // REAL-TIME
	if _, err := os.Stat(stalePath); err != nil {
		t.Errorf("post-close stale file should remain (loop stopped), stat err = %v", err)
	}
}

func TestNew_StartsWithCleanup_EvictsStale(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Construct directly so we can use a very short interval for the test.
	s, err := newStoreRoot(filepath.Join(dir, ".gbot", "cache"))
	if err != nil {
		t.Fatalf("newStoreRoot: %v", err)
	}
	s.cancelStop, s.stopDone = s.startCleanupLoop(context.Background(), 50*time.Millisecond, 1*time.Nanosecond)
	defer s.Close()
	stalePath, err := s.Save(CategoryImage, []byte("will-be-evicted"), ".png")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// maxAge=1ns means everything is stale; wait for one tick.
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		if _, err := os.Stat(stalePath); os.IsNotExist(err) {
			return // pass
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME
	}
	t.Errorf("stale file was not evicted by cleanup loop: %s still exists", stalePath)
}

func TestStartCleanup_StopsOnContextCancel(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := s.StartCleanup(ctx, time.Hour, 30*24*time.Hour)
	cancel()
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
		// pass — stop() returned promptly after ctx cancel.
	case <-time.After(2 * time.Second):
		t.Error("StartCleanup stop function did not return after context cancel")
	}
}

func TestStartCleanup_EvictsOldFiles(t *testing.T) {
	t.Parallel()
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	stalePath, err := s.Save(CategoryImage, []byte("old"), ".png")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	stop := s.StartCleanup(context.Background(), 50*time.Millisecond, 30*24*time.Hour)
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME
	for time.Now().Before(deadline) {           // REAL-TIME
		if _, err := os.Stat(stalePath); os.IsNotExist(err) {
			stop()
			return // pass
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME
	}
	stop()
	t.Errorf("stale file not evicted by StartCleanup loop")
}
