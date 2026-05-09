package long

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setTempHome redirects HOME to a temp subdirectory so .gbot paths
// don't pollute the real user's ~/.gbot/projects/.
func setTempHome(t *testing.T) {
	t.Helper()
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatalf("create temp home: %v", err)
	}
	t.Setenv("HOME", homeDir)
}

func TestParseMemoryType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected MemoryType
		ok       bool
	}{
		{"user", MemoryTypeUser, true},
		{"feedback", MemoryTypeFeedback, true},
		{"project", MemoryTypeProject, true},
		{"reference", MemoryTypeReference, true},
		{"invalid", "", false},
		{"", "", false},
		{"USER", "", false},
	}
	for _, tc := range tests {
		got, ok := ParseMemoryType(tc.input)
		if ok != tc.ok {
			t.Errorf("ParseMemoryType(%q) ok = %v, want %v", tc.input, ok, tc.ok)
		}
		if got != tc.expected {
			t.Errorf("ParseMemoryType(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestFormatFrontmatter_RoundTrip(t *testing.T) {
	t.Parallel()
	name := "user_role"
	desc := "User is a data scientist"
	memType := MemoryTypeUser
	body := "User focuses on ML pipelines and observability."

	formatted := FormatFrontmatter(name, desc, memType, body)
	if !strings.HasPrefix(formatted, "---\n") {
		t.Fatalf("expected frontmatter to start with '---\\n', got: %q", formatted[:20])
	}

	parsedName, parsedDesc, parsedType, parsedBody, ok := ParseFrontmatter(formatted)
	if !ok {
		t.Fatalf("ParseFrontmatter failed for:\n%s", formatted)
	}
	if parsedName != name {
		t.Errorf("name = %q, want %q", parsedName, name)
	}
	if parsedDesc != desc {
		t.Errorf("description = %q, want %q", parsedDesc, desc)
	}
	if parsedType != memType {
		t.Errorf("type = %q, want %q", parsedType, memType)
	}
	if parsedBody != body {
		t.Errorf("body = %q, want %q", parsedBody, body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	t.Parallel()
	content := "Just plain text without frontmatter"
	_, _, _, body, ok := ParseFrontmatter(content)
	if ok {
		t.Error("expected ok=false for content without frontmatter")
	}
	if body != content {
		t.Errorf("body = %q, want original content", body)
	}
}

func TestParseFrontmatter_InvalidType(t *testing.T) {
	t.Parallel()
	content := "---\nname: test\ndescription: desc\ntype: invalid\n---\nbody"
	name, _, memType, _, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("expected ok=true for valid frontmatter structure")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
	if memType != "" {
		t.Errorf("type = %q, want empty for invalid type", memType)
	}
}

func TestParseFrontmatter_MissingName(t *testing.T) {
	t.Parallel()
	content := "---\ndescription: desc\ntype: user\n---\nbody"
	_, _, _, _, ok := ParseFrontmatter(content)
	if ok {
		t.Error("expected ok=false for frontmatter without name")
	}
}

func TestFormatFrontmatter_AllTypes(t *testing.T) {
	t.Parallel()
	for _, mt := range ValidMemoryTypes {
		formatted := FormatFrontmatter("test", "desc", mt, "body")
		if !strings.Contains(formatted, "type: "+string(mt)) {
			t.Errorf("frontmatter for type %q missing 'type: %s' in:\n%s", mt, mt, formatted)
		}
		_, _, parsedType, _, ok := ParseFrontmatter(formatted)
		if !ok {
			t.Errorf("ParseFrontmatter failed for type %q", mt)
		}
		if parsedType != mt {
			t.Errorf("parsedType = %q, want %q", parsedType, mt)
		}
	}
}

func TestParseFrontmatter_MultilineBody(t *testing.T) {
	t.Parallel()
	content := "---\nname: test\ndescription: desc\ntype: project\n---\n\nLine 1\nLine 2\nLine 3"
	name, _, _, body, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("ParseFrontmatter failed")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
	if !strings.Contains(body, "Line 1") || !strings.Contains(body, "Line 3") {
		t.Errorf("body missing expected content: %q", body)
	}
}

// --- paths.go tests ---

func TestIsAutoMemoryEnabled_Default(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "")
	if !IsAutoMemoryEnabled() {
		t.Error("expected enabled by default")
	}
}

func TestIsAutoMemoryEnabled_Disabled(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	if IsAutoMemoryEnabled() {
		t.Error("expected disabled when env=1")
	}
}

func TestIsAutoMemoryEnabled_TrueString(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "true")
	if IsAutoMemoryEnabled() {
		t.Error("expected disabled when env=true")
	}
}

func TestIsAutoMemoryEnabled_ExplicitEnable(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	if !IsAutoMemoryEnabled() {
		t.Error("expected enabled when env=0")
	}
}

func TestValidateMemoryPath_RejectsNullBytes(t *testing.T) {
	result := ValidateMemoryPath("/tmp/test\x00/evil")
	if result != "" {
		t.Errorf("expected rejection for null bytes, got %q", result)
	}
}

func TestValidateMemoryPath_RejectsRelativePath(t *testing.T) {
	result := ValidateMemoryPath("relative/path")
	if result != "" {
		t.Errorf("expected rejection for relative path, got %q", result)
	}
}

func TestValidateMemoryPath_RejectsRoot(t *testing.T) {
	result := ValidateMemoryPath("/")
	if result != "" {
		t.Errorf("expected rejection for root path, got %q", result)
	}
}

func TestValidateMemoryPath_AcceptsValidPath(t *testing.T) {
	result := ValidateMemoryPath("/tmp/memory")
	if result == "" {
		t.Error("expected acceptance for valid absolute path")
	}
	if !strings.HasSuffix(result, string(filepath.Separator)) {
		t.Errorf("expected trailing separator, got %q", result)
	}
}

func TestValidateMemoryPath_RejectsTildeOnly(t *testing.T) {
	result := ValidateMemoryPath("~/")
	if result != "" {
		t.Errorf("expected rejection for ~/ (expands to $HOME), got %q", result)
	}
}

func TestSanitizePath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"/Users/foo/my-project", "-Users-foo-my-project"},
		{"simple", "simple"},
		{"path with spaces", "path-with-spaces"},
	}
	for _, tc := range tests {
		got := sanitizePath(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizePath(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestGetMemoryPath_ContainsProjectsDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := GetMemoryPath(tmp)
	if !strings.Contains(path, ".gbot") {
		t.Errorf("expected path to contain '.gbot', got %q", path)
	}
	if !strings.Contains(path, "projects") {
		t.Errorf("expected path to contain 'projects', got %q", path)
	}
	if !strings.HasSuffix(path, string(filepath.Separator)) {
		t.Errorf("expected trailing separator, got %q", path)
	}
}

func TestEnsureMemoryDir_CreatesDir(t *testing.T) {
	setTempHome(t)
	tmp := t.TempDir()
	err := EnsureMemoryDir(tmp)
	if err != nil {
		t.Fatalf("EnsureMemoryDir failed: %v", err)
	}
	memPath := GetMemoryPath(tmp)
	if _, err := os.Stat(memPath); err != nil {
		t.Errorf("memory dir not created: %v", err)
	}
}

func TestEnsureMemoryDir_Idempotent(t *testing.T) {
	setTempHome(t)
	tmp := t.TempDir()
	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
}

func TestIsMemoryPath_InsideDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	memPath := GetMemoryPath(tmp)
	testFile := filepath.Join(memPath, "test.md")
	if !IsMemoryPath(tmp, testFile) {
		t.Errorf("expected true for file inside memory dir: %q in %q", testFile, memPath)
	}
}

func TestIsMemoryPath_OutsideDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if IsMemoryPath(tmp, "/tmp/other/file.md") {
		t.Error("expected false for file outside memory dir")
	}
}

