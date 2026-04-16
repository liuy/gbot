package agent

import (
	"strings"
	"testing"
)

func TestGetAgentDefinition(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantType  string
	}{
		{"General", "General", "General"},
		{"Explore", "Explore", "Explore"},
		{"Plan", "Plan", "Plan"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def, err := GetAgentDefinition(tt.agentType)
			if err != nil {
				t.Fatalf("GetAgentDefinition(%q) returned error: %v", tt.agentType, err)
			}
			if def.AgentType != tt.wantType {
				t.Errorf("AgentType = %q, want %q", def.AgentType, tt.wantType)
			}
			if def.SystemPrompt == nil {
				t.Error("SystemPrompt must not be nil")
			}
			prompt := def.SystemPrompt()
			if prompt == "" {
				t.Error("SystemPrompt() returned empty string")
			}
		})
	}
}

func TestGetAgentDefinition_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input   string
		wantKey string
	}{
		{"explore", "Explore"},
		{"EXPLORE", "Explore"},
		{"plan", "Plan"},
		{"PLAN", "Plan"},
		{"general", "General"},
		{"GENERAL", "General"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			def, err := GetAgentDefinition(tt.input)
			if err != nil {
				t.Fatalf("GetAgentDefinition(%q) should not error, got: %v", tt.input, err)
			}
			if def.AgentType != tt.wantKey {
				t.Errorf("GetAgentDefinition(%q) = %q, want %q", tt.input, def.AgentType, tt.wantKey)
			}
		})
	}
}

func TestGetAgentDefinitionUnknown(t *testing.T) {
	_, err := GetAgentDefinition("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown agent type")
	}
	if !strings.Contains(err.Error(), "unknown agent type") {
		t.Errorf("error should mention 'unknown agent type', got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention the type name, got: %v", err)
	}
}

func TestListAgentDefinitions(t *testing.T) {
	defs := ListAgentDefinitions()
	if len(defs) < 3 {
		t.Errorf("expected at least 3 agent definitions, got %d", len(defs))
	}
	// Verify sorted order
	for i := 1; i < len(defs); i++ {
		if defs[i].AgentType < defs[i-1].AgentType {
			t.Errorf("definitions not sorted: %q before %q", defs[i-1].AgentType, defs[i].AgentType)
		}
	}
}

func TestDefaultAgentType(t *testing.T) {
	def, err := GetAgentDefinition("")
	if err != nil {
		t.Fatalf("empty string should default to General, got error: %v", err)
	}
	if def.AgentType != "General" {
		t.Errorf("empty string should return General, got %q", def.AgentType)
	}
}

func TestGeneralPurposeAgentTools(t *testing.T) {
	def, _ := GetAgentDefinition("General")
	if len(def.Tools) != 1 || def.Tools[0] != "*" {
		t.Errorf("General should have wildcard tools, got %v", def.Tools)
	}
	if def.Model != "inherit" {
		t.Errorf("General model should be 'inherit', got %q", def.Model)
	}
	if def.OmitClaudeMd {
		t.Error("General should not omit CLAUDE.md")
	}
}

func TestExploreAgentDisallowedTools(t *testing.T) {
	def, _ := GetAgentDefinition("Explore")
	expectedDisallowed := []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"}
	if len(def.DisallowedTools) != len(expectedDisallowed) {
		t.Fatalf("Explore should have %d disallowed tools, got %d: %v", len(expectedDisallowed), len(def.DisallowedTools), def.DisallowedTools)
	}
	for _, expected := range expectedDisallowed {
		found := false
		for _, actual := range def.DisallowedTools {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Explore should disallow %q, but it's not in DisallowedTools: %v", expected, def.DisallowedTools)
		}
	}
	if !def.OmitClaudeMd {
		t.Error("Explore should omit CLAUDE.md")
	}
}

func TestPlanAgentDisallowedTools(t *testing.T) {
	def, _ := GetAgentDefinition("Plan")
	expectedDisallowed := []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"}
	if len(def.DisallowedTools) != len(expectedDisallowed) {
		t.Fatalf("Plan should have %d disallowed tools, got %d", len(expectedDisallowed), len(def.DisallowedTools))
	}
}

func TestSystemPromptsNotEmpty(t *testing.T) {
	for _, def := range ListAgentDefinitions() {
		prompt := def.SystemPrompt()
		if len(prompt) < 50 {
			t.Errorf("agent %q system prompt too short (%d chars): %q", def.AgentType, len(prompt), prompt)
		}
	}
}

func TestExploreSystemPromptContainsReadOnly(t *testing.T) {
	def, _ := GetAgentDefinition("Explore")
	prompt := def.SystemPrompt()
	if !strings.Contains(prompt, "READ-ONLY") {
		t.Error("Explore system prompt should mention READ-ONLY")
	}
}

func TestPlanSystemPromptContainsCriticalFiles(t *testing.T) {
	def, _ := GetAgentDefinition("Plan")
	prompt := def.SystemPrompt()
	if !strings.Contains(prompt, "Critical Files for Implementation") {
		t.Error("Plan system prompt should mention 'Critical Files for Implementation'")
	}
}
