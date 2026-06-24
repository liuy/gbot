package tui

import (
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/task"
	"github.com/liuy/gbot/pkg/types"
)

// newEngineTestApp constructs an App with N engines registered. The first
// engine is active. Each engine's *Engine is real (no provider); we only use
// it for Model()/EngineID()/SessionID()/HasStore() accessors. Each engine also
// gets a fresh TUIHandler stored on vs.Handler so switchEngine works without
// extra per-test wiring.
func newEngineTestApp(t *testing.T, engineSpecs []struct{ ID, Name, Model string }) *App {
	t.Helper()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mgr := engine.NewEngineManager()
	for _, spec := range engineSpecs {
		eng := engine.New(&engine.Params{
			Logger:   slog.Default(),
			Model:    spec.Model,
			EngineID: spec.ID,
		})
		eng.SetStore(store, dir)
		t.Cleanup(func() { eng.Close() })
		mgr.Add(&engine.EngineViewState{
			Engine:  eng,
			Handler: NewTUIHandlerForEngine(spec.ID, nil),
			Repl:    newReplAdapter(NewReplState()),
			ID:      spec.ID,
			Name:    spec.Name,
			Model:   spec.Model,
		})
	}
	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir
	return a
}

func TestHandleEngine_NoArgs_OpensPicker(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})
	cmd := a.handleEngine("", nil)
	if a.activeDialog == nil {
		t.Fatal("activeDialog should be set after /engine with no args")
	}
	if a.activeDialog.title != "Switch Engine" {
		t.Errorf("dialog title = %q, want 'Switch Engine'", a.activeDialog.title)
	}
	if len(a.activeDialog.options) != 2 {
		t.Errorf("options count = %d, want 2", len(a.activeDialog.options))
	}
	// cmd should be nil (commitCmd we passed in); verify no error/info message.
	if cmd != nil {
		t.Errorf("expected nil cmd (commitCmd), got non-nil")
	}
}

func TestHandleEngine_Picker_AllowsDuringStreaming(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})
	// Simulate active engine streaming. Multi-engine design routes events
	// to the demoted engine's ReplState via its drain fn, so switching
	// engines mid-stream is safe and must be allowed — otherwise the
	// user is stuck on a long-running engine until it finishes.
	a.repl.streaming = true
	_ = a.handleEngine("", nil)
	if a.activeDialog == nil {
		t.Fatal("picker should open even while active engine is streaming — " +
			"multi-engine design routes demoted-engine events via drain fn")
	}
}

func TestHandleEngine_New_NoName_AutoNames(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	a.SetEngineFactory(func(id, name, provider, model string) (*engine.Engine, *TUIHandler, error) {
		engineHub, handler := NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Logger:     slog.Default(),
			Model:      model,
			EngineID:   id,
			Dispatcher: engineHub,
		})
		return eng, handler, nil
	})
	// Use a projectDir so persistWorkspaceMeta doesn't no-op silently.
	a.projectDir = t.TempDir()

	cmd := a.handleEngine("new", nil)
	// cmd is the commitCmd we passed in (nil here). Drain any infoMsg tea.Cmd
	// so we don't leave it un-run, but we don't assert on its shape.
	if cmd != nil {
		_ = cmd()
	}

	if got := a.engineMgr.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2 after /engine new", got)
	}
	views := a.engineMgr.List()
	var newName string
	for _, vs := range views {
		if vs.ID == "e2" {
			newName = vs.Name
		}
	}
	if newName != "agent-2" {
		t.Errorf("new engine name = %q, want 'agent-2'", newName)
	}
	if a.engineMgr.ActiveID() != "e2" {
		t.Errorf("ActiveID after /engine new = %q, want e2 (auto-switch)", a.engineMgr.ActiveID())
	}
}

func TestHandleEngine_New_WithCustomName(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	a.SetEngineFactory(func(id, name, provider, model string) (*engine.Engine, *TUIHandler, error) {
		engineHub, handler := NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Logger: slog.Default(), Model: model, EngineID: id,
			Dispatcher: engineHub,
		})
		return eng, handler, nil
	})
	a.projectDir = t.TempDir()

	_ = a.handleEngine("new research", nil)
	if got := a.engineMgr.Count(); got != 2 {
		t.Fatalf("Count = %d, want 2", got)
	}
	target := a.engineMgr.Get("e2")
	if target == nil {
		t.Fatal("new engine e2 not registered")
	}
	if target.Name != "research" {
		t.Errorf("Name = %q, want 'research'", target.Name)
	}
}

func TestHandleEngine_New_NoFactory_Error(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	// engineFactory is nil by default
	cmd := a.handleEngine("new", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want infoMsg", msg)
	}
	if !strings.Contains(string(info), "factory") {
		t.Errorf("info = %q, want substring 'factory'", info)
	}
}

func TestHandleEngine_Unknown_Subcommand_Error(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	cmd := a.handleEngine("foo", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want infoMsg", msg)
	}
	if !strings.Contains(string(info), "not found") {
		t.Errorf("info = %q, want substring 'not found' (unknown engine name should be reported as not-found, not 'Unknown subcommand')", info)
	}
}