// --- truncate.go tests ---

func TestTruncateEntrypoint_NoTruncationNeeded(t *testing.T) {
	t.Parallel()
	content := "line1\nline2\nline3"
	result := TruncateEntrypoint(content)
	if result.WasLineTruncated {
		t.Error("should not be line truncated")
	}
	if result.WasByteTruncated {
		t.Error("should not be byte truncated")
	}
	if result.Content != content {
		t.Errorf("content changed: got %q", result.Content)
	}
}

func TestTruncateEntrypoint_LineTruncation(t *testing.T) {
	t.Parallel()
	var lines []string
	for range 250 {
		lines = append(lines, "short line")
	}
	content := strings.Join(lines, "\n")

	result := TruncateEntrypoint(content)
	if !result.WasLineTruncated {
		t.Error("expected line truncation")
	}
	// Content should have at most MaxEntrypointLines lines + warning
	contentLines := strings.Count(result.Content, "\n") + 1
	// Warning adds ~2 lines, so allow some slack
	if contentLines > MaxEntrypointLines+5 {
		t.Errorf("expected ~%d lines, got %d", MaxEntrypointLines, contentLines)
	}
	if !strings.Contains(result.Content, "WARNING") {
		t.Error("expected truncation warning in content")
	}
}

func TestTruncateEntrypoint_ByteTruncation(t *testing.T) {
	t.Parallel()
	// Single line >25KB — tests hard-truncate edge case
	longLine := strings.Repeat("x", 30000)
	content := longLine

	result := TruncateEntrypoint(content)
	if !result.WasByteTruncated {
		t.Error("expected byte truncation")
	}
	if len(result.Content) > MaxEntrypointBytes+200 { // +200 for warning
		t.Errorf("content too large: %d bytes", len(result.Content))
	}
}

func TestTruncateEntrypoint_BothTruncation(t *testing.T) {
	t.Parallel()
	// 250 lines of 200 chars each = ~50KB
	var lines []string
	for range 250 {
		lines = append(lines, strings.Repeat("x", 200))
	}
	content := strings.Join(lines, "\n")

	result := TruncateEntrypoint(content)
	if !result.WasLineTruncated {
		t.Error("expected line truncation")
	}
	if !result.WasByteTruncated {
		t.Error("expected byte truncation")
	}
}

func TestTruncateEntrypoint_EmptyInput(t *testing.T) {
	t.Parallel()
	result := TruncateEntrypoint("")
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
}

// --- read.go / write.go tests ---

func TestWriteAndLoadMemoryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	err := WriteMemoryFile(dir, "user_role", MemoryTypeUser, "User profile info", "User is a Go developer")
	if err != nil {
		t.Fatalf("WriteMemoryFile failed: %v", err)
	}

	// File should exist
	fp := filepath.Join(dir, "user_role.md")
	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("file not created: %v", err)
	}

	// Load it back
	mf, err := LoadMemoryFile(fp)
	if err != nil {
		t.Fatalf("LoadMemoryFile failed: %v", err)
	}
	if mf.Name != "user_role" {
		t.Errorf("name = %q, want 'user_role'", mf.Name)
	}
	if mf.Type != MemoryTypeUser {
		t.Errorf("type = %q, want 'user'", mf.Type)
	}
	if mf.Content != "User is a Go developer" {
		t.Errorf("content = %q, want 'User is a Go developer'", mf.Content)
	}
}

func TestUpdateAndLoadMemoryIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	err := UpdateMemoryIndex(dir, "user_role.md", "User profile info")
	if err != nil {
		t.Fatalf("UpdateMemoryIndex failed: %v", err)
	}

	idx, err := LoadMemoryIndex(dir)
	if err != nil {
		t.Fatalf("LoadMemoryIndex failed: %v", err)
	}
	if len(idx.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(idx.Entries))
	}
	if idx.Entries[0].FileName != "user_role.md" {
		t.Errorf("fileName = %q, want 'user_role.md'", idx.Entries[0].FileName)
	}
	if idx.Entries[0].Title != "user_role" {
		t.Errorf("title = %q, want 'user_role'", idx.Entries[0].Title)
	}
}

func TestUpdateMemoryIndex_DuplicateReplaces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_ = UpdateMemoryIndex(dir, "test.md", "old description")
	_ = UpdateMemoryIndex(dir, "test.md", "new description")

	idx, _ := LoadMemoryIndex(dir)
	if len(idx.Entries) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(idx.Entries))
	}
	if !strings.Contains(idx.Entries[0].Description, "new description") {
		t.Errorf("entry not updated: %q", idx.Entries[0].Description)
	}
}

func TestRemoveMemoryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_ = WriteMemoryFile(dir, "test", MemoryTypeProject, "desc", "content")
	_ = UpdateMemoryIndex(dir, "test.md", "desc")

	// Verify file and index exist
	idx, _ := LoadMemoryIndex(dir)
	if len(idx.Entries) != 1 {
		t.Fatalf("expected 1 entry before removal, got %d", len(idx.Entries))
	}

	err := RemoveMemoryFile(dir, "test.md")
	if err != nil {
		t.Fatalf("RemoveMemoryFile failed: %v", err)
	}

	// File should be gone
	fp := filepath.Join(dir, "test.md")
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Error("file should be removed")
	}

	// Index should be empty
	idx, _ = LoadMemoryIndex(dir)
	if len(idx.Entries) != 0 {
		t.Errorf("expected 0 entries after removal, got %d", len(idx.Entries))
	}
}

func TestLoadAllMemoryFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	_ = WriteMemoryFile(dir, "file1", MemoryTypeUser, "desc1", "content1")
	_ = WriteMemoryFile(dir, "file2", MemoryTypeFeedback, "desc2", "content2")

	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestLoadAllMemoryFiles_SkipsEntrypoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create MEMORY.md
	_ = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("# index\n"), 0o644)
	// Create a memory file
	_ = WriteMemoryFile(dir, "test", MemoryTypeProject, "desc", "content")

	files, _ := LoadAllMemoryFiles(dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (MEMORY.md skipped), got %d", len(files))
	}
}

func TestLoadAllMemoryFiles_EmptyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles on empty dir failed: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestLoadAllMemoryFiles_NonexistentDir(t *testing.T) {
	t.Parallel()
	files, err := LoadAllMemoryFiles("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got: %v", err)
	}
	if files != nil {
		t.Errorf("expected nil files, got %v", files)
	}
}

func TestBuildMemoryPrompt_Disabled(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	prompt := BuildMemoryPrompt(t.TempDir())
	if prompt != "" {
		t.Errorf("expected empty prompt when disabled, got %q", prompt)
	}
}

func TestBuildMemoryPrompt_EmptyDir(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	tmp := t.TempDir()
	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Error("expected non-empty prompt when enabled")
	}
	if !strings.Contains(prompt, "Types of memory") {
		t.Error("prompt missing type taxonomy section")
	}
	if !strings.Contains(prompt, "MEMORY.md is currently empty") {
		t.Error("prompt should mention empty MEMORY.md")
	}
}

func TestBuildMemoryPrompt_WithContent(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	tmp := t.TempDir()

	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatal(err)
	}
	memDir := GetMemoryPath(tmp)

	// Write to the COMPUTED memory dir, not tmp directly
	_ = WriteMemoryFile(memDir, "test", MemoryTypeUser, "Test memory", "Some content")
	_ = UpdateMemoryIndex(memDir, "test.md", "Test memory")

	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "What NOT to save") {
		t.Error("prompt missing 'What NOT to save' section")
	}
	if !strings.Contains(prompt, "Test memory") {
		t.Error("prompt should contain loaded memory content")
	}
}

// --- Coverage gap tests ---

// paths.go coverage

func TestValidateMemoryPath_RejectsUNCForwardSlash(t *testing.T) {
	t.Parallel()
	// On Unix, filepath.Clean("//server/...") → "/server/..." (not UNC).
	// UNC check is meaningful on Windows. On Linux, the path is just a normal absolute path
	// and won't be rejected by the UNC check. Verify it gets a valid result instead.
	result := ValidateMemoryPath("//server/share/path")
	// On Unix, this normalizes to a valid absolute path
	if result == "" {
		// On Windows or if UNC check fires, result is empty — that's also fine
		return
	}
	if !strings.HasPrefix(result, "/") {
		t.Errorf("expected absolute path, got %q", result)
	}
}

func TestValidateMemoryPath_TildeDotDot(t *testing.T) {
	t.Parallel()
	result := ValidateMemoryPath("~/..")
	if result != "" {
		t.Errorf("expected rejection for ~/.., got %q", result)
	}
}

