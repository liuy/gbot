package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	agenttool "github.com/liuy/gbot/pkg/tool/agent"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/tui"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/wui"
)

// wuiFactory builds a fully wired engine (ToolRefs + WireEngine) the way the
// production engineFactory does, so tests can assert on the real tool registry.
func wuiFactory(prov llm.Provider) tui.EngineFactoryFn {
	return func(id, name, providerName, model string) (*engine.Engine, *tui.TUIHandler, error) {
		h, handler := tui.NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Provider:    prov,
			Logger:      slog.Default(),
			Model:       model,
			EngineID:    id,
			Dispatcher:  h,
			TokenBudget: 5000,
			AutoCompact: engine.AutoCompactConfig{ContextWindow: 5000},
		})
		deps := engine.SharedDeps{WorkingDir: filepath.Dir(id), ShortStore: nil}
		refs := engine.CreateTools(deps, task.NewList(""))
		engine.WireEngine(eng, refs, deps.McpReg)
		eng.SetToolRefs(refs)
		return eng, handler, nil
	}
}

// blockingProvider parks inside Stream until release closes, which is how the
// connector test catches the engine mid-query.
type blockingProvider struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingProvider) Name() string { return "blocking" }

func (p *blockingProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	ch := make(chan llm.StreamEvent, 8)
	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText, Text: ""}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "OK"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", Usage: &types.Usage{InputTokens: 10, OutputTokens: 1}, DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
		ch <- llm.StreamEvent{Type: "message_stop"}
		close(ch)
	}()
	return ch, nil
}

func (p *blockingProvider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		ID:         "blocking-resp",
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    []types.ContentBlock{types.NewTextBlock("done")},
		StopReason: "end_turn",
	}, nil
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
	// The two timestamps are what the browser's engine list sorts by; a
	// zero-valued one renders as "1970".
	if vs.CreatedAt.IsZero() || vs.LastActiveAt.IsZero() {
		t.Errorf("CreatedAt/LastActiveAt = %v/%v, want both stamped at create time", vs.CreatedAt, vs.LastActiveAt)
	}
	if vs.LastActiveAt.Before(vs.CreatedAt) {
		t.Errorf("LastActiveAt %v precedes CreatedAt %v", vs.LastActiveAt, vs.CreatedAt)
	}
	// File delivery to the browser goes through artifacts; the Send tool
	// stays exclusive to WeChat-bound engines.
	if _, ok := vs.Engine.ToolRefs().Reg.Lookup("Send"); ok {
		t.Error("Send tool registered on a WUI engine; WUI file delivery must go through artifacts")
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

	// onClose fires exactly once per Engine.Close, which is how the test sees
	// the failed engine being closed rather than leaked.
	var closedSessions []string
	factory := func(id, name, prov, model string) (*engine.Engine, *tui.TUIHandler, error) {
		eng, handler, err := wuiFactory(&spyProvider{})(id, name, prov, model)
		if err != nil {
			return nil, nil, err
		}
		eng.SetOnClose(func(sessionID string) { closedSessions = append(closedSessions, sessionID) })
		return eng, handler, nil
	}

	_, err = createEngineForWUI("doomed", mgr, factory, store, projectDir, wc, "", "")
	if err == nil {
		t.Fatal("createEngineForWUI must fail when the session cannot be created")
	}
	if !strings.HasPrefix(err.Error(), "new session: ") {
		t.Errorf("error = %q, want the \"new session: \" prefix", err.Error())
	}
	// The store error must stay in the chain: callers classify it, and %v
	// would print the same text while severing the wrap.
	if inner := errors.Unwrap(err); inner == nil {
		t.Errorf("error %q does not wrap the underlying session error", err.Error())
	}
	if n := len(mgr.List()); n != 0 {
		t.Errorf("manager holds %d engines after a failed create, want 0 (the engine must be closed, not added)", n)
	}
	if len(closedSessions) != 1 {
		t.Errorf("engine Close ran %d times, want exactly 1 before the error return", len(closedSessions))
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

// TestCreateEngineForWUI_SubscribesConnectorToNewEngine pins the connector
// half of the registration: mgr.Add alone leaves the browser side without a
// slot, so the new engine's events never reach it. BusyReport is the exported
// read-back of that slot — it only lists engines the connector holds.
func TestCreateEngineForWUI_SubscribesConnectorToNewEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})
	prov := &blockingProvider{entered: make(chan struct{}), release: make(chan struct{})}

	id, err := createEngineForWUI("busy-1", mgr, wuiFactory(prov), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("createEngineForWUI: %v", err)
	}
	vs := mgr.Get(id)
	if vs == nil {
		t.Fatal("created engine missing from the manager")
	}
	t.Cleanup(func() { close(prov.release); vs.Engine.Close() })

	vs.Engine.Query(context.Background(), "hello", "")
	<-prov.entered

	rep := wc.BusyReport()
	if !rep.Busy || len(rep.Items) != 1 {
		t.Fatalf("BusyReport = %+v, want exactly one busy item for the new engine", rep)
	}
	if rep.Items[0].EngineID != id || rep.Items[0].Engine != "busy-1" {
		t.Errorf("busy item = %+v, want engine id %q and name %q", rep.Items[0], id, "busy-1")
	}
}