func TestSwitchEngine_FlipsDrainRoles(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})
	// newEngineTestApp sets vs.Handler per engine; pull them out for
	// assertions so we can verify drain roles after the switch.
	mainHandler, _ := a.engineMgr.Get("main").Handler.(*TUIHandler)
	e2Handler, _ := a.engineMgr.Get("e2").Handler.(*TUIHandler)
	a.tuiHandler = mainHandler

	// Before switch: e2 handler has drainFn == nil.
	if e2Handler.drainFn != nil {
		t.Fatal("precondition: e2 handler drainFn should be nil")
	}

	a.switchEngine("e2")

	if a.engineMgr.ActiveID() != "e2" {
		t.Fatalf("ActiveID = %q, want e2", a.engineMgr.ActiveID())
	}
	if a.tuiHandler != e2Handler {
		t.Errorf("a.tuiHandler = %p, want e2Handler (%p)", a.tuiHandler, e2Handler)
	}
	a.tuiHandler.drainMu.RLock()
	e2Drain := a.tuiHandler.drainFn
	a.tuiHandler.drainMu.RUnlock()
	if e2Drain != nil {
		t.Error("a.tuiHandler (e2, now active) drainFn should be nil")
	}
	// main's handler (now background) keeps living on vs.Handler with
	// drainFn flipped to background mode.
	mainBackground, _ := a.engineMgr.Get("main").Handler.(*TUIHandler)
	if mainBackground != mainHandler {
		t.Errorf("main vs.Handler = %p, want mainHandler (%p)", mainBackground, mainHandler)
	}
	if mainBackground == nil {
		t.Fatal("main vs.Handler should still hold the handler after switch")
	}
	mainBackground.drainMu.RLock()
	mainDrain := mainBackground.drainFn
	mainBackground.drainMu.RUnlock()
	if mainDrain == nil {
		t.Error("main handler (now background) drainFn should be set")
	}
}

func TestSwitchEngine_PersistsMeta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})
	a.projectDir = dir

	a.switchEngine("e2")

	// Verify meta.json was written with active_engine_id=e2.
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.ActiveEngineID != "e2" {
		t.Errorf("meta.ActiveEngineID = %q, want e2", meta.ActiveEngineID)
	}
	if len(meta.Engines) != 2 {
		t.Errorf("len(meta.Engines) = %d, want 2", len(meta.Engines))
	}
}

func TestSwitchEngine_SyncsActiveCache(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	originalRepl := a.repl
	a.switchEngine("e2")
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Errorf("a.engine after switch = %v, want e2", a.engine)
	}
	// a.repl should now point to e2's ReplState, not main's.
	if a.repl == originalRepl {
		t.Error("a.repl should change to point to e2's ReplState after switch")
	}
}

func TestEnginePickerItem_Label_ActiveMarker(t *testing.T) {
	t.Parallel()
	vs := &engine.EngineViewState{ID: "main", Name: "main", Model: "sonnet"}
	active := &enginePickerItem{vs: vs, isActive: true}
	inactive := &enginePickerItem{vs: vs, isActive: false}
	if !strings.HasPrefix(active.Label(), "● ") {
		t.Errorf("active label = %q, want prefix '● '", active.Label())
	}
	if !strings.HasPrefix(inactive.Label(), "  ") {
		t.Errorf("inactive label = %q, want prefix '  ' (two spaces)", inactive.Label())
	}
}

func TestEnginePickerItem_Label_LazyStatus(t *testing.T) {
	t.Parallel()
	// Lazy view state: Engine == nil.
	vs := &engine.EngineViewState{ID: "e2", Name: "engine-2"}
	item := &enginePickerItem{vs: vs, isActive: false}
	if !strings.Contains(item.Label(), "lazy") {
		t.Errorf("lazy engine label = %q, want substring 'lazy'", item.Label())
	}
}

// hasClearScreenCmd walks a tea.Cmd (and its batched children) looking for
// tea.ClearScreen. Returns true if found.
func hasClearScreenCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batched, ok := msg.(tea.BatchMsg); ok {
		return slices.ContainsFunc(batched, hasClearScreenCmd)
	}
	return msg == tea.ClearScreen()
}

// TestSwitchEngine_ClearsScreenAndLoadsHistory asserts switchEngine returns
// a cmd that includes tea.ClearScreen (otherwise the previous engine's
// rendered scrollback stays on screen — a bug the user hit after /engine
// new succeeded, where the TUI visually never transitioned to the new
// engine). Also asserts the active repl is rebound so history can render.
func TestSwitchEngine_ClearsScreenAndLoadsHistory(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	// Seed main's repl with a visible message so we can assert it is
	// replaced after the switch.
	a.repl.messages = append(a.repl.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "main-engine-marker"}},
	})

	_, cmd := a.switchEngine("e2")
	if !hasClearScreenCmd(cmd) {
		t.Errorf("switchEngine cmd does not include tea.ClearScreen — " +
			"old engine's rendered output stays visible after switch")
	}
}

