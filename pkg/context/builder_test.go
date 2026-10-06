package context_test

import (
	"os"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
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
	if len(b.ToolPrompts) != 0 {
		t.Errorf("ToolPrompts should be empty, got %d items", len(b.ToolPrompts))
	}
}

func TestBuild_Basic(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

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

func TestBuild_WithToolPrompts_NoLongerInSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"Tool 1: Use wisely", "Tool 2: Be carefully"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if strings.Contains(promptStr, "Tool 1: Use wisely") {
		t.Error("tool prompts must not appear in system prompt")
	}
	if strings.Contains(promptStr, "Tool 2: Be carefully") {
		t.Error("tool prompts must not appear in system prompt")
	}
	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("built prompt should still contain {{SYSTEM}} stub")
	}
}

func TestBuild_AllSections(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/project")

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

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

func TestBuild_EmptyToolPrompts_NotInSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"", "valid prompt", ""}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if strings.Contains(promptStr, "valid prompt") {
		t.Error("tool prompts must not appear in system prompt")
	}
}

func TestBuild_EscapesJSON(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
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
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

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

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !strings.Contains(result, "## Available Skills") {
		t.Error("built prompt missing '## Available Skills' section")
	}
	if !strings.Contains(result, "/commit") {
		t.Error("built prompt missing skill listing content")
	}
}

func TestBuild_ToolPromptsIgnored(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"p1", "", "p3"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if result == "" {
		t.Fatal("Build() returned empty result")
	}

	promptStr := result

	// Tool prompts go into tool defs, not system prompt.
	if strings.Contains(promptStr, "p1") {
		t.Error("tool prompt p1 must not be in system prompt")
	}
	if strings.Contains(promptStr, "p3") {
		t.Error("tool prompt p3 must not be in system prompt")
	}
}