// TestCreateEngineForWUI_PersistMetaFailureIsWarned pins the degradation path:
// meta.json cannot be written (the target is a non-empty directory, so the
// atomic rename fails), and the create must still succeed with a warning.
func TestCreateEngineForWUI_PersistMetaFailureIsWarned(t *testing.T) {
	logs := captureAppLogs(t)
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.MkdirAll(filepath.Join(projectDir, "meta.json", "block"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	mgr := engine.NewEngineManager()
	wc := wui.New(mgr, config.ProviderMap{}, map[string]*config.Provider{})

	id, err := createEngineForWUI("metafail", mgr, wuiFactory(&spyProvider{}), store, projectDir, wc, "", "")
	if err != nil {
		t.Fatalf("a meta.json write failure must not fail the create: %v", err)
	}
	rec := waitForAppLog(t, logs, "wui: persist meta after engine new")
	if rec.level != slog.LevelWarn {
		t.Errorf("log level = %v, want WARN", rec.level)
	}
	if rec.attrs["error"] == nil {
		t.Error("the persist-meta warning must carry the underlying error")
	}
	if vs := mgr.Get(id); vs == nil {
		t.Error("the engine must stay registered despite the meta write failure")
	}
}

func TestDeleteSessionsFor_DeletesOnlyThatEngine(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateSessionWithEngine(projectDir, "m", "e2"); err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	main, err := store.CreateSessionWithEngine(projectDir, "m", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	fn := deleteSessionsFor(store, projectDir)
	if err := fn("e2"); err != nil {
		t.Fatalf("deleteSessionsFor: %v", err)
	}
	left, err := store.ListSessionsByEngine(projectDir, "e2", 10)
	if err != nil {
		t.Fatalf("ListSessionsByEngine: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("e2 sessions left = %d, want 0", len(left))
	}
	mainLeft, err := store.ListSessionsByEngine(projectDir, "main", 10)
	if err != nil {
		t.Fatalf("ListSessionsByEngine: %v", err)
	}
	if len(mainLeft) != 1 || mainLeft[0].SessionID != main.SessionID {
		t.Fatalf("main sessions = %+v, want the one seeded session", mainLeft)
	}
}

func TestDeleteSessionsFor_PropagatesStoreErrors(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fn := deleteSessionsFor(store, projectDir)
	err = fn("e2")
	if err == nil {
		t.Fatal("deleteSessionsFor must forward the store error, got nil")
	}
	if err.Error() != "query engine sessions: sql: database is closed" {
		t.Fatalf("err = %q", err.Error())
	}
}