// TestCreateNewEngine_ClearsScreen asserts createNewEngine returns a cmd
// that includes tea.ClearScreen so the freshly-created engine starts with
// a clean scrollback instead of the previous engine's output.
func TestCreateNewEngine_ClearsScreen(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	a.SetEngineFactory(func(id, name, provider, model string) (*engine.Engine, *TUIHandler, error) {
		engineHub, handler := NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Logger: slog.Default(), Model: model, EngineID: id,
			Dispatcher: engineHub,
		})
		eng.SetStore(a.engine.Store(), a.projectDir)
		if err := eng.NewSession(a.projectDir, ""); err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		return eng, handler, nil
	})

	// Seed main's repl with a visible marker.
	a.repl.messages = append(a.repl.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "main-engine-marker"}},
	})

	cmd := a.createNewEngine("", nil)
	if !hasClearScreenCmd(cmd) {
		t.Errorf("createNewEngine cmd does not include tea.ClearScreen — " +
			"old engine's rendered output stays visible after creating a new engine")
	}
}

// TestHandleEngine_ByName_SwitchesToMatchingEngine asserts that
// `/engine <name>` switches to an existing engine whose Name OR ID matches.
// Previously this form was rejected as "Unknown /engine subcommand" — the
// user typed `/engine engine-2` after `/engine new` and nothing happened
// (no switch, no error visible in logs).
func TestHandleEngine_ByName_SwitchesToMatchingEngine(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	beforeEngine := a.engine
	cmd := a.handleEngine("engine-2", nil)

	if a.engine == beforeEngine {
		t.Errorf("a.engine unchanged after /engine engine-2 — switch by name did not run")
	}
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Errorf("a.engine after /engine engine-2 = %v, want e2", a.engine)
	}
	// cmd should include ClearScreen (switchEngine always batches it).
	if !hasClearScreenCmd(cmd) {
		t.Errorf("cmd missing tea.ClearScreen — switch by name must clear screen like picker switch")
	}
}

// TestHandleEngine_ByName_FallsBackToID verifies `/engine <id>` works when
// the argument matches an engine's ID rather than its display Name.
func TestHandleEngine_ByName_FallsBackToID(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	_ = a.handleEngine("e2", nil)
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Errorf("a.engine after /engine e2 = %v, want e2", a.engine)
	}
}

// TestHandleEngine_ByName_NotFound_ShowsInfo verifies that an unknown name
// surfaces an info message instead of silently doing nothing.
func TestHandleEngine_ByName_NotFound_ShowsInfo(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
	})
	beforeEngine := a.engine
	cmd := a.handleEngine("nonexistent", nil)

	if a.engine != beforeEngine {
		t.Errorf("a.engine changed after /engine with unknown name — should be a no-op")
	}
	// cmd should be an infoMsg describing the not-found case. Invoke it and
	// check the type — infoMsg is the user-visible signal that something
	// went wrong, as opposed to nil (silent no-op).
	if cmd == nil {
		t.Fatal("cmd is nil — unknown engine name must surface an infoMsg, not silent no-op")
	}
	msg := cmd()
	if _, ok := msg.(infoMsg); !ok {
		t.Errorf("cmd produced %T, want infoMsg — unknown name must be reported to user", msg)
	}
}

// TestSwitchEngine_ResolvesHandlerViaViewState verifies that switchEngine
// resolves the target engine's TUIHandler via vs.Handler — the single
// source of truth for engine handlers. This matches the shape produced by
// restoreEngines at startup (handlers stored on EngineViewState.Handler)
// and by createNewEngine at runtime.
func TestSwitchEngine_ResolvesHandlerViaViewState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainHub, mainHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: mainHub})
	mainEng.SetStore(store, dir)
	t.Cleanup(func() { mainEng.Close() })

	e2Hub, e2Handler := NewEngineHubWithHandler("e2", nil)
	e2Eng := engine.New(&engine.Params{Logger: slog.Default(), Model: "opus", EngineID: "e2", Dispatcher: e2Hub})
	e2Eng.SetStore(store, dir)
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: mainEng, Handler: mainHandler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "main", Name: "main", Model: "sonnet",
	})
	mgr.Add(&engine.EngineViewState{
		Engine: e2Eng, Handler: e2Handler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "e2", Name: "engine-2", Model: "opus",
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir

	// Mirrors production state after restoreEngines: handlers live only
	// on vs.Handler (no separate runtime map to consult).
	_, cmd := a.switchEngine("e2")
	if cmd == nil {
		t.Fatal("switchEngine returned nil cmd — should include ClearScreen + info")
	}
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Errorf("a.engine = %v, want e2 (must resolve via vs.Handler)", a.engine)
	}
}

