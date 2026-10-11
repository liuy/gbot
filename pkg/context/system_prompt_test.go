package context_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
	"github.com/liuy/gbot/pkg/memory/long"
)

// BuildSystemPrompt must wire ProjectDir and SkillListing into the builder;
// dropping either assignment silently drops them from every prompt.
func TestBuildSystemPrompt_Wiring(t *testing.T) {
	setTempHome(t)
	workingDir := t.TempDir()

	got := context.BuildSystemPrompt(workingDir, "/proj/dir", []string{"tool prompt here"}, "- skill-a — does things", "")

	if !strings.Contains(got, "projectspace=/proj/dir") {
		t.Errorf("prompt missing projectspace=/proj/dir; Environment line not wired to ProjectDir")
	}
	if !strings.HasSuffix(got, "\n\n## Available Skills\n\n- skill-a — does things") {
		t.Errorf("prompt must end with the skill listing section, tail=%q", tail(got, 120))
	}
	if strings.Contains(got, "tool prompt here") {
		t.Error("tool prompts must not be embedded in the system prompt")
	}
}

// The memory directory override must reach the typed-memory prompt.
func TestBuildSystemPrompt_TypedMemoryUsesOverride(t *testing.T) {
	setTempHome(t)
	workingDir := t.TempDir()
	override := t.TempDir()
	if err := os.WriteFile(filepath.Join(override, "MEMORY.md"), []byte("- [Note](note.md) — note\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "note.md"), []byte("override note body"), 0644); err != nil {
		t.Fatal(err)
	}

	got := context.BuildSystemPrompt(workingDir, "", nil, "", override)
	typed := long.BuildMemoryPrompt(workingDir, override)
	if typed == "" {
		t.Fatal("expected a typed-memory prompt from the override dir")
	}
	if !strings.Contains(got, typed) {
		t.Errorf("prompt must embed the typed-memory section for the override dir; tail=%q", tail(got, 200))
	}
}

func TestBuildSystemPrompt_Basic(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	result := context.BuildSystemPrompt(tmpDir, "", nil, "", "")
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string")
	}
	if !strings.Contains(result, "{{SYSTEM}}") {
		t.Error("prompt missing {{SYSTEM}} stub")
	}
}

func TestBuildSystemPrompt_ToolPromptsNotInSystemPrompt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"Bash: execute shell commands", "Grep: search file contents"}
	result := context.BuildSystemPrompt(tmpDir, "", toolPrompts, "", "")

	if strings.Contains(result, "Bash: execute shell commands") {
		t.Error("tool prompts must not appear in system prompt")
	}
	if strings.Contains(result, "Grep: search file contents") {
		t.Error("tool prompts must not appear in system prompt")
	}
}

func TestBuildSystemPrompt_WithSkillListing(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	skillListing := "/commit - create a commit\n/review - review code"
	result := context.BuildSystemPrompt(tmpDir, "", nil, skillListing, "")

	if !strings.Contains(result, "## Available Skills") {
		t.Error("prompt missing '## Available Skills' section")
	}
	if !strings.Contains(result, "/commit") {
		t.Error("prompt missing skill listing content")
	}
}

func TestBuildSystemPrompt_AllSections(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	skillListing := "/test - run tests"
	result := context.BuildSystemPrompt(tmpDir, "", nil, skillListing, "")
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string")
	}

	expectedParts := []string{
		"{{SYSTEM}}",
		"## Available Skills",
		"/test - run tests",
		"Runtime:",
		"model={{MODEL}}",
	}
	for _, part := range expectedParts {
		if !strings.Contains(result, part) {
			t.Errorf("prompt missing expected part: %q", part)
		}
	}
}

func TestBuildSystemPrompt_NonexistentDir(t *testing.T) {
	t.Parallel()
	result := context.BuildSystemPrompt("/nonexistent/path/that/does/not/exist", "", nil, "", "")
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string for nonexistent dir")
	}
	if !strings.Contains(result, "{{SYSTEM}}") {
		t.Error("prompt should contain {{SYSTEM}} stub even for nonexistent dir")
	}
}

func TestBuildSystemPrompt_EmptyToolPrompts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"", "valid prompt", ""}
	result := context.BuildSystemPrompt(tmpDir, "", toolPrompts, "", "")

	if strings.Contains(result, "valid prompt") {
		t.Error("tool prompts must not appear in system prompt")
	}
}
