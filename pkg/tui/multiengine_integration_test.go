package tui

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// newMultiEngineIntegrationApp builds an App with two real engines backed by
// the same SQLite store but separate sessions. Used for multi-engine
// isolation tests.
func newMultiEngineIntegrationApp(t *testing.T) (*App, *engine.Engine, *engine.Engine) {
	t.Helper()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainEng := engine.New(&engine.Params{
		Logger: slog.Default(), Model: "sonnet", EngineID: "main",
	})
	mainEng.SetStore(store, projectDir)
	if err := mainEng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("main NewSession: %v", err)
	}
	t.Cleanup(func() { mainEng.Close() })

	e2Eng := engine.New(&engine.Params{
		Logger: slog.Default(), Model: "opus", EngineID: "e2",
	})
	e2Eng.SetStore(store, projectDir)
	if err := e2Eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("e2 NewSession: %v", err)
	}
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine:  mainEng,
		Handler: NewTUIHandlerForEngine("main", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "main", Name: "main",
		ActiveSessionID: mainEng.SessionID(), Model: mainEng.Model(),
	})
	mgr.Add(&engine.EngineViewState{
		Engine:  e2Eng,
		Handler: NewTUIHandlerForEngine("e2", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "e2", Name: "engine-2",
		ActiveSessionID: e2Eng.SessionID(), Model: e2Eng.Model(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a.projectDir = projectDir
	return a, mainEng, e2Eng
}

// TestMultiEngine_Isolation_Model verifies that two engines keep their own
// model setting after switching between them.
func TestMultiEngine_Isolation_Model(t *testing.T) {
	t.Parallel()
	a, mainEng, e2Eng := newMultiEngineIntegrationApp(t)

	if a.engine.Model() != "sonnet" {
		t.Errorf("active main model = %q, want sonnet", a.engine.Model())
	}

	// Switch to e2; its model must be opus.
	a.switchEngine("e2")
	if a.engine != e2Eng {
		t.Errorf("after switch: a.engine pointer = %p, want e2 (%p)", a.engine, e2Eng)
	}
	if a.engine.Model() != "opus" {
		t.Errorf("after switch: e2 model = %q, want opus", a.engine.Model())
	}
	if a.engine.EngineID() != "e2" {
		t.Errorf("after switch: a.engine.EngineID = %q, want e2", a.engine.EngineID())
	}

	// Switch back to main; model must still be sonnet.
	a.switchEngine("main")
	if a.engine != mainEng {
		t.Errorf("after switch back: a.engine pointer = %p, want main (%p)", a.engine, mainEng)
	}
	if a.engine.Model() != "sonnet" {
		t.Errorf("after switch back: main model = %q, want sonnet", a.engine.Model())
	}
	if a.engine.EngineID() != "main" {
		t.Errorf("after switch back: a.engine.EngineID = %q, want main", a.engine.EngineID())
	}
}

// TestMultiEngine_Isolation_Session verifies each engine's session ID stays
// independent after switching.
func TestMultiEngine_Isolation_Session(t *testing.T) {
	t.Parallel()
	a, mainEng, e2Eng := newMultiEngineIntegrationApp(t)
	mainSession := mainEng.SessionID()
	e2Session := e2Eng.SessionID()
	if mainSession == e2Session {
		t.Fatal("main and e2 sessions must be different")
	}

	if a.sessionID != mainSession {
		t.Errorf("initial active session = %q, want %q", a.sessionID, mainSession)
	}
	a.switchEngine("e2")
	if a.sessionID != e2Session {
		t.Errorf("after switch to e2: session = %q, want %q", a.sessionID, e2Session)
	}
	a.switchEngine("main")
	if a.sessionID != mainSession {
		t.Errorf("after switch back: session = %q, want %q", a.sessionID, mainSession)
	}
}

// TestMultiEngine_BackgroundStreaming_AccumulatesState verifies that events
// fed to a background engine's drain fn accumulate in its ReplState WITHOUT
// disturbing the active engine's state.
func TestMultiEngine_BackgroundStreaming_AccumulatesState(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	mainRepl := a.repl // active engine's ReplState (main)
	e2VS := a.engineMgr.Get("e2")
	if e2VS == nil {
		t.Fatal("e2 view state not found")
	}
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r

	// Build a drain fn for e2 (background mode).
	drain := a.buildBackgroundDrainFn(e2VS)

	// Simulate engine-2 streaming a chunk + tool call + completion.
	drain(turnStartMsg{})
	drain(textDeltaMsg{Text: "hello from e2"})
	drain(toolStartMsg{ID: "t-bg", Name: "Bash", Summary: "ls"})
	drain(toolEndMsg{ToolUseID: "t-bg", Output: "done"})
	drain(queryEndMsg{})

	// Verify engine-2 accumulated the state.
	e2Msgs := e2Repl.Messages()
	if len(e2Msgs) == 0 {
		t.Fatal("e2 ReplState has no messages after drain")
	}
	last := e2Msgs[len(e2Msgs)-1]
	if last.Role != "assistant" {
		t.Errorf("e2 last msg role = %q, want assistant", last.Role)
	}
	// Find the text block with "hello from e2".
	foundText := false
	for _, blk := range last.Blocks {
		if blk.Type == BlockText && strings.Contains(blk.Text, "hello from e2") {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Errorf("e2 did not accumulate streaming text: %+v", last.Blocks)
	}
	// Verify tool view accumulated.
	tcv := e2Repl.FindToolView("t-bg")
	if tcv == nil {
		t.Fatal("e2 did not accumulate tool view")
	}
	if !tcv.Done {
		t.Errorf("e2 tool view should be Done after toolEnd")
	}
	if tcv.Output != "done" {
		t.Errorf("e2 tool output = %q, want 'done'", tcv.Output)
	}

	// CRITICAL: main ReplState must NOT have been mutated by e2's drain.
	mainMsgs := mainRepl.Messages()
	if len(mainMsgs) != 0 {
		t.Errorf("main ReplState was mutated by background drain: %d messages", len(mainMsgs))
	}
}

// TestMultiEngine_StatusBar_Render verifies the status bar reflects each
// engine's state during render.
func TestMultiEngine_StatusBar_Render(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	// Initially: both engines idle. main is active (no suffix), e2 idle.
	bar := a.renderEngineStatusBar()
	if !strings.Contains(bar, "main") {
		t.Errorf("status bar missing 'main': %q", bar)
	}
	if !strings.Contains(bar, "engine-2 (idle)") {
		t.Errorf("status bar missing 'engine-2 (idle)': %q", bar)
	}

	// Simulate e2 streaming: set its ReplState to streaming + pending tool.
	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	e2Repl.mu.Lock()
	e2Repl.streaming = true
	e2Repl.pendingToolStart["t1"] = time.Now() // REAL-TIME: ordering key only; absolute value irrelevant
	e2Repl.pendingTool["t1"] = &ToolCallView{ID: "t1", Name: "Read", Done: false}
	e2Repl.mu.Unlock()

	bar2 := a.renderEngineStatusBar()
	if !strings.Contains(bar2, "engine-2 (Read)") {
		t.Errorf("status bar missing 'engine-2 (Read)' after e2 starts streaming: %q", bar2)
	}
}

// TestMultiEngine_SwitchDoesNotResetBackgroundDrain verifies that switching to
// an engine doesn't lose the state it accumulated while in the background.
func TestMultiEngine_SwitchDoesNotResetBackgroundDrain(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	drain := a.buildBackgroundDrainFn(e2VS)
	drain(turnStartMsg{})
	drain(textDeltaMsg{Text: "accumulated"})

	// Now switch to e2. The cached a.repl should point to e2Repl (same allocation),
	// and the accumulated state must survive.
	a.switchEngine("e2")
	if a.repl != e2Repl {
		t.Fatal("after switch, a.repl should be e2's ReplState (same allocation)")
	}
	msgs := a.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("e2 messages lost after switch")
	}
	last := msgs[len(msgs)-1]
	found := false
	for _, blk := range last.Blocks {
		if blk.Type == BlockText && blk.Text == "accumulated" {
			found = true
		}
	}
	if !found {
		t.Errorf("accumulated text lost after switch: %+v", last.Blocks)
	}
}

// TestActiveRepl_ValueCopy_PreservesAdapterPointer verifies that the
// *a.repl = *NewReplState() reset pattern preserves the adapter pointer.
// After reset, the registered adapter must still point to the same allocation,
// and the state must be zeroed.
func TestActiveRepl_ValueCopy_PreservesAdapterPointer(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	// Capture the pointer registered in the active view state.
	activeVS := a.engineMgr.Active()
	if activeVS == nil {
		t.Fatal("no active engine")
	}
	adapter := activeVS.Repl.(replSnapshotAdapter)
	registeredPtr := adapter.r

	// Mutate the active ReplState to non-empty.
	a.repl.mu.Lock()
	a.repl.streaming = true
	a.repl.messages = append(a.repl.messages, MessageView{Role: "user"})
	a.repl.mu.Unlock()

	// Verify adapter still sees the mutation.
	if !adapter.IsStreaming() {
		t.Error("adapter should reflect streaming=true before reset")
	}

	// Reset via Reset() (mirrors what createNewSession / picker / rewind use).
	a.repl.Reset()

	// CRITICAL: the registered adapter must still point to the same allocation.
	adapter2, ok := activeVS.Repl.(replSnapshotAdapter)
	if !ok {
		t.Fatalf("active VS Repl type changed: %T", activeVS.Repl)
	}
	if adapter2.r != registeredPtr {
		t.Errorf("adapter pointer changed: was %p, now %p (must stay stable)", registeredPtr, adapter2.r)
	}
	// And a.repl should still point to the same allocation.
	if a.repl != registeredPtr {
		t.Errorf("a.repl pointer changed: was %p, now %p", registeredPtr, a.repl)
	}
	// State must be reset.
	if adapter2.IsStreaming() {
		t.Error("adapter should reflect streaming=false after reset")
	}
	if len(adapter2.r.Messages()) != 0 {
		t.Errorf("messages should be empty after reset, got %d", len(adapter2.r.Messages()))
	}
}

// Ensure types/tool are referenced for the drain-fn tests.
var _ = types.RoleAssistant
var _ = tool.SearchReadKind{}

// TestMultiEngine_SwitchRendersCorrectHistory is the end-to-end regression
// test for engine switching. It builds a three-engine timeline:
//
//  1. main: pre-seeded with two history messages (mirrors restart resume).
//  2. engine-2: created via /engine new, then seeded with its own messages.
//  3. engine-3: created via /engine new, never seeded (empty).
//
// Then cycles through every engine in a non-trivial order and asserts the
// active repl's messages match the expected set. This catches:
//
//   - restored engines with no Repl leaving stale a.repl on switch-back
//     (TestSwitchEngine_NilTargetRepl_BuildsFreshFromEngineMessages pins
//     this in isolation, but the cycle here exercises the multi-hop path).
//   - engines sharing ReplState due to incorrect caching.
//   - empty engines showing the previous engine's scrollback.
func TestMultiEngine_SwitchRendersCorrectHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainHub, mainHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: mainHub})
	mainEng.SetStore(store, projectDir)
	if err := mainEng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("main NewSession: %v", err)
	}
	mainEng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("main-q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("main-a1")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("main-q2")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("main-a2")}},
	})
	mainEng.PersistNewMessages()
	t.Cleanup(func() { mainEng.Close() })

	mgr := engine.NewEngineManager()
	// main: NO Repl — mirrors restoreEngines on restart.
	mgr.Add(&engine.EngineViewState{
		Engine: mainEng, Handler: mainHandler,
		ID: "main", Name: "main", Model: "sonnet",
		ActiveSessionID: mainEng.SessionID(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a.projectDir = projectDir

	// Factory mirrors main.go: per-engine Hub+handler, store wiring, new session.
	a.SetEngineFactory(func(id, name, model string) (*engine.Engine, *TUIHandler, error) {
		eh, handler := NewEngineHubWithHandler(id, nil)
		eng := engine.New(&engine.Params{Logger: slog.Default(), Model: model, EngineID: id, Dispatcher: eh})
		eng.SetStore(store, projectDir)
		if err := eng.NewSession(projectDir, ""); err != nil {
			return nil, nil, err
		}
		return eng, handler, nil
	})

	// --- Step 1: /engine new → engine-2, then seed e2 with messages. ---
	if cmd := a.handleEngine("new", nil); cmd == nil {
		t.Fatal("handleEngine(new) returned nil cmd — ClearScreen must fire on creation")
	} else {
		// Drain the batched cmd to materialize any internal side effects.
		_ = cmd()
	}
	if a.engine == nil || a.engine.EngineID() != "e2" {
		t.Fatalf("after /engine new, a.engine = %v, want e2", a.engine)
	}
	e2Eng := a.engine
	// Seed e2's history. In production this would arrive via background
	// streaming (drain fn appends to e2VS.ReplState). The test mirrors
	// that post-streaming state by writing to the view state's ReplState
	// directly — bypassing Engine.SetMessages which would not sync the
	// ReplState (an Engine↔ReplState drift that streaming normally hides).
	e2VS := mgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	e2Repl.messages = []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "e2-q1"}}},
		{Role: "assistant", Blocks: []ContentBlock{{Type: BlockText, Text: "e2-a1"}}},
	}
	// Mirror into engine.messages too so future code paths that read
	// Engine.Messages() see consistent state.
	e2Eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("e2-q1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("e2-a1")}},
	})
	e2Eng.PersistNewMessages()

	// --- Step 2: /engine new → engine-3 (empty). ---
	if cmd := a.handleEngine("new", nil); cmd == nil {
		t.Fatal("handleEngine(new) #2 returned nil cmd")
	} else {
		_ = cmd()
	}
	if a.engine == nil || a.engine.EngineID() != "e3" {
		t.Fatalf("after second /engine new, a.engine = %v, want e3", a.engine)
	}

	// --- Step 3: Cycle through all engines and verify rendering state. ---
	// Expected set per engine. e3 has NO history (never seeded).
	cases := []struct {
		engineID string
		markers  []string
	}{
		{"main", []string{"main-q1", "main-a1", "main-q2", "main-a2"}},
		{"e2", []string{"e2-q1", "e2-a1"}},
		{"e3", nil}, // empty
	}
	// Cycle order exercises main → e2 → e3 → main → e3 → e2 so any stale
	// pointer to the previous ReplState is caught.
	for _, c := range []struct{ from, to string }{
		{"e3", "main"},
		{"main", "e2"},
		{"e2", "e3"},
		{"e3", "main"},
		{"main", "e3"},
		{"e3", "e2"},
	} {
		if _, cmd := a.switchEngine(c.to); cmd == nil {
			t.Fatalf("switchEngine(%s→%s) returned nil cmd", c.from, c.to)
		}
		// Find the matching expectation.
		var want []string
		for _, exp := range cases {
			if exp.engineID == c.to {
				want = exp.markers
				break
			}
		}
		if a.repl == nil {
			t.Fatalf("after switch %s→%s: a.repl is nil", c.from, c.to)
		}
		msgs := a.repl.Messages()
		for _, marker := range want {
			if !replContainsText(msgs, marker) {
				t.Errorf("after switch %s→%s: a.repl missing %q; got %d msgs",
					c.from, c.to, marker, len(msgs))
			}
		}
		if want == nil && len(msgs) != 0 {
			t.Errorf("after switch %s→%s: e3 should be empty but a.repl has %d msgs: %+v",
				c.from, c.to, len(msgs), msgs)
		}
	}
}

