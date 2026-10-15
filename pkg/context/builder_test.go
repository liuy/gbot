package context_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/memory/long"
)

func TestNewBuilder(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	if b == nil {
		t.Fatal("NewBuilder returned nil")
	}
	if b.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q, want %q", b.WorkingDir, "/work")
	}
	if b.MaxTokens != 100000 {
		t.Errorf("MaxTokens = %d, want 100000", b.MaxTokens)
	}
}

// The typed-memory section is appended after a blank-line separator; the
// MemoryFiles fallback only fires when there is no typed prompt.
func TestBuild_TypedMemorySeparator(t *testing.T) {
	setTempHome(t)
	workingDir := t.TempDir()
	memDir := long.GetMemoryPath(workingDir)
	if err := os.MkdirAll(memDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "note.md"), []byte("typed note"), 0644); err != nil {
		t.Fatal(err)
	}

	b := context.NewBuilder(workingDir)
	// The fallback section must not be reachable while a typed prompt exists.
	b.MemoryFiles = []context.MemoryFile{{Path: "/fallback/only.md", Content: "fallback only"}}

	got := b.Build()
	typed := long.BuildMemoryPrompt(workingDir, "")
	if typed == "" {
		t.Fatal("expected a non-empty typed-memory prompt with auto memory enabled")
	}
	if !strings.HasSuffix(got, "\n\n"+typed) {
		t.Errorf("Build must append the typed-memory prompt after a blank line; tail=%q", truncateForTest(got, 200))
	}
	if strings.Contains(got, "fallback only") {
		t.Error("MemoryFiles section must not be emitted when the typed-memory prompt is present")
	}
}

// With typed memory off, the MemoryFiles section is appended verbatim —
// including its leading "\n\n## Memory" header.
func TestBuild_MemoryFilesSectionExact(t *testing.T) {
	t.Setenv("GBOT_AUTO_MEMORY_ENABLED", "0")

	b := context.NewBuilder("/work")
	b.MemoryFiles = []context.MemoryFile{
		{Path: "/tmp/.gbot/memory/notes.md", Content: "Use strict mode"},
	}

	got := b.Build()
	want := "\n\n## Memory\n\n- [notes.md](/tmp/.gbot/memory/notes.md)\nUse strict mode\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("Build tail = %q, want %q", tail(got, len(want)+40), want)
	}
}

func TestBuild_SkillListingExact(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.SkillListing = "- build — runs make check"

	got := b.Build()
	want := "\n\n## Available Skills\n\n- build — runs make check"
	if !strings.HasSuffix(got, want) {
		t.Errorf("Build tail = %q, want %q", tail(got, len(want)+40), want)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestBuild_Basic(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result := b.Build()

	promptStr := result

	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("built prompt missing {{SYSTEM}} stub")
	}
	if !strings.Contains(promptStr, "{{SOUL}}") {
		t.Error("built prompt missing {{SOUL}} stub")
	}
	if !strings.Contains(promptStr, "Runtime:") {
		t.Error("built prompt missing runtime info")
	}
}

func TestBuild_AllSections(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/project")

	result := b.Build()

	promptStr := result

	expectedParts := []string{
		"{{SYSTEM}}",
		"/project",
	}

	for _, part := range expectedParts {
		if !strings.Contains(promptStr, part) {
			t.Errorf("built prompt missing expected part: %q", part)
		}
	}
}

func TestBuild_EscapesJSON(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result := b.Build()
	if result == "" {
		t.Error("Build() returned empty string")
	}
}

func TestRuntimeInfo(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/test/dir")
	info := b.RuntimeInfo()

	if !strings.Contains(info, "host=") {
		t.Error("runtime info missing host=")
	}
	if !strings.Contains(info, "os=") {
		t.Error("runtime info missing os=")
	}
	if !strings.Contains(info, "go=") {
		t.Error("runtime info missing go=")
	}
	if !strings.Contains(info, "shell=") {
		t.Error("runtime info missing shell=")
	}
	if !strings.Contains(info, "model={{MODEL}}") {
		t.Error("runtime info missing model={{MODEL}}")
	}
}

func TestPlatformInfo_EmptyShell(t *testing.T) {
	t.Parallel()
	origShell := os.Getenv("SHELL")
	_ = os.Setenv("SHELL", "")
	defer func() { _ = os.Setenv("SHELL", origShell) }()

	b := context.NewBuilder("/test")
	info := b.RuntimeInfo()
	if !strings.Contains(info, "/bin/bash") {
		t.Errorf("expected /bin/bash fallback, got %q", info)
	}
}

func TestBaseSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	prompt := b.BaseSystemPrompt()
	if !strings.Contains(prompt, "{{SYSTEM}}") {
		t.Error("base prompt should contain {{SYSTEM}} stub")
	}
}

func TestDefaultBasePrompt(t *testing.T) {
	t.Parallel()
	prompt := context.DefaultBasePrompt()
	if !strings.Contains(prompt, "You are creature") {
		t.Error("default base prompt missing identity")
	}
	if strings.Contains(prompt, "{{SOUL}}") {
		t.Error("DefaultBasePrompt should NOT contain {{SOUL}} — it's added by builder")
	}
	if !strings.Contains(prompt, "# Code style") {
		t.Error("default base prompt missing Code style section")
	}
}

func TestBuild_NoFiles(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result := b.Build()

	promptStr := result

	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("prompt should contain {{SYSTEM}} stub")
	}
	if !strings.Contains(promptStr, "{{SOUL}}") {
		t.Error("prompt should contain {{SOUL}} stub")
	}
	if !strings.Contains(promptStr, "Runtime:") {
		t.Error("prompt should contain runtime info even without files")
	}
}

func TestBuild_WithSkillListing(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.SkillListing = "/commit - create a commit\n/review - review code"

	result := b.Build()

	if !strings.Contains(result, "## Available Skills") {
		t.Error("built prompt missing '## Available Skills' section")
	}
	if !strings.Contains(result, "/commit") {
		t.Error("built prompt missing skill listing content")
	}
}
