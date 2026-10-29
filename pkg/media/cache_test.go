package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
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
	for _, cat := range []Category{CategoryImage, CategoryDocument, CategoryParse, CategoryTrace} {
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

func TestCleanup_StatErrorWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 0o000 does not block stat on Windows")
	}
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	// Strip search permission on the store root so the category stat fails
	// with a permission error, not IsNotExist.
	if err := os.Chmod(s.RootDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(s.RootDir, 0o755) }()

	var logBuf bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldDefault)

	removed := s.Cleanup(CategoryImage, time.Hour)
	if removed != 0 {
		t.Errorf("removed = %d, want 0 on stat error", removed)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "cleanup stat failed") {
		t.Errorf("cleanup log = %q, want a stat-failed warning (silent permission errors hide a dead sweep)", logs)
	}
	if !strings.Contains(logs, string(CategoryImage)) {
		t.Errorf("cleanup log = %q, want it to name the failing dir %q", logs, string(CategoryImage))
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
	parsePath, _ := s.Save(CategoryParse, []byte("parse-stale"), ".md")
	tracePath, _ := s.Save(CategoryTrace, []byte("trace-stale"), ".zip")
	old := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(imgPath, old, old); err != nil {
		t.Fatalf("chtimes img: %v", err)
	}
	if err := os.Chtimes(docPath, old, old); err != nil {
		t.Fatalf("chtimes doc: %v", err)
	}
	if err := os.Chtimes(parsePath, old, old); err != nil {
		t.Fatalf("chtimes parse: %v", err)
	}
	if err := os.Chtimes(tracePath, old, old); err != nil {
		t.Fatalf("chtimes trace: %v", err)
	}
	removed := s.CleanupAll(30 * 24 * time.Hour)
	if removed != 4 {
		t.Errorf("removed = %d, want 4 (one per category)", removed)
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

func TestCleanupNestedSubdirs(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	nested := filepath.Join(s.RootDir, "traces", "browser")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(nested, "20260101-000000.zip")
	fresh := filepath.Join(nested, "fresh.zip")
	if err := os.WriteFile(old, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-31 * 24 * time.Hour) // REAL-TIME
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	removed := s.Cleanup(CategoryTrace, 30*24*time.Hour)
	if removed != 1 {
		t.Fatalf("Cleanup removed %d files, want 1 (nested old zip)", removed)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old nested zip still exists")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh zip was removed: %v", err)
	}
}

// countFiles lists the regular files directly inside dir (no recursion).
func countFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			files = append(files, e.Name())
		}
	}
	return files
}

func TestSaveReader_DedupNoRewrite(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	b := []byte("streamed-once-streamed-twice")
	path1, size1, err := s.SaveReader(CategoryDocument, ".txt", bytes.NewReader(b), -1)
	if err != nil {
		t.Fatalf("first SaveReader: %v", err)
	}
	backdated := time.Now().Add(-2 * time.Hour) // REAL-TIME
	if err := os.Chtimes(path1, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	path2, size2, err := s.SaveReader(CategoryDocument, ".txt", bytes.NewReader(b), -1)
	if err != nil {
		t.Fatalf("second SaveReader: %v", err)
	}
	if path1 != path2 {
		t.Errorf("dedup path mismatch: %q != %q", path1, path2)
	}
	if size1 != int64(len(b)) || size2 != int64(len(b)) {
		t.Errorf("sizes = %d/%d, want %d/%d", size1, size2, len(b), len(b))
	}
	info, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat dedup path: %v", err)
	}
	if !info.ModTime().Equal(backdated) {
		t.Errorf("dedup rewrote file: mtime = %v, want %v", info.ModTime(), backdated)
	}
}

func TestSaveReader_TooLarge_NoLeftovers(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	path, size, err := s.SaveReader(CategoryDocument, ".txt", bytes.NewReader(bytes.Repeat([]byte("x"), 1024)), 512)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if path != "" || size != 0 {
		t.Errorf("over-cap return = (%q, %d), want (\"\", 0)", path, size)
	}
	if got := countFiles(t, filepath.Join(s.RootDir, string(CategoryDocument))); len(got) != 0 {
		t.Errorf("category dir = %v, want empty (temp removed on over-cap)", got)
	}
}

func TestSaveReader_ReadError_CleansTemp(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	boom := errors.New("boom")
	r := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte("y"), 100)), iotest.ErrReader(boom))
	path, size, err := s.SaveReader(CategoryDocument, ".txt", r, -1)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it wrapping boom (%%w)", err)
	}
	if path != "" || size != 0 {
		t.Errorf("read-error return = (%q, %d), want (\"\", 0)", path, size)
	}
	if got := countFiles(t, filepath.Join(s.RootDir, string(CategoryDocument))); len(got) != 0 {
		t.Errorf("category dir = %v, want empty (temp removed on read error)", got)
	}
}

