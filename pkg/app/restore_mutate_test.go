package app

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
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

	logs := captureAppLogs(t)
	mgr := engine.NewEngineManager()
	got := restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    restoreFactory(&spyProvider{}),
		store:      store,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})

	// The refusal must name the owner, and the recovery must say which session
	// it landed on — both logs carry the session id truncated to 8.
	rec := exactlyOne(t, logs, "restore: recorded session belongs to another engine, recovering engine-scoped", "foreign session")
	if rec.attrs["id"] != "main" {
		t.Errorf("id attr = %v, want main", rec.attrs["id"])
	}
	if rec.attrs["session"] != foreign.SessionID[:8] {
		t.Errorf("session attr = %v, want %q", rec.attrs["session"], foreign.SessionID[:8])
	}
	if rec.attrs["owner"] != "other" {
		t.Errorf("owner attr = %v, want other", rec.attrs["owner"])
	}
	info := exactlyOne(t, logs, "restore: resumed engine's own latest session", "foreign session")
	if info.attrs["session"] != own.SessionID[:8] {
		t.Errorf("resumed session attr = %v, want %q", info.attrs["session"], own.SessionID[:8])
	}

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
	logs := captureAppLogs(t)
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

	// The skipped engine must be named: a boot that quietly loses one engine
	// is otherwise indistinguishable from one that never had it.
	rec := exactlyOne(t, logs, "restore: build engine failed", "factory failure")
	if rec.attrs["id"] != "broken" {
		t.Errorf("id attr = %v, want broken", rec.attrs["id"])
	}
	if rec.attrs["error"] == nil {
		t.Error("the build-engine error must carry the factory error")
	}
	if rec.level != slog.LevelError {
		t.Errorf("level = %v, want ERROR", rec.level)
	}

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

// runRestore lays out projectDir, optionally with a seed meta.json, runs
// restoreEngines with log capture, and returns the manager plus the records.
func runRestore(t *testing.T, projectDir string, store *short.Store, seed *short.WorkspaceMeta, factory tui.EngineFactoryFn) (*engine.EngineManager, *appLogHandler) {
	t.Helper()
	if seed != nil {
		if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
			t.Fatalf("WriteWorkspaceMeta: %v", err)
		}
	}
	logs := captureAppLogs(t)
	mgr := engine.NewEngineManager()
	restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    factory,
		store:      store,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})
	return mgr, logs
}

// exactlyOne returns the single record with msg, failing the test otherwise.
// The count matters as much as the presence: a warning that also fires on the
// success path is not a signal.
func exactlyOne(t *testing.T, logs *appLogHandler, msg string, context string) appLogRec {
	t.Helper()
	recs := logs.find(msg)
	if len(recs) != 1 {
		t.Fatalf("%s: log %q count = %d, want exactly 1", context, msg, len(recs))
	}
	return recs[0]
}

func newRestoreStore(t *testing.T, projectDir string) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// A readable meta.json must produce no read warning: the warning is the only
// signal that a workspace was reset to a synthesized main engine.
func TestRestoreEngines_ValidMetaLogsNoReadWarning(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	if recs := logs.find("restore: read workspace meta failed, will synthesize default"); len(recs) != 0 {
		t.Errorf("read warning on a readable meta: %d records", len(recs))
	}
	if vs := mgr.Get("main"); vs == nil {
		t.Error("main engine missing")
	}
}

