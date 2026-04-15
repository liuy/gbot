package context_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

func TestLoadMemoryFiles_Empty(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 0 {
		t.Errorf("expected 0 files with empty dir, got %d", len(files))
	}
}

func TestLoadMemoryFiles_SingleFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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

func TestLoadMemoryFiles_SkipsDuplicates(t *testing.T) {
	// NOT parallel: modifies HOME env var
	// Create a home dir and a working dir under it so they overlap
	homeDir := filepath.Join(t.TempDir(), "home")
	workDir := filepath.Join(homeDir, "project")
	memDir := filepath.Join(homeDir, ".gbot", "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "deduplicated content"
	if err := os.WriteFile(filepath.Join(memDir, "shared.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Set HOME so LoadMemoryFiles scans both home and working dir
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// workingDir is under homeDir, so the same file appears in both scans
	files := context.LoadMemoryFiles(workDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (deduplicated), got %d", len(files))
	}
	if files[0].Content != content {
		t.Errorf("expected %q, got %q", content, files[0].Content)
	}
}

func TestLoadMemoryFiles_ReadFileError(t *testing.T) {
	t.Parallel()
	// Create a file and make it unreadable via chmod 0000.
	// ReadDir will see it but ReadFile will fail with permission denied.
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
	// Pass a path that doesn't exist at all — os.ReadDir fails, continues
	files := context.LoadMemoryFiles("/nonexistent/path/that/does/not/exist")
	if len(files) != 0 {
		t.Errorf("expected 0 files for nonexistent dir, got %d", len(files))
	}
}

func TestLoadMemoryFiles_DeduplicationAcrossDirs(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(memDir, "shared.md"), []byte("shared content"), 0644); err != nil {
		t.Fatal(err)
	}

	// LoadMemoryFiles scans both workingDir/.gbot/memory and ~/.gbot/memory.
	// With tmpDir, only tmpDir/.gbot/memory exists — should load exactly once.
	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Content != "shared content" {
		t.Errorf("expected 'shared content', got %q", files[0].Content)
	}
}

func TestLoadMemoryFiles_SeenDedup(t *testing.T) {
	// NOT parallel: modifies HOME env var
	// When workingDir == homeDir, the same memory dir is scanned twice.
	// The seen map should prevent duplicate entries.
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}

	content := "seen dedup content"
	if err := os.WriteFile(filepath.Join(memDir, "unique.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Set HOME to tmpDir so both scans hit the same memory dir
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", tmpDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	files := context.LoadMemoryFiles(tmpDir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (seen dedup), got %d", len(files))
	}
	if files[0].Content != content {
		t.Errorf("expected %q, got %q", content, files[0].Content)
	}
}

func TestLoadMemoryFiles_FilepathAbsError(t *testing.T) {
	t.Parallel()
	// filepath.Abs on an already-absolute path never errors — it just
	// returns the path. This test creates a scenario where LoadMemoryFiles
	// processes a file to ensure the happy path through filepath.Abs is
	// covered. The error path at memory.go:48-49 is structurally unreachable
	// since filepath.Join produces absolute paths from memoryDirs().
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	// Verify the path is absolute
	if !filepath.IsAbs(files[0].Path) {
		t.Errorf("expected absolute path, got %q", files[0].Path)
	}
}

func TestLoadMemoryFiles_MarkdownExtensions(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	memDir := filepath.Join(tmpDir, ".gbot", "memory")
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
	t.Parallel()
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
