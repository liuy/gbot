package app

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wechat"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/reload"
)

// wechatDeps wires one connector against an offline BaseURL: the poll loop
// fails fast and backs off, so the connector goroutines stay quiet.
func wechatDeps(t *testing.T, mgr *engine.EngineManager, store *short.Store, daemonMode bool) (startWeChatDeps, *[]*media.Store) {
	t.Helper()
	projectDir := t.TempDir()
	mediaStores := []*media.Store{}
	deps := engine.SharedDeps{WorkingDir: projectDir, ShortStore: store}
	return startWeChatDeps{
		state:              &wechat.State{AccountID: "acct1", Token: "tok", BaseURL: "http://127.0.0.1:1"},
		engineMgr:          mgr,
		provider:           &spyProvider{},
		model:              "glm-5",
		maxTokens:          1024,
		contextWindow:      5000,
		logger:             slog.Default(),
		workingDir:         projectDir,
		projectDir:         projectDir,
		store:              store,
		deps:               deps,
		primaryProviderCfg: &config.Provider{Name: "zhipu"},
		mediaStores:        &mediaStores,
		daemonMode:         daemonMode,
		env:                reload.NewEnvHolder(&reload.Env{}),
	}, &mediaStores
}

func TestStartWeChatConnector_BuildsReadOnlyEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)

	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}

	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine wechat-acct1 missing, manager holds %v", engineIDs(mgr))
	}
	if vs.Name != "WeChat acct1" {
		t.Errorf("Name = %q, want %q", vs.Name, "WeChat acct1")
	}
	if !vs.ReadOnly {
		t.Error("a connector-driven engine must be ReadOnly (the TUI must not type into it)")
	}
	if vs.System {
		t.Error("a wechat engine must not be marked System")
	}
	if vs.Model != "zhipu/glm-5" {
		t.Errorf("Model = %q, want %q (provider name + model)", vs.Model, "zhipu/glm-5")
	}
	if vs.ActiveSessionID == "" || vs.ActiveSessionID != vs.Engine.SessionID() {
		t.Errorf("ActiveSessionID = %q, want the engine's fresh session %q", vs.ActiveSessionID, vs.Engine.SessionID())
	}
	if vs.Engine.Model() != "glm-5" {
		t.Errorf("engine Model = %q, want the bare registration name %q", vs.Engine.Model(), "glm-5")
	}
	if vs.Engine.MaxTokens() != 1024 {
		t.Errorf("engine MaxTokens = %d, want 1024", vs.Engine.MaxTokens())
	}
	if _, ok := vs.Engine.ToolRefs().Reg.Lookup("Send"); !ok {
		t.Error("the WeChat Send tool must be registered on the connector engine")
	}
	if len(*mediaStores) != 1 || (*mediaStores)[0] == nil {
		t.Errorf("mediaStores = %v, want the connector's media cache captured for shutdown", *mediaStores)
	}
	for _, ms := range *mediaStores {
		if ms != nil {
			ms.Close()
		}
	}
}

func TestStartWeChatConnector_RestoredEngineBecomesReadOnly(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)

	// The engine arrives from restoreEngines with ReadOnly:false; the
	// connector must flip it instead of rebuilding (which would drop the
	// restored session).
	eng, _, err := wuiFactory(&spyProvider{})("wechat-acct1", "wechat-acct1", "", "glm-5")
	if err != nil {
		t.Fatalf("seed engine: %v", err)
	}
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, "restored"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	seededSession := eng.SessionID()
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "wechat-acct1", Name: "wechat-acct1", ReadOnly: false})

	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}

	vs := mgr.Get("wechat-acct1")
	if vs == nil {
		t.Fatalf("engine vanished, manager holds %v", engineIDs(mgr))
	}
	if !vs.ReadOnly {
		t.Error("the restored engine must be flipped to ReadOnly")
	}
	if got := len(mgr.List()); got != 1 {
		t.Errorf("manager holds %d engines, want 1 (no rebuild)", got)
	}
	if vs.Engine.SessionID() != seededSession {
		t.Errorf("session = %q, want the restored %q (the connector must not mint a fresh one)", vs.Engine.SessionID(), seededSession)
	}
	for _, ms := range *mediaStores {
		if ms != nil {
			ms.Close()
		}
	}
}

