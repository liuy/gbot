package app

import (
	"testing"

	"github.com/liuy/gbot/pkg/memory/short"
)

func TestPlanRestore_NilMeta(t *testing.T) {
	engines, activeID := planRestore(nil, "glm-5.2")
	if len(engines) != 1 {
		t.Fatalf("len(engines) = %d, want 1", len(engines))
	}
	em := engines[0]
	if em.ID != "main" || em.Name != "main" {
		t.Errorf("engine = %+v, want id/name=main", em)
	}
	if em.Model != "glm-5.2" {
		t.Errorf("Model = %q, want glm-5.2", em.Model)
	}
	if em.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID = %q, want empty (first run)", em.ActiveSessionID)
	}
	if activeID != "main" {
		t.Errorf("activeID = %q, want main", activeID)
	}
}

func TestPlanRestore_EmptyEngines(t *testing.T) {
	meta := &short.WorkspaceMeta{CurrentSessionID: "legacy-abc"}
	engines, activeID := planRestore(meta, "glm-5.2")
	if len(engines) != 1 || engines[0].ID != "main" {
		t.Fatalf("engines = %+v, want one synthesized main", engines)
	}
	if activeID != "main" {
		t.Errorf("activeID = %q, want main", activeID)
	}
}

// The real-data shape from ~/.gbot/projects/-home-yliu-repos-gbot/meta.json:
// legacy e-N ids and legacy display names that collide with the agent-N id
// grammar must survive boot byte-identical. Nothing is rewritten — not the
// ids, not the names, not the sessions.engine_id rows, not the history files.
func TestPlanRestore_KeepsLegacyIDsAndNamesVerbatim(t *testing.T) {
	meta := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5"},
			{ID: "e2", Name: "e2", Model: "zhipu/glm-5"},
			{ID: "e3", Name: "agent-3", Model: "zhipu/glm-5"},
			{ID: "e4", Name: "agent-5", Model: "zhipu/glm-5"},
			{ID: "e5", Name: "agent-6", Model: "zhipu/glm-5"},
			{ID: "wechat-acct@host", Name: "wechat-acct@host", Model: "zhipu/glm-5"},
		},
		ActiveEngineID: "e4",
	}
	engines, activeID := planRestore(meta, "glm-5.2")

	wantIDs := []string{"main", "e2", "e3", "e4", "e5", "wechat-acct@host"}
	wantNames := []string{"main", "e2", "agent-3", "agent-5", "agent-6", "wechat-acct@host"}
	for i := range wantIDs {
		if engines[i].ID != wantIDs[i] {
			t.Errorf("engines[%d].ID = %q, want %q verbatim", i, engines[i].ID, wantIDs[i])
		}
		if engines[i].Name != wantNames[i] {
			t.Errorf("engines[%d].Name = %q, want %q verbatim", i, engines[i].Name, wantNames[i])
		}
	}
	if activeID != "e4" {
		t.Errorf("activeID = %q, want e4 (the persisted id, not a remapped one)", activeID)
	}
}

// An engine list already in the agent-N namespace is likewise untouched.
func TestPlanRestore_KeepsAgentNIDsVerbatim(t *testing.T) {
	meta := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main"},
			{ID: "agent-1", Name: "agent-1"},
			{ID: "agent-2", Name: "research"},
		},
		ActiveEngineID: "agent-2",
	}
	engines, activeID := planRestore(meta, "glm")
	if len(engines) != 3 {
		t.Fatalf("len(engines) = %d, want 3", len(engines))
	}
	for i, want := range []string{"main", "agent-1", "agent-2"} {
		if engines[i].ID != want {
			t.Errorf("engines[%d].ID = %q, want %q untouched", i, engines[i].ID, want)
		}
	}
	if engines[1].Name != "agent-1" || engines[2].Name != "research" {
		t.Errorf("names = [%q, %q], want [agent-1, research] untouched", engines[1].Name, engines[2].Name)
	}
	if activeID != "agent-2" {
		t.Errorf("activeID = %q, want agent-2", activeID)
	}
}

func TestPlanRestore_EmptyActiveID_DefaultsToMain(t *testing.T) {
	meta := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", ActiveSessionID: "s1", Model: "glm"},
		},
		// ActiveEngineID intentionally empty — edge case from hand-edited meta.json
	}
	_, activeID := planRestore(meta, "glm")
	if activeID != "main" {
		t.Errorf("activeID = %q, want main (default when ActiveEngineID empty)", activeID)
	}
}

func TestAgentIDSuffix(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"agent-1", 1},
		{"agent-42", 42},
		{"main", 0},
		{"e2", 0},
		{"agent-", 0},
		{"agent-x", 0},
		{"agent-1x", 0},
		{"xagent-1", 0},
		// A hand-edited meta with an absurd suffix must not seed a garbage
		// counter: Atoi overflows and the value is dropped, not clamped.
		{"agent-99999999999999999999", 0},
	}
	for _, tc := range cases {
		if got := agentIDSuffix(tc.in); got != tc.want {
			t.Errorf("agentIDSuffix(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestMaxAgentIDSuffix(t *testing.T) {
	cases := []struct {
		name    string
		engines []short.EngineMeta
		want    int
	}{
		{"empty", nil, 0},
		{"no agent-N anywhere", []short.EngineMeta{{ID: "main", Name: "main"}, {ID: "e2", Name: "e2"}}, 0},
		{"id only", []short.EngineMeta{{ID: "agent-5", Name: "research"}}, 5},
		{"name only", []short.EngineMeta{{ID: "e2", Name: "agent-3"}}, 3},
		{"max over ids and names", []short.EngineMeta{{ID: "agent-2", Name: "agent-2"}, {ID: "e7", Name: "agent-9"}}, 9},
		{"descending order keeps the max", []short.EngineMeta{{ID: "agent-9", Name: "agent-9"}, {ID: "agent-2", Name: "agent-2"}}, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := maxAgentIDSuffix(tc.engines); got != tc.want {
				t.Errorf("maxAgentIDSuffix(%+v) = %d, want %d", tc.engines, got, tc.want)
			}
		})
	}
}