// An unreadable meta.json must warn once and still synthesize main.
func TestRestoreEngines_CorruptMetaWarnsAndSynthesizes(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	if err := os.WriteFile(filepath.Join(projectDir, "meta.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	logs := captureAppLogs(t)
	mgr := engine.NewEngineManager()
	restoreEngines(restoreEnginesDeps{
		mgr:        mgr,
		factory:    restoreFactory(&spyProvider{}),
		store:      store,
		projectDir: projectDir,
		model:      "zhipu/glm-5",
	})
	rec := exactlyOne(t, logs, "restore: read workspace meta failed, will synthesize default", "corrupt meta")
	if rec.level != slog.LevelWarn {
		t.Errorf("level = %v, want WARN", rec.level)
	}
	if rec.attrs["error"] == nil {
		t.Error("the read warning must carry the parse error")
	}
	if vs := mgr.Get("main"); vs == nil {
		t.Error("a corrupt meta must still synthesize the main engine")
	}
}

// The recorded session must be adopted when it exists AND belongs to this
// engine — even when a newer own session exists. The recovery path (own latest)
// would silently move the user to a different chat.
func TestRestoreEngines_AdoptsRecordedOwnSession(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	recorded, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	if _, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main"); err != nil {
		t.Fatalf("CreateSessionWithEngine(newer): %v", err)
	}
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: recorded.SessionID}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID != recorded.SessionID {
		t.Errorf("ActiveSessionID = %q, want the recorded own session %q (not a recovery fallback)", vs.ActiveSessionID, recorded.SessionID)
	}
	for _, msg := range []string{
		"restore: recorded session belongs to another engine, recovering engine-scoped",
		"restore: recorded session missing, recovering engine-scoped",
		"restore: resumed engine's own latest session",
	} {
		if recs := logs.find(msg); len(recs) != 0 {
			t.Errorf("unexpected recovery log %q: %d records", msg, len(recs))
		}
	}
}

// A session row with no engine_id is not owned by anyone, so it must not be
// read as a foreign adoption.
func TestRestoreEngines_AdoptsSessionWithNoOwner(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	// The store defaults a blank engine id to main, so the unowned row has to
	// be written directly: an empty sessions.engine_id is what an older database
	// (pre-engine-binding) holds.
	unowned, err := store.CreateSessionWithEngine(projectDir, "glm-5", "temp")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	if _, err := store.DB().Exec(`UPDATE sessions SET engine_id = '' WHERE session_id = ?`, unowned.SessionID); err != nil {
		t.Fatalf("clear engine_id: %v", err)
	}
	got, err := store.GetSession(unowned.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.EngineID != "" {
		t.Fatalf("fixture: session EngineID = %q, want empty", got.EngineID)
	}
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: unowned.SessionID}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID != unowned.SessionID {
		t.Errorf("ActiveSessionID = %q, want the unowned recorded session %q", vs.ActiveSessionID, unowned.SessionID)
	}
	if recs := logs.find("restore: recorded session belongs to another engine, recovering engine-scoped"); len(recs) != 0 {
		t.Errorf("an unowned session was read as foreign: %d records", len(recs))
	}
}

// A recorded id with no row must say "recorded session missing" — the specific
// reason is what distinguishes this recovery from the foreign-owner one.
func TestRestoreEngines_RecordedSessionMissingRecovers(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	own, err := store.CreateSessionWithEngine(projectDir, "glm-5", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: "ghost-session"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: recorded session missing, recovering engine-scoped", "stale id")
	if rec.attrs["id"] != "main" {
		t.Errorf("id attr = %v, want main", rec.attrs["id"])
	}
	if rec.attrs["error"] == nil {
		t.Error("the missing-session warning must carry the store error")
	}
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID != own.SessionID {
		t.Errorf("ActiveSessionID = %q, want own latest session %q", vs.ActiveSessionID, own.SessionID)
	}
	// Exactly one recovery reason: a missing row must not also report a failed
	// switch, which is what a half-applied "not adoptable" produces.
	if recs := logs.find("restore: switch session failed, recovering engine-scoped"); len(recs) != 0 {
		t.Errorf("spurious switch-failure warning after a known-missing session: %d records", len(recs))
	}
}

// A stale recorded id on an engine with no sessions of its own must land on a
// freshly created session — the recovery query returning nothing is a normal
// shape, not an index to dereference.
func TestRestoreEngines_NoOwnSessionsCreatesFresh(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: "ghost-session"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	exactlyOne(t, logs, "restore: recorded session missing, recovering engine-scoped", "no own sessions")
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID == "" {
		t.Error("ActiveSessionID = empty, want a freshly created session")
	}
	if vs.Engine.SessionID() != vs.ActiveSessionID {
		t.Errorf("engine session = %q, want the view state's %q", vs.Engine.SessionID(), vs.ActiveSessionID)
	}
	if recs := logs.find("restore: session init failed"); len(recs) != 0 {
		t.Errorf("fresh session creation reported as failed: %d records", len(recs))
	}
}

// With no store the recorded session cannot be checked or switched: the switch
// failure must be reported, and the engine must end up with no session rather
// than the unusable recorded id.
func TestRestoreEngines_SwitchSessionFailureWarns(t *testing.T) {
	projectDir := t.TempDir()
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", ActiveSessionID: "recorded-but-no-store"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, nil, seed, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: switch session failed, recovering engine-scoped", "nil store")
	if rec.attrs["id"] != "main" {
		t.Errorf("id attr = %v, want main", rec.attrs["id"])
	}
	if rec.attrs["error"] == nil {
		t.Error("the switch warning must carry the underlying error")
	}
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID = %q, want empty when no session can be created", vs.ActiveSessionID)
	}
	// The fresh-session attempt must also be reported.
	exactlyOne(t, logs, "restore: session init failed", "nil store")
}