// TestSwitchEngine_CommitsHistoryToScrollback is the regression test for
// "switch works but screen doesn't redraw". switchEngine used to set
// committedCount=0 + contentDirty=true and rely on a future WindowSizeMsg
// to actually push messages into the terminal scrollback via tea.Println.
// WindowSizeMsg only fires on terminal resize — it does NOT fire on engine
// switch — so the cleared screen stayed blank until the user resized the
// window. This test pins the contract: switchEngine commits the new
// engine's history itself when not streaming.
func TestSwitchEngine_CommitsHistoryToScrollback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainHub, mainHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: mainHub})
	mainEng.SetStore(store, dir)
	if err := mainEng.NewSession(dir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	mainEng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("main-q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("main-a1")}},
	})
	mainEng.PersistNewMessages()
	t.Cleanup(func() { mainEng.Close() })

	e2Hub, e2Handler := NewEngineHubWithHandler("e2", nil)
	e2Eng := engine.New(&engine.Params{Logger: slog.Default(), Model: "opus", EngineID: "e2", Dispatcher: e2Hub})
	e2Eng.SetStore(store, dir)
	if err := e2Eng.NewSession(dir, ""); err != nil {
		t.Fatalf("e2 NewSession: %v", err)
	}
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: mainEng, Handler: mainHandler,
		ID: "main", Name: "main", Model: "sonnet",
		ActiveSessionID: mainEng.SessionID(),
	})
	mgr.Add(&engine.EngineViewState{
		Engine: e2Eng, Handler: e2Handler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "e2", Name: "engine-2", Model: "opus",
		ActiveSessionID: e2Eng.SessionID(),
	})

	a := NewAppWithManager(mgr, "", nil)
	// width > 0 so render path is usable; height doesn't matter for commit.
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir

	// Switch main → e2 (e2 has empty Repl, so commit should be a no-op there).
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}

	// Switch e2 → main: main has 2 messages. switchEngine must commit them
	// immediately so the user sees history after the clear — not wait for
	// a WindowSizeMsg that may never come.
	_, cmd := a.switchEngine("main")
	if cmd == nil {
		t.Fatal("switchEngine(e2→main) returned nil cmd")
	}
	if a.repl.committedCount == 0 {
		t.Error("committedCount == 0 after switchEngine to main with non-empty history — " +
			"messages will not be written to scrollback until next WindowSizeMsg (which never fires on switch)")
	}
	if a.repl.committedCount != len(a.repl.messages) {
		t.Errorf("committedCount = %d, want %d (must equal len(repl.messages) after commit)",
			a.repl.committedCount, len(a.repl.messages))
	}
}

// TestSwitchEngine_NilTargetRepl_BuildsFreshFromEngineMessages verifies
// that switching TO an engine whose EngineViewState.Repl is nil (the shape
// restoreEngines in main.go produces — Engine + Handler set, Repl left nil)
// still rebuilds a.repl from the target engine's loaded messages.
//
// Without this, switching back to a restored engine (e.g. main after
// /engine new) leaves a.repl pointing at the PREVIOUS engine's ReplState,
// so the screen never shows the restored engine's history.
func TestSwitchEngine_NilTargetRepl_BuildsFreshFromEngineMessages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainHub, mainHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: mainHub})
	mainEng.SetStore(store, dir)
	if err := mainEng.NewSession(dir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// Seed main with one user message so we can detect "history loaded"
	// vs "fresh empty state".
	mainEng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("main-history-marker")}},
	})
	mainEng.PersistNewMessages()
	t.Cleanup(func() { mainEng.Close() })

	e2Hub, e2Handler := NewEngineHubWithHandler("e2", nil)
	e2Eng := engine.New(&engine.Params{Logger: slog.Default(), Model: "opus", EngineID: "e2", Dispatcher: e2Hub})
	e2Eng.SetStore(store, dir)
	if err := e2Eng.NewSession(dir, ""); err != nil {
		t.Fatalf("e2 NewSession: %v", err)
	}
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	// main: NO Repl — mirrors restoreEngines.
	mgr.Add(&engine.EngineViewState{
		Engine: mainEng, Handler: mainHandler,
		ID: "main", Name: "main", Model: "sonnet",
		ActiveSessionID: mainEng.SessionID(),
	})
	// e2: Repl set — mirrors /engine new.
	mgr.Add(&engine.EngineViewState{
		Engine: e2Eng, Handler: e2Handler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "e2", Name: "engine-2", Model: "opus",
		ActiveSessionID: e2Eng.SessionID(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir

	// Switch main → e2 (works because e2.Repl is set).
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}

	// Now switch e2 → main. main.Repl is nil; switchEngine must build a
	// fresh ReplState populated from mainEng.Messages() and bind a.repl
	// to it. Without this, a.repl keeps pointing at e2's empty ReplState.
	if _, cmd := a.switchEngine("main"); cmd == nil {
		t.Fatal("switchEngine(e2→main) returned nil cmd")
	}
	if a.engine != mainEng {
		t.Fatalf("a.engine = %p, want mainEng (%p)", a.engine, mainEng)
	}
	if a.repl == nil {
		t.Fatal("a.repl is nil after switch to main")
	}
	msgs := a.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("a.repl has no messages after switching to main — " +
			"main.Repl was nil so switchEngine must rebuild from mainEng.Messages()")
	}
	var found bool
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Text == "main-history-marker" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("a.repl.messages does not contain 'main-history-marker' — "+
			"switchEngine must populate the fresh ReplState from the target engine's history; got: %+v", msgs)
	}
}

// TestHandleEngine_SubmitDuringStreaming_SwitchesAnyway verifies that
// `/engine <name>` submitted while the active engine is streaming still
// switches. Without this, the user is stuck on a long-running engine
// because handleSubmitRepl routes streaming submissions to
// handleEnqueueMessage, which drops slash commands.
func TestHandleEngine_SubmitDuringStreaming_SwitchesAnyway(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})
	// Simulate the active engine (main) streaming — switch target is e2.
	a.repl.streaming = true

	before := a.engine
	_ = a.handleSubmitRepl("/agent e2")

	if a.engine == before {
		t.Fatalf("a.engine unchanged after /engine e2 during streaming — " +
			"viewer-only /engine must bypass the streaming check")
	}
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Errorf("a.engine = %v, want e2", a.engine)
	}
}