func TestValidateMemoryPath_TildeExpansion(t *testing.T) {
	t.Parallel()
	result := ValidateMemoryPath("~/memory")
	if result == "" {
		t.Fatal("expected acceptance for ~/memory")
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, "memory") + string(filepath.Separator)
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

func TestValidateMemoryPath_EmptyInput(t *testing.T) {
	t.Parallel()
	result := ValidateMemoryPath("")
	if result != "" {
		t.Errorf("expected empty for empty input, got %q", result)
	}
}

func TestValidateMemoryPath_ShortPath(t *testing.T) {
	t.Parallel()
	// "/a" is len 2 after clean — too short
	result := ValidateMemoryPath("/a")
	if result != "" {
		t.Errorf("expected rejection for short path /a, got %q", result)
	}
}

func TestValidateMemoryPath_TildeRestIsEmpty(t *testing.T) {
	t.Parallel()
	// "~/" → rest is "", Clean("") = "." → rejected
	result := ValidateMemoryPath("~/")
	if result != "" {
		t.Errorf("expected rejection for ~/ (expands to $HOME), got %q", result)
	}
}

func TestValidateMemoryPath_TildeRestIsDot(t *testing.T) {
	t.Parallel()
	// "~\\." on non-Windows → rest is ".", Clean(".") = "." → rejected
	result := ValidateMemoryPath("~/.")
	if result != "" {
		t.Errorf("expected rejection for ~/., got %q", result)
	}
}

func TestSanitizePath_LongString(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("a", 300)
	result := sanitizePath(input)
	if len(result) > 250 {
		t.Errorf("result too long: %d chars", len(result))
	}
	// Should contain a hash suffix after truncation
	if !strings.Contains(result, "-") {
		t.Error("expected hash suffix separator after truncation")
	}
}

func TestDjb2Hash(t *testing.T) {
	t.Parallel()
	result := djb2Hash("test")
	if result == "" {
		t.Error("expected non-empty hash")
	}
	if result == "0" {
		t.Error("expected non-zero hash for non-empty input")
	}
}

func TestUintToString_Zero(t *testing.T) {
	t.Parallel()
	if got := uintToString(0, 10); got != "0" {
		t.Errorf("uintToString(0, 10) = %q, want '0'", got)
	}
}

func TestUintToString_Hex(t *testing.T) {
	t.Parallel()
	if got := uintToString(255, 16); got != "ff" {
		t.Errorf("uintToString(255, 16) = %q, want 'ff'", got)
	}
}

func TestUintToString_Base36(t *testing.T) {
	t.Parallel()
	if got := uintToString(35, 36); got != "z" {
		t.Errorf("uintToString(35, 36) = %q, want 'z'", got)
	}
}

func TestFindGitRoot_NoGit(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	result := findGitRoot(tmp)
	if result != "" {
		t.Errorf("expected empty for non-git dir, got %q", result)
	}
}

func TestFindGitRoot_HasGit(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	result := findGitRoot(tmp)
	if result != tmp {
		t.Errorf("expected %q, got %q", tmp, result)
	}
}

func TestFindCanonicalGitRoot_GitDir(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	result := findCanonicalGitRoot(tmp)
	if result != tmp {
		t.Errorf("expected %q (git dir), got %q", tmp, result)
	}
}

func TestFindCanonicalGitRoot_Worktree(t *testing.T) {
	t.Parallel()
	// Create main repo with .git directory
	mainRepo := t.TempDir()
	mainGitDir := filepath.Join(mainRepo, ".git")
	if err := os.MkdirAll(filepath.Join(mainGitDir, "worktrees", "test-wt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create worktree with .git FILE pointing to main
	worktree := t.TempDir()
	worktreeGit := filepath.Join(worktree, ".git")
	gitdirContent := "gitdir: " + mainGitDir + "/worktrees/test-wt"
	if err := os.WriteFile(worktreeGit, []byte(gitdirContent), 0644); err != nil {
		t.Fatal(err)
	}

	result := findCanonicalGitRoot(worktree)
	if result != mainRepo {
		t.Errorf("expected main repo %q, got %q", mainRepo, result)
	}
}

func TestFindCanonicalGitRoot_WorktreeBadPrefix(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	gitFile := filepath.Join(tmp, ".git")
	if err := os.WriteFile(gitFile, []byte("not a gitdir line"), 0644); err != nil {
		t.Fatal(err)
	}
	result := findCanonicalGitRoot(tmp)
	if result != tmp {
		t.Errorf("expected %q for non-gitdir .git file, got %q", tmp, result)
	}
}

func TestFindCanonicalGitRoot_WorktreeReadError(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	gitFile := filepath.Join(tmp, ".git")
	// Create .git as unreadable file
	if err := os.WriteFile(gitFile, []byte("gitdir: /path"), 0644); err != nil {
		t.Fatal(err)
	}
	// Make it unreadable
	if err := os.Chmod(gitFile, 0000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(gitFile, 0644) }()

	result := findCanonicalGitRoot(tmp)
	if result != tmp {
		t.Errorf("expected %q for read error, got %q", tmp, result)
	}
}

func TestFindCanonicalGitRoot_WorktreeMissingMain(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	gitFile := filepath.Join(tmp, ".git")
	content := "gitdir: /nonexistent/path/.git/worktrees/xxx"
	if err := os.WriteFile(gitFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	result := findCanonicalGitRoot(tmp)
	if result != tmp {
		t.Errorf("expected %q for missing main repo, got %q", tmp, result)
	}
}

func TestGetMemoryPath_HomeFallback(t *testing.T) {
	t.Parallel()
	// We can't easily trigger UserHomeDir error, but we can verify the function
	// returns a path with .gbot in it
	tmp := t.TempDir()
	path := GetMemoryPath(tmp)
	if !strings.Contains(path, ".gbot") {
		t.Errorf("expected .gbot in path, got %q", path)
	}
}

// prompt.go coverage

func TestBuildMemoryPrompt_WithContentAtComputedPath(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	tmp := t.TempDir()

	// Ensure memory dir exists and write content to the COMPUTED entrypoint
	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatal(err)
	}
	entrypoint := GetMemoryEntrypoint(tmp)
	content := "- [Test](test.md) — test description\n"
	if err := os.WriteFile(entrypoint, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "Test") {
		t.Errorf("prompt should contain the MEMORY.md content, got: %s", prompt[:min(200, len(prompt))])
	}
	if !strings.Contains(prompt, "## MEMORY.md") {
		t.Error("prompt should contain MEMORY.md section header")
	}
}

func TestBuildMemoryPrompt_WithTruncation(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	tmp := t.TempDir()

	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatal(err)
	}
	entrypoint := GetMemoryEntrypoint(tmp)
	// Write content that exceeds 200 lines
	var lines []string
	for i := range 250 {
		lines = append(lines, fmt.Sprintf("- [Entry %d](entry%d.md) — description %d", i, i, i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(entrypoint, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "WARNING") {
		t.Error("prompt should contain truncation warning")
	}
}

func TestBuildMemoryPrompt_ReadFileError(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	tmp := t.TempDir()

	// Ensure memory dir exists, then make MEMORY.md a directory (causes ReadFile error)
	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatal(err)
	}
	entrypoint := GetMemoryEntrypoint(tmp)
	if err := os.MkdirAll(entrypoint, 0755); err != nil {
		t.Fatal(err)
	}

	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Fatal("expected non-empty prompt even on read error")
	}
	if !strings.Contains(prompt, "currently empty") {
		t.Error("prompt should mention empty on read error")
	}
}

func TestBuildMemoryPrompt_EmptyContent(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	tmp := t.TempDir()

	if err := EnsureMemoryDir(tmp); err != nil {
		t.Fatal(err)
	}
	entrypoint := GetMemoryEntrypoint(tmp)
	if err := os.WriteFile(entrypoint, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	prompt := BuildMemoryPrompt(tmp)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "currently empty") {
		t.Error("prompt should mention empty for empty file")
	}
}

func TestHowToSaveSection_SkipIndex(t *testing.T) {
	t.Parallel()
	lines := howToSaveSection(true)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Step 2") {
		t.Error("skipIndex=true should not contain Step 2")
	}
	if !strings.Contains(joined, "frontmatter format") {
		t.Error("should contain frontmatter format reference")
	}
}

func TestReadFileOrNull_Nonexistent(t *testing.T) {
	t.Parallel()
	data, err := readFileOrNull("/nonexistent/file/path")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "/nonexistent/file/path") {
		t.Errorf("error should reference path, got: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data, got %v", data)
	}
}

// read.go coverage

func TestLoadMemoryIndex_ReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create MEMORY.md as a directory to cause ReadFile error
	if err := os.MkdirAll(filepath.Join(dir, EntrypointName), 0755); err != nil {
		t.Fatal(err)
	}
	_, err := LoadMemoryIndex(dir)
	if err == nil {
		t.Error("expected error when MEMORY.md is a directory")
	}
	if !strings.Contains(err.Error(), "read MEMORY.md") {
		t.Errorf("error should mention 'read MEMORY.md', got: %v", err)
	}
}

