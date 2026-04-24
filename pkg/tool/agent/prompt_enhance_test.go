package agent

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

func TestEnhanceSystemPrompt_FallbackOnEmpty(t *testing.T) {
	result := enhanceSystemPrompt("", nil, "/tmp", false, "")
	if !strings.Contains(result, defaultAgentPrompt) {
		t.Error("expected defaultAgentPrompt fallback when basePrompt is empty")
	}
}

func TestEnhanceSystemPrompt_UsesCustomPrompt(t *testing.T) {
	result := enhanceSystemPrompt("Custom agent prompt", nil, "/tmp", false, "")
	if !strings.Contains(result, "Custom agent prompt") {
		t.Error("expected custom prompt to be used")
	}
	if strings.Contains(result, defaultAgentPrompt) {
		t.Error("default prompt should NOT appear when custom is provided")
	}
}

func TestEnhanceSystemPrompt_ContainsNotes(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
	if !strings.Contains(result, "absolute file paths") {
		t.Error("expected notes about absolute paths")
	}
	if !strings.Contains(result, "avoid using emojis") {
		t.Error("expected notes about emojis")
	}
	if !strings.Contains(result, "Do not use a colon before tool calls") {
		t.Error("expected notes about colons")
	}
}

func TestEnhanceSystemPrompt_ContainsEnvBlock(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/home/user/project", true, "sonnet")
	if !strings.Contains(result, "<env>") {
		t.Error("expected <env> block")
	}
	if !strings.Contains(result, "Working directory: /home/user/project") {
		t.Error("expected working directory in env block")
	}
	if !strings.Contains(result, "Is directory a git repo: Yes") {
		t.Error("expected isGit=Yes")
	}
	if !strings.Contains(result, "You are powered by the model sonnet") {
		t.Error("expected model name")
	}
}

func TestEnhanceSystemPrompt_NotGitRepo(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
	if !strings.Contains(result, "Is directory a git repo: No") {
		t.Error("expected isGit=No")
	}
}

func TestEnhanceSystemPrompt_NoModel(t *testing.T) {
	result := enhanceSystemPrompt("test", nil, "/tmp", false, "")
	if strings.Contains(result, "You are powered by the model") {
		t.Error("model line should not appear when model is empty")
	}
}

func TestEnhanceSystemPrompt_ToolNames(t *testing.T) {
	tools := map[string]tool.Tool{
		"Grep": &mockTool{name: "Grep"},
		"Read": &mockTool{name: "Read"},
		"Bash": &mockTool{name: "Bash"},
	}
	result := enhanceSystemPrompt("test", tools, "/tmp", false, "")
	if !strings.Contains(result, "Enabled tools:") {
		t.Error("expected Enabled tools section")
	}
	if !strings.Contains(result, "- Bash") {
		t.Error("expected Bash in tool list")
	}
	if !strings.Contains(result, "- Grep") {
		t.Error("expected Grep in tool list")
	}
	if !strings.Contains(result, "- Read") {
		t.Error("expected Read in tool list")
	}
}

func TestFormatToolNamesList_Empty(t *testing.T) {
	if got := formatToolNamesList(nil); got != "" {
		t.Errorf("expected empty string for nil tools, got %q", got)
	}
}

func TestFormatToolNamesList_Sorted(t *testing.T) {
	tools := map[string]tool.Tool{
		"Zebra":  &mockTool{name: "Zebra"},
		"Alpha":  &mockTool{name: "Alpha"},
		"Middle": &mockTool{name: "Middle"},
	}
	got := formatToolNamesList(tools)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0] != "- Alpha" {
		t.Errorf("expected first tool Alpha, got %q", lines[0])
	}
	if lines[1] != "- Middle" {
		t.Errorf("expected second tool Middle, got %q", lines[1])
	}
	if lines[2] != "- Zebra" {
		t.Errorf("expected third tool Zebra, got %q", lines[2])
	}
}