func TestStartWeChatConnector_DaemonModePinsMemoryDir(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, true)

	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector (daemon): %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	wantDir := filepath.Join(d.projectDir, "memory", "wechat-acct1")
	if !strings.Contains(vs.Engine.SystemPrompt(), wantDir) {
		t.Errorf("daemon-mode system prompt must carry the per-engine memory dir %q", wantDir)
	}

	// Non-daemon mode must NOT inject the memory dir.
	mgr2 := engine.NewEngineManager()
	d2, mediaStores2 := wechatDeps(t, mgr2, store, false)
	if err := startWeChatConnector(d2); err != nil {
		t.Fatalf("startWeChatConnector (attach): %v", err)
	}
	vs2 := mgr2.Get("wechat-acct1")
	if vs2 == nil || vs2.Engine == nil {
		t.Fatalf("attach-mode engine missing, manager holds %v", engineIDs(mgr2))
	}
	if strings.Contains(vs2.Engine.SystemPrompt(), filepath.Join(d2.projectDir, "memory", "wechat-acct1")) {
		t.Error("attach mode must not pin a per-engine memory dir")
	}

	for _, ms := range append(*mediaStores, *mediaStores2...) {
		if ms != nil {
			ms.Close()
		}
	}
}

func engineIDs(mgr *engine.EngineManager) []string {
	out := make([]string, 0, len(mgr.List()))
	for _, vs := range mgr.List() {
		out = append(out, vs.ID)
	}
	return out
}

// A view state whose Engine field is nil (a half-restored entry) must surface
// as the "engine not found" error rather than being wired into a connector.
func TestStartWeChatConnector_NilEngineFieldReturnsError(t *testing.T) {
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{ID: "wechat-acct1", Engine: nil})

	d, mediaStores := wechatDeps(t, mgr, nil, false)
	defer func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	}()

	err := startWeChatConnector(d)
	if err == nil {
		t.Fatal("a view state with a nil Engine must not produce a live connector")
	}
	if err.Error() != "engine not found after creation: wechat-acct1" {
		t.Errorf("error = %q, want %q", err.Error(), "engine not found after creation: wechat-acct1")
	}
	if len(*mediaStores) != 0 {
		t.Errorf("mediaStores = %v, want nothing captured for a failed connector", *mediaStores)
	}
}

// The engine can be deleted (TUI/daemon "remove engine") while the connector is
// being wired. EngineManager.Get returns nil for a removed id, so the lookup
// that feeds the connector must be nil-checked: a vanished engine is the same
// "engine not found" failure, not a nil dereference.
//
// The window is a few instructions wide, so the removal is raced in: a goroutine
// hammers Remove until it lands between the build/adopt branch and the lookup.
func TestStartWeChatConnector_EngineRemovedBeforeWiring(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const wantErr = "engine not found after creation: wechat-acct1"
	const attempts = 200
	for attempt := 1; attempt <= attempts; attempt++ {
		mgr := engine.NewEngineManager()
		// Remove refuses to empty the manager, so a second engine has to exist
		// for the concurrent removal to ever succeed.
		mgr.Add(&engine.EngineViewState{ID: "keepalive"})

		d, mediaStores := wechatDeps(t, mgr, store, false)
		stop, removed := make(chan struct{}), make(chan struct{})
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if mgr.Remove("wechat-acct1") == nil {
					close(removed)
					return
				}
			}
		}()

		err := startWeChatConnector(d)
		close(stop)
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
		if err == nil {
			continue // the removal landed after the lookup — try again
		}
		if err.Error() != wantErr {
			t.Fatalf("attempt %d: error = %q, want %q", attempt, err.Error(), wantErr)
		}
		select {
		case <-removed:
		default:
			t.Fatalf("attempt %d: error %q without the racer having removed the engine", attempt, err)
		}
		return
	}
	t.Fatalf("the removal never landed inside the wiring window in %d attempts; the guard is untested", attempts)
}