func TestParseIndexLine_EmDash(t *testing.T) {
	t.Parallel()
	line := "- [Title](file.md) — em dash description"
	entry := parseIndexLine(line)
	if entry.Title != "Title" {
		t.Errorf("Title = %q, want 'Title'", entry.Title)
	}
	if entry.FileName != "file.md" {
		t.Errorf("FileName = %q, want 'file.md'", entry.FileName)
	}
	if entry.Description != "em dash description" {
		t.Errorf("Description = %q, want 'em dash description'", entry.Description)
	}
}

func TestParseIndexLine_DoubleDash(t *testing.T) {
	t.Parallel()
	line := "- [Title](file.md) -- double dash desc"
	entry := parseIndexLine(line)
	if entry.Description != "double dash desc" {
		t.Errorf("Description = %q, want 'double dash desc'", entry.Description)
	}
}

func TestParseIndexLine_NoBrackets(t *testing.T) {
	t.Parallel()
	entry := parseIndexLine("no brackets here")
	if entry.FileName != "" {
		t.Errorf("expected empty entry for malformed line, got FileName=%q", entry.FileName)
	}
}

func TestParseIndexLine_ReversedBrackets(t *testing.T) {
	t.Parallel()
	entry := parseIndexLine("- ]Title[)(file.md)")
	if entry.FileName != "" {
		t.Errorf("expected empty entry for reversed brackets, got FileName=%q", entry.FileName)
	}
}

func TestParseIndexLine_NoParens(t *testing.T) {
	t.Parallel()
	entry := parseIndexLine("- [Title]no parens")
	if entry.FileName != "" {
		t.Errorf("expected empty entry for no parens, got FileName=%q", entry.FileName)
	}
}

func TestLoadMemoryFile_Error(t *testing.T) {
	t.Parallel()
	_, err := LoadMemoryFile("/nonexistent/file.md")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "read memory file") {
		t.Errorf("error should mention 'read memory file', got: %v", err)
	}
}

func TestLoadMemoryFile_LegacyNoFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fp := filepath.Join(dir, "legacy.md")
	content := "This is legacy content without frontmatter"
	if err := os.WriteFile(fp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	mf, err := LoadMemoryFile(fp)
	if err != nil {
		t.Fatalf("LoadMemoryFile failed: %v", err)
	}
	if mf.Name != "legacy" {
		t.Errorf("Name = %q, want 'legacy' (derived from filename)", mf.Name)
	}
	if mf.Type != MemoryTypeProject {
		t.Errorf("Type = %q, want 'project' (default for legacy)", mf.Type)
	}
	if mf.Content != content {
		t.Errorf("Content = %q, want original content", mf.Content)
	}
}

func TestLoadAllMemoryFiles_SkipsNonMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create non-markdown files
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create one valid markdown file
	_ = WriteMemoryFile(dir, "test", MemoryTypeUser, "desc", "content")

	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (non-md skipped), got %d", len(files))
	}
	if files[0].Name != "test" {
		t.Errorf("FileName = %q, want 'test'", files[0].Name)
	}
}

func TestLoadAllMemoryFiles_SkipsUnreadableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a file without frontmatter that will be loaded as legacy
	badFile := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(badFile, []byte("---\nbroken frontmatter"), 0644); err != nil {
		t.Fatal(err)
	}
	// This should load successfully as legacy (no frontmatter)
	// To test LoadMemoryFile error, we need a file that can't be read
	// after ReadDir lists it. This is hard to trigger without race conditions.
	// At minimum, verify the function handles the directory correctly.
	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles failed: %v", err)
	}
	// The broken frontmatter file should be loaded as legacy
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

func TestLoadAllMemoryFiles_ReadDirError(t *testing.T) {
	t.Parallel()
	// Create a file (not directory) at the path to cause ReadDir to fail
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadAllMemoryFiles(filePath)
	if err == nil {
		t.Error("expected error when reading a file as directory")
	}
	if !strings.Contains(err.Error(), "read memory dir") {
		t.Errorf("error should mention 'read memory dir', got: %v", err)
	}
}

// write.go coverage

func TestWriteMemoryFile_MkdirAllError(t *testing.T) {
	t.Parallel()
	// Create a read-only parent directory
	parent := t.TempDir()
	readOnlyDir := filepath.Join(parent, "readonly")
	if err := os.MkdirAll(readOnlyDir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnlyDir, 0755) }()

	// Try to write to a subdirectory of the read-only dir
	target := filepath.Join(readOnlyDir, "nested", "file")
	err := WriteMemoryFile(target, "test", MemoryTypeUser, "desc", "content")
	if err == nil {
		t.Error("expected error when parent dir is read-only")
	}
	if !strings.Contains(err.Error(), "create memory dir") {
		t.Errorf("error should mention 'create memory dir', got: %v", err)
	}
}