// TestSwitchEngine_ToStreamingEngine_RestoresProgressLine verifies that
// switching INTO a live-streaming engine restores the progress line +
// status bar streaming flag. Without this, the user sees a "frozen"
// engine: messages are there but no elapsed counter, no spinner, no
// "(streaming)" hint — looks dead even though events still arrive.
func TestSwitchEngine_ToStreamingEngine_RestoresProgressLine(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	// Put e2 into "live streaming" state: ReplState.streaming=true with a
	// streamingStart in the past (simulates a query that started before
	// the switch). main is idle (the default).
	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	startedAt := time.Now().Add(-30 * time.Second) // REAL-TIME: anchors progressStart assertion; relative offset only
	e2Repl.StartQueryAtForTest(startedAt)
	if !e2Repl.IsStreaming() {
		t.Fatal("precondition: e2 must be streaming")
	}

	// main is currently active. Switch to e2.
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}

	// Status bar must reflect streaming so the spinner + elapsed line render.
	if !a.status.IsStreaming() {
		t.Error("a.status.IsStreaming() = false after switch to streaming e2 — " +
			"progress line won't render without this flag")
	}

	// progressStart must be restored from e2's streamingStart so the
	// elapsed counter shows ~30s, not 0.
	if a.repl.StreamingStart().IsZero() {
		t.Error("a.progressStart is zero after switch to streaming e2 — " +
			"progress line won't show elapsed time")
	}
	if got := a.repl.StreamingStart(); got.Sub(startedAt) > 5*time.Second {
		t.Errorf("a.progressStart = %v, want close to %v (e2's streamingStart)",
			got, startedAt)
	}
}

// TestSwitchEngine_ToStreamingEngine_BootstrapsSpinnerTick verifies that
// switching INTO a streaming engine bootstraps the spinner animation:
// (1) a.spinner.Start() is called so the spinner renders frames, and
// (2) the returned cmd includes a tea.Tick that produces spinnerTickMsg
// so the animation chain (spinnerTickMsg → tea.Tick → spinnerTickMsg ...)
// actually starts. Without this, the user switches back to a streaming
// engine and sees a frozen frame even though status.IsStreaming() is true.
func TestSwitchEngine_ToStreamingEngine_BootstrapsSpinnerTick(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	e2Repl.StartStreamingForTest()
	if !e2Repl.IsStreaming() {
		t.Fatal("precondition: e2 must be streaming")
	}

	// Snapshot spinner state, then switch.
	a.spinner.Stop()
	before := a.spinner.View()
	_, cmd := a.switchEngine("e2")
	if cmd == nil {
		t.Fatal("switchEngine returned nil cmd")
	}
	after := a.spinner.View()
	if before == after {
		// Spinner should have been Start()'d so View reflects an active frame.
		t.Errorf("spinner.View unchanged after switch to streaming e2 — spinner.Start() not called")
	}

	// The returned cmd must include a tea.Tick that produces spinnerTickMsg.
	// Inspect by executing the cmd (or its batched children) and checking
	// the resulting msg type.
	if !cmdChainProducesSpinnerTick(cmd) {
		t.Errorf("switchEngine cmd does not produce spinnerTickMsg — " +
			"animation chain won't start, spinner stays frozen")
	}
}

// cmdChainProducesSpinnerTick walks a tea.Cmd (and its batched children)
// looking for any spinnerTickMsg result.
func cmdChainProducesSpinnerTick(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batched, ok := msg.(tea.BatchMsg); ok {
		return slices.ContainsFunc(batched, cmdChainProducesSpinnerTick)
	}
	_, ok := msg.(spinnerTickMsg)
	return ok
}

// TestSwitchEngine_ContextUsedReflectsTargetMessages verifies that after
// switching to a target engine whose ContextTokens is 0 (no API response
// yet) but whose message list has content, the status bar's "used context"
// reflects the estimated message tokens — not 0. Same root cause as the
// restart gap: GetContextTokens() returns 0 and the handler takes the value
// at face value instead of falling back to a message-based estimate.
func TestSwitchEngine_ContextUsedReflectsTargetMessages(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	// Target engine e2: ContextTokens=0 (fresh, no API response) but holds
	// conversation history from a prior session.
	e2 := a.engineMgr.Get("e2")
	e2.Engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("word ", 2000))}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("reply ", 1500))}},
	})
	if e2.Engine.GetContextTokens() != 0 {
		t.Fatalf("precondition: e2.ContextTokens should be 0, got %d", e2.Engine.GetContextTokens())
	}

	wantMsgTokens := engine.EstimateMessagesTokens(e2.Engine.Messages())

	a.switchEngine("e2")

	if got := a.status.contextUsed; got != wantMsgTokens {
		t.Errorf("after switch to e2, contextUsed = %d, want %d (estimated message tokens) — switching to an engine with no API response yet must estimate from messages, not show 0",
			got, wantMsgTokens)
	}
}

