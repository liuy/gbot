package app

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wui"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
)

// wuiFactory builds a fully wired engine (ToolRefs + WireEngine) the way the
// production engineFactory does, so registerWUISendTool has a real registry.
func wuiFactory(spy *spyProvider) tui.EngineFactoryFn {
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
		deps := engine.SharedDeps{WorkingDir: filepath.Dir(id), ShortStore: nil}
		refs := engine.CreateTools(deps, task.NewList(""))
		engine.WireEngine(eng, refs, deps)
		eng.SetToolRefs(refs)
		return eng, handler, nil
	}
}

func TestCreateEngineForWUI_RegistersEngineAndSession(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	id, err := createEngineForWUI("", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "t", "m")
	if err != nil {
		t.Fatalf("createEngineForWUI: %v", err)
	}
	if id == "" {
		t.Fatal("createEngineForWUI returned an empty engine ID")
	}
	vs := mgr.Get(id)
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine %s was not added to the manager", id)
	}
	if vs.ID != id {
		t.Errorf("view state ID = %q, want %q", vs.ID, id)
	}
	if vs.Name == "" {
		t.Error("an empty requested name must fall back to a generated one")
	}
	if vs.Engine.SessionID() == "" {
		t.Error("the new engine must have a session")
	}
	if vs.ActiveSessionID != vs.Engine.SessionID() {
		t.Errorf("ActiveSessionID = %q, want the engine's session %q", vs.ActiveSessionID, vs.Engine.SessionID())
	}
	if vs.Model != vs.Engine.Model() {
		t.Errorf("view state Model = %q, want the engine model %q", vs.Model, vs.Engine.Model())
	}
	if vs.System || vs.ReadOnly {
		t.Errorf("a wui-created engine must not be System/ReadOnly, got %+v", vs)
	}
	if _, ok := vs.Engine.ToolRefs().Reg.Lookup("Send"); !ok {
		t.Error("the Send tool must be registered on a wui-created engine")
	}
	if got := len(mgr.List()); got != 1 {
		t.Errorf("manager holds %d engines, want 1", got)
	}
}

func TestCreateEngineForWUI_KeepsRequestedName(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	id, err := createEngineForWUI("worker-2", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("createEngineForWUI: %v", err)
	}
	vs := mgr.Get(id)
	if vs == nil {
		t.Fatalf("engine %s missing from the manager", id)
	}
	if vs.Name != "worker-2" {
		t.Errorf("Name = %q, want the requested %q", vs.Name, "worker-2")
	}
}

func TestCreateEngineForWUI_FactoryErrorWrapped(t *testing.T) {
	projectDir := t.TempDir()
	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})
	sentinel := errors.New("factory exploded")

	id, err := createEngineForWUI("x", mgr,
		func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
			return nil, nil, sentinel
		}, nil, projectDir, wc, "", "")
	if id != "" {
		t.Errorf("engine ID = %q, want empty on a factory failure", id)
	}
	if err == nil {
		t.Fatal("a factory failure must surface as an error")
	}
	if err.Error() != "factory: factory exploded" {
		t.Errorf("error = %q, want %q", err.Error(), "factory: factory exploded")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the factory error", err)
	}
	if n := len(mgr.List()); n != 0 {
		t.Errorf("manager holds %d engines after a failed create, want 0", n)
	}
}

func TestCreateEngineForWUI_SessionFailureClosesEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// A closed store makes NewSession fail, which is the only failure the
	// factory path can hit after the engine exists.
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	_, err = createEngineForWUI("doomed", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err == nil {
		t.Fatal("createEngineForWUI must fail when the session cannot be created")
	}
	if !strings.HasPrefix(err.Error(), "new session: ") {
		t.Errorf("error = %q, want the \"new session: \" prefix", err.Error())
	}
	if n := len(mgr.List()); n != 0 {
		t.Errorf("manager holds %d engines after a failed create, want 0 (the engine must be closed, not added)", n)
	}
}