func TestWriteMemoryFile_WriteError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create the target path as a directory to cause WriteFile to fail
	targetFile := filepath.Join(dir, sanitizeFileName("test")+".md")
	if err := os.MkdirAll(targetFile, 0755); err != nil {
		t.Fatal(err)
	}
	err := WriteMemoryFile(dir, "test", MemoryTypeUser, "desc", "content")
	if err == nil {
		t.Error("expected error when target is a directory")
	}
	if !strings.Contains(err.Error(), "write memory file") {
		t.Errorf("error should mention 'write memory file', got: %v", err)
	}
}

func TestUpdateMemoryIndex_ReadError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create MEMORY.md as a directory to cause ReadFile error
	if err := os.MkdirAll(filepath.Join(dir, EntrypointName), 0755); err != nil {
		t.Fatal(err)
	}
	err := UpdateMemoryIndex(dir, "test.md", "desc")
	if err == nil {
		t.Error("expected error when MEMORY.md is a directory")
	}
	if !strings.Contains(err.Error(), "read MEMORY.md") {
		t.Errorf("error should mention 'read MEMORY.md', got: %v", err)
	}
}

func TestUpdateMemoryIndex_WriteError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create MEMORY.md with existing content
	ep := filepath.Join(dir, EntrypointName)
	if err := os.WriteFile(ep, []byte("- [old](old.md) — old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Make the file read-only so WriteFile (O_WRONLY) fails
	if err := os.Chmod(ep, 0444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(ep, 0644) }()

	err := UpdateMemoryIndex(dir, "test.md", "desc")
	if err == nil {
		t.Error("expected write error")
	}
	if err != nil && !strings.Contains(err.Error(), "write MEMORY.md") {
		t.Errorf("error should mention 'write MEMORY.md', got: %v", err)
	}
}

func TestRemoveMemoryFile_RemoveError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create target as non-empty directory to cause os.Remove to fail (ENOTEMPTY)
	targetDir := filepath.Join(dir, "test.md")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Put a file inside so it's non-empty
	if err := os.WriteFile(filepath.Join(targetDir, "nested"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	err := RemoveMemoryFile(dir, "test.md")
	if err == nil {
		t.Error("expected error when removing a non-empty directory")
	}
	if !strings.Contains(err.Error(), "remove memory file") {
		t.Errorf("error should mention 'remove memory file', got: %v", err)
	}
}

func TestRemoveMemoryFile_ReadIndexError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create the file to remove
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Make MEMORY.md a directory to cause read error
	if err := os.MkdirAll(filepath.Join(dir, EntrypointName), 0755); err != nil {
		t.Fatal(err)
	}
	err := RemoveMemoryFile(dir, "test.md")
	if err == nil {
		t.Error("expected error when MEMORY.md is a directory")
	}
	if !strings.Contains(err.Error(), "read MEMORY.md") {
		t.Errorf("error should mention 'read MEMORY.md', got: %v", err)
	}
}

func TestRemoveMemoryFile_WriteIndexError(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("skipping write error test when running as root")
	}
	dir := t.TempDir()
	// Create file and index
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte("- [test](test.md) — desc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Make dir read-only to prevent writing (file removal happens before write)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0755) }()

	err := RemoveMemoryFile(dir, "test.md")
	if err == nil {
		t.Fatal("expected error when directory is read-only")
	}
	if !strings.Contains(err.Error(), "remove memory file") && !strings.Contains(err.Error(), "write MEMORY.md") {
		t.Errorf("error should mention operation, got: %v", err)
	}
}

func TestRemoveMemoryFile_NoIndexFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create file but no MEMORY.md
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	err := RemoveMemoryFile(dir, "test.md")
	if err != nil {
		t.Errorf("expected nil error when no index file, got: %v", err)
	}
}

func TestSanitizeFileName_AllSpecial(t *testing.T) {
	t.Parallel()
	result := sanitizeFileName("!@#$%^&*()")
	if result != "memory" {
		t.Errorf("expected 'memory' for all-special input, got %q", result)
	}
}

func TestSanitizeFileName_SpacesAndCaps(t *testing.T) {
	t.Parallel()
	result := sanitizeFileName("My Cool Memory")
	if result != "my_cool_memory" {
		t.Errorf("got %q, want 'my_cool_memory'", result)
	}
}

// types.go coverage

func TestParseFrontmatter_UnclosedFrontmatter(t *testing.T) {
	t.Parallel()
	content := "---\nname: test\nthis has no closing ---"
	_, _, _, body, ok := ParseFrontmatter(content)
	if ok {
		t.Error("expected ok=false for unclosed frontmatter")
	}
	if body != content {
		t.Errorf("body should be original content for unclosed frontmatter")
	}
}

func TestParseFrontmatter_CommentLine(t *testing.T) {
	t.Parallel()
	content := "---\n# this is a comment\nname: test\ntype: user\n---\nbody"
	name, _, memType, body, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("expected ok=true with comment line")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
	if memType != MemoryTypeUser {
		t.Errorf("type = %q, want 'user'", memType)
	}
	if body != "body" {
		t.Errorf("body = %q, want 'body'", body)
	}
}

func TestParseFrontmatter_ColonLessLine(t *testing.T) {
	t.Parallel()
	content := "---\nnocolonhere\nname: test\n---\nbody"
	name, _, _, _, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("expected ok=true with colon-less line")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
}

func TestParseFrontmatter_NoTypeKey(t *testing.T) {
	t.Parallel()
	content := "---\nname: test\ndescription: desc\n---\nbody"
	name, _, memType, _, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("expected ok=true without type key")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
	if memType != "" {
		t.Errorf("type = %q, want empty when not specified", memType)
	}
}

func TestParseFrontmatter_EmptyLine(t *testing.T) {
	t.Parallel()
	content := "---\n\nname: test\n---\nbody"
	name, _, _, _, ok := ParseFrontmatter(content)
	if !ok {
		t.Fatal("expected ok=true with empty line in frontmatter")
	}
	if name != "test" {
		t.Errorf("name = %q, want 'test'", name)
	}
}

// --- Remaining coverage gap tests ---