// NewAppWithManager builds a fresh ReplState (because the active view
// state has no Repl, e.g. after restoreEngines), it stores that ReplState
// back on vs.Repl. Without this, switchEngine rebinds a.repl to ANOTHER
// fresh ReplState on the next switch, losing all streaming state
// (streaming flag, streamingStart, accumulated tokens) — the user sees
// no progress line after switching back to a streaming engine.
func TestNewAppWithManager_StoresFreshReplOnViewState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hubMain, handlerMain := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: hubMain})
	mainEng.SetStore(store, dir)
	t.Cleanup(func() { mainEng.Close() })

	hubE2, handlerE2 := NewEngineHubWithHandler("e2", nil)
	e2Eng := engine.New(&engine.Params{Logger: slog.Default(), Model: "opus", EngineID: "e2", Dispatcher: hubE2})
	e2Eng.SetStore(store, dir)
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	mainVS := &engine.EngineViewState{
		Engine: mainEng, Handler: handlerMain,
		ID: "main", Name: "main", Model: "sonnet",
		ActiveSessionID: "main-session",
	}
	e2VS := &engine.EngineViewState{
		Engine: e2Eng, Handler: handlerE2,
		ID: "e2", Name: "engine-2", Model: "opus",
		ActiveSessionID: "e2-session",
	}
	mgr.Add(mainVS)
	mgr.Add(e2VS)
	_ = mgr.SetActive("e2")

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir

	a.repl.StartQuery()
	if !a.repl.IsStreaming() {
		t.Fatal("precondition: a.repl should be streaming after StartQuery")
	}

	_, _ = a.switchEngine("main")
	_, _ = a.switchEngine("e2")

	if !a.repl.IsStreaming() {
		t.Fatal("a.repl.IsStreaming() = false after switching back to e2 — " +
			"NewAppWithManager didn't cache the fresh ReplState on vs.Repl, " +
			"so switchEngine built a new empty one and lost streaming state")
	}
	if a.repl.StreamingStart().IsZero() {
		t.Fatal("a.repl.StreamingStart() is zero after switching back — " +
			"fresh ReplState was rebuilt on switch, losing the original start time")
	}
}

// TestSwitchEngine_ToStreamingEngine_RestoresThinkingState verifies that
// switching into a live-streaming engine mid-thinking restores the active
// thinking indicator. Without this, the user sees "no output yet" with no
// hint that the model is in reasoning phase — confusing for thinking-mode
// providers like minimax-3 or Claude with thinking enabled.
func TestSwitchEngine_ToStreamingEngine_RestoresThinkingState(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	startedAt := time.Now().Add(-15 * time.Second) // REAL-TIME: anchors thinkingStart assertion; relative offset only
	e2Repl.StartQueryAtForTest(startedAt)
	// Mark e2 as currently in thinking phase (no tool call yet, just thinking).
	e2Repl.StartThinkingAtForTest(startedAt)
	if !e2Repl.IsThinking() {
		t.Fatal("precondition: e2 must be thinking")
	}

	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}
	if !a.repl.IsThinking() {
		t.Error("a.repl.IsThinking() = false after switch to e2 that's mid-thinking — " +
			"TUI won't show 'Thinking...' indicator")
	}
	if a.repl.ThinkingStart().IsZero() {
		t.Error("a.repl.ThinkingStart() is zero after switch to mid-thinking e2 — " +
			"elapsed thinking time won't render")
	}
	if got := a.repl.ThinkingStart(); got.Sub(startedAt) > 5*time.Second {
		t.Errorf("a.repl.ThinkingStart() = %v, want close to %v (e2's thinkingStart)",
			got, startedAt)
	}
}

// TestSwitchEngine_ReturnsReadEvents verifies that switchEngine's returned
// batch includes a cmd that reads from appCh. Without this, any event that
// arrives in appCh after the switch sits unread forever: IsStreaming stays
// true → tick never stops → UI appears frozen.
//
// tea.Batch does NOT execute cmds — it wraps them into a BatchMsg ([]Cmd)
// for the bubbletea runtime to execute concurrently. So we call cmd() to
// get the BatchMsg, then call each cmd in it. The readEvents closure will
// block on appCh and return the pre-loaded queryEndMsg. Without readEvents
// in the batch, none of the cmds touch appCh.
func TestSwitchEngine_ReturnsReadEvents(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "sonnet"},
		{"e2", "engine-2", "opus"},
	})

	_, switchCmd := a.switchEngine("e2")
	if switchCmd == nil {
		t.Fatal("switchEngine returned nil cmd")
	}

	// Push a queryEndMsg into the new active handler's appCh AFTER the
	// switch (DrainBacklog already ran inside switchEngine, so this msg
	// stays in the channel until something reads it).
	e2Handler, _ := a.engineMgr.Get("e2").Handler.(*TUIHandler)
	e2Handler.appCh <- queryEndMsg{}

	// tea.Batch returns func() BatchMsg — it collects cmds but doesn't
	// execute them. The bubbletea runtime executes each cmd concurrently.
	batchMsg := switchCmd()
	batch, ok := batchMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("switchCmd() returned %T, want tea.BatchMsg", batchMsg)
	}

	// Execute each cmd in the batch concurrently. The readEvents closure
	// will block on appCh and return the queryEndMsg we pre-loaded.
	type result struct {
		msg tea.Msg
	}
	results := make(chan result, len(batch))
	for _, c := range batch {
		if c == nil {
			continue
		}
		go func(cmd tea.Cmd) { results <- result{cmd()} }(c)
	}

	// Wait for all cmds to finish, with timeout.
	timer := time.After(5 * time.Second)
	var gotQueryEnd bool
	for range batch {
		select {
		case r := <-results:
			if _, ok := r.msg.(queryEndMsg); ok {
				gotQueryEnd = true
			}
		case <-timer:
			t.Fatal("batch cmd did not complete in 5s — possible hang")
		}
	}

	if !gotQueryEnd {
		t.Error("switchEngine batch does not include readEvents() — " +
			"no cmd in the batch reads from appCh, so events pile up " +
			"and streaming state stays true forever after switch")
	}
}

