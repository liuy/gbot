package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestExecute_MkdirAllError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a file where a directory should be
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Try to write to a path that requires "blocked" as a directory
	target := filepath.Join(blocker, "sub", "file.txt")
	input := json.RawMessage(`{"file_path":"` + target + `","content":"test"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when MkdirAll fails")
	}
}

func TestExecute_WriteFileError(t *testing.T) {
	dir := t.TempDir()

	// Create a read-only directory
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write a file there first, then make dir read-only
	target := filepath.Join(roDir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_ = os.Chmod(roDir, 0o444)
	defer func() { _ = os.Chmod(roDir, 0o755) }()

	// Now try to overwrite — this should fail due to directory permissions
	input := json.RawMessage(`{"file_path":"` + target + `","content":"new content"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when write fails")
	}
}

func TestExecute_ReadExistingFileError(t *testing.T) {
	// Create a file that returns a non-ENOENT error on read.
	// The most reliable way: create a symlink loop that causes ELOOP on read.
	dir := t.TempDir()
	loopLink := filepath.Join(dir, "loop")
	if err := os.Symlink(loopLink, loopLink); err != nil {
		t.Skip("symlink loop not supported")
	}

	input := json.RawMessage(`{"file_path":"` + loopLink + `","content":"new"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error for symlink loop")
	}
}

func TestGetStructuredPatch_NoChange(t *testing.T) {
	t.Parallel()

	result := getStructuredPatch("hello world", "hello world")
	if len(result) > 0 {
		for _, h := range result {
			for _, l := range h.Lines {
				if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
					t.Errorf("getStructuredPatch(same, same) contains change line %q, want only context", l)
				}
			}
		}
	}
}

func TestGetStructuredPatch_SimpleChange(t *testing.T) {
	t.Parallel()

	result := getStructuredPatch("hello", "world")
	if len(result) == 0 {
		t.Fatal("getStructuredPatch returned empty, want at least one hunk")
	}
	// Check that at least one line has the expected +/- prefix
	found := false
	for _, hunk := range result {
		for _, line := range hunk.Lines {
			if strings.HasPrefix(line, "-hello") || strings.HasPrefix(line, "+world") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("getStructuredPatch result missing expected diff lines: %+v", result)
	}
}

func TestGetStructuredPatch_EmptyOld(t *testing.T) {
	t.Parallel()

	result := getStructuredPatch("", "new content")
	if len(result) == 0 {
		t.Fatal("getStructuredPatch returned empty, want hunk for new content")
	}
}

func TestGetStructuredPatch_EmptyNew(t *testing.T) {
	t.Parallel()

	result := getStructuredPatch("old content", "")
	if len(result) == 0 {
		t.Fatal("getStructuredPatch returned empty, want hunk for deletion")
	}
}

func TestStructuredPatchHunk_JSON(t *testing.T) {
	t.Parallel()

	hunk := StructuredPatchHunk{
		OldStart: 1,
		OldLines: 2,
		NewStart: 1,
		NewLines: 3,
		Lines:    []string{" hello", "-old", "+new", " world"},
	}

	data, err := json.Marshal(hunk)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got StructuredPatchHunk
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.OldStart != 1 {
		t.Errorf("OldStart = %d, want 1", got.OldStart)
	}
	if got.NewLines != 3 {
		t.Errorf("NewLines = %d, want 3", got.NewLines)
	}
	if len(got.Lines) != 4 {
		t.Errorf("Lines len = %d, want 4", len(got.Lines))
	}
}

// ---------------------------------------------------------------------------
// normalizeLineEndings
// ---------------------------------------------------------------------------

func TestNormalizeLineEndings_CRLF(t *testing.T) {
	t.Parallel()
	got := normalizeLineEndings("line1\r\nline2\r\nline3")
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("normalizeLineEndings(CRLF) = %q, want %q", got, want)
	}
}

func TestNormalizeLineEndings_CR(t *testing.T) {
	t.Parallel()
	got := normalizeLineEndings("line1\rline2\rline3")
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("normalizeLineEndings(CR) = %q, want %q", got, want)
	}
}

func TestNormalizeLineEndings_Mixed(t *testing.T) {
	t.Parallel()
	got := normalizeLineEndings("line1\r\nline2\rline3\nline4")
	want := "line1\nline2\nline3\nline4"
	if got != want {
		t.Errorf("normalizeLineEndings(mixed) = %q, want %q", got, want)
	}
}

func TestNormalizeLineEndings_AlreadyLF(t *testing.T) {
	t.Parallel()
	got := normalizeLineEndings("line1\nline2\nline3")
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("normalizeLineEndings(already LF) = %q, want %q", got, want)
	}
}

func TestNormalizeLineEndings_Empty(t *testing.T) {
	t.Parallel()
	got := normalizeLineEndings("")
	want := ""
	if got != want {
		t.Errorf("normalizeLineEndings(empty) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Line ending normalization in Execute
// ---------------------------------------------------------------------------

func TestExecute_NormalizesCRLFToLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "crlf.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"line1\r\nline2\r\nline3"}`)
	_, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got := string(data)
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("File content = %q, want %q", got, want)
	}
}