// A store that cannot create a session must leave a warning behind, not a
// silently session-less engine.
func TestRestoreEngines_NewSessionFailureWarns(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	mgr, logs := runRestore(t, projectDir, store, nil, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: session init failed", "closed store")
	if rec.attrs["id"] != "main" {
		t.Errorf("id attr = %v, want main", rec.attrs["id"])
	}
	if rec.attrs["error"] == nil {
		t.Error("the session-init warning must carry the store error")
	}
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.ActiveSessionID != "" {
		t.Errorf("ActiveSessionID = %q, want empty after a failed session init", vs.ActiveSessionID)
	}
}

// A garbage effort from a hand-edited meta.json must be reported and must NOT
// round-trip back into the view state.
func TestRestoreEngines_InvalidThinkingWarns(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", Thinking: "bogus"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: invalid thinking effort", "garbage effort")
	if rec.attrs["value"] != "bogus" {
		t.Errorf("value attr = %v, want bogus", rec.attrs["value"])
	}
	if rec.attrs["id"] != "main" {
		t.Errorf("id attr = %v, want main", rec.attrs["id"])
	}
	if vs := mgr.Get("main"); vs == nil || vs.Thinking != "" {
		t.Errorf("view state Thinking = %+v, want no sticky override", vs)
	}
}

// A valid effort is a user choice: it must survive the restart as a sticky
// override on the view state (which is what PersistMeta writes back).
func TestRestoreEngines_ValidThinkingBecomesSticky(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5", Thinking: "high"}},
		ActiveEngineID: "main",
	}
	mgr, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	if recs := logs.find("restore: invalid thinking effort"); len(recs) != 0 {
		t.Errorf("valid effort reported as invalid: %d records", len(recs))
	}
	vs := mgr.Get("main")
	if vs == nil {
		t.Fatalf("main missing, manager holds %v", engineIDs(mgr))
	}
	if vs.Thinking != llm.EffortHigh {
		t.Errorf("Thinking = %q, want the persisted %q", vs.Thinking, llm.EffortHigh)
	}
}

// An active id that no engine restored must be reported: the daemon otherwise
// boots with an active engine the operator never asked for.
func TestRestoreEngines_SetActiveFailureWarns(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	seed := &short.WorkspaceMeta{
		Engines:        []short.EngineMeta{{ID: "main", Name: "main", Model: "zhipu/glm-5"}},
		ActiveEngineID: "ghost",
	}
	_, logs := runRestore(t, projectDir, store, seed, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: set active engine failed", "ghost active")
	if rec.attrs["id"] != "ghost" {
		t.Errorf("id attr = %v, want ghost", rec.attrs["id"])
	}
	if rec.attrs["error"] == nil {
		t.Error("the set-active warning must carry the manager error")
	}
}

// meta.json cannot be written (a non-empty directory sits in its place): the
// restore must still finish, with the write failure reported.
func TestRestoreEngines_PersistMetaFailureWarns(t *testing.T) {
	projectDir := t.TempDir()
	store := newRestoreStore(t, projectDir)
	if err := os.MkdirAll(filepath.Join(projectDir, "meta.json", "block"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mgr, logs := runRestore(t, projectDir, store, nil, restoreFactory(&spyProvider{}))
	rec := exactlyOne(t, logs, "restore: write workspace meta failed", "unwritable meta")
	if rec.attrs["error"] == nil {
		t.Error("the meta-write warning must carry the underlying error")
	}
	if vs := mgr.Get("main"); vs == nil {
		t.Error("the restore must still register the engine when meta cannot be written")
	}
}
