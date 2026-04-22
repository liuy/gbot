package grep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// goGrep — the fallback when rg is not available
// ---------------------------------------------------------------------------

func TestGoGrep_SingleFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "hello world\nfoo bar\nhello again\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "hello", fp, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
	for _, m := range output.Matches {
		if !strings.Contains(m.Content, "hello") {
			t.Errorf("Match content = %q, should contain 'hello'", m.Content)
		}
	}
	// Verify line numbers: "hello world" is line 1, "hello again" is line 3
	if len(output.Matches) != 2 {
		t.Fatalf("len(Matches) = %d, want 2", len(output.Matches))
	}
	if output.Matches[0].Line != 1 {
		t.Errorf("Matches[0].Line = %d, want 1", output.Matches[0].Line)
	}
	if output.Matches[1].Line != 3 {
		t.Errorf("Matches[1].Line = %d, want 3", output.Matches[1].Line)
	}
}

func TestGoGrep_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("findme alpha\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("findme beta\nno match\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "findme", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 2 {
		t.Errorf("Count = %d, want 2", output.Count)
	}
}

func TestGoGrep_NoMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(fp, []byte("nothing to see here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "UNIQUE_PATTERN_XYZ", fp, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 0 {
		t.Errorf("Count = %d, want 0", output.Count)
	}
	if output.Matches == nil {
		t.Error("Matches should be non-nil empty slice")
	}
}

func TestGoGrep_NonexistentPath(t *testing.T) {
	t.Parallel()

	_, err := goGrep(context.Background(), "pattern", "/nonexistent/path/xyz", "")
	if err == nil {
		t.Fatal("goGrep() should return error for nonexistent path")
	}
	if !strings.Contains(err.Error(), "path does not exist") {
		t.Errorf("error = %q, want error containing 'path does not exist'", err.Error())
	}
}

func TestGoGrep_DirectoryWithFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a file (not directory) to test that the directory read path fails
	fp := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(fp, []byte("test\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Pass a valid directory path
	result, err := goGrep(context.Background(), "test", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1", output.Count)
	}
}

func TestGoGrep_SkipsSubdirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.txt"), []byte("searchme\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// File inside subdir — should be skipped by goGrep (non-recursive)
	if err := os.WriteFile(filepath.Join(subdir, "deep.txt"), []byte("searchme\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "searchme", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1 (subdirectory files should be skipped)", output.Count)
	}
}

// ---------------------------------------------------------------------------
// grepFile
// ---------------------------------------------------------------------------

func TestGrepFile_Matches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	content := "first line\nsecond match line\nthird line\nmatch again\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "match")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("len(matches) = %d, want 2", len(matches))
	}
	if matches[0].Line != 2 {
		t.Errorf("matches[0].Line = %d, want 2", matches[0].Line)
	}
	if matches[1].Line != 4 {
		t.Errorf("matches[1].Line = %d, want 4", matches[1].Line)
	}
	if matches[0].File != fp {
		t.Errorf("matches[0].File = %q, want %q", matches[0].File, fp)
	}
}

func TestGrepFile_NoMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte("nothing here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "UNIQUE_PATTERN")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("len(matches) = %d, want 0", len(matches))
	}
}

func TestGrepFile_NonexistentFile(t *testing.T) {
	t.Parallel()

	_, err := grepFile("/nonexistent/file.txt", "pattern")
	if err == nil {
		t.Fatal("grepFile() should return error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "nonexistent") && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("error = %q, want error about nonexistent file", err.Error())
	}
}

func TestGrepFile_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	matches, err := grepFile(fp, "anything")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("len(matches) = %d, want 0 for empty file", len(matches))
	}
}

// ---------------------------------------------------------------------------
// Execute with ToolUseContext providing WorkingDir
// ---------------------------------------------------------------------------

func TestExecute_WorkingDirFromToolUseContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "ctx_test.txt")
	if err := os.WriteFile(fp, []byte("marker_token_12345\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	tctx := &types.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"pattern":"marker_token_12345"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1 (WorkingDir=%s)", output.NumFiles, dir)
	}
	if output.Mode != "files_with_matches" {
		t.Errorf("Mode = %q, want files_with_matches", output.Mode)
	}
}

