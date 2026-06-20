package tui

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// TestMultiEngine_HubRouting_Isolation is the critical end-to-end regression
// test for the per-engine Hub wiring. It exercises the real factory path
// (handleEngine "new" → engineFactory → NewEngineHubWithHandler → engine.New
// with that Hub as Dispatcher) and then drives events through each engine's
// Hub via Engine.Dispatcher().Dispatch, asserting:
//
//  1. Events from engine A reach only engine A's ReplState.
//  2. Events from engine B reach only engine B's ReplState.
//  3. The isolation holds across switchEngine flips (active ⇄ background).
//
// Before the fix, the factory passed Dispatcher: <shared bootstrap Hub> for
// every new engine, so all events drained to the bootstrap engine's
// ReplState regardless of which engine emitted them. This test fails on that
// bug because dispatching events through engine-2's Hub would also reach
// main's ReplState via the shared Hub.
func TestMultiEngine_HubRouting_Isolation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	store, err := short.NewStore(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Bootstrap engine with its own dedicated Hub (mirrors main.go).
	bootstrapHub, bootstrapHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{
		Logger: slog.Default(), Model: "sonnet", EngineID: "main",
		Dispatcher: bootstrapHub,
	})
	mainEng.SetStore(store, projectDir)
	if err := mainEng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("main NewSession: %v", err)
	}
	t.Cleanup(func() { mainEng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine:  mainEng,
		Handler: bootstrapHandler, // single source of truth for switchEngine
		Repl:    newReplAdapter(NewReplState()),
		ID:      "main", Name: "main",
		ActiveSessionID: mainEng.SessionID(), Model: mainEng.Model(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a.projectDir = projectDir
	// bootstrapHub already has bootstrapHandler subscribed (from
	// NewEngineHubWithHandler above); NewAppWithManager bound a.tuiHandler
	// to vs.Handler. No extra wiring needed.

	// Factory captures real per-engine Hub construction (mirrors main.go).
	a.SetEngineFactory(func(id, name, modelArg string) (*engine.Engine, *TUIHandler, error) {
		engineHub, handler := NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{
			Logger: slog.Default(), Model: modelArg, EngineID: id,
			Dispatcher: engineHub,
		})
		eng.SetStore(store, projectDir)
		return eng, handler, nil
	})

	// Create engine-2 via the real /engine new path.
	cmd := a.handleEngine("new", nil)
	if cmd != nil {
		_ = cmd()
	}
	if got := mgr.Count(); got != 2 {
		t.Fatalf("engine count = %d, want 2 after /engine new", got)
	}
	e2VS := mgr.Get("e2")
	if e2VS == nil {
		t.Fatal("e2 view state not registered")
	}
	e2Eng := e2VS.Engine
	if e2Eng == nil {
		t.Fatal("e2 engine not built by factory")
	}

	// Each engine's Dispatcher should be its OWN Hub (not the bootstrap Hub).
	// This is the invariant the critical bug broke.
	e2Disp, e2IsHub := e2Eng.Dispatcher().(*hub.Hub)
	if !e2IsHub {
		t.Fatalf("e2 Dispatcher type = %T, want *hub.Hub", e2Eng.Dispatcher())
	}
	mainDisp, mainIsHub := mainEng.Dispatcher().(*hub.Hub)
	if !mainIsHub {
		t.Fatalf("main Dispatcher type = %T, want *hub.Hub", mainEng.Dispatcher())
	}
	if e2Disp == mainDisp {
		t.Fatal("e2 and main share the same Hub — per-engine isolation broken")
	}

	// At this point, switchEngine has made e2 active and main background.
	if mgr.ActiveID() != "e2" {
		t.Fatalf("ActiveID = %q, want e2 after /engine new auto-switch", mgr.ActiveID())
	}

	mainRepl := mgr.Get("main").Repl.(replSnapshotAdapter).r
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r

	// Dispatch turnStart + textDelta through main's Hub. main is now
	// background, so its handler has drainFn set → its ReplState should
	// accumulate the turn (StartQuery creates the empty assistant msg, then
	// AppendChunk grows the text block). e2's ReplState must NOT.
	mainDisp.Dispatch(types.QueryEvent{Type: types.EventTurnStart})
	mainDisp.Dispatch(types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello from main",
	})

	// main's ReplState should reflect the streamed text. The drain fn wraps
	// the events so turnStartMsg → StartQuery (empty assistant msg) then
	// textDeltaMsg → AppendChunk grows the last text block.
	mainMsgs := mainRepl.Messages()
	if len(mainMsgs) == 0 {
		t.Fatal("main ReplState has no messages after main Hub dispatch")
	}
	if !containsText(mainMsgs, "hello from main") {
		t.Errorf("main ReplState missing 'hello from main': %+v", mainMsgs)
	}

	// e2's ReplState must be untouched (this is the isolation guarantee).
	e2MsgsBefore := e2Repl.Messages()
	if containsText(e2MsgsBefore, "hello from main") {
		t.Errorf("e2 ReplState was polluted by main's event — isolation broken")
	}

	// Dispatch through e2's Hub: now e2 is active (drainFn=nil), so the
	// event is queued in e2 handler's appCh, NOT immediately applied to
	// ReplState. The handler.drainedByAppOnRead path requires draining appCh
	// through App.Update to materialize the state. To assert Hub isolation
	// directly we instead flip e2 to background via switchEngine("main"),
	// then dispatch through e2's Hub — the drain fn should apply the event
	// to e2's ReplState only.
	a.switchEngine("main")
	if mgr.ActiveID() != "main" {
		t.Fatalf("ActiveID after switch back to main = %q, want main", mgr.ActiveID())
	}

	e2Disp.Dispatch(types.QueryEvent{Type: types.EventTurnStart})
	e2Disp.Dispatch(types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello from e2",
	})

	e2MsgsAfter := e2Repl.Messages()
	if !containsText(e2MsgsAfter, "hello from e2") {
		t.Errorf("e2 ReplState missing 'hello from e2' after e2 Hub dispatch: %+v", e2MsgsAfter)
	}

	// And main's ReplState must NOT contain e2's text (even though main is
	// now active — events dispatched through e2's Hub only reach e2's
	// handler, not main's).
	mainMsgsAfter := mainRepl.Messages()
	if containsText(mainMsgsAfter, "hello from e2") {
		t.Errorf("main ReplState was polluted by e2's event — isolation broken")
	}
	// main's prior state must survive.
	if !containsText(mainMsgsAfter, "hello from main") {
		t.Errorf("main's earlier 'hello from main' was lost: %+v", mainMsgsAfter)
	}
}

// containsText reports whether any MessageView in msgs contains a text block
// with the given substring.
func containsText(msgs []MessageView, want string) bool {
	for _, m := range msgs {
		for _, blk := range m.Blocks {
			if blk.Type == BlockText && strings.Contains(blk.Text, want) {
				return true
			}
		}
	}
	return false
}