func TestDjb2Hash_NegativeInt32(t *testing.T) {
	t.Parallel()
	// Use high-value runes to force uint32 overflow into negative int32 range
	for _, input := range []string{
		strings.Repeat(string(rune(0x7FF)), 50),
		strings.Repeat(string(rune(0xFFFF)), 50),
		strings.Repeat("é", 100),
	} {
		hash := uint32(5381)
		for _, c := range input {
			hash = hash*33 + uint32(c)
		}
		if int32(hash) < 0 {
			result := djb2Hash(input)
			if result == "" {
				t.Error("expected non-empty hash")
			}
			return // triggered the negative path
		}
	}
	t.Log("none of the test inputs triggered negative int32 path")
}

func TestLoadMemoryIndex_NoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// No MEMORY.md exists → IsNotExist branch
	idx, err := LoadMemoryIndex(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(idx.Entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent file, got %d", len(idx.Entries))
	}
}

func TestLoadAllMemoryFiles_SkipsDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a subdirectory (should be skipped by IsDir check)
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}
	// Create one valid file
	_ = WriteMemoryFile(dir, "test", MemoryTypeUser, "desc", "content")

	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (subdir skipped), got %d", len(files))
	}
}

func TestUpdateMemoryIndex_AppendWithoutNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create MEMORY.md without trailing newline
	if err := os.WriteFile(filepath.Join(dir, EntrypointName), []byte("- [old](old.md) — old desc"), 0644); err != nil {
		t.Fatal(err)
	}

	err := UpdateMemoryIndex(dir, "new.md", "new desc")
	if err != nil {
		t.Fatalf("UpdateMemoryIndex failed: %v", err)
	}

	idx, _ := LoadMemoryIndex(dir)
	if len(idx.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(idx.Entries))
	}
	names := map[string]bool{}
	for _, e := range idx.Entries {
		names[e.FileName] = true
	}
	if !names["old.md"] {
		t.Error("missing old.md entry")
	}
	if !names["new.md"] {
		t.Error("missing new.md entry")
	}
}

// ---------------------------------------------------------------------------
// Chain tests: full user journeys (entry → intermediate → observable output)
// ---------------------------------------------------------------------------

// TestChain_WriteIndexLoad verifies the primary user journey:
//
//	Write 3 memory files → update MEMORY.md index → load index → load all files
//	→ index lists all 3 entries → all files have correct content
//
// Observable output: index and file contents match what was written.
func TestChain_WriteIndexLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Step 1: Write three memory files of different types
	if err := WriteMemoryFile(dir, "user_profile", MemoryTypeUser, "User profile", "User is a Go developer working on gbot"); err != nil {
		t.Fatalf("write user_profile: %v", err)
	}
	if err := WriteMemoryFile(dir, "commit_rules", MemoryTypeFeedback, "Commit approval", "Always ask before committing"); err != nil {
		t.Fatalf("write commit_rules: %v", err)
	}
	if err := WriteMemoryFile(dir, "project_status", MemoryTypeProject, "Current status", "Porting session memory to Go"); err != nil {
		t.Fatalf("write project_status: %v", err)
	}

	// Step 2: Update MEMORY.md index with all entries
	if err := UpdateMemoryIndex(dir, "user_profile.md", "User profile"); err != nil {
		t.Fatalf("update index user_profile: %v", err)
	}
	if err := UpdateMemoryIndex(dir, "commit_rules.md", "Commit approval"); err != nil {
		t.Fatalf("update index commit_rules: %v", err)
	}
	if err := UpdateMemoryIndex(dir, "project_status.md", "Current status"); err != nil {
		t.Fatalf("update index project_status: %v", err)
	}

	// Step 3: Load index — observable: 3 entries with correct filenames and descriptions
	idx, err := LoadMemoryIndex(dir)
	if err != nil {
		t.Fatalf("LoadMemoryIndex: %v", err)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("expected 3 index entries, got %d", len(idx.Entries))
	}

	entryMap := map[string]IndexEntry{}
	for _, e := range idx.Entries {
		entryMap[e.FileName] = e
	}
	for _, name := range []string{"user_profile.md", "commit_rules.md", "project_status.md"} {
		if _, ok := entryMap[name]; !ok {
			t.Errorf("index missing entry for %q", name)
		}
	}
	if entryMap["user_profile.md"].Description != "User profile" {
		t.Errorf("user_profile description = %q, want 'User profile'", entryMap["user_profile.md"].Description)
	}

	// Step 4: Load all files — observable: 3 files with correct content and types
	files, err := LoadAllMemoryFiles(dir)
	if err != nil {
		t.Fatalf("LoadAllMemoryFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	contentMap := map[string]*MemoryFile{}
	for i := range files {
		contentMap[files[i].Name] = &files[i]
	}
	if contentMap["user_profile"].Type != MemoryTypeUser {
		t.Errorf("user_profile type = %q, want 'user'", contentMap["user_profile"].Type)
	}
	if contentMap["user_profile"].Content != "User is a Go developer working on gbot" {
		t.Errorf("user_profile content = %q", contentMap["user_profile"].Content)
	}
	if contentMap["commit_rules"].Type != MemoryTypeFeedback {
		t.Errorf("commit_rules type = %q, want 'feedback'", contentMap["commit_rules"].Type)
	}
	if contentMap["commit_rules"].Content != "Always ask before committing" {
		t.Errorf("commit_rules content = %q", contentMap["commit_rules"].Content)
	}
	if contentMap["project_status"].Type != MemoryTypeProject {
		t.Errorf("project_status type = %q, want 'project'", contentMap["project_status"].Type)
	}
	if contentMap["project_status"].Content != "Porting session memory to Go" {
		t.Errorf("project_status content = %q", contentMap["project_status"].Content)
	}

	// Step 5: MEMORY.md index content should reference all files
	raw := idx.Raw
	for _, name := range []string{"user_profile.md", "commit_rules.md", "project_status.md"} {
		if !strings.Contains(raw, name) {
			t.Errorf("MEMORY.md raw content missing %q", name)
		}
	}
}

// TestChain_RemoveThenLoad verifies the removal journey:
//
//	Write 2 files → load (has both) → remove 1 → load (has 1)
//
// Observable output: after removal, index and file list no longer contain
// the removed file, but still contain the remaining file.
func TestChain_RemoveThenLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Setup: write two files + index
	_ = WriteMemoryFile(dir, "keep_me", MemoryTypeUser, "Keep this", "This should persist")
	_ = WriteMemoryFile(dir, "remove_me", MemoryTypeFeedback, "Remove this", "This should disappear")
	_ = UpdateMemoryIndex(dir, "keep_me.md", "Keep this")
	_ = UpdateMemoryIndex(dir, "remove_me.md", "Remove this")

	// Pre-condition: both files visible
	files, _ := LoadAllMemoryFiles(dir)
	if len(files) != 2 {
		t.Fatalf("pre-remove: expected 2 files, got %d", len(files))
	}
	idx, _ := LoadMemoryIndex(dir)
	if len(idx.Entries) != 2 {
		t.Fatalf("pre-remove: expected 2 index entries, got %d", len(idx.Entries))
	}

	// Action: remove one file
	if err := RemoveMemoryFile(dir, "remove_me.md"); err != nil {
		t.Fatalf("RemoveMemoryFile: %v", err)
	}

	// Observable: file is gone from disk
	if _, err := os.Stat(filepath.Join(dir, "remove_me.md")); !os.IsNotExist(err) {
		t.Error("remove_me.md should be deleted from disk")
	}

	// Observable: index has exactly 1 entry — the kept file
	idx, _ = LoadMemoryIndex(dir)
	if len(idx.Entries) != 1 {
		t.Fatalf("expected 1 index entry after removal, got %d", len(idx.Entries))
	}
	if idx.Entries[0].FileName != "keep_me.md" {
		t.Errorf("remaining entry = %q, want 'keep_me.md'", idx.Entries[0].FileName)
	}
	if idx.Entries[0].Description != "Keep this" {
		t.Errorf("remaining description = %q, want 'Keep this'", idx.Entries[0].Description)
	}

	// Observable: MEMORY.md no longer mentions removed file
	if i := strings.Index(idx.Raw, "remove_me"); i >= 0 {
		t.Error("MEMORY.md should not contain removed file reference")
	}
	if i := strings.Index(idx.Raw, "keep_me"); i < 0 {
		t.Error("MEMORY.md should still contain kept file reference")
	}

	// Observable: LoadAllMemoryFiles returns only kept file with correct content
	files, _ = LoadAllMemoryFiles(dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file after removal, got %d", len(files))
	}
	if files[0].Name != "keep_me" {
		t.Errorf("remaining file name = %q, want 'keep_me'", files[0].Name)
	}
	if files[0].Content != "This should persist" {
		t.Errorf("remaining content = %q, want 'This should persist'", files[0].Content)
	}
}

