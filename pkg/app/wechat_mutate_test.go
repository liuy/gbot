package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/connector/wechat"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/media"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/reload"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
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

// Tier-2 boot pairs a zero-value provider config with an empty model; the
// view-state Model must persist "" (not "/") so the meta.json restore feeds
// the factory an empty model arg — the factory's degraded-boot fallback only
// triggers on the empty string.
func TestStartWeChatConnector_DegradedModelIsEmptyString(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	d.provider = llm.NewNullProvider()
	d.model = ""
	d.primaryProviderCfg = &config.Provider{}
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})

	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	if vs.Model != "" {
		t.Errorf("Model = %q, want \"\" (degraded boot must not persist a bogus \"/\")", vs.Model)
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

// dumpText flattens the request dump's messages so a marker can be searched.
func dumpText(dump *engine.APIRequestDump) string {
	var b strings.Builder
	for _, m := range dump.Messages {
		for _, cb := range m.Content {
			b.WriteString(cb.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func dumpHasTool(dump *engine.APIRequestDump, name string) bool {
	for _, td := range dump.Tools {
		if td.Name == name {
			return true
		}
	}
	return false
}

// The fresh-build branch wires ~15 Params fields and three view-state fields.
// Each one is a behavior the connector depends on (modality gating, budget,
// thinking resolution, the shared hub), so the wiring is pinned field by field
// rather than by "an engine appeared".
func TestStartWeChatConnector_PinsEngineWiring(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	const marker = "WECHAT-FIXTURE-CLAUDE-MD"
	if err := os.WriteFile(filepath.Join(d.workingDir, "CLAUDE.md"), []byte(marker), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	prov := &config.Provider{Name: "zhipu"}
	prov.Models.Set("glm-5", config.ModelConfig{Input: []string{"text", "image"}})
	d.primaryProviderCfg = prov
	d.modelThinking = map[string]llm.Effort{"glm-5": llm.EffortHigh}

	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	if vs.Engine.Provider() == nil {
		t.Error("engine Provider = nil, want the configured provider")
	}
	if !vs.Engine.SupportsModality("image") {
		t.Error("InputModalities not wired: the engine must accept image input for this provider")
	}
	if vs.Engine.TokenBudget() != 5000 {
		t.Errorf("TokenBudget = %d, want the configured context window 5000", vs.Engine.TokenBudget())
	}
	if vs.Engine.TaskList() == nil {
		t.Error("TaskList not wired on the connector engine")
	}
	if vs.Engine.EngineID() != "wechat-acct1" {
		t.Errorf("EngineID = %q, want wechat-acct1", vs.Engine.EngineID())
	}
	if vs.Engine.Dispatcher() == nil {
		t.Error("Dispatcher not wired: the engine would dispatch to nowhere")
	}
	if got := vs.Engine.Thinking(); got != llm.EffortHigh {
		t.Errorf("Thinking = %q, want the model's configured effort %q", got, llm.EffortHigh)
	}
	dump := vs.Engine.DumpAPIRequest()
	if dump == nil {
		t.Fatal("DumpAPIRequest returned nil")
	}
	if dump.WorkingDir != d.workingDir {
		t.Errorf("request WorkingDir = %q, want %q", dump.WorkingDir, d.workingDir)
	}
	if !strings.Contains(dumpText(dump), marker) {
		t.Error("the working dir's CLAUDE.md never reaches the API request (context map empty)")
	}
	if !dumpHasTool(dump, "Bash") {
		t.Errorf("request carries no Bash tool: WireEngine/ToolsProvider wiring missing (%d tools)", len(dump.Tools))
	}
	if vs.Handler == nil {
		t.Error("the connector's TUIHandler must be stored on the view state (switchEngine flips drain roles)")
	}
	if vs.Repl == nil {
		t.Error("Repl snapshot missing on the connector engine")
	}
	// The fresh branch owns its hub: the isolated-hub fallback must stay silent.
	if recs := logs.find("wechat: engine dispatcher is not a *hub.Hub, using isolated hub"); len(recs) != 0 {
		t.Errorf("fresh-build engine fell back to an isolated hub: %d records", len(recs))
	}
}

// The poll loop must actually start: a connector that never polls looks healthy
// and silently stops answering. The offline BaseURL makes the first poll fail
// fast, and that failure line is the proof the goroutine ran.
func TestStartWeChatConnector_StartsPollLoop(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	if recs := waitForAppLogCount(t, logs, "wechat: poll error", 1); len(recs) == 0 {
		t.Error("no poll error after 3s — the connector's poll loop never started")
	}
}

// A store that cannot create the fresh session must be reported: the engine
// would otherwise serve WeChat with whatever session it happened to hold.
func TestStartWeChatConnector_NewSessionFailureWarns(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	rec := exactlyOne(t, logs, "wechat: new session failed", "closed store")
	if rec.attrs["error"] == nil {
		t.Error("the new-session warning must carry the store error")
	}
}

// Daemon mode wires three per-engine pieces: the memory dir override, the
// matching system prompt, and the refresher that re-applies both after a
// config reload. The refresher must be installed (RefreshContext reports
// whether one exists) and must rebuild the same prompt, not clear it.
func TestStartWeChatConnector_DaemonWiresRefresher(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, true)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	const marker = "WECHAT-FIXTURE-CLAUDE-MD"
	if err := os.WriteFile(filepath.Join(d.workingDir, "CLAUDE.md"), []byte(marker), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	wantDir := filepath.Join(d.projectDir, "memory", "wechat-acct1")
	if got := vs.Engine.MemoryDir(); got != wantDir {
		t.Errorf("MemoryDir = %q, want %q", got, wantDir)
	}
	if !vs.Engine.RefreshContext() {
		t.Error("no context refresher installed: a config reload would never land on this engine")
	}
	// The refresh must rebuild, not blank: both halves of the prompt-cache
	// prefix have to survive it.
	if sp := vs.Engine.SystemPrompt(); !strings.Contains(sp, wantDir) {
		t.Errorf("system prompt after refresh = %q, want it to still carry %q", sp, wantDir)
	}
	if !strings.Contains(dumpText(vs.Engine.DumpAPIRequest()), marker) {
		t.Error("the refresh dropped the user-context half: CLAUDE.md no longer reaches the request")
	}
}

// A restored engine's hub lives only as the engine's Dispatcher. If the
// recovery does not land, the connector subscribes to an isolated hub and
// never sees the engine's events — the fallback says so, so its silence is
// the assertion.
func TestStartWeChatConnector_RestoredEngineSharesEngineHub(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	eng, _, err := wuiFactory(&spyProvider{})("wechat-acct1", "wechat-acct1", "", "glm-5")
	if err != nil {
		t.Fatalf("seed engine: %v", err)
	}
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, "restored"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "wechat-acct1", Name: "wechat-acct1"})
	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	if recs := logs.find("wechat: engine dispatcher is not a *hub.Hub, using isolated hub"); len(recs) != 0 {
		t.Errorf("restored engine's hub was not recovered: %d records", len(recs))
	}
	if vs := mgr.Get("wechat-acct1"); vs == nil || vs.Engine.Dispatcher() == nil {
		t.Error("restored engine lost its dispatcher")
	}
}

// countDispatcher is a non-*hub.Hub dispatcher: it satisfies the engine's
// Dispatcher contract but cannot be recovered as the connector's hub.
type countDispatcher struct{ n atomic.Int32 }

func (d *countDispatcher) Dispatch(_ types.QueryEvent) { d.n.Add(1) }

// An engine whose dispatcher is not a *hub.Hub must degrade loudly and still
// get a hub: the connector would otherwise be wired to nil.
func TestStartWeChatConnector_NonHubDispatcherUsesIsolatedHub(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	disp := &countDispatcher{}
	eng := engine.New(&engine.Params{
		Provider:   &spyProvider{},
		Model:      "glm-5",
		EngineID:   "wechat-acct1",
		Logger:     slog.Default(),
		Dispatcher: disp,
	})
	deps := engine.SharedDeps{WorkingDir: projectDir, ShortStore: store}
	refs := engine.CreateTools(deps, task.NewList(""))
	engine.WireEngine(eng, refs, deps.McpReg)
	eng.SetToolRefs(refs)
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, "seed"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "wechat-acct1", Name: "wechat-acct1"})
	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	rec := exactlyOne(t, logs, "wechat: engine dispatcher is not a *hub.Hub, using isolated hub", "non-hub dispatcher")
	if rec.attrs["id"] != "wechat-acct1" {
		t.Errorf("id attr = %v, want wechat-acct1", rec.attrs["id"])
	}
}

// The job-notification wiring WireEngine installs is what makes a finished
// background job reach this engine's attachment queue. Without it the job
// completes and the model never hears about it.
func TestStartWeChatConnector_WiresJobNotifications(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	refs := vs.Engine.ToolRefs()
	if refs.BashReg == nil {
		t.Fatal("BashReg missing on the connector engine's ToolRefs")
	}
	refs.BashReg.OnNotify(bash.JobNotification{JobID: "j1", Status: "completed", Summary: "done"})
	if n := vs.Engine.AttachmentsLen(); n != 1 {
		t.Errorf("attachment queue = %d after a job notification, want 1 (WireEngine wiring missing)", n)
	}
}

// A typed-nil *hub.Hub dispatcher satisfies the type assertion but is not a
// usable hub: the nil check in the recovery is what keeps the connector off it.
func TestStartWeChatConnector_TypedNilDispatcherUsesIsolatedHub(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	var nilHub *hub.Hub
	eng := engine.New(&engine.Params{
		Provider:   &spyProvider{},
		Model:      "glm-5",
		EngineID:   "wechat-acct1",
		Logger:     slog.Default(),
		Dispatcher: nilHub,
	})
	deps := engine.SharedDeps{WorkingDir: projectDir, ShortStore: store}
	refs := engine.CreateTools(deps, task.NewList(""))
	engine.WireEngine(eng, refs, deps.McpReg)
	eng.SetToolRefs(refs)
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, "seed"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "wechat-acct1", Name: "wechat-acct1"})
	logs := captureAppLogs(t)
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	exactlyOne(t, logs, "wechat: engine dispatcher is not a *hub.Hub, using isolated hub", "typed-nil dispatcher")
}