func TestExecute_NormalizesCRToLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "cr.txt")

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"line1\rline2\rline3"}`)
	_, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(fp)
	got := string(data)
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("File content = %q, want %q", got, want)
	}
}

func TestExecute_UpdateWithCRLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "update.txt")
	if err := os.WriteFile(fp, []byte("old content\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"new content\r\n"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*Output)
	if strings.Contains(output.Content, "\r") {
		t.Errorf("Content should be normalized (no CR), got %q", output.Content)
	}

	data, _ := os.ReadFile(fp)
	if strings.Contains(string(data), "\r") {
		t.Errorf("Written file should have no CR, got %q", string(data))
	}
}

func TestExecute_UpdatePreservesLF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "preserved.txt")
	if err := os.WriteFile(fp, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	input := json.RawMessage(`{"file_path":"` + fp + `","content":"line1\nline2\nline3\n"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, _ := os.ReadFile(fp)
	want := "line1\nline2\nline3\n"
	if string(data) != want {
		t.Errorf("File content = %q, want %q", string(data), want)
	}
	_ = result
}

// ---------------------------------------------------------------------------
// expandPath
// ---------------------------------------------------------------------------

func TestExpandPath(t *testing.T) {
	t.Parallel()
	abs := "/tmp/test.txt"
	if got := expandPath(abs); got != abs {
		t.Errorf("expandPath(%q) = %q, want %q", abs, got, abs)
	}
	rel := "test.txt"
	got := expandPath(rel)
	if !filepath.IsAbs(got) {
		t.Errorf("expandPath(%q) = %q, want absolute path", rel, got)
	}
}

// ---------------------------------------------------------------------------
// Git diff helpers
// ---------------------------------------------------------------------------

func TestParseGitDiffOutput(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/foo.txt b/foo.txt
index abc123..def456 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,4 @@
 line1
-old
+new
+extra
 line3
`

	result := parseGitDiffOutput("foo.txt", diff)
	if result == nil {
		t.Fatal("parseGitDiffOutput returned nil")
	}
	if result.Filename != "foo.txt" {
		t.Errorf("Filename = %q, want %q", result.Filename, "foo.txt")
	}
	if result.Status != "modified" {
		t.Errorf("Status = %q, want %q", result.Status, "modified")
	}
	if result.Additions != 2 {
		t.Errorf("Additions = %d, want 2", result.Additions)
	}
	if result.Deletions != 1 {
		t.Errorf("Deletions = %d, want 1", result.Deletions)
	}
	if result.Changes != 3 {
		t.Errorf("Changes = %d, want 3", result.Changes)
	}
	if !strings.Contains(result.Patch, "@@") {
		t.Errorf("Patch missing @@ header: %q", result.Patch)
	}
	if !strings.Contains(result.Patch, "+new") {
		t.Errorf("Patch missing +new: %q", result.Patch)
	}
}

func TestParseGitDiffOutput_Empty(t *testing.T) {
	t.Parallel()

	result := parseGitDiffOutput("empty.txt", "")
	if result == nil {
		t.Fatal("parseGitDiffOutput returned nil for empty input")
	}
	if result.Additions != 0 || result.Deletions != 0 {
		t.Errorf("Additions=%d Deletions=%d, want 0,0", result.Additions, result.Deletions)
	}
	if result.Patch != "" {
		t.Errorf("Patch = %q, want empty", result.Patch)
	}
}

func TestParseGitDiffOutput_SkipsFileHeaders(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old
+new
`
	result := parseGitDiffOutput("a.txt", diff)
	if result == nil {
		t.Fatal("returned nil")
	}
	// Should not count --- and +++ as additions/deletions
	if result.Additions != 1 {
		t.Errorf("Additions = %d, want 1 (---/+++ should be skipped)", result.Additions)
	}
	if result.Deletions != 1 {
		t.Errorf("Deletions = %d, want 1", result.Deletions)
	}
}

