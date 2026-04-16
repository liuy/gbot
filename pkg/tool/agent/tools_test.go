package agent

import (
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func TestResolveToolsWildcard(t *testing.T) {
	allTools := makeTestTools("Bash", "Read", "Edit", "Write", "Grep", "Agent")
	def, _ := GetAgentDefinition("General")

	result := ResolveAgentTools(allTools, def)

	// Agent is allowed (gbot supports nested agents)
	if _, ok := result["Agent"]; !ok {
		t.Error("Agent should be kept for General agent")
	}
	if len(result) != 6 {
		t.Errorf("expected 6 tools (all kept), got %d: %v", len(result), mapKeys(result))
	}
}

func TestResolveToolsBlacklist(t *testing.T) {
	allTools := makeTestTools("Bash", "Read", "Edit", "Write", "Grep", "Agent", "NotebookEdit", "ExitPlanMode")
	def, _ := GetAgentDefinition("Explore")

	result := ResolveAgentTools(allTools, def)

	// Explore disallows: ExitPlanMode, Edit, Write, NotebookEdit (Agent allowed)
	disallowed := []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"}
	for _, d := range disallowed {
		if _, ok := result[d]; ok {
			t.Errorf("Explore should disallow %q, but it's in result", d)
		}
	}
	// Should keep Bash, Read, Grep, Agent
	expected := []string{"Bash", "Read", "Grep", "Agent"}
	for _, e := range expected {
		if _, ok := result[e]; !ok {
			t.Errorf("Explore should keep %q, but it's missing", e)
		}
	}
}

func TestResolveToolsWhitelist(t *testing.T) {
	allTools := makeTestTools("Bash", "Read", "Edit", "Write", "Grep")
	// Create a custom agent with explicit whitelist
	def := &types.AgentDefinition{
		AgentType:       "custom",
		Tools:           []string{"Read", "Grep"},
		DisallowedTools: nil,
	}

	result := ResolveAgentTools(allTools, def)

	if len(result) != 2 {
		t.Errorf("expected 2 tools (whitelist), got %d: %v", len(result), mapKeys(result))
	}
	if _, ok := result["Read"]; !ok {
		t.Error("Read should be in whitelist result")
	}
	if _, ok := result["Grep"]; !ok {
		t.Error("Grep should be in whitelist result")
	}
	if _, ok := result["Bash"]; ok {
		t.Error("Bash should NOT be in whitelist result")
	}
}

func TestAllAgentDisallowedToolsStacks(t *testing.T) {
	allTools := makeTestTools("Bash", "Agent", "Read")
	// Even with an explicit whitelist that includes Agent
	def := &types.AgentDefinition{
		AgentType:       "test",
		Tools:           []string{"Bash", "Agent", "Read"},
		DisallowedTools: nil,
	}

	result := ResolveAgentTools(allTools, def)

	// Agent is allowed — AllAgentDisallowedTools is empty
	if _, ok := result["Agent"]; !ok {
		t.Error("Agent should be kept (AllAgentDisallowedTools is empty)")
	}
	if len(result) != 3 {
		t.Errorf("expected 3 tools (whitelist), got %d", len(result))
	}
}

func TestResolveToolsEmptyInput(t *testing.T) {
	allTools := map[string]tool.Tool{}
	def, _ := GetAgentDefinition("General")

	result := ResolveAgentTools(allTools, def)

	if len(result) != 0 {
		t.Errorf("empty input should return empty map, got %d items", len(result))
	}
}

func TestFilterToolsForAgentNilDisallowed(t *testing.T) {
	allTools := makeTestTools("Bash", "Read")
	def := &types.AgentDefinition{
		AgentType:       "test",
		DisallowedTools: nil,
	}

	result := FilterToolsForAgent(allTools, def)

	// No globally disallowed tools — all input tools kept
	if len(result) != 2 {
		t.Errorf("expected 2 tools (no Agent in input), got %d", len(result))
	}
}

func TestIsWildcard(t *testing.T) {
	if !isWildcard([]string{"*"}) {
		t.Error(`["*"] should be wildcard`)
	}
	if isWildcard([]string{"Bash", "Read"}) {
		t.Error(`["Bash","Read"] should not be wildcard`)
	}
	if isWildcard(nil) {
		t.Error("nil should not be wildcard (handled separately)")
	}
	if isWildcard([]string{"*", "Bash"}) {
		t.Error(`["*","Bash"] should not be wildcard`)
	}
}

func mapKeys(m map[string]tool.Tool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