// MaxTurns must stay 0 (no cap): the WeChat engine runs multi-turn tool loops,
// and a negative cap stops the loop before the first LLM call.
func TestStartWeChatConnector_NoTurnCap(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	spy := &spyProvider{}
	d, mediaStores := wechatDeps(t, mgr, store, false)
	d.provider = spy
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine missing, manager holds %v", engineIDs(mgr))
	}
	vs.Engine.QuerySync(context.Background(), "hi", "")
	if spy.streamCalls == 0 {
		t.Error("the WeChat engine made no LLM call: MaxTurns caps the loop before the first turn")
	}
}

// wechat.go:77/80/84 — the deps-sourced Params fields must be the engine's
// actual collaborators. Nil fallbacks in engine.New make clearing them
// silently swap in package defaults: the host logger stops seeing engine
// events, MCPTools() reads an empty view, and the Task tool's list diverges
// from the engine's.
func TestStartWeChatConnector_PinsDepsCollaboratorsIntoEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	d, mediaStores := wechatDeps(t, mgr, store, false)
	t.Cleanup(func() {
		for _, ms := range *mediaStores {
			if ms != nil {
				ms.Close()
			}
		}
	})
	var loggedKeys []string
	d.logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			loggedKeys = append(loggedKeys, a.Key)
			return a
		},
	}))
	reg := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	reg.SetToolsForTest([]mcp.DiscoveredTool{{
		Name: "mcp__srv__thing", OriginalName: "thing", ServerName: "srv",
		InputSchema: json.RawMessage(`{}`),
	}})
	d.mcpRegistry = reg
	if err := startWeChatConnector(d); err != nil {
		t.Fatalf("startWeChatConnector: %v", err)
	}
	vs := mgr.Get("wechat-acct1")
	if vs == nil || vs.Engine == nil {
		t.Fatalf("engine wechat-acct1 missing")
	}
	vs.Engine.EnqueueAttachment(types.QueuedItem{Value: "pin", Mode: types.ItemModePrompt})
	if len(loggedKeys) == 0 {
		t.Error("deps logger saw no engine event — Params.Logger fell back to slog.Default()")
	}
	if got := len(vs.Engine.MCPTools()); got != 1 {
		t.Errorf("MCPTools = %d, want 1 through the deps registry", got)
	}
}