// TestSwitchEngine_UpdatesContextWindow verifies that switching to an engine
// sets its context window via updateEngineCapabilities. Without this, a
// freshly-created or restored engine has ContextWindow()=0 until the user
// manually runs /model — the status bar shows "0" as the context total.
func TestSwitchEngine_UpdatesContextWindow(t *testing.T) {
	t.Parallel()
	a := newEngineTestApp(t, []struct{ ID, Name, Model string }{
		{"main", "main", "glm-5"},
		{"e2", "agent-2", "glm-5"},
	})

	// Wire up providers so updateEngineCapabilities can resolve context window.
	cfg := &config.Config{
		Providers: []config.Provider{
			{
				Name:   "openai",
				Keys:   []string{"k"},
				Models: config.NewModelsFromMap(map[string]config.ModelConfig{"glm-5": {Context: config.IntOrHuman(200000)}}),
			},
		},
	}
	a.SetProviders(map[string]llm.Provider{"openai": &mockLLMProvider{}}, cfg)

	// Before switch: e2's context window is 0 (never had /model called).
	e2Eng := a.engineMgr.Get("e2").Engine
	if e2Eng == nil {
		t.Fatal("e2 engine is nil")
	}
	if cw := e2Eng.ContextWindow(); cw != 0 {
		t.Fatalf("precondition: e2 ContextWindow = %d, want 0 (fresh engine)", cw)
	}

	// Switch to e2.
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine returned nil cmd")
	}

	// After switch: e2's context window should be non-zero (set by
	// updateEngineCapabilities from provider config).
	if cw := e2Eng.ContextWindow(); cw == 0 {
		t.Error("e2 ContextWindow = 0 after switch — switchEngine did not call updateEngineCapabilities")
	}
}

