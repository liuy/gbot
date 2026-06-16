package fileread

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// applyOffsetLimit — full coverage
// ---------------------------------------------------------------------------

func TestApplyOffsetLimit_Basic(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc\nd\ne"
	got := applyOffsetLimit(s, 2, 2)
	want := "b\nc"
	if got != want {
		t.Errorf("applyOffsetLimit offset=2 limit=2 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_ZeroOffset(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc"
	got := applyOffsetLimit(s, 0, 2)
	want := "a\nb"
	if got != want {
		t.Errorf("applyOffsetLimit offset=0 limit=2 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_NegativeOffset(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc"
	got := applyOffsetLimit(s, -5, 2)
	want := "a\nb"
	if got != want {
		t.Errorf("applyOffsetLimit offset=-5 limit=2 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_NoLimit(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc\nd"
	got := applyOffsetLimit(s, 2, 0)
	want := "b\nc\nd"
	if got != want {
		t.Errorf("applyOffsetLimit offset=2 limit=0 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_OffsetBeyondEnd(t *testing.T) {
	t.Parallel()
	s := "a\nb"
	got := applyOffsetLimit(s, 100, 5)
	want := ""
	if got != want {
		t.Errorf("applyOffsetLimit offset=100 = %q, want empty", got)
	}
}

func TestApplyOffsetLimit_LimitBeyondEnd(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc"
	got := applyOffsetLimit(s, 1, 100)
	want := "a\nb\nc"
	if got != want {
		t.Errorf("applyOffsetLimit offset=1 limit=100 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_StartEqualLen(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc"
	// start = len(lines)-1, should still return last line
	got := applyOffsetLimit(s, 3, 1)
	want := "c"
	if got != want {
		t.Errorf("applyOffsetLimit offset=3 limit=1 = %q, want %q", got, want)
	}
}

func TestApplyOffsetLimit_ExactRange(t *testing.T) {
	t.Parallel()
	s := "a\nb\nc\nd\ne"
	got := applyOffsetLimit(s, 2, 3)
	want := "b\nc\nd"
	if got != want {
		t.Errorf("applyOffsetLimit offset=2 limit=3 = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// formatArchiveEntries — entry with Info() error path, size=0 path, empty path
// ---------------------------------------------------------------------------

// fakeDirEntry implements fs.DirEntry for testing formatArchiveEntries branches.
type fakeDirEntry struct {
	name   string
	isDir  bool
	size   int64
	infoOK bool
}

func (f fakeDirEntry) Name() string      { return f.name }
func (f fakeDirEntry) IsDir() bool       { return f.isDir }
func (f fakeDirEntry) Type() fs.FileMode { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) {
	if !f.infoOK {
		return nil, errors.New("stat error")
	}
	return fakeInfo{name: f.name, size: f.size, isDir: f.isDir}, nil
}

type fakeInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.isDir }
func (f fakeInfo) Sys() any           { return nil }

func TestFormatArchiveEntries_Empty(t *testing.T) {
	t.Parallel()
	got := formatArchiveEntries(nil)
	if got != "(empty archive directory)" {
		t.Errorf("formatArchiveEntries(nil) = %q, want %q", got, "(empty archive directory)")
	}
}

func TestFormatArchiveEntries_InfoError(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "broken", infoOK: false},
	}
	got := formatArchiveEntries(entries)
	if got != "broken" {
		t.Errorf("formatArchiveEntries Info() error = %q, want %q", got, "broken")
	}
}

func TestFormatArchiveEntries_Dir(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "sub", isDir: true, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if got != "sub/" {
		t.Errorf("formatArchiveEntries dir = %q, want %q", got, "sub/")
	}
}

func TestFormatArchiveEntries_FileWithSize(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "data.txt", size: 2048, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if !strings.Contains(got, "data.txt") || !strings.Contains(got, "2.0KB") {
		t.Errorf("formatArchiveEntries file with size = %q, should contain name and size", got)
	}
}

func TestFormatArchiveEntries_FileZeroSize(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "empty.txt", size: 0, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	if got != "empty.txt" {
		t.Errorf("formatArchiveEntries zero-size file = %q, want %q", got, "empty.txt")
	}
}

func TestFormatArchiveEntries_SortedCaseInsensitive(t *testing.T) {
	t.Parallel()
	entries := []fs.DirEntry{
		fakeDirEntry{name: "Zoo.txt", size: 0, infoOK: true},
		fakeDirEntry{name: "abc.txt", size: 0, infoOK: true},
	}
	got := formatArchiveEntries(entries)
	// abc.txt sorts before Zoo.txt in case-insensitive order
	if !strings.HasPrefix(got, "abc.txt") {
		t.Errorf("formatArchiveEntries should sort case-insensitive; got: %q", got)
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS — file member with binary content and with offset/limit applied
// ---------------------------------------------------------------------------

func TestTryArchivePath_ReadMemberWithOffsetLimit(t *testing.T) {
	t.Parallel()
	// Build a fresh zip with multi-line content so offset/limit is exercised.
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "multi.zip")
	if err := makeZip(zipPath, map[string]string{
		"hello.txt": "first line\nsecond line\nthird line\nfourth line",
	}); err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: zipPath + ":hello.txt", Offset: 2, Limit: 1}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "second line") {
		t.Errorf("offset=2 limit=1 should return 'second line'; got: %q", text)
	}
	if strings.Contains(text, "first line") || strings.Contains(text, "third line") {
		t.Errorf("offset=2 limit=1 should not contain first/third; got: %q", text)
	}
}

func TestTryArchivePath_BinaryMember(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "bin.zip")
	if err := makeZip(zipPath, map[string][]byte{
		"binary.bin": {0x00, 0x01, 0x02, 0x00, 0xFF, 0xFE},
	}); err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: zipPath + ":binary.bin"}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for binary archive member")
	}
	to, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if !strings.Contains(to.Content, "[Cannot read binary archive entry") {
		t.Errorf("binary member should be reported as binary; got: %q", to.Content)
	}
	if !strings.Contains(to.Content, "binary.bin") {
		t.Errorf("binary member content should mention file name; got: %q", to.Content)
	}
}

func TestTryArchivePath_NonExistentMember(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs + ":nonexistent/file.txt"}
	_, handled, err := tryArchivePath(context.Background(), in)
	if err == nil {
		t.Fatal("expected error for non-existent archive member")
	}
	if !strings.Contains(err.Error(), "not found inside archive") {
		t.Errorf("error should mention not found; got: %v", err)
	}
	if !handled {
		t.Error("expected handled=true when archive path resolves")
	}
}

func TestTryArchivePath_NotAFile(t *testing.T) {
	t.Parallel()
	// Path matches archive pattern but doesn't exist as a file.
	// Should return (nil, false, nil).
	in := Input{FilePath: "/nonexistent/foo.zip"}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if handled {
		t.Error("expected handled=false for nonexistent archive")
	}
	if result != nil {
		t.Error("expected nil result for nonexistent archive")
	}
}

// ---------------------------------------------------------------------------
// parseArchivePathCandidates — second match (multiple occurrences)
// ---------------------------------------------------------------------------

func TestParseArchivePathCandidates_MultipleMatches(t *testing.T) {
	t.Parallel()
	// Two .gz suffixes should yield two candidates.
	cands := parseArchivePathCandidates("foo.gz/bar.gz")
	if len(cands) < 2 {
		t.Errorf("expected >= 2 candidates for two .gz suffixes, got %d", len(cands))
	}
}

func TestParseArchivePathCandidates_BackslashNormalization(t *testing.T) {
	t.Parallel()
	cands := parseArchivePathCandidates("foo.zip\\dir\\file.txt")
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	// Backslashes become forward slashes; subPath preserves them as written.
	if cands[0].subPath != "/dir/file.txt" {
		t.Errorf("backslash subPath = %q, want %q", cands[0].subPath, "/dir/file.txt")
	}
}

func TestParseArchivePathCandidates_AbsolutePath(t *testing.T) {
	t.Parallel()
	cands := parseArchivePathCandidates("/home/user/test.zip:dir/file")
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	want := "/home/user/test.zip"
	if cands[0].archivePath != want {
		t.Errorf("archivePath = %q, want %q", cands[0].archivePath, want)
	}
}

// ---------------------------------------------------------------------------
// isValidUTF8 — additional cases for full coverage
// ---------------------------------------------------------------------------

func TestIsValidUTF8_Truncated3Byte(t *testing.T) {
	t.Parallel()
	// 3-byte start (0xE0) followed by one continuation, then truncated
	if isValidUTF8([]byte{0xE0, 0xA0}) {
		t.Error("truncated 3-byte sequence should be invalid")
	}
}

func TestIsValidUTF8_Truncated4Byte(t *testing.T) {
	t.Parallel()
	// 4-byte start (0xF0) followed by 2 continuations, then truncated
	if isValidUTF8([]byte{0xF0, 0x90, 0x80}) {
		t.Error("truncated 4-byte sequence should be invalid")
	}
}

func TestIsValidUTF8_BadContinuation(t *testing.T) {
	t.Parallel()
	// 2-byte start, then bad continuation byte (not 0x80-0xBF)
	if isValidUTF8([]byte{0xC3, 0x41}) {
		t.Error("invalid continuation byte should be invalid")
	}
}

// ---------------------------------------------------------------------------
// normalizeArchivePath — additional edge cases
// ---------------------------------------------------------------------------

func TestNormalizeArchivePath_Backslash(t *testing.T) {
	t.Parallel()
	if got := normalizeArchivePath("dir\\file"); got != "dir/file" {
		t.Errorf("normalizeArchivePath backslash = %q, want %q", got, "dir/file")
	}
}

func TestNormalizeArchivePath_LeadingSlash(t *testing.T) {
	t.Parallel()
	if got := normalizeArchivePath("/dir/file"); got != "dir/file" {
		t.Errorf("normalizeArchivePath leading slash = %q, want %q", got, "dir/file")
	}
}

// ---------------------------------------------------------------------------
// tryArchivePath — archive.FileSystem returns an error (corrupt archive)
// ---------------------------------------------------------------------------

func TestTryArchivePath_CorruptArchive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// File with .zip extension but content is not a real zip.
	zipPath := filepath.Join(dir, "broken.zip")
	if err := os.WriteFile(zipPath, []byte("this is not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: zipPath}
	_, handled, err := tryArchivePath(context.Background(), in)
	// archives.FileSystem may either error or refuse; both are acceptable.
	// If it errors, that's the handled=true with error path.
	// If it succeeds but readArchiveFS fails, that's also handled=true.
	if err != nil {
		if !handled {
			t.Errorf("handled should be true when archive read errors; err=%v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS — directory that doesn't support ReadDir (defensive branch)
// ---------------------------------------------------------------------------

// noReadDirFS is a minimal fs.FS where Open returns a non-ReadDir directory.
type noReadDirFS struct{}

func (noReadDirFS) Open(name string) (fs.File, error) {
	// Both "." and "subdir" return a directory entry that does NOT implement
	// fs.ReadDirFile, exercising the defensive branch in readArchiveFS.
	if name == "." || name == "subdir" {
		return &fakeDir{fakeFile{name: name}}, nil
	}
	return nil, fs.ErrNotExist
}

type fakeFile struct{ name string }

func (f *fakeFile) Stat() (fs.FileInfo, error) {
	return fakeInfo{name: f.name, isDir: true}, nil
}
func (f *fakeFile) Read([]byte) (int, error) { return 0, fs.ErrInvalid }
func (f *fakeFile) Close() error             { return nil }

// fakeDir wraps fakeFile but does NOT implement fs.ReadDirFile.
type fakeDir struct{ fakeFile }

func TestReadArchiveFS_DirWithoutReadDir(t *testing.T) {
	t.Parallel()
	in := Input{FilePath: "fake.zip:subdir"}
	_, err := readArchiveFS(noReadDirFS{}, "subdir", in)
	if err == nil {
		t.Fatal("expected error for directory without ReadDir")
	}
	if !strings.Contains(err.Error(), "does not support ReadDir") {
		t.Errorf("error should mention ReadDir; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS: empty subPath resolves to root
// ---------------------------------------------------------------------------

func TestReadArchiveFS_EmptySubPath(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	// Indirectly via tryArchivePath with no subpath
	in := Input{FilePath: abs}
	result, handled, err := tryArchivePath(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// ---------------------------------------------------------------------------
// readArchiveFS — text member without offset/limit (passes through unchanged)
// ---------------------------------------------------------------------------

func TestReadArchiveFS_TextMemberNoOffset(t *testing.T) {
	t.Parallel()
	abs, err := filepath.Abs("testdata/test.zip")
	if err != nil {
		t.Fatal(err)
	}
	in := Input{FilePath: abs + ":config.yaml"}
	result, err := readArchiveFSViaTryArchivePath(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	to, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("expected TextOutput, got %T", result.Data)
	}
	if !strings.Contains(to.Content, "name: test") {
		t.Errorf("config.yaml missing expected content; got: %q", to.Content)
	}
}

// readArchiveFSViaTryArchivePath is a thin wrapper to call tryArchivePath and
// return only the result. It exists so callers don't have to assert on
// handled/error.
func readArchiveFSViaTryArchivePath(in Input) (*tool.ToolResult, error) {
	r, _, err := tryArchivePath(context.Background(), in)
	return r, err
}

// makeZip creates a zip archive at path containing the provided entries.
// Entries map member name → text content ([]byte version accepts raw bytes).
func makeZip(path string, entries any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()

	switch v := entries.(type) {
	case map[string]string:
		for name, content := range v {
			w, err := zw.Create(name)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte(content)); err != nil {
				return err
			}
		}
	case map[string][]byte:
		for name, content := range v {
			w, err := zw.Create(name)
			if err != nil {
				return err
			}
			if _, err := w.Write(content); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported entries type")
	}
	return nil
}
