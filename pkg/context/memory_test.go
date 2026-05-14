package context_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/memory/long"
)

func setTempHome(t *testing.T) {
	t.Helper()
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatalf("create temp home: %v", err)
	}
	t.Setenv("HOME", homeDir)
}

func TestLoadMemoryFiles_Empty(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files with empty dir, got %d", len(files))
	}
}

func TestLoadMemoryFiles_SingleFile(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "Always use Go standard library patterns."
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != content {
		t.Errorf("expected %q, got %q", content, files[0].Content)
	}
}

func TestLoadMemoryFiles_MultipleFiles(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create files with names that sort deterministically
	if err := os.WriteFile(filepath.Join(memDir, "b-notes.md"), []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "a-notes.md"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	// Sorted by name: a first, b second
	if files[0].Content != "first" {
		t.Errorf("expected first file 'first', got %q", files[0].Content)
	}
	if files[1].Content != "second" {
		t.Errorf("expected second file 'second', got %q", files[1].Content)
	}
}

func TestLoadMemoryFiles_SkipsNonMarkdown(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("valid"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "data.json"), []byte(`{"key":"val"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "image.png"), []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (skipping non-md), got %d", len(files))
	}
	if files[0].Content != "valid" {
		t.Errorf("expected 'valid', got %q", files[0].Content)
	}
}

func TestLoadMemoryFiles_SkipsEmptyFiles(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "empty.md"), []byte("   \n  "), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "real.md"), []byte("has content"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (skipping empty), got %d", len(files))
	}
	if files[0].Content != "has content" {
		t.Errorf("expected 'has content', got %q", files[0].Content)
	}
}

func TestLoadMemoryFiles_SkipsDirectories(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory that should be skipped
	if err := os.MkdirAll(filepath.Join(memDir, "subdir.md"), 0755); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files (subdir.md is a directory), got %d", len(files))
	}
}

func TestLoadMemoryFiles_MigratesFromLegacyPath(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "legacy content"
	if err := os.WriteFile(filepath.Join(legacyDir, "old.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file after migration, got %d", len(files))
	}
	// Migration adds frontmatter but preserves original content
	if !strings.Contains(files[0].Content, content) {
		t.Errorf("expected content to contain %q, got %q", content, files[0].Content)
	}
	if !strings.HasPrefix(files[0].Content, "---\n") {
		t.Error("expected frontmatter from migration")
	}
	if !strings.Contains(files[0].Content, "Migrated from legacy memory") {
		t.Error("expected migration description in frontmatter")
	}
}

func TestLoadMemoryFiles_MigrationSkipsWhenNewPathHasContent(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write directly to new path — migration should be skipped
	if err := os.WriteFile(filepath.Join(memDir, "new.md"), []byte("new content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also create legacy file — should be ignored since new path already has content
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "legacy.md"), []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (only new path), got %d", len(files))
	}
	if files[0].Content != "new content" {
		t.Errorf("expected 'new content', got %q", files[0].Content)
	}
}

func TestLoadMemoryFiles_ReadFileError(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(memDir, "unreadable.md")
	if err := os.WriteFile(target, []byte("secret content"), 0644); err != nil {
		t.Fatal(err)
	}
	// Remove read permission — ReadDir still lists it, ReadFile fails
	if err := os.Chmod(target, 0000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	defer func() { _ = os.Chmod(target, 0644) }() // restore for cleanup

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files when file is unreadable, got %d", len(files))
	}
}

func TestLoadMemoryFiles_NonExistentDir(t *testing.T) {
	setTempHome(t)
	// Pass a path that doesn't exist at all — os.ReadDir fails, returns nil
	files := context.LoadMemoryFiles("/nonexistent/path/that/does/not/exist")
	if len(files) != 0 {
		t.Errorf("expected 0 files for nonexistent dir, got %d", len(files))
	}
}

func TestLoadMemoryFiles_FilepathAbsError(t *testing.T) {
	setTempHome(t)
	// filepath.Abs on an already-absolute path never errors — it just
	// returns the path. This test verifies the happy path through filepath.Abs.
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "abs_test.md"), []byte("abs path test"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !filepath.IsAbs(files[0].Path) {
		t.Errorf("expected absolute path, got %q", files[0].Path)
	}
}

func TestLoadMemoryFiles_MarkdownExtensions(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "a.md"), []byte("md"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "b.markdown"), []byte("markdown"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "c.mdx"), []byte("mdx"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 3 {
		t.Fatalf("expected 3 files (md, markdown, mdx), got %d", len(files))
	}
}

func TestFormatMemorySection_Empty(t *testing.T) {
	t.Parallel()
	result := context.FormatMemorySection(nil)
	if result != "" {
		t.Errorf("expected empty for nil, got %q", result)
	}
}

func TestFormatMemorySection_WithFiles(t *testing.T) {
	t.Parallel()
	files := []context.MemoryFile{
		{Path: "/tmp/.gbot/memory/notes.md", Content: "Use strict mode"},
		{Path: "/tmp/.gbot/memory/style.md", Content: "No tabs"},
	}
	result := context.FormatMemorySection(files)

	if !strings.Contains(result, "## Memory") {
		t.Error("missing Memory section header")
	}
	if !strings.Contains(result, "notes.md") {
		t.Error("missing notes.md reference")
	}
	if !strings.Contains(result, "Use strict mode") {
		t.Error("missing notes.md content")
	}
	if !strings.Contains(result, "No tabs") {
		t.Error("missing style.md content")
	}
}

func TestFormatMemorySection_HomePathTilde(t *testing.T) {
	t.Parallel()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home dir")
	}

	files := []context.MemoryFile{
		{Path: homeDir + "/.gbot/memory/test.md", Content: "content"},
	}
	result := context.FormatMemorySection(files)

	if !strings.Contains(result, "~/.gbot/memory/test.md") {
		t.Errorf("expected ~ shortened path in output, got: %s", result)
	}
}

func TestBuild_WithMemoryFiles(t *testing.T) {
	// Disable typed-memory to test the legacy MemoryFiles path
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	b := context.NewBuilder("/work")
	b.MemoryFiles = []context.MemoryFile{
		{Path: "/work/.gbot/memory/test.md", Content: "Remember this"},
	}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}

	if !strings.Contains(promptStr, "## Memory") {
		t.Error("built prompt missing Memory section")
	}
	if !strings.Contains(promptStr, "Remember this") {
		t.Error("built prompt missing memory content")
	}
}

func TestBuild_WithTypedMemory(t *testing.T) {
	// Enable typed-memory (env=0 means enabled)
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")
	b := context.NewBuilder("/work")

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}

	if !strings.Contains(promptStr, "Types of memory") {
		t.Error("built prompt missing typed-memory section")
	}
	if !strings.Contains(promptStr, "What NOT to save") {
		t.Error("built prompt missing 'What NOT to save' section")
	}
}

func TestLoadMemoryFiles_Disabled(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "1")
	files := context.LoadMemoryFiles(t.TempDir())
	if files != nil {
		t.Errorf("expected nil when disabled, got %d files", len(files))
	}
}

// --- loadFromIndex path (MEMORY.md index) ---

func TestLoadMemoryFiles_WithIndex(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create MEMORY.md index with two entries
	indexContent := "- [User Profile](user_profile.md) — user info\n- [Feedback](feedback.md) — guidelines\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "user_profile.md"), []byte("Always use Go"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "feedback.md"), []byte("No tabs"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 2 {
		t.Fatalf("expected 2 files via index, got %d", len(files))
	}

	var foundUser, foundFeedback bool
	for _, f := range files {
		if strings.Contains(f.Path, "user_profile.md") {
			foundUser = true
			if f.Content != "Always use Go" {
				t.Errorf("user_profile content = %q, want 'Always use Go'", f.Content)
			}
		}
		if strings.Contains(f.Path, "feedback.md") {
			foundFeedback = true
			if f.Content != "No tabs" {
				t.Errorf("feedback content = %q, want 'No tabs'", f.Content)
			}
		}
	}
	if !foundUser {
		t.Error("user_profile.md not loaded via index")
	}
	if !foundFeedback {
		t.Error("feedback.md not loaded via index")
	}
}

func TestLoadMemoryFiles_IndexSkipsMissingFiles(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Index references two files, but only one exists
	indexContent := "- [Exists](exists.md) — present\n- [Missing](missing.md) — gone\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "exists.md"), []byte("present content"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (missing skipped), got %d", len(files))
	}
	if files[0].Content != "present content" {
		t.Errorf("content = %q, want 'present content'", files[0].Content)
	}
}

func TestLoadMemoryFiles_IndexSkipsEmptyFiles(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Index references one empty file
	indexContent := "- [Empty](empty.md) — blank\n"
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte(indexContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "empty.md"), []byte("   \n  \n"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Fatalf("expected 0 files (empty content skipped), got %d", len(files))
	}
}

func TestLoadMemoryFiles_IndexFallbackToScan(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// MEMORY.md exists but has no valid index entries
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory Index\nNo entries here.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Regular .md file should be picked up by scan fallback
	if err := os.WriteFile(filepath.Join(memDir, "notes.md"), []byte("useful note"), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file from scan fallback, got %d", len(files))
	}
	if files[0].Content != "useful note" {
		t.Errorf("content = %q, want 'useful note'", files[0].Content)
	}
	// Verify MEMORY.md itself is NOT in the results (scan skips MEMORY.md)
	for _, f := range files {
		if strings.Contains(f.Path, "MEMORY.md") {
			t.Error("MEMORY.md should be skipped in scan fallback")
		}
	}
}

// --- migrateLegacyMemory edge cases ---

func TestMigrateLegacyMemory_HomeDirPath(t *testing.T) {
	setTempHome(t)
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)

	// Create legacy memory in HOME dir
	homeLegacy := filepath.Join(homeDir, ".gbot", "memory")
	if err := os.MkdirAll(homeLegacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeLegacy, "global.md"), []byte("global preference"), 0644); err != nil {
		t.Fatal(err)
	}

	// workingDir has NO legacy dir — only HOME has it
	tmpDir := t.TempDir()
	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file migrated from HOME, got %d", len(files))
	}
	if !strings.Contains(files[0].Content, "global preference") {
		t.Errorf("expected 'global preference' in content, got %q", files[0].Content)
	}
	if !strings.HasPrefix(files[0].Content, "---\n") {
		t.Error("expected frontmatter from migration")
	}
}

func TestMigrateLegacyMemory_SkipsNonMarkdown(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "data.json"), []byte(`{"key":"val"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "notes.txt"), []byte("text file"), 0644); err != nil {
		t.Fatal(err)
	}
	// Directory named like a file — should be skipped by IsDir check
	if err := os.MkdirAll(filepath.Join(legacyDir, "subdir.md"), 0755); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files (non-md and dirs skipped), got %d", len(files))
	}
}