func TestCreateEngineForWUI_RejectsReservedName(t *testing.T) {
	projectDir := t.TempDir()
	mgr := engine.NewEngineManager()
	seedEng, _, err := wuiFactory(&spyProvider{})("main", "main", "", "m")
	if err != nil {
		t.Fatalf("seed factory: %v", err)
	}
	t.Cleanup(seedEng.Close)
	mgr.Add(&engine.EngineViewState{Engine: seedEng, ID: "main", Name: "main"})
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	id, err := createEngineForWUI("agent-3", mgr, wuiFactory(&spyProvider{}), nil, projectDir, wc, "", "")
	if err == nil {
		t.Fatal("createEngineForWUI must reject a name in the reserved agent-N grammar")
	}
	if !strings.Contains(err.Error(), "agent-N id namespace") {
		t.Errorf("error = %q, want the namespace-collision message", err.Error())
	}
	if id != "" {
		t.Errorf("engine ID = %q, want empty on a rejected name", id)
	}
	if n := len(mgr.List()); n != 1 {
		t.Errorf("manager holds %d engines, want 1 (only the seeded main)", n)
	}
}

func TestCreateEngineForWUI_MintsFromInjectedMinter(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	idm := agenttool.NewIDMinter()
	mgr.SetIDMinter(idm)
	seedEng, _, err := wuiFactory(&spyProvider{})("main", "main", "", "m")
	if err != nil {
		t.Fatalf("seed factory: %v", err)
	}
	t.Cleanup(seedEng.Close)
	mgr.Add(&engine.EngineViewState{Engine: seedEng, ID: "main", Name: "main"})
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	id, err := createEngineForWUI("", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("createEngineForWUI: %v", err)
	}
	if id != "agent-1" {
		t.Errorf("engine ID = %q, want agent-1 from the injected minter", id)
	}
	vs := mgr.Get(id)
	if vs == nil {
		t.Fatal("created engine missing from the manager")
	}
	if vs.Name != "agent-1" {
		t.Errorf("auto name = %q, want agent-1 (name == id)", vs.Name)
	}
}

func TestCreateEngineForWUI_TrimsNameBeforeValidationAndStorage(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})
	id, err := createEngineForWUI("  x  ", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("createEngineForWUI: %v", err)
	}
	vs := mgr.Get(id)
	if vs == nil {
		t.Fatal("created engine missing from the manager")
	}
	if vs.Name != "x" {
		t.Errorf("stored name = %q, want the trimmed \"x\"", vs.Name)
	}
	_, err = createEngineForWUI("  x  ", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err == nil {
		t.Fatal("second createEngineForWUI(\"  x  \") succeeded; want the duplicate-name error")
	}
	if err.Error() != `engine name "x" is already in use` {
		t.Errorf("error = %q, want the duplicate-name message for the trimmed name", err.Error())
	}
}
func TestCreateEngineForWUI_WhitespaceNameUsesMintedID(t *testing.T) {
	// "   " is an empty name once trimmed: it takes the auto-name path
	// (name == minted id) instead of the empty-name rejection.
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	idm := agenttool.NewIDMinter()
	mgr.SetIDMinter(idm)
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})
	id, err := createEngineForWUI("   ", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("createEngineForWUI(whitespace name): %v", err)
	}
	vs := mgr.Get(id)
	if vs == nil {
		t.Fatal("created engine missing from the manager")
	}
	if id != "agent-1" || vs.Name != "agent-1" {
		t.Errorf("id/name = (%q, %q), want (agent-1, agent-1)", id, vs.Name)
	}
}
func TestRegisterWUISendTool_IsIdempotent(t *testing.T) {
	eng, _, err := wuiFactory(&spyProvider{})("eng-1", "one", "", "m")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer eng.Close()
	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	reg := eng.ToolRefs().Reg
	sendCount := func() int {
		n := 0
		for _, name := range reg.Names() {
			if name == "Send" {
				n++
			}
		}
		return n
	}

	registerWUISendTool(eng, wc)
	before := sendCount()

	registerWUISendTool(eng, wc)
	after := sendCount()

	if before != 1 {
		t.Fatalf("first registerWUISendTool registered Send %d times, want exactly 1 (names = %v)", before, reg.Names())
	}
	if after != before {
		t.Errorf("Send count = %d after the second registerWUISendTool, want %d — the lookup guard must keep it registered once", after, before)
	}
	if _, ok := reg.Lookup("Send"); !ok {
		t.Fatal("Send tool missing after registerWUISendTool")
	}
}