// countingReader never ends, so a save that drains it past maxBytes+1 is
// observable as over-consumption of the caller's stream (an HTTP body would be
// read further than the cap detector needs).
type countingReader struct{ n int }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.n += len(p)
	return len(p), nil
}

// The over-cap detector must stop at exactly one byte past the limit: enough to
// prove the stream is over, no more.
func TestSaveReader_OverCapReadsOneBytePastLimit(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	cr := &countingReader{}
	path, size, err := s.SaveReader(CategoryDocument, ".txt", cr, 1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if cr.n != 2 {
		t.Errorf("source bytes consumed = %d, want 2 (maxBytes+1) — the copy must not drain the reader", cr.n)
	}
	if path != "" || size != 0 {
		t.Errorf("over-cap return = (%q, %d), want (\"\", 0)", path, size)
	}
	if got := countFiles(t, filepath.Join(s.RootDir, string(CategoryDocument))); len(got) != 0 {
		t.Errorf("category dir = %v, want empty", got)
	}
}

// maxBytes <= 0 is the documented unbounded sentinel: 0 must behave exactly like
// -1, not like a cap of zero bytes.
func TestSaveReader_ZeroLimitMeansUnbounded(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	b := bytes.Repeat([]byte("z"), 100)
	path, size, err := s.SaveReader(CategoryDocument, ".txt", bytes.NewReader(b), 0)
	if err != nil {
		t.Fatalf("SaveReader(maxBytes=0): %v", err)
	}
	if size != 100 {
		t.Errorf("size = %d, want 100 (0 is unbounded, not a zero-byte cap)", size)
	}
	if want := s.Path(CategoryDocument, b, ".txt"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(got, b) {
		t.Errorf("object bytes = %q, want the full 100-byte input", got)
	}
}

// Exactly maxBytes bytes is accepted: the limit is inclusive, so the over-cap
// detector fires at maxBytes+1 and not at maxBytes.
func TestSaveReader_ExactlyAtLimit_Accepted(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	b := bytes.Repeat([]byte("q"), 100)
	path, size, err := s.SaveReader(CategoryDocument, ".txt", bytes.NewReader(b), 100)
	if err != nil {
		t.Fatalf("SaveReader(maxBytes=100, 100 bytes): %v", err)
	}
	if size != 100 {
		t.Errorf("size = %d, want 100", size)
	}
	if want := s.Path(CategoryDocument, b, ".txt"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// A missing category dir makes the temp create fail: the error must name the
// directory, keep the errno reachable through %w, and return the zero values.
func TestSaveReader_MissingCategoryDir_Errors(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	dir := filepath.Join(s.RootDir, string(CategoryDocument))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove category dir: %v", err)
	}
	path, size, err := s.SaveReader(CategoryDocument, ".txt", strings.NewReader("abc"), -1)
	if err == nil {
		t.Fatal("SaveReader into a missing category dir should fail")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want it wrapping os.ErrNotExist (%%w)", err)
	}
	if want := "media: create temp in " + dir + ": "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("err = %q, want it to start with %q", err, want)
	}
	if path != "" || size != 0 {
		t.Errorf("temp-create return = (%q, %d), want (\"\", 0)", path, size)
	}
	if _, err := s.Save(CategoryDocument, []byte("abc"), ".txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Save err = %v, want it wrapping os.ErrNotExist", err)
	}
}

// A directory parked at the destination fails the rename: the errno must stay
// reachable through %w and the return must be the zero values.
func TestSaveReader_RenameFailure_WrapsCause(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	data := []byte("rename-target")
	dst := s.Path(CategoryDocument, data, ".pdf")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	path, size, err := s.SaveReader(CategoryDocument, ".pdf", bytes.NewReader(data), -1)
	if err == nil {
		t.Fatal("SaveReader into a directory-occupied destination should fail")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("err = %v, want it wrapping the rename errno (%%w)", err)
	}
	if path != "" || size != 0 {
		t.Errorf("rename-failure return = (%q, %d), want (\"\", 0)", path, size)
	}
	if !strings.Contains(err.Error(), dst) {
		t.Errorf("err = %v, want it naming the destination %q", err, dst)
	}
	if got := countFiles(t, filepath.Join(s.RootDir, string(CategoryDocument))); len(got) != 0 {
		t.Errorf("category dir = %v, want empty (temp removed on rename failure)", got)
	}
}

// waitForTemps polls dir until exactly want save-*.tmp temps exist, or fails
// after a deadline. The deadline failure mode is the whole point of M4: with a
// destination-derived temp name the second save reuses the first temp's path
// and the count never reaches two distinct names.
func waitForTemps(t *testing.T, dir string, want int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME
	for {
		matches, err := filepath.Glob(filepath.Join(dir, "save-*.tmp"))
		if err != nil {
			t.Fatalf("glob temps: %v", err)
		}
		if len(matches) == want {
			return matches
		}
		if len(matches) > want {
			t.Fatalf("found %d temps, want exactly %d: %v", len(matches), want, matches)
		}
		if time.Now().After(deadline) { // REAL-TIME
			t.Fatalf("only %d in-flight temps after deadline, want %d: %v", len(matches), want, matches)
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME
	}
}

func TestSave_UniqueTempNamesPerCall(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	dir := filepath.Join(s.RootDir, string(CategoryImage))
	payload := []byte("abcd")

	// Save 1: hold the copy mid-stream (pipe writer left open) so its temp is
	// observable on disk. io.Pipe writes are synchronous, so Write returns only
	// after the copy consumed the bytes — the ordering is deterministic.
	pr1, pw1 := io.Pipe()
	res1 := make(chan error, 1)
	go func() {
		_, _, err := s.SaveReader(CategoryImage, ".png", pr1, -1)
		res1 <- err
	}()
	if _, err := pw1.Write(payload); err != nil {
		t.Fatalf("write pipe1: %v", err)
	}
	waitForTemps(t, dir, 1)

	// Save 2: identical bytes while the first copy is still parked.
	pr2, pw2 := io.Pipe()
	res2 := make(chan error, 1)
	go func() {
		_, _, err := s.SaveReader(CategoryImage, ".png", pr2, -1)
		res2 <- err
	}()
	if _, err := pw2.Write(payload); err != nil {
		t.Fatalf("write pipe2: %v", err)
	}
	temps := waitForTemps(t, dir, 2)
	if temps[0] == temps[1] {
		t.Fatalf("two concurrent saves share temp name %q — the first rename can publish the other's bytes", temps[0])
	}

	// Release both writers; both copies complete and collapse onto one object.
	if err := pw1.Close(); err != nil {
		t.Fatalf("close pipe1: %v", err)
	}
	if err := pw2.Close(); err != nil {
		t.Fatalf("close pipe2: %v", err)
	}
	for _, res := range []chan error{res1, res2} {
		if err := <-res; err != nil {
			t.Fatalf("SaveReader: %v", err)
		}
	}

	files := countFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("category dir = %v, want exactly one published object", files)
	}
	got, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("object bytes = %q, want %q", got, payload)
	}
	sum := sha256.Sum256(payload)
	wantName := hex.EncodeToString(sum[:8]) + ".png"
	if files[0] != wantName {
		t.Errorf("object name = %q, want %q", files[0], wantName)
	}
}

