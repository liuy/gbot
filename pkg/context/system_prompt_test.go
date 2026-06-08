package context_test

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

func TestBuildSystemPrompt_Basic(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	result := context.BuildSystemPrompt(tmpDir, nil, "")
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string")
	}
	if !strings.Contains(result, "You are gbot") {
		t.Error("prompt missing 'You are gbot'")
	}
}

func TestBuildSystemPrompt_ToolPromptsNotInSystemPrompt(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"Bash: execute shell commands", "Grep: search file contents"}
	result := context.BuildSystemPrompt(tmpDir, toolPrompts, "")

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
	result := context.BuildSystemPrompt(tmpDir, nil, skillListing)

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
	result := context.BuildSystemPrompt(tmpDir, nil, skillListing)
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string")
	}

	expectedParts := []string{
		"You are gbot",
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
	result := context.BuildSystemPrompt("/nonexistent/path/that/does/not/exist", nil, "")
	if result == "" {
		t.Fatal("BuildSystemPrompt returned empty string for nonexistent dir")
	}
	if !strings.Contains(result, "You are gbot") {
		t.Error("prompt should contain base system prompt even for nonexistent dir")
	}
}

func TestBuildSystemPrompt_EmptyToolPrompts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	toolPrompts := []string{"", "valid prompt", ""}
	result := context.BuildSystemPrompt(tmpDir, toolPrompts, "")

	if strings.Contains(result, "valid prompt") {
		t.Error("tool prompts must not appear in system prompt")
	}
}