func TestMigrateLegacyMemory_SkipsEmptyFiles(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "empty.md"), []byte("   \n  "), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files (empty md skipped), got %d", len(files))
	}
}

func TestMigrateLegacyMemory_PreservesExistingFrontmatter(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	// File already has frontmatter
	existing := "---\nname: test\n---\noriginal body"
	if err := os.WriteFile(filepath.Join(legacyDir, "prefixed.md"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	// Should NOT be double-wrapped — original frontmatter preserved
	if files[0].Content != existing {
		t.Errorf("content = %q, want %q", files[0].Content, existing)
	}
}

func TestMigrateLegacyMemory_WriteFailureGraceful(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	memDir := long.GetMemoryPath(tmpDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create legacy file
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "fail.md"), []byte("should not appear"), 0644); err != nil {
		t.Fatal(err)
	}

	// Make new dir read-only so WriteFile fails
	if err := os.Chmod(memDir, 0555); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	defer func() { _ = os.Chmod(memDir, 0755) }()

	files := context.LoadMemoryFiles(tmpDir)
	// Migration failed, nothing to scan
	if len(files) != 0 {
		t.Errorf("expected 0 files (write failed gracefully), got %d", len(files))
	}
}

func TestMigrateLegacyMemory_ReadFailSkipped(t *testing.T) {
	setTempHome(t)
	tmpDir := t.TempDir()
	legacyDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(legacyDir, "unreadable.md")
	if err := os.WriteFile(target, []byte("hidden"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	defer func() { _ = os.Chmod(target, 0644) }()

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files (unreadable legacy skipped), got %d", len(files))
	}
}

func TestLoadMemoryFiles_ScanReadDirFails(t *testing.T) {
	setTempHome(t)
	homeDir := os.Getenv("HOME")

	// Create ~/.gbot read-only so EnsureMemoryDir fails to create subdirs
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gbotDir, 0555); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	defer func() { _ = os.Chmod(gbotDir, 0755) }()

	files := context.LoadMemoryFiles(t.TempDir())
	if len(files) != 0 {
		t.Errorf("expected 0 files when memory dir inaccessible, got %d", len(files))
	}
}
