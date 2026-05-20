package context_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

func TestBuildSystemPrompt_Basic(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	result := context.BuildSystemPrompt(tmpDir, nil, "")
	if result == nil {
		t.Fatal("BuildSystemPrompt returned nil")
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if !strings.Contains(promptStr, "You are gbot") {
		t.Error("prompt missing 'You are gbot'")
	}
}

func TestBuildSystemPrompt_WithToolPrompts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"Bash: execute shell commands", "Grep: search file contents"}
	result := context.BuildSystemPrompt(tmpDir, toolPrompts, "")

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if !strings.Contains(promptStr, "Bash: execute shell commands") {
		t.Error("prompt missing first tool prompt")
	}
	if !strings.Contains(promptStr, "Grep: search file contents") {
		t.Error("prompt missing second tool prompt")
	}
}

func TestBuildSystemPrompt_WithSkillListing(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	skillListing := "/commit - create a commit\n/review - review code"
	result := context.BuildSystemPrompt(tmpDir, nil, skillListing)

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if !strings.Contains(promptStr, "## Available Skills") {
		t.Error("prompt missing '## Available Skills' section")
	}
	if !strings.Contains(promptStr, "/commit") {
		t.Error("prompt missing skill listing content")
	}
}

func TestBuildSystemPrompt_AllSections(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"Edit: edit files"}
	skillListing := "/test - run tests"
	result := context.BuildSystemPrompt(tmpDir, toolPrompts, skillListing)
	if result == nil {
		t.Fatal("BuildSystemPrompt returned nil")
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	expectedParts := []string{
		"You are gbot",
		"Edit: edit files",
		"## Available Skills",
		"/test - run tests",
		tmpDir,
		"Platform:",
	}
	for _, part := range expectedParts {
		if !strings.Contains(promptStr, part) {
			t.Errorf("prompt missing expected part: %q", part)
		}
	}
}

func TestBuildSystemPrompt_NonexistentDir(t *testing.T) {
	t.Parallel()
	// Nonexistent dir: LoadGitStatus returns zero-value info, Build() still succeeds.
	result := context.BuildSystemPrompt("/nonexistent/path/that/does/not/exist", nil, "")
	if result == nil {
		t.Fatal("BuildSystemPrompt returned nil for nonexistent dir")
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	// Should still contain the base system prompt.
	if !strings.Contains(promptStr, "You are gbot") {
		t.Error("prompt should contain base system prompt even for nonexistent dir")
	}
}

func TestBuildSystemPrompt_EmptyToolPrompts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Empty strings in toolPrompts slice should be filtered by Builder.
	toolPrompts := []string{"", "valid prompt", ""}
	result := context.BuildSystemPrompt(tmpDir, toolPrompts, "")

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	if !strings.Contains(promptStr, "valid prompt") {
		t.Error("prompt missing valid tool prompt")
	}
}
