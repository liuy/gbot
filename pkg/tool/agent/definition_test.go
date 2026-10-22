package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// Note: Plan/Executor/Reviewer agents ship as bundled markdown under
// pkg/tool/agent/bundled/ and are only visible through the Loader. The
// tests here cover the hardcoded General/Explore agents only.

func TestGetAgentDefinition(t *testing.T) {
	tests := []struct {
		name      string
		agentType string
		wantType  string
	}{
		{"General", "General", "General"},
		{"Explore", "Explore", "Explore"},
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

func TestSetDefinitionOverrideForTest(t *testing.T) {
	probe := &types.AgentDefinition{AgentType: "SeamProbe", WhenToUse: "probe"}
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe": probe})
	t.Cleanup(func() { SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe": nil}) })

	def, err := GetAgentDefinition("SeamProbe")
	if err != nil {
		t.Fatalf("GetAgentDefinition with override: %v", err)
	}
	if def != probe {
		t.Errorf("override lookup returned %p, want the injected definition %p", def, probe)
	}

	// The override must beat an initialized global loader too: the loader
	// reads the host user's agent dirs, so a test that relies on it would
	// depend on machine state.
	prev := globalLoader
	globalLoader = NewLoader(t.TempDir())
	t.Cleanup(func() { globalLoader = prev })
	def, err = GetAgentDefinition("SeamProbe")
	if err != nil || def != probe {
		t.Errorf("override must win over the global loader: def=%p err=%v, want %p nil", def, err, probe)
	}

	// Merge semantics: a second injection under a different key must not
	// clobber the first — parallel tests inject concurrently.
	other := &types.AgentDefinition{AgentType: "SeamProbe2", WhenToUse: "probe2"}
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe2": other})
	t.Cleanup(func() { SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe2": nil}) })
	if def, err = GetAgentDefinition("SeamProbe"); err != nil || def != probe {
		t.Errorf("later injection clobbered the earlier key: def=%p err=%v, want %p nil", def, err, probe)
	}
	if def, err = GetAgentDefinition("SeamProbe2"); err != nil || def != other {
		t.Errorf("merged key not resolvable: def=%p err=%v, want %p nil", def, err, other)
	}

	// Removing the key with a nil value must restore the normal lookup.
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe": nil})
	if _, err = GetAgentDefinition("SeamProbe"); err == nil {
		t.Error("removed override still resolved; a nil-valued key must restore the normal lookup")
	}
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbe": probe})
}

// TestSetDefinitionOverrideForTest_NilRemovalProcessesEveryKey pins that one
// merge call removes every nil-valued key: a loop that stops at the first
// removal strands the other key in either map-iteration order, so the
// per-key cleanup pattern stays sound when one call removes several keys.
func TestSetDefinitionOverrideForTest_NilRemovalProcessesEveryKey(t *testing.T) {
	first := &types.AgentDefinition{AgentType: "SeamProbeR1", WhenToUse: "r"}
	second := &types.AgentDefinition{AgentType: "SeamProbeR2", WhenToUse: "r"}
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbeR1": first})
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"SeamProbeR2": second})
	t.Cleanup(func() {
		SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{
			"SeamProbeR1": nil, "SeamProbeR2": nil,
		})
	})

	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{
		"SeamProbeR1": nil, "SeamProbeR2": nil,
	})
	for _, agentType := range []string{"SeamProbeR1", "SeamProbeR2"} {
		if def, err := GetAgentDefinition(agentType); err == nil || def != nil {
			t.Errorf("GetAgentDefinition(%q) after nil-removal = (%p, %v), want removal of every key in the call", agentType, def, err)
		}
	}
}

func TestSetDefinitionOverrideForTest_EmptyTypeUsesGeneralKey(t *testing.T) {
	probe := &types.AgentDefinition{AgentType: "General", WhenToUse: "probe"}
	SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"General": probe})
	t.Cleanup(func() { SetDefinitionOverrideForTest(map[string]*types.AgentDefinition{"General": nil}) })

	// The empty-string type is normalized to "General" before the override
	// lookup, so an override keyed by "General" must catch it.
	def, err := GetAgentDefinition("")
	if err != nil {
		t.Fatalf("GetAgentDefinition(\"\") with General override: %v", err)
	}
	if def != probe {
		t.Error("empty agent type did not resolve through the General-keyed override")
	}
}

func TestGetAgentDefinition_CaseInsensitive(t *testing.T) {
	tests := []struct {
		input   string
		wantKey string
	}{
		{"explore", "Explore"},
		{"EXPLORE", "Explore"},
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
	// Only General and Explore are hardcoded now; Plan/Executor/Reviewer
	// come from bundled markdown and require Loader initialization.
	if len(defs) < 2 {
		t.Errorf("expected at least 2 hardcoded agent definitions, got %d", len(defs))
	}
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
		found := slices.Contains(def.DisallowedTools, expected)
		if !found {
			t.Errorf("Explore should disallow %q, but it's not in DisallowedTools: %v", expected, def.DisallowedTools)
		}
	}
	if !def.OmitClaudeMd {
		t.Error("Explore should omit CLAUDE.md")
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

func TestIsOneShotAgent(t *testing.T) {
	tests := []struct {
		agentType string
		want      bool
	}{
		{"Explore", true},
		{"Plan", true},
		{"Planner", true},
		{"General", false},
		{"Executor", false},
		{"Reviewer", false},
		{"", false},
		{"nonexistent", false},
	}
	for _, tt := range tests {
		t.Run(tt.agentType, func(t *testing.T) {
			if got := IsOneShotAgent(tt.agentType); got != tt.want {
				t.Errorf("IsOneShotAgent(%q) = %v, want %v", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestBuiltInAgentsHaveSourceField(t *testing.T) {
	for _, def := range ListAgentDefinitions() {
		if def.Source != types.AgentSourceBuiltIn {
			t.Errorf("built-in agent %q Source = %v, want AgentSourceBuiltIn", def.AgentType, def.Source)
		}
		if def.BaseDir != "built-in" {
			t.Errorf("built-in agent %q BaseDir = %q, want 'built-in'", def.AgentType, def.BaseDir)
		}
	}
}

func TestListAgentDefinitions_LoaderPathSorted(t *testing.T) {
	// The Agent tool's prompt embeds this list; an unstable order changes the
	// system prompt between process runs and breaks the engine's prompt-cache
	// prefix on every restart. The fallback path sorts; the loader path must
	// too (its cached order is filesystem load order).
	l := NewLoader(t.TempDir())
	l.cached = []*types.AgentDefinition{
		{AgentType: "Zeta", WhenToUse: "z"},
		{AgentType: "Alpha", WhenToUse: "a"},
		{AgentType: "Mid", WhenToUse: "m"},
	}
	// Unlocked on purpose: nothing else can reach this loader until the
	// globalLoader swap below publishes it, so "loaded is only touched under the
	// write lock" stays mechanically checkable in loader.go.
	l.loaded = true // mark loaded: keep the hand-seeded order
	prev := globalLoader
	globalLoader = l
	t.Cleanup(func() { globalLoader = prev })

	defs := ListAgentDefinitions()
	var got []string
	for _, d := range defs {
		got = append(got, d.AgentType)
	}
	want := []string{"Alpha", "Mid", "Zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("definitions[%d] = %q, want %q (loader path must be sorted)", i, got[i], want[i])
		}
	}
}