// replContainsText reports whether any block in msgs contains the given text.
func replContainsText(msgs []MessageView, want string) bool {
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if strings.Contains(b.Text, want) {
				return true
			}
		}
	}
	return false
}

// TestMultiEngine_BackgroundStreaming_StatusAndAccumulatedOutput verifies
// the two things the user reported broken:
//
//  1. While e2 streams in background, renderEngineStatusBar must show e2
//     as streaming (IsStreaming=true on its ReplState, propagated via
//     Snapshot). Otherwise the user thinks e2 stopped.
//  2. tool_output_delta events dispatched through e2's Hub while e2 is
//     in background must accumulate in e2's ReplState. When the user
//     switches back, the accumulated tool output must be visible.
func TestMultiEngine_BackgroundStreaming_StatusAndAccumulatedOutput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Each engine gets its own Hub+handler (mirrors main.go factory).
	mainHub, mainHandler := NewEngineHubWithHandler("main", nil)
	mainEng := engine.New(&engine.Params{Logger: slog.Default(), Model: "sonnet", EngineID: "main", Dispatcher: mainHub})
	mainEng.SetStore(store, projectDir)
	if err := mainEng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("main NewSession: %v", err)
	}
	t.Cleanup(func() { mainEng.Close() })

	e2Hub, e2Handler := NewEngineHubWithHandler("e2", nil)
	e2Eng := engine.New(&engine.Params{Logger: slog.Default(), Model: "opus", EngineID: "e2", Dispatcher: e2Hub})
	e2Eng.SetStore(store, projectDir)
	if err := e2Eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("e2 NewSession: %v", err)
	}
	t.Cleanup(func() { e2Eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: mainEng, Handler: mainHandler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "main", Name: "main", Model: "sonnet",
		ActiveSessionID: mainEng.SessionID(),
	})
	mgr.Add(&engine.EngineViewState{
		Engine: e2Eng, Handler: e2Handler,
		Repl: newReplAdapter(NewReplState()),
		ID:   "e2", Name: "engine-2", Model: "opus",
		ActiveSessionID: e2Eng.SessionID(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a.projectDir = projectDir

	// Switch to e2 first so it's the active engine, then start streaming.
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}
	e2VS := mgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r

	// Mark e2 as streaming + register the pending tool directly on its
	// ReplState. The production path goes turnStartMsg → StartQuery (sets
	// streaming=true), then toolStartMsg → PendingToolStarted. Doing it
	// directly avoids pulling in Update() plumbing for the setup.
	e2Repl.StartQuery()
	e2Repl.PendingToolStarted("tool-bg", "Bash", "make check", "",
		tool.SearchReadKind{})
	if !e2Repl.IsStreaming() {
		t.Fatalf("precondition: e2 should be streaming after StartQuery, got streaming=%v", e2Repl.IsStreaming())
	}

	// Switch e2 → main. e2 goes to background; its handler gets drainFn.
	if _, cmd := a.switchEngine("main"); cmd == nil {
		t.Fatal("switchEngine(e2→main) returned nil cmd")
	}

	// (1) Status bar must still show e2 as streaming while in background.
	bar := a.renderEngineStatusBar()
	if strings.Contains(bar, "engine-2 (idle)") {
		t.Errorf("status bar shows e2 as idle while it's streaming in background: %q", bar)
	}
	if !strings.Contains(bar, "engine-2 (streaming)") && !strings.Contains(bar, "engine-2 (") {
		t.Errorf("status bar missing streaming indicator for background e2: %q", bar)
	}

	// (2) Simulate make check producing cumulative output snapshots while
	// e2 is background. Production emits the FULL output each event
	// (strings.Join(allLinesSoFar, "\n")), so PendingToolOutput replaces
	// rather than appends — that's the contract.
	snapshots := []string{"line-a", "line-a\nline-b", "line-a\nline-b\nline-c"}
	for _, snap := range snapshots {
		e2Hub.Dispatch(types.QueryEvent{
			Type:       types.EventToolOutputDelta,
			ToolResult: &types.ToolResultEvent{ToolUseID: "tool-bg", DisplayOutput: snap},
		})
	}

	// Switch back to e2. Its accumulated state must be visible.
	if _, cmd := a.switchEngine("e2"); cmd == nil {
		t.Fatal("switchEngine(main→e2) returned nil cmd")
	}
	tcv := e2Repl.FindToolView("tool-bg")
	if tcv == nil {
		t.Fatal("e2 ReplState lost tool-bg after switch-back — drain fn must accumulate tool calls")
	}
	for _, want := range []string{"line-a", "line-b", "line-c"} {
		if !strings.Contains(tcv.Output, want) {
			t.Errorf("e2 tool output missing %q after switch-back; got: %q", want, tcv.Output)
		}
	}
}