func TestSaveReader_PathMatchesHash(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	// Deterministic pseudo-random bytes: generated content defeats any
	// shortcut that hashes a prefix instead of the whole stream.
	rng := rand.New(rand.NewSource(42))
	data := make([]byte, 300*1024)
	if _, err := rng.Read(data); err != nil {
		t.Fatalf("rng: %v", err)
	}
	path, size, err := s.SaveReader(CategoryImage, "", bytes.NewReader(data), -1)
	if err != nil {
		t.Fatalf("SaveReader: %v", err)
	}
	wantPath := s.Path(CategoryImage, data, "")
	if path != wantPath {
		t.Errorf("path = %q, want %q (hash drifted from the copied bytes)", path, wantPath)
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("object bytes diverge from input (truncated copy?)")
	}
}

func TestSave_StillDedupsAndRejectsDir(t *testing.T) {
	s, err := NewAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	data := []byte("regression-guard")
	path1, err := s.Save(CategoryDocument, data, ".txt")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	backdated := time.Now().Add(-2 * time.Hour) // REAL-TIME
	if err := os.Chtimes(path1, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	path2, err := s.Save(CategoryDocument, data, ".txt")
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if path1 != path2 {
		t.Errorf("dedup path mismatch: %q != %q", path1, path2)
	}
	info, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.ModTime().Equal(backdated) {
		t.Errorf("dedup rewrote file: mtime = %v, want %v", info.ModTime(), backdated)
	}

	// A directory parked at the destination: rename fails, the error names the
	// destination, and no temp is left behind.
	other := []byte("collides-with-a-dir")
	dst := s.Path(CategoryDocument, other, ".pdf")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if _, err := s.Save(CategoryDocument, other, ".pdf"); err == nil {
		t.Fatal("Save into a directory-occupied destination should fail")
	} else if !strings.Contains(err.Error(), dst) {
		t.Errorf("err = %v, want it naming the destination %q", err, dst)
	}
	dirEntries, err := os.ReadDir(filepath.Dir(dst))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var temps []string
	for _, e := range dirEntries {
		if strings.HasPrefix(e.Name(), "save-") {
			temps = append(temps, e.Name())
		}
	}
	if len(temps) != 0 {
		t.Errorf("leftover temps after rename failure: %v", temps)
	}
}
