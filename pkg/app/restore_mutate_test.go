package app

import (
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tui"
)

func restoreFactory(spy *spyProvider) tui.EngineFactoryFn {
	return func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
		h, handler := tui.NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Provider:    spy,
			Logger:      slog.Default(),
			Model:       model,
			EngineID:    id,
			Dispatcher:  h,
			TokenBudget: 5000,
			AutoCompact: engine.AutoCompactConfig{ContextWindow: 5000},
		})
		return eng, handler, nil
	}
}

// A recorded session that belongs to a DIFFERENT engine must never be adopted
// (incident #2: agent-3 showed main's chat after a restart); recovery is
// engine-scoped — the engine's own latest session.
func TestRestoreEngines_ForeignSessionFallsBackToOwnLatest(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	foreign, err := store.CreateSessionWithEngine(projectDir, "glm-5", "other")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(other): %v", err)
	}
	own, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine(main): %v", err)
	}

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: foreign.SessionID},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	mgr := engine.NewEngineManager()
	got := restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    restoreFactory(&spyProvider{}),
		store:      store,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})

	vs := mgr.Get("main")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("main engine missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID == foreign.SessionID {
		t.Errorf("main adopted %s, a session owned by engine %q", foreign.SessionID, "other")
	}
	if vs.ActiveSessionID != own.SessionID {
		t.Errorf("ActiveSessionID = %q, want main's own latest session %q", vs.ActiveSessionID, own.SessionID)
	}
	if got != own.SessionID {
		t.Errorf("restoreEngines returned %q, want the active engine's session %q", got, own.SessionID)
	}
	if vs.Engine.SessionID() != own.SessionID {
		t.Errorf("engine session = %q, want %q", vs.Engine.SessionID(), own.SessionID)
	}
}

// A factory failure for one engine must not abort the rest of the restore.
func TestRestoreEngines_FactoryErrorSkipsOnlyThatEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "broken", Name: "broken", Model: "zhipu/glm-5"},
			{ID: "main", Name: "main", Model: "zhipu/glm-5"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}

	base := restoreFactory(&spyProvider{})
	mgr := engine.NewEngineManager()
	got := restoreEngines(restoreEnginesDeps{
		mgr: mgr,
		factory: func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
			if id == "broken" {
				return nil, nil, errors.New("factory exploded")
			}
			return base(id, name, prov, model)
		},
		store:      store,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})

	if vs := mgr.Get("broken"); vs != nil {
		t.Errorf("the failed engine was added anyway: %+v", vs)
	}
	vs := mgr.Get("main")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("main missing after a sibling failure, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID == "" {
		t.Error("main must still get a session when a sibling engine fails")
	}
	if got != vs.ActiveSessionID {
		t.Errorf("restoreEngines returned %q, want %q", got, vs.ActiveSessionID)
	}
}

// With no store, restore still synthesizes the main engine and reports no
// session (the empty return is what keeps SessionStart from firing).
func TestRestoreEngines_NoStoreReturnsEmptySession(t *testing.T) {
	projectDir := t.TempDir()
	mgr := engine.NewEngineManager()

	got := restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    restoreFactory(&spyProvider{}),
		store:      nil,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})
	if got != "" {
		t.Errorf("restoreEngines = %q, want empty without a store", got)
	}
	vs := mgr.Get("main")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("main engine missing, manager holds %v", engineIDs(mgr))
	}
	if vs.Model != "zhipu/glm-5" {
		t.Errorf("Model = %q, want the default model %q", vs.Model, "zhipu/glm-5")
	}
	if vs.Name != "main" || vs.ID != "main" {
		t.Errorf("synthesized engine = (%q, %q), want (main, main)", vs.ID, vs.Name)
	}
	if vs.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID = %q, want empty without a store", vs.ActiveSessionID)
	}
}

// planRestore must keep the meta's engine list verbatim (identity, order and
// count) while defaulting the active id.
func TestPlanRestore_PreservesMetaOrderAndCount(t *testing.T) {
	meta := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "zeta", Name: "Zeta", Model: "p/m"},
			{ID: "alpha", Name: "Alpha", Model: "p/m"},
		},
		ActiveEngineID: "zeta",
	}
	list, active := planRestore(meta, "fallback/model")
	if len(list) != 2 {
		t.Fatalf("planRestore returned %d engines, want 2", len(list))
	}
	if list[0].ID != "zeta" || list[1].ID != "alpha" {
		t.Errorf("engine order = %v, want the meta order (zeta, alpha)", []string{list[0].ID, list[1].ID})
	}
	if active != "zeta" {
		t.Errorf("activeID = %q, want %q", active, "zeta")
	}

	// The default model only applies to the synthesized main engine.
	list2, active2 := planRestore(nil, "fallback/model")
	if len(list2) != 1 || list2[0].ID != "main" || list2[0].Model != "fallback/model" {
		t.Errorf("nil meta → %+v, want one main engine with the default model", list2)
	}
	if active2 != "main" {
		t.Errorf("activeID for nil meta = %q, want main", active2)
	}
}