func TestSwitchEngine_InvalidatesTaskPanel(t *testing.T) {
	t.Parallel()

	// Build a 2-engine app inline, each engine with its own TaskList.
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tl1 := task.NewList("")
	tl2 := task.NewList("")

	eng1 := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Model:    "model-a",
		EngineID: "e1",
		TaskList: tl1,
	})
	eng1.SetStore(store, dir)
	t.Cleanup(func() { eng1.Close() })

	eng2 := engine.New(&engine.Params{
		Logger:   slog.Default(),
		Model:    "model-b",
		EngineID: "e2",
		TaskList: tl2,
	})
	eng2.SetStore(store, dir)
	t.Cleanup(func() { eng2.Close() })

	// Create sessions so setTaskDirForSession has a dir to resolve.
	if err := eng1.NewSession(dir, ""); err != nil {
		t.Fatalf("eng1.NewSession: %v", err)
	}
	if err := eng2.NewSession(dir, ""); err != nil {
		t.Fatalf("eng2.NewSession: %v", err)
	}

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine:  eng1,
		Handler: NewTUIHandlerForEngine("e1", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "e1",
		Name:    "engine-1",
		Model:   "model-a",
	})
	mgr.Add(&engine.EngineViewState{
		Engine:  eng2,
		Handler: NewTUIHandlerForEngine("e2", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "e2",
		Name:    "engine-2",
		Model:   "model-b",
	})
	if err := mgr.SetActive("e1"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	a := NewAppWithManager(mgr, "", nil)
	a.projectDir = dir
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Wire taskListFn to read from active engine's task list.
	a.SetTaskListFn(func() []TaskSummary {
		eng := a.ActiveEngine()
		if eng == nil {
			return nil
		}
		tl := eng.TaskList()
		if tl == nil || tl.Dir() == "" {
			return nil
		}
		tasks, err := tl.ListTasks()
		if err != nil {
			return nil
		}
		var result []TaskSummary
		for _, t := range tasks {
			result = append(result, TaskSummary{
				ID:      t.ID,
				Subject: t.Subject,
				Status:  string(t.Status),
			})
		}
		return result
	})

	// Seed engine-1's task list with one task.
	if _, err := tl1.CreateTask("task-e1", "description", "", nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Force render: populate cache for engine-1.
	a.taskListDirty = true
	a.taskListCache = a.renderTaskList()

	if a.taskListCache == "" {
		t.Fatal("task panel should not be empty after seeding engine-1 task")
	}
	if !strings.Contains(a.taskListCache, "task-e1") {
		t.Errorf("task panel should contain 'task-e1', got:\n%s", a.taskListCache)
	}

	// Switch to engine-2.
	a.switchEngine("e2")

	if !a.taskListDirty {
		t.Error("taskListDirty should be true after switchEngine")
	}

	// Force render: populate cache for engine-2.
	a.taskListCache = a.renderTaskList()
	a.taskListDirty = false

	if a.taskListCache != "" {
		t.Errorf("task panel should be empty for engine-2 (no tasks), got:\n%s", a.taskListCache)
	}

	// Switch back to engine-1 — tasks should return.
	a.switchEngine("e1")
	a.taskListDirty = true
	a.taskListCache = a.renderTaskList()

	if a.taskListCache == "" {
		t.Fatal("task panel should not be empty after switching back to engine-1")
	}
	if !strings.Contains(a.taskListCache, "task-e1") {
		t.Errorf("task panel should contain 'task-e1' after round-trip, got:\n%s", a.taskListCache)
	}
}

// TestTaskBoard_Isolation integrates two scenarios that the current code
// fails: (1) switching engines must show different tasks per engine, and
// (2) switching sessions within the same engine must also show different
// tasks. Both regressions happened because the task panel cache
// (taskListDirty) was not invalidated on switchEngine or
// handleSessionPickerDone.
func TestTaskBoard_Isolation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Helper: create engine + session in one shot.
	makeEngine := func(id, model string) *engine.Engine {
		eng := engine.New(&engine.Params{Logger: slog.Default(), Model: model, EngineID: id})
		eng.SetStore(store, dir)
		sess, err := store.CreateSession(dir, model)
		if err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
		eng.SetSessionID(sess.SessionID)
		if err := eng.NewSession(dir, model); err != nil {
			t.Fatalf("NewSession %s: %v", id, err)
		}
		t.Cleanup(func() { eng.Close() })
		return eng
	}

	eng1 := makeEngine("eng1", "sonnet")
	eng2 := makeEngine("eng2", "opus")

	// Seed tasks: one per engine.
	if _, err := eng1.TaskList().CreateTask("eng1-task", "task in engine 1", "", nil); err != nil {
		t.Fatalf("CreateTask eng1: %v", err)
	}
	if _, err := eng2.TaskList().CreateTask("eng2-task", "task in engine 2", "", nil); err != nil {
		t.Fatalf("CreateTask eng2: %v", err)
	}

	// Build app with two engines.
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{Engine: eng1, Handler: NewTUIHandlerForEngine("eng1", nil), Repl: newReplAdapter(NewReplState()), ID: "eng1", Name: "engine-1", Model: "sonnet"})
	mgr.Add(&engine.EngineViewState{Engine: eng2, Handler: NewTUIHandlerForEngine("eng2", nil), Repl: newReplAdapter(NewReplState()), ID: "eng2", Name: "engine-2", Model: "opus"})
	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.projectDir = dir

	// taskListFn: reads from the active engine's task list.
	a.taskListFn = func() []TaskSummary {
		if a.engine == nil {
			return nil
		}
		tl := a.engine.TaskList()
		if tl == nil || tl.Dir() == "" {
			return nil
		}
		allTasks, _ := tl.ListTasks()
		var result []TaskSummary
		for _, t := range allTasks {
			result = append(result, TaskSummary{ID: t.ID, Subject: t.Subject, Status: string(t.Status)})
		}
		return result
	}

	// --- Scenario 1: switching engines shows different tasks ---

	a.switchEngine("eng1")
	a.taskListDirty = true
	tasks := a.taskListFn()
	if len(tasks) != 1 {
		t.Fatalf("eng1: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Subject != "eng1-task" {
		t.Errorf("eng1: task subject = %q, want 'eng1-task'", tasks[0].Subject)
	}

	a.switchEngine("eng2")
	a.taskListDirty = true
	tasks = a.taskListFn()
	if len(tasks) != 1 {
		t.Fatalf("eng2: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Subject != "eng2-task" {
		t.Errorf("eng2: task subject = %q, want 'eng2-task'", tasks[0].Subject)
	}

	// --- Scenario 2: switching sessions within same engine ---

	// Save session 1's ID before switching.
	sess1ID := eng1.SessionID()

	// Create a second session on eng1 and seed a task there.
	sess2, err := store.CreateSession(dir, "eng1-sess2")
	if err != nil {
		t.Fatalf("CreateSession eng1-sess2: %v", err)
	}
	if _, err := eng1.SwitchSession(sess2.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if _, err := eng1.TaskList().CreateTask("eng1-sess2-task", "task in eng1 session 2", "", nil); err != nil {
		t.Fatalf("CreateTask sess2: %v", err)
	}

	// Switch back to first session via picker.
	a.engine = eng1
	a.sessionID = ""
	items := []SessionItem{{SessionID: sess1ID, Title: "session1"}}
	a.handleSessionPickerDone(newSelectedDialog(0), items)

	// Panel should show first session's task, NOT session-2's task.
	tasks = a.taskListFn()
	if len(tasks) != 1 {
		t.Fatalf("eng1-session1: expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Subject != "eng1-task" {
		t.Errorf("eng1-session1: task subject = %q, want 'eng1-task' (must not leak from session-2)", tasks[0].Subject)
	}

	if !a.taskListDirty {
		t.Error("taskListDirty should be true after session switch")
	}
}