func TestExecute_NilToolUseContext_FallsBackToGetwd(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"pattern":"xyzQwertyNoMatch123456"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Fatal("Output is nil")
	}
	if output.Mode != "files_with_matches" {
		t.Errorf("Mode = %q, want files_with_matches", output.Mode)
	}
}

func TestExecute_ToolUseContextEmptyWorkingDir(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{WorkingDir: ""}
	input := json.RawMessage(`{"pattern":"xyzQwertyNoMatch123456"}`)
	result, err := Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if output == nil {
		t.Fatal("Output is nil")
	}
	if output.Mode != "files_with_matches" {
		t.Errorf("Mode = %q, want files_with_matches", output.Mode)
	}
}

// ---------------------------------------------------------------------------
// goGrep additional edge cases
// ---------------------------------------------------------------------------

func TestGoGrep_SingleFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a subdirectory and pass it as if it were a file
	subdir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// goGrep with a path that is not a regular file: Stat succeeds but grepFile fails
	result, err := goGrep(context.Background(), "pattern", subdir, "")
	if err != nil {
		// It's a directory, so goGrep takes the IsDir branch, not single file
		t.Fatalf("goGrep() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Count != 0 {
		t.Errorf("Count = %d, want 0 for empty directory", output.Count)
	}
}

func TestGoGrep_UnreadableFileInDirectory(t *testing.T) {
	// Skip on Windows where permission bits behave differently
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}
	t.Parallel()

	dir := t.TempDir()

	// Create a readable file with a match
	goodFile := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(goodFile, []byte("findme here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Create an unreadable file
	badFile := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(badFile, []byte("findme too\n"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() {
		_ = os.Chmod(badFile, 0o644) // restore for cleanup
	}()

	result, err := goGrep(context.Background(), "findme", dir, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}

	output := result.Data.(*Output)
	// Only the good file should match; the bad file's grepFile error is
	// silently continued past (line 223-224 coverage).
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1 (only readable file matches)", output.Count)
	}
}

func TestGoGrep_DirectoryReadDirError(t *testing.T) {
	// Cover the ReadDir error path by using a deleted directory.
	// We create a dir, get its path, then remove it — Stat will fail first.
	// To cover ReadDir specifically, we need Stat to succeed but ReadDir to fail.
	// This is hard to trigger naturally; instead we use a file as the searchPath
	// to ensure the IsDir=false branch is tested (line 205-210).
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(fp, []byte("test content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := goGrep(context.Background(), "test", fp, "")
	if err != nil {
		t.Fatalf("goGrep() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.Count != 1 {
		t.Errorf("Count = %d, want 1", output.Count)
	}
}

func TestGoGrep_ReadDirErrorViaPermission(t *testing.T) {
	// Cover the os.ReadDir error path by removing execute permission on a directory
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()
	innerDir := filepath.Join(dir, "inner")
	if err := os.Mkdir(innerDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Put a file in inner so ReadDir would find it
	if err := os.WriteFile(filepath.Join(innerDir, "f.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Remove execute permission so ReadDir fails
	if err := os.Chmod(innerDir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(innerDir, 0o755) }()

	// Now pass innerDir to goGrep — Stat should succeed (we own it)
	// but ReadDir should fail due to no execute permission
	_, err := goGrep(context.Background(), "hello", innerDir, "")
	if err == nil {
		t.Fatal("expected ReadDir error due to permissions")
	}
	if !strings.Contains(err.Error(), "read directory") {
		t.Errorf("error should mention read directory, got: %v", err)
	}
}

func TestGoGrep_SingleFileGrepFileError(t *testing.T) {
	// Cover the single-file grepFile error path (lines 207-209)
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()
	fp := filepath.Join(dir, "unreadable.txt")
	if err := os.WriteFile(fp, []byte("test\n"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	_, err := goGrep(context.Background(), "test", fp, "")
	if err == nil {
		t.Fatal("expected error for unreadable single file")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should mention permission denied, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestEmptyResult(t *testing.T) {
	t.Parallel()

	// Test all three modes
	result := emptyResult("content")
	out := result.Data.(*Output)
	if out.Mode != "content" || out.Content != "" || out.NumLines != 0 {
		t.Errorf("content emptyResult = %+v", out)
	}

	result = emptyResult("count")
	out = result.Data.(*Output)
	if out.Mode != "count" || out.NumFiles != 0 || out.NumMatches != 0 {
		t.Errorf("count emptyResult = %+v", out)
	}

	result = emptyResult("files_with_matches")
	out = result.Data.(*Output)
	if out.Mode != "files_with_matches" || out.NumFiles != 0 || len(out.Filenames) != 0 {
		t.Errorf("files_with_matches emptyResult = %+v", out)
	}
}

func TestApplyHeadLimit_Truncation(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c", "d", "e"}
	// limit=2, offset=0: 5 items → 2, truncation (5 > 2) → return limit
	limited, applied := applyHeadLimit(items, 2, 0)
	if len(limited) != 2 {
		t.Errorf("len = %d, want 2", len(limited))
	}
	if applied == nil || *applied != 2 {
		t.Errorf("applied = %v, want 2", applied)
	}
}

func TestApplyHeadLimit_NoTruncation(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b"}
	// limit=2, offset=0: 2 items → 2, no truncation (2 not > 2) → nil
	limited, applied := applyHeadLimit(items, 2, 0)
	if len(limited) != 2 {
		t.Errorf("len = %d, want 2", len(limited))
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil (no truncation)", applied)
	}
}

func TestApplyHeadLimit_OffsetUnlimited(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c"}
	// limit=0 (unlimited), offset=1 → return items[1:]
	limited, applied := applyHeadLimit(items, 0, 1)
	if len(limited) != 2 || limited[0] != "b" {
		t.Errorf("limited = %v, want [b, c]", limited)
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil", applied)
	}
}

func TestApplyHeadLimit_OffsetBeyondLength(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b"}
	// offset=5 >= len(items)=2 → empty
	limited, applied := applyHeadLimit(items, 3, 5)
	if len(limited) != 0 {
		t.Errorf("len = %d, want 0", len(limited))
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil", applied)
	}
}

func TestApplyHeadLimit_LimitBeyondLength(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b"}
	// limit=10, offset=0: end=10 > len=2 → end=2, no truncation (2-0=2 not > 2)
	limited, applied := applyHeadLimit(items, 10, 0)
	if len(limited) != 2 {
		t.Errorf("len = %d, want 2", len(limited))
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil (no truncation)", applied)
	}
}

func TestToRelativePath(t *testing.T) {
	t.Parallel()

	// Normal case: abs path → relative
	abs := "/home/user/project/file.go"
	rel := toRelativePath(abs)
	// Should be something like "../../../file.go" relative to cwd
	if rel == "" {
		t.Error("toRelativePath returned empty string")
	}

	// Error case: impossible path (outside any common root)
	// filepath.Rel may fail for paths with no common prefix
	// The function should return abs path on error
	abs2 := toRelativePath("/")
	if abs2 == "" {
		t.Error("toRelativePath returned empty for root")
	}
}

func TestGrepFile_RegexPattern(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "regex.txt")
	if err := os.WriteFile(fp, []byte("abc\ndef\nghi\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Valid regex pattern
	matches, err := grepFile(fp, "a.*c")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 1 || matches[0].Content != "abc" {
		t.Errorf("matches = %+v, want [abc]", matches)
	}
}

func TestGrepFile_InvalidRegexFallsBackToContains(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(fp, []byte("[invalid(regex\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Invalid regex: falls back to strings.Contains
	matches, err := grepFile(fp, "[invalid(regex")
	if err != nil {
		t.Fatalf("grepFile() error: %v", err)
	}
	if len(matches) != 1 || matches[0].Content != "[invalid(regex" {
		t.Errorf("matches = %+v, want [[invalid(regex]]", matches)
	}
}

func TestGrepFile_ScannerError(t *testing.T) {
	// Skip on Windows
	if runtime.GOOS == "windows" {
		t.Skip("skipping on Windows")
	}

	dir := t.TempDir()
	fp := filepath.Join(dir, "scanerr.txt")
	// Create a file that the scanner will error on.
	// The default scanner buffer is 64K. A very long line (> 64K) triggers bufio.Scanner: token too long.
	// We need to write a line longer than the max token size (64K default).
	longLine := strings.Repeat("x", 70000) // 70K > 64K
	if err := os.WriteFile(fp, []byte(longLine+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := grepFile(fp, "x")
	if err == nil {
		t.Fatal("grepFile() should return error for token too long")
	}
	// Error message should indicate the issue
	if !strings.Contains(err.Error(), "token") && !strings.Contains(err.Error(), "Buf") {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestSplitGlobPatterns_BraceExpansion(t *testing.T) {
	t.Parallel()

	// TS logic: brace patterns kept intact, others split on comma
	patterns := splitGlobPatterns("*.{ts,tsx,js}")
	if len(patterns) != 1 || patterns[0] != "*.{ts,tsx,js}" {
		t.Errorf("patterns = %v, want [*.{ts,tsx,js}]", patterns)
	}

	patterns = splitGlobPatterns("*.go *.rs")
	if len(patterns) != 2 || patterns[0] != "*.go" || patterns[1] != "*.rs" {
		t.Errorf("patterns = %v, want [*.go, *.rs]", patterns)
	}

	patterns = splitGlobPatterns("foo,bar,baz")
	if len(patterns) != 3 {
		t.Errorf("len(patterns) = %d, want 3", len(patterns))
	}
}

func TestSplitGlobPatterns_Empty(t *testing.T) {
	t.Parallel()

	patterns := splitGlobPatterns("")
	if len(patterns) != 0 {
		t.Errorf("patterns = %v, want []", patterns)
	}
}

// ---------------------------------------------------------------------------
// buildResult count mode with offset (line 359-361)
// ---------------------------------------------------------------------------

func TestBuildResult_CountModeWithOffset(t *testing.T) {
	t.Parallel()

	// Simulate rg output in count mode: "filename:count" format
	rgOutput := "file_a.txt:5\nfile_b.txt:3\nfile_c.txt:7\n"
	result, err := buildResult("count", rgOutput, 250, 1)
	if err != nil {
		t.Fatalf("buildResult() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.Mode != "count" {
		t.Errorf("Mode = %q, want count", out.Mode)
	}
	// offset=1 skips first file, so 2 files
	if out.NumFiles != 2 {
		t.Errorf("NumFiles = %d, want 2", out.NumFiles)
	}
	// 3 + 7 = 10 matches (skipping file_a's 5)
	if out.NumMatches != 10 {
		t.Errorf("NumMatches = %d, want 10", out.NumMatches)
	}
	if out.AppliedOffset == nil || *out.AppliedOffset != 1 {
		t.Errorf("AppliedOffset = %v, want 1", out.AppliedOffset)
	}
}

func TestBuildResult_CountModeNoOffset(t *testing.T) {
	t.Parallel()

	rgOutput := "file_a.txt:5\n"
	result, err := buildResult("count", rgOutput, 250, 0)
	if err != nil {
		t.Fatalf("buildResult() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.AppliedOffset != nil {
		t.Errorf("AppliedOffset = %v, want nil when offset=0", out.AppliedOffset)
	}
}

// ---------------------------------------------------------------------------
// applyHeadLimit unlimited with offset >= len (line 448-450)
// ---------------------------------------------------------------------------

func TestApplyHeadLimit_UnlimitedOffsetBeyondLength(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c"}
	// limit=0 (unlimited), offset=5 >= len(items)=3 -> empty
	limited, applied := applyHeadLimit(items, 0, 5)
	if len(limited) != 0 {
		t.Errorf("len = %d, want 0", len(limited))
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil", applied)
	}
}

func TestApplyHeadLimit_UnlimitedNoOffset(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c"}
	// limit=0 (unlimited), offset=0 -> return all items
	limited, applied := applyHeadLimit(items, 0, 0)
	if len(limited) != 3 {
		t.Errorf("len = %d, want 3", len(limited))
	}
	if applied != nil {
		t.Errorf("applied = %v, want nil", applied)
	}
}

// ---------------------------------------------------------------------------
// toRelativePath error paths (line 507-513)
// ---------------------------------------------------------------------------

func TestToRelativePath_EmptyPath(t *testing.T) {
	t.Parallel()
	rel := toRelativePath("")
	if rel != "" && rel != "." {
		t.Errorf("toRelativePath('') = %q, want '' or '.'", rel)
	}
}

func TestToRelativePath_RootPath(t *testing.T) {
	t.Parallel()
	// "/" — should return something (either "/" or relative path to root)
	rel := toRelativePath("/")
	if rel == "" {
		t.Error("toRelativePath('/') should return non-empty")
	}
}

func TestToRelativePath_RelativePathInput(t *testing.T) {
	t.Parallel()
	// Passing a relative path — filepath.Rel may succeed or fail;
	// on failure it returns the input as-is
	rel := toRelativePath("some/relative/path")
	if rel == "" {
		t.Error("toRelativePath should return non-empty for relative input")
	}
	// Should either be the input itself or a recomputed relative path
	// from cwd — either way it must contain "some"
	if !strings.Contains(rel, "some") {
		t.Errorf("toRelativePath('some/relative/path') = %q, should contain 'some'", rel)
	}
}

func TestToRelativePath_GetwdError(t *testing.T) {
	// NO t.Parallel() — changes working directory
	// Cover line 507-509: os.Getwd() error path.
	// This happens when the current working directory has been deleted.
	dir := t.TempDir()
	subdir := dir + "/deleted"
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// Save and restore cwd
	origWd, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot get cwd: %v", err)
	}
	defer func() { _ = os.Chdir(origWd) }()

	// Change into subdir, then delete it
	if err := os.Chdir(subdir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	// Remove the directory we're in — on Linux this makes Getwd fail
	if err := os.Remove(subdir); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Now os.Getwd() should fail, so toRelativePath returns absPath as-is
	result := toRelativePath("/some/abs/path")
	if result != "/some/abs/path" {
		t.Errorf("toRelativePath with Getwd error = %q, want %q", result, "/some/abs/path")
	}
}

// ---------------------------------------------------------------------------
// buildResult content mode with offset (line 359-361 coverage for content)
// ---------------------------------------------------------------------------

func TestBuildResult_ContentModeWithOffset(t *testing.T) {
	t.Parallel()

	rgOutput := "file_a.txt:1:line1\nfile_a.txt:5:line2\nfile_a.txt:10:line3\n"
	result, err := buildResult("content", rgOutput, 250, 1)
	if err != nil {
		t.Fatalf("buildResult() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.Mode != "content" {
		t.Errorf("Mode = %q, want content", out.Mode)
	}
	// offset=1 skips first line, so 2 lines remain
	if out.NumLines != 2 {
		t.Errorf("NumLines = %d, want 2", out.NumLines)
	}
	if out.AppliedOffset == nil || *out.AppliedOffset != 1 {
		t.Errorf("AppliedOffset = %v, want 1", out.AppliedOffset)
	}
}

// ---------------------------------------------------------------------------
// buildResult files_with_matches mode with offset
// ---------------------------------------------------------------------------

func TestBuildResult_FilesWithMatchesWithOffset(t *testing.T) {
	t.Parallel()

	// Need real files for sortByMtime to work properly
	dir := t.TempDir()
	fileA := dir + "/a.txt"
	fileB := dir + "/b.txt"
	if err := os.WriteFile(fileA, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rgOutput := fileA + "\n" + fileB + "\n"
	result, err := buildResult("files_with_matches", rgOutput, 250, 1)
	if err != nil {
		t.Fatalf("buildResult() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.Mode != "files_with_matches" {
		t.Errorf("Mode = %q, want files_with_matches", out.Mode)
	}
	// offset=1 skips first file, so 1 file
	if out.NumFiles != 1 {
		t.Errorf("NumFiles = %d, want 1", out.NumFiles)
	}
	if out.AppliedOffset == nil || *out.AppliedOffset != 1 {
		t.Errorf("AppliedOffset = %v, want 1", out.AppliedOffset)
	}
}

func TestBuildResult_FilesWithMatchesNoOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fileA := dir + "/a.txt"
	if err := os.WriteFile(fileA, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rgOutput := fileA + "\n"
	result, err := buildResult("files_with_matches", rgOutput, 250, 0)
	if err != nil {
		t.Fatalf("buildResult() error: %v", err)
	}

	out := result.Data.(*Output)
	if out.AppliedOffset != nil {
		t.Errorf("AppliedOffset = %v, want nil when offset=0", out.AppliedOffset)
	}
}