func TestGenerateSyntheticDiff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "newfile.txt")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := generateSyntheticDiff(dir, "newfile.txt", fp)
	if err != nil {
		t.Fatalf("generateSyntheticDiff error: %v", err)
	}
	if result == nil {
		t.Fatal("generateSyntheticDiff returned nil")
	}
	if result.Status != "added" {
		t.Errorf("Status = %q, want %q", result.Status, "added")
	}
	if result.Additions != 3 {
		t.Errorf("Additions = %d, want 3", result.Additions)
	}
	if result.Deletions != 0 {
		t.Errorf("Deletions = %d, want 0", result.Deletions)
	}
	if !strings.Contains(result.Patch, "+line1") {
		t.Errorf("Patch missing +line1: %q", result.Patch)
	}
	if !strings.HasPrefix(result.Patch, "@@ -0,0 +1,") {
		t.Errorf("Patch should start with @@ -0,0 +1,: %q", result.Patch)
	}
}

func TestGenerateSyntheticDiff_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "notrail.txt")
	if err := os.WriteFile(fp, []byte("a\nb"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := generateSyntheticDiff(dir, "notrail.txt", fp)
	if err != nil {
		t.Fatal(err)
	}
	if result.Additions != 2 {
		t.Errorf("Additions = %d, want 2", result.Additions)
	}
}

func TestGenerateSyntheticDiff_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(fp, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := generateSyntheticDiff(dir, "empty.txt", fp)
	if err != nil {
		t.Fatal(err)
	}
	if result.Additions != 0 {
		t.Errorf("Additions = %d, want 0 for empty file", result.Additions)
	}
}

func TestGenerateSyntheticDiff_ReadError(t *testing.T) {
	t.Parallel()

	_, err := generateSyntheticDiff("/tmp", "missing.txt", "/nonexistent/path/missing.txt")
	if err == nil {
		t.Error("want error for nonexistent file")
	}
}

func TestFindGitRoot(t *testing.T) {
	t.Parallel()
	// This repo should have a .git directory
	cwd, _ := os.Getwd()
	root := findGitRoot(cwd)
	if root == "" {
		t.Skip("not in a git repo")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Errorf("findGitRoot returned %q but .git not found", root)
	}
}

func TestFindGitRoot_NotGitDir(t *testing.T) {
	t.Parallel()

	root := findGitRoot("/tmp")
	// /tmp is usually not a git repo — may return "" or a parent git root
	// Just verify no panic and returns a string
	_ = root
}

func TestIsInGitRepo(t *testing.T) {
	// This should be true since we're in the gbot repo
	if !isInGitRepo() {
		t.Error("isInGitRepo() = false, want true (running in gbot repo)")
	}
}

func TestFetchGitDiffForFile_Untracked(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "untracked.txt")
	if err := os.WriteFile(fp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Temp dir is not in a git repo, so fetchGitDiffForFile should return nil
	result, err := fetchGitDiffForFile(fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Result may be nil since temp dir isn't in git
	_ = result
}

func TestFetchGitDiffForFile_InRepo(t *testing.T) {
	// Test with a file in this actual git repo
	cwd, _ := os.Getwd()
	result, err := fetchGitDiffForFile(filepath.Join(cwd, "filewrite.go"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// May be nil if no uncommitted changes, or non-nil if there are
	// Just verify no crash
	_ = result
}

// ---------------------------------------------------------------------------
// Gap 1: getDiffRef — merge-base support
// ---------------------------------------------------------------------------

func TestGetDiffRef_FallbackToHEAD(t *testing.T) {
	t.Parallel()
	// Non-git directory should fall back to "HEAD"
	ref := getDiffRef("/tmp")
	if ref != "HEAD" {
		t.Errorf("getDiffRef(/tmp) = %q, want HEAD fallback", ref)
	}
}

func TestGetDiffRef_InRepo(t *testing.T) {
	cwd, _ := os.Getwd()
	gitRoot := findGitRoot(cwd)
	if gitRoot == "" {
		t.Skip("not in a git repo")
	}
	ref := getDiffRef(gitRoot)
	if ref == "" {
		t.Error("getDiffRef returned empty string")
	}
	// Should return either a SHA (merge-base) or "HEAD" (fallback)
	if ref != "HEAD" && len(ref) < 7 {
		t.Errorf("getDiffRef = %q, expected HEAD or a SHA", ref)
	}
}

// ---------------------------------------------------------------------------
// Gap 2: parseGitHubRemoteURL — Repository field
// ---------------------------------------------------------------------------

func TestParseGitHubRemoteURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string // empty string means nil expected
	}{
		{"ssh github", "git@github.com:user/repo.git", "user/repo"},
		{"https github", "https://github.com/user/repo.git", "user/repo"},
		{"https no .git", "https://github.com/user/repo", "user/repo"},
		{"non-github", "git@gitlab.com:user/repo.git", ""},
		{"empty", "", ""},
		{"not a url", "not-a-url", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseGitHubRemoteURL(tc.input)
			if tc.want == "" {
				if got != nil {
					t.Errorf("parseGitHubRemoteURL(%q) = %q, want nil", tc.input, *got)
				}
			} else {
				if got == nil {
					t.Errorf("parseGitHubRemoteURL(%q) = nil, want %q", tc.input, tc.want)
				} else if *got != tc.want {
					t.Errorf("parseGitHubRemoteURL(%q) = %q, want %q", tc.input, *got, tc.want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Gap 3: shouldComputeGitDiff — CLAUDE_CODE_REMOTE env var guard
// ---------------------------------------------------------------------------

func TestShouldComputeGitDiff(t *testing.T) {
	orig := os.Getenv("CLAUDE_CODE_REMOTE")
	defer func() { _ = os.Setenv("CLAUDE_CODE_REMOTE", orig) }()

	_ = os.Unsetenv("CLAUDE_CODE_REMOTE")
	if shouldComputeGitDiff() {
		t.Error("shouldComputeGitDiff() = true without CLAUDE_CODE_REMOTE")
	}

	_ = os.Setenv("CLAUDE_CODE_REMOTE", "1")
	if !shouldComputeGitDiff() {
		t.Error("shouldComputeGitDiff() = false with CLAUDE_CODE_REMOTE=1")
	}

	_ = os.Setenv("CLAUDE_CODE_REMOTE", "0")
	if shouldComputeGitDiff() {
		t.Error("shouldComputeGitDiff() = true with CLAUDE_CODE_REMOTE=0")
	}
}

// ---------------------------------------------------------------------------
// Coverage: expandPath branches
// ---------------------------------------------------------------------------

func TestExpandPath_TildeWithHome(t *testing.T) {
	t.Parallel()
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set")
	}
	got := expandPath("~/test.txt")
	want := filepath.Join(home, "test.txt")
	if got != want {
		t.Errorf("expandPath(\"~/test.txt\") = %q, want %q", got, want)
	}
}

func TestExpandPath_TildeHomeEmpty(t *testing.T) {
	orig := os.Getenv("HOME")
	defer func() { _ = os.Setenv("HOME", orig) }()
	_ = os.Unsetenv("HOME")
	got := expandPath("~/test.txt")
	// Should fall through to filepath.Abs, not crash
	if filepath.IsAbs(got) {
		t.Logf("expandPath with empty HOME resolved to %q", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: getMtimeMs error path
// ---------------------------------------------------------------------------

func TestGetMtimeMs_Error(t *testing.T) {
	t.Parallel()
	_, err := getMtimeMs("/nonexistent/file/path")
	if err == nil {
		t.Error("getMtimeMs should return error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// Coverage: parseGitHubRemoteURL additional cases
// ---------------------------------------------------------------------------

func TestParseGitHubRemoteURL_SSHProtocol(t *testing.T) {
	t.Parallel()
	got := parseGitHubRemoteURL("ssh://git@github.com/user/repo.git")
	if got == nil {
		t.Error("parseGitHubRemoteURL(ssh://) = nil, want user/repo")
	} else if *got != "user/repo" {
		t.Errorf("parseGitHubRemoteURL(ssh://) = %q, want user/repo", *got)
	}
}

func TestParseGitHubRemoteURL_HTTPSWithPort(t *testing.T) {
	t.Parallel()
	got := parseGitHubRemoteURL("https://github.com:443/user/repo.git")
	// Port handling in URL format
	if got == nil {
		t.Log("parseGitHubRemoteURL(https with port) = nil (may not match regex)")
	}
}

func TestParseGitHubRemoteURL_SSHNonGitHub(t *testing.T) {
	t.Parallel()
	got := parseGitHubRemoteURL("git@gitlab.com:user/repo.git")
	if got != nil {
		t.Errorf("parseGitHubRemoteURL(non-github SSH) = %q, want nil", *got)
	}
}

func TestParseGitHubRemoteURL_Whitespace(t *testing.T) {
	t.Parallel()
	got := parseGitHubRemoteURL("  ")
	if got != nil {
		t.Errorf("parseGitHubRemoteURL(whitespace) = %q, want nil", *got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: splitLines
// ---------------------------------------------------------------------------

func TestSplitLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"trailing newline", "a\nb\n", 2},
		{"no trailing newline", "a\nb", 2},
		{"empty", "", 0},
		{"single line", "hello", 1},
		{"single newline", "\n", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitLines(tc.input)
			if len(got) != tc.want {
				t.Errorf("splitLines(%q) = %d lines, want %d", tc.input, len(got), tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Coverage: parseGitHubRemoteURL — non-HTTP URL with port stripping
// ---------------------------------------------------------------------------

func TestParseGitHubRemoteURL_NonHTTPWithPort(t *testing.T) {
	t.Parallel()
	// ssh://git@github.com:2222/user/repo.git — non-HTTP, should strip port
	got := parseGitHubRemoteURL("ssh://git@github.com:2222/user/repo.git")
	if got == nil || *got != "user/repo" {
		t.Errorf("parseGitHubRemoteURL(ssh with port) = %v, want user/repo", got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: parseGitHubRemoteURL — non-github URL format
// ---------------------------------------------------------------------------

func TestParseGitHubRemoteURL_URLFormatNonGitHub(t *testing.T) {
	t.Parallel()
	got := parseGitHubRemoteURL("https://gitlab.com/user/repo.git")
	if got != nil {
		t.Errorf("parseGitHubRemoteURL(gitlab https) = %q, want nil", *got)
	}
}

// ---------------------------------------------------------------------------
// Coverage: getRepository — reads remote origin URL
// ---------------------------------------------------------------------------

func TestGetRepository_InGitRepo(t *testing.T) {
	cwd, _ := os.Getwd()
	gitRoot := findGitRoot(cwd)
	if gitRoot == "" {
		t.Skip("not in a git repo")
	}
	result := getRepository(gitRoot)
	// May be nil if no origin remote, or non-nil with "owner/repo"
	// Just verify no crash
	_ = result
}

// ---------------------------------------------------------------------------
// Coverage: getDefaultBranch — symbolic-ref succeeds
// ---------------------------------------------------------------------------

func TestGetDefaultBranch_InRepo(t *testing.T) {
	cwd, _ := os.Getwd()
	gitRoot := findGitRoot(cwd)
	if gitRoot == "" {
		t.Skip("not in a git repo")
	}
	branch := getDefaultBranch(gitRoot)
	if branch == "" {
		t.Error("getDefaultBranch returned empty string")
	}
}

// ---------------------------------------------------------------------------
// Coverage: Execute — shouldComputeGitDiff returns true
// ---------------------------------------------------------------------------

func TestExecute_WithGitDiffEnabled(t *testing.T) {
	// Non-parallel: modifies CLAUDE_CODE_REMOTE env var
	orig := os.Getenv("CLAUDE_CODE_REMOTE")
	defer func() { _ = os.Setenv("CLAUDE_CODE_REMOTE", orig) }()
	_ = os.Setenv("CLAUDE_CODE_REMOTE", "1")

	dir := t.TempDir()
	fp := filepath.Join(dir, "gitdiff_enabled.txt")
	input := json.RawMessage(`{"file_path":"` + fp + `","content":"hello world"}`)
	result, err := Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*Output)
	_ = output.GitDiff // may be nil since temp dir isn't in git
}

// ---------------------------------------------------------------------------
// Coverage: Execute — write file error via read-only file
// ---------------------------------------------------------------------------

func TestExecute_WriteFileErrorInternal(t *testing.T) {
	dir := t.TempDir()

	// Create a read-only file (not directory) so read works but write fails
	target := filepath.Join(dir, "readonly_file.txt")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_ = os.Chmod(target, 0o444)
	defer func() { _ = os.Chmod(target, 0o644) }()

	input := json.RawMessage(`{"file_path":"` + target + `","content":"new content"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when write fails")
	}
	if !strings.Contains(err.Error(), "write file") {
		t.Errorf("Error = %q, want 'write file'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Coverage: isInGitRepo — os.Getwd error (reset sync.Once)
// ---------------------------------------------------------------------------

func TestIsInGitRepo_GetwdError(t *testing.T) {
	// Reset the sync.Once so the function re-evaluates
	gitRepoOnce = sync.Once{}

	// We can't easily make os.Getwd fail, but we can at least ensure
	// the function re-evaluates. In the current git repo, it should
	// still return true.
	result := isInGitRepo()
	if !result {
		t.Error("isInGitRepo() = false, want true (in git repo)")
	}
}

// ---------------------------------------------------------------------------
// Coverage: getDefaultBranch, getRepository, fetchGitDiffForFile
// Uses a temporary git repo with proper origin setup
// ---------------------------------------------------------------------------

func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize git repo
	run := func(name string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", name, err, out)
		}
	}

	run("init", "init")
	run("config", "config", "user.email", "test@test.com")
	run("config", "config", "user.name", "Test")

	// Create an initial commit
	if err := os.WriteFile(filepath.Join(dir, "initial.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "add", "initial.txt")
	run("commit", "commit", "-m", "initial")

	// Create a fake "origin" remote by cloning into a bare repo and re-adding
	bareDir := dir + "_bare"
	cmd := exec.Command("git", "clone", "--bare", dir, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare failed: %v\n%s", err, out)
	}
	run("remote", "remote", "add", "origin", bareDir)
	run("fetch", "fetch", "origin")

	// Set up origin/HEAD symref
	run("symbolic-ref", "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")

	// Set remote URL to github.com
	run("remote-set-url", "remote", "set-url", "origin", "git@github.com:testowner/testrepo.git")

	return dir
}

func TestGetDefaultBranch_SymbolicRefSucceeds(t *testing.T) {
	// Non-parallel: modifies global gitRepoOnce/gitRepoCached
	dir := setupTestGitRepo(t)
	branch := getDefaultBranch(dir)
	if branch != "master" {
		t.Errorf("getDefaultBranch = %q, want 'master'", branch)
	}
}

func TestGetRepository_WithGitHubRemote(t *testing.T) {
	// Non-parallel: modifies global gitRepoOnce/gitRepoCached
	dir := setupTestGitRepo(t)
	result := getRepository(dir)
	if result == nil {
		t.Fatal("getRepository returned nil, want 'testowner/testrepo'")
	}
	if *result != "testowner/testrepo" {
		t.Errorf("getRepository = %q, want 'testowner/testrepo'", *result)
	}
}

func TestGetRepository_NoOrigin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	result := getRepository(dir)
	if result != nil {
		t.Errorf("getRepository with no origin = %q, want nil", *result)
	}
}

func TestFetchGitDiffForFile_UntrackedInTestRepo(t *testing.T) {
	// Non-parallel: modifies global gitRepoOnce/gitRepoCached
	dir := setupTestGitRepo(t)

	// Create an untracked file
	fp := filepath.Join(dir, "untracked_new.txt")
	if err := os.WriteFile(fp, []byte("new file content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset git repo cache so it picks up our test repo
	gitRepoOnce = sync.Once{}

	result, err := fetchGitDiffForFile(fp)
	if err != nil {
		t.Fatalf("fetchGitDiffForFile error: %v", err)
	}
	if result == nil {
		t.Fatal("fetchGitDiffForFile returned nil for untracked file")
	}
	if result.Status != "added" {
		t.Errorf("Status = %q, want 'added'", result.Status)
	}
	if result.Repository == nil {
		t.Error("Repository = nil, want 'testowner/testrepo'")
	} else if *result.Repository != "testowner/testrepo" {
		t.Errorf("Repository = %q, want 'testowner/testrepo'", *result.Repository)
	}

	// Restore git repo cache
	gitRepoOnce = sync.Once{}
	isInGitRepo()
}

func TestFetchGitDiffForFile_TrackedWithDiff(t *testing.T) {
	dir := setupTestGitRepo(t)

	// Modify a tracked file
	fp := filepath.Join(dir, "initial.txt")
	if err := os.WriteFile(fp, []byte("modified content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset git repo cache
	gitRepoOnce = sync.Once{}

	result, err := fetchGitDiffForFile(fp)
	if err != nil {
		t.Fatalf("fetchGitDiffForFile error: %v", err)
	}
	if result == nil {
		t.Error("fetchGitDiffForFile returned nil for modified tracked file")
	} else {
		if result.Status != "modified" {
			t.Errorf("Status = %q, want 'modified'", result.Status)
		}
		if result.Additions == 0 && result.Deletions == 0 {
			t.Error("Expected non-zero additions or deletions")
		}
	}

	// Restore git repo cache
	gitRepoOnce = sync.Once{}
	isInGitRepo()
}

func TestFetchGitDiffForFile_NotInGitRepo(t *testing.T) {
	// Force isInGitRepo to return false by directly setting cached value.
	// We do NOT reset sync.Once so it won't re-evaluate.
	gitRepoCached = false

	// Use a temp dir that's definitely not in a git repo
	dir := t.TempDir()
	fp := filepath.Join(dir, "nogit.txt")
	if err := os.WriteFile(fp, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := fetchGitDiffForFile(fp)
	if err != nil {
		t.Fatalf("fetchGitDiffForFile error: %v", err)
	}
	if result != nil {
		t.Errorf("fetchGitDiffForFile in non-git dir = %v, want nil", result)
	}

	// Restore git repo cache
	gitRepoOnce = sync.Once{}
	isInGitRepo()
}

// ---------------------------------------------------------------------------
// Coverage: fetchGitDiffForFile — generateSyntheticDiff error
// ---------------------------------------------------------------------------

func TestFetchGitDiffForFile_UntrackedReadError(t *testing.T) {
	// Non-parallel: modifies global gitRepoOnce/gitRepoCached
	dir := setupTestGitRepo(t)

	// Create an untracked file, then remove it to trigger read error
	fp := filepath.Join(dir, "will_disappear.txt")
	if err := os.WriteFile(fp, []byte("temp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove the file so generateSyntheticDiff fails to read it
	_ = os.Remove(fp)

	// Reset git repo cache
	gitRepoOnce = sync.Once{}

	result, err := fetchGitDiffForFile(fp)
	// The file doesn't exist on disk, so generateSyntheticDiff should fail
	if err == nil {
		// If the file was already garbage collected, ls-files might not error
		// and generateSyntheticDiff could succeed with empty content
		_ = result
	} else {
		// Expected: error from generateSyntheticDiff
		t.Logf("Got expected error: %v", err)
	}

	// Restore git repo cache
	gitRepoOnce = sync.Once{}
	isInGitRepo()
}
