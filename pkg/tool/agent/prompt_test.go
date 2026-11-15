package agent

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestFormatAgentList_NoFilter(t *testing.T) {
	defs := []*types.AgentDefinition{
		{AgentType: "General"},
		{AgentType: "Explore"},
	}
	result := formatAgentList(defs, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}

func TestFormatAgentList_WithFilter(t *testing.T) {
	defs := []*types.AgentDefinition{
		{AgentType: "General"},
		{AgentType: "Explore"},
	}
	result := formatAgentList(defs, []string{"General"})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0].AgentType != "General" {
		t.Errorf("expected General, got %q", result[0].AgentType)
	}
}

func TestFormatAgentList_EmptyFilter(t *testing.T) {
	defs := []*types.AgentDefinition{
		{AgentType: "General"},
	}
	result := formatAgentList(defs, []string{})
	if len(result) != 1 {
		t.Fatalf("empty filter should return all, got %d", len(result))
	}
}

func TestGetToolsDescription_AllTools(t *testing.T) {
	def := &types.AgentDefinition{}
	result := getToolsDescription(def)
	if result != "All tools" {
		t.Errorf("expected 'All tools', got %q", result)
	}
}

func TestGetToolsDescription_Allowlist(t *testing.T) {
	def := &types.AgentDefinition{
		Tools: []string{"Read", "Grep"},
	}
	result := getToolsDescription(def)
	if result != "Read, Grep" {
		t.Errorf("expected 'Read, Grep', got %q", result)
	}
}

func TestGetToolsDescription_Denylist(t *testing.T) {
	def := &types.AgentDefinition{
		DisallowedTools: []string{"Edit", "Write"},
	}
	result := getToolsDescription(def)
	if !strings.Contains(result, "All tools except") {
		t.Errorf("expected 'All tools except ...', got %q", result)
	}
}

func TestGetToolsDescription_AllowAndDeny(t *testing.T) {
	def := &types.AgentDefinition{
		Tools:           []string{"Read", "Grep", "Edit"},
		DisallowedTools: []string{"Edit"},
	}
	result := getToolsDescription(def)
	if result != "Read, Grep" {
		t.Errorf("expected 'Read, Grep', got %q", result)
	}
}

func TestGetToolsDescription_AllowAndDeny_AllDenied(t *testing.T) {
	def := &types.AgentDefinition{
		Tools:           []string{"Edit"},
		DisallowedTools: []string{"Edit"},
	}
	result := getToolsDescription(def)
	if result != "None" {
		t.Errorf("expected 'None' when all tools denied, got %q", result)
	}
}

func TestFormatAgentLine_InPromptTest(t *testing.T) {
	def := &types.AgentDefinition{
		AgentType: "Explore",
		WhenToUse: "Find files",
		Tools:     []string{"Read"},
	}
	line := FormatAgentLine(def)
	if !strings.Contains(line, "Explore") {
		t.Errorf("line should contain agent type: %q", line)
	}
	if !strings.Contains(line, "Find files") {
		t.Errorf("line should contain WhenToUse: %q", line)
	}
}

func TestAgentPrompt(t *testing.T) {
	prompt := agentPrompt()
	if prompt == "" {
		t.Error("agentPrompt should not be empty")
	}
	if !strings.Contains(prompt, "Launch a new agent") {
		t.Error("agentPrompt should contain launch instructions")
	}
}

func TestAgentPrompt_WithAllowedTypes(t *testing.T) {
	prompt := AgentPrompt([]string{"Explore"})
	if !strings.Contains(prompt, "Explore") {
		t.Error("filtered prompt should contain Explore")
	}
	if strings.Contains(prompt, "General") {
		t.Error("filtered prompt should NOT contain General")
	}
}

func TestAgentBoilerplatePrompt_ZeroAgentForm(t *testing.T) {
	bp := AgentBoilerplatePrompt()
	if bp == "" {
		t.Fatal("AgentBoilerplatePrompt should not be empty")
	}
	if !strings.Contains(bp, "Launch a new agent") {
		t.Error("boilerplate should carry the tool instructions")
	}
	// The zero-agent form is the template with an empty list: the header is
	// immediately followed by the blank separator the empty %s leaves.
	if !strings.Contains(bp, "Available agent types and the tools they have access to:\n\n\n") {
		t.Error("boilerplate should splice an empty agent list into the header")
	}
	// AgentPrompt always reads the registry (built-in agents at minimum), so
	// no registered definition's line may survive in the boilerplate.
	for _, def := range ListAgentDefinitions() {
		if line := FormatAgentLine(def); strings.Contains(bp, line) {
			t.Errorf("boilerplate carries agent line %q", line)
		}
	}
	if bp == AgentPrompt(nil) {
		t.Error("boilerplate must differ from the registry-filled prompt")
	}
}
