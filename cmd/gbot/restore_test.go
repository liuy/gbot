package main

import (
	"testing"

	"github.com/liuy/gbot/pkg/memory/short"
)

func TestPlanRestore_NilMeta_CreatesMainEngine(t *testing.T) {
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

func TestPlanRestore_EmptyEnginesArray_CreatesMainEngine(t *testing.T) {
	// Edge case: meta exists but Engines array is empty (corrupted file or
	// future schema drift). Should behave like first run.
	meta := &short.WorkspaceMeta{
		CurrentSessionID: "legacy-abc",
	}
	engines, activeID := planRestore(meta, "glm-5.2")
	if len(engines) != 1 {
		t.Fatalf("len(engines) = %d, want 1 (synthesize main)", len(engines))
	}
	if engines[0].ID != "main" {
		t.Errorf("engine.ID = %q, want main", engines[0].ID)
	}
	if activeID != "main" {
		t.Errorf("activeID = %q, want main", activeID)
	}
}

func TestPlanRestore_MultiEngineMeta_HonorsActiveID(t *testing.T) {
	meta := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", ActiveSessionID: "sess-1", Model: "glm-5.2"},
			{ID: "e2", Name: "engine-2", ActiveSessionID: "sess-2", Model: "deepseek-v4"},
		},
		ActiveEngineID: "e2",
	}
	engines, activeID := planRestore(meta, "glm-5.2")
	if len(engines) != 2 {
		t.Fatalf("len(engines) = %d, want 2", len(engines))
	}
	if activeID != "e2" {
		t.Errorf("activeID = %q, want e2 (honored ActiveEngineID)", activeID)
	}
	// Engines array must be passed through unchanged — restoreEngines
	// iterates it and calls factory per entry.
	if engines[0].ID != "main" || engines[1].ID != "e2" {
		t.Errorf("engine order = [%s, %s], want [main, e2]", engines[0].ID, engines[1].ID)
	}
	if engines[0].ActiveSessionID != "sess-1" {
		t.Errorf("engine[0].ActiveSessionID = %q, want sess-1", engines[0].ActiveSessionID)
	}
}

func TestPlanRestore_MultiEngineMeta_EmptyActiveID_DefaultsToMain(t *testing.T) {
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