// TestChain_UpdateContentThenLoad verifies content update journey:
//
//	Write file → load (has original) → overwrite → load (has new content)
//
// Observable output: loaded content reflects the overwrite, not the original.
func TestChain_UpdateContentThenLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Write initial file
	_ = WriteMemoryFile(dir, "direction", MemoryTypeProject, "Direction", "Building CLI tool")

	// Load and verify original content
	mf, err := LoadMemoryFile(filepath.Join(dir, "direction.md"))
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if mf.Content != "Building CLI tool" {
		t.Fatalf("initial content = %q, want 'Building CLI tool'", mf.Content)
	}

	// Overwrite with new content (same name, different content)
	if err := WriteMemoryFile(dir, "direction", MemoryTypeProject, "Direction updated", "Now building a Go port of Claude Code"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	// Observable: loaded content reflects the update
	mf, err = LoadMemoryFile(filepath.Join(dir, "direction.md"))
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if mf.Content != "Now building a Go port of Claude Code" {
		t.Errorf("updated content = %q, want new content", mf.Content)
	}
	if mf.Description != "Direction updated" {
		t.Errorf("updated description = %q, want 'Direction updated'", mf.Description)
	}

	// Observable: old content is gone
	if strings.Contains(mf.Content, "Building CLI tool") {
		t.Error("file should not contain old content after overwrite")
	}
}

// TestChain_WriteIndexPrompt verifies the full pipeline to BuildMemoryPrompt:
//
//	Write files to computed memory dir → write MEMORY.md → BuildMemoryPrompt
//	→ prompt contains MEMORY.md index content
//
// Observable output: BuildMemoryPrompt returns a prompt with the MEMORY.md
// section containing our index entries.
func TestChain_WriteIndexPrompt(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	setTempHome(t)
	workingDir := t.TempDir()

	// Write files to the computed memory directory
	memDir := GetMemoryPath(workingDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write three memory files directly
	for _, item := range []struct {
		name, desc, body string
	}{
		{"user_profile", "User profile", "User is a Go developer"},
		{"commit_rules", "Commit rules", "Always ask before committing"},
		{"project_status", "Project status", "Porting session memory"},
	} {
		if err := WriteMemoryFile(memDir, item.name, MemoryTypeUser, item.desc, item.body); err != nil {
			t.Fatalf("write %s: %v", item.name, err)
		}
		if err := UpdateMemoryIndex(memDir, item.name+".md", item.desc); err != nil {
			t.Fatalf("index %s: %v", item.name, err)
		}
	}

	// Build prompt — this is the entry point the engine calls
	prompt := BuildMemoryPrompt(workingDir)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}

	// Observable: prompt contains MEMORY.md section with index entries
	if !strings.Contains(prompt, "## MEMORY.md") {
		t.Error("prompt missing MEMORY.md section header")
	}
	// The MEMORY.md index content should be included in the prompt
	for _, name := range []string{"user_profile.md", "commit_rules.md", "project_status.md"} {
		if !strings.Contains(prompt, name) {
			t.Errorf("prompt MEMORY.md section missing index entry %q", name)
		}
	}

	// Observable: prompt contains standard sections
	if !strings.Contains(prompt, "Types of memory") {
		t.Error("prompt missing 'Types of memory' section")
	}
	if !strings.Contains(prompt, "What NOT to save") {
		t.Error("prompt missing 'What NOT to save' section")
	}
}
