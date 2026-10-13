package app

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
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

// The boot seed must clear every agent-N suffix already in the persisted
// namespace — engine ids AND engine display names. While an engine is named
// agent-3 its name-fallback address must never be shadowed by a live
// sub-agent id, so a mint has to skip 3.
func TestRestoreEngines_SeedsMinterPastIDSuffixesAndNames(t *testing.T) {
	cases := []struct {
		name    string
		engines []short.EngineMeta
		want    string
	}{
		{
			name:    "legacy id named agent-N",
			engines: []short.EngineMeta{{ID: "main", Name: "main"}, {ID: "e2", Name: "agent-3"}},
			want:    "agent-4",
		},
		{
			name:    "engine id agent-N",
			engines: []short.EngineMeta{{ID: "main", Name: "main"}, {ID: "agent-5", Name: "research"}},
			want:    "agent-6",
		},
		{
			name:    "max over ids and names",
			engines: []short.EngineMeta{{ID: "main", Name: "main"}, {ID: "agent-2", Name: "agent-2"}, {ID: "e7", Name: "agent-9"}},
			want:    "agent-10",
		},
		{
			name:    "no agent-N anywhere",
			engines: []short.EngineMeta{{ID: "main", Name: "main"}, {ID: "e2", Name: "e2"}},
			want:    "agent-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			seed := &short.WorkspaceMeta{Engines: tc.engines, ActiveEngineID: "main"}
			if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
				t.Fatalf("WriteWorkspaceMeta: %v", err)
			}

			idm := agenttool.NewIDMinter()
			mgr := engine.NewEngineManager()
			mgr.SetIDMinter(idm)
			restoreEngines(restoreEnginesDeps{
				mgr:        mgr,
				factory:    restoreFactory(&spyProvider{}),
				store:      store,
				projectDir: projectDir,
				model:      "zhipu/glm-5",
				minter:     idm,
			})

			if got := idm.Mint(); got != tc.want {
				t.Errorf("post-restore Mint = %q, want %q", got, tc.want)
			}
		})
	}
}

// A manager wired with the process-global minter must be paired with the same
// minter in deps: skipping the seed silently lets a runtime mint shadow a
// persisted id. That is a boot-time wiring bug, so it fails loudly.
func TestRestoreEngines_PanicsWhenDepsMinterMissing(t *testing.T) {
	projectDir := t.TempDir()
	idm := agenttool.NewIDMinter()
	mgr := engine.NewEngineManager()
	mgr.SetIDMinter(idm)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("restoreEngines returned silently; want a panic on the minter wiring mismatch")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "minter") {
			t.Errorf("panic = %q, want a message naming the minter wiring bug", msg)
		}
	}()
	restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    restoreFactory(&spyProvider{}),
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})
}

func TestRestoreEngines_PanicsWhenManagerMinterMissing(t *testing.T) {
	projectDir := t.TempDir()
	idm := agenttool.NewIDMinter()
	mgr := engine.NewEngineManager()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("restoreEngines returned silently; want a panic when deps carry a minter the manager lacks")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "minter") {
			t.Errorf("panic = %q, want a message naming the minter wiring bug", msg)
		}
	}()
	restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		minter:     idm,
		factory:    restoreFactory(&spyProvider{}),
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})
}

// End-to-end boot pin of the minimal design: a real Start leaves a persisted
// legacy engine's e-N id AND its agent-N-looking name untouched, and seeds the
// process-global counter above that suffix so the next runtime mint cannot
// shadow the address the engine already answers to.
func TestStart_BootSeedsCounterPastPersistedIDSuffixesAndNames(t *testing.T) {
	inst := startForGates(t, "")

	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "t/m"},
			{ID: "e2", Name: "agent-3", Model: "t/m"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(inst.ProjectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	if inst.PIDCleanup != nil {
		inst.PIDCleanup() // release the lock so the second boot can take it
	}

	second, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	shutdownInstance(t, second)

	vs := second.EngineMgr.Get("e2")
	if vs == nil {
		t.Fatalf("legacy engine e2 not restored verbatim, manager holds %v", engineIDs(second.EngineMgr))
	}
	if vs.Name != "agent-3" {
		t.Errorf("e2 Name = %q, want agent-3 (persisted name untouched)", vs.Name)
	}
	if got := second.EngineMgr.NewEngineID(); got != "agent-4" {
		t.Errorf("first runtime NewEngineID = %q, want agent-4 (counter seeded past the name)", got)
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
