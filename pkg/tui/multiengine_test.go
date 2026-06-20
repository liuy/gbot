package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
)

func TestRenderEngineStatusBar_SingleEngine_Hidden(t *testing.T) {
	t.Parallel()
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		ID: "main", Name: "main", Model: "sonnet",
		Repl: newReplAdapter(NewReplState()),
	})
	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := a.renderEngineStatusBar()
	if got != "" {
		t.Errorf("single-engine bar = %q, want empty", got)
	}
}

func TestRenderEngineStatusBar_MultipleEngines(t *testing.T) {
	t.Parallel()
	mgr := engine.NewEngineManager()
	mainRepl := NewReplState()
	mgr.Add(&engine.EngineViewState{
		ID: "main", Name: "main", Model: "sonnet",
		Repl: newReplAdapter(mainRepl),
	})
	// engine-2: streaming, current tool "Edit".
	e2Repl := NewReplState()
	e2Repl.mu.Lock()
	e2Repl.streaming = true
	e2Repl.pendingToolStart["t1"] = time.Now() // REAL-TIME: ordering key only; absolute value irrelevant
	e2Repl.pendingTool["t1"] = &ToolCallView{ID: "t1", Name: "Edit", Done: false}
	e2Repl.mu.Unlock()
	mgr.Add(&engine.EngineViewState{
		ID: "e2", Name: "engine-2", Model: "opus",
		Repl: newReplAdapter(e2Repl),
	})
	// engine-3: idle.
	mgr.Add(&engine.EngineViewState{
		ID: "e3", Name: "engine-3", Model: "haiku",
		Repl: newReplAdapter(NewReplState()),
	})
	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	got := a.renderEngineStatusBar()
	// Active engine: no suffix (● conveys active). Streaming with tool:
	// "(tool)". Idle: "(idle)".
	if !strings.Contains(got, "main") {
		t.Errorf("output missing active engine name 'main': %q", got)
	}
	if strings.Contains(got, "main (active)") {
		t.Errorf("active engines must not have '(active)' suffix — the ● bullet conveys it: %q", got)
	}
	if !strings.Contains(got, "engine-2 (Edit)") {
		t.Errorf("output missing 'engine-2 (Edit)': %q", got)
	}
	if !strings.Contains(got, "engine-3 (idle)") {
		t.Errorf("output missing 'engine-3 (idle)': %q", got)
	}
	// Exactly two separators (3 engines → 2 separators).
	if sepCount := strings.Count(got, " · "); sepCount != 2 {
		t.Errorf("separator count = %d, want 2 (bar=%q)", sepCount, got)
	}
}

// TestBuildBackgroundDrainFn_NilRepl_DoesNotPanic verifies that
// buildBackgroundDrainFn tolerates an EngineViewState with Repl == nil.
// restoreEngines in main.go adds the main engine's view state without
// setting Repl; the first /engine new then calls switchEngine →
// buildBackgroundDrainFn(mainVS), which used to do an unchecked type
// assertion `vs.Repl.(replSnapshotAdapter)` and panic on the nil.
func TestBuildBackgroundDrainFn_NilRepl_DoesNotPanic(t *testing.T) {
	t.Parallel()

	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider:   &tuiMockProvider{},
		Model:      "test-model",
		Dispatcher: h,
	})
	a := NewApp(eng, "", h)
	vs := &engine.EngineViewState{
		Engine:          eng,
		Repl:            nil, // mirrors restoreEngines in main.go
		ID:              "main",
		Name:            "main",
		ActiveSessionID: "session-1",
		Model:           eng.Model(),
	}

	var drain func(tea.Msg)
	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		drain = a.buildBackgroundDrainFn(vs)
	}()

	if panicked {
		t.Fatal("buildBackgroundDrainFn panicked on vs.Repl == nil — " +
			"restoreEngines adds view states without Repl, the first /engine new must not crash")
	}
	if drain == nil {
		t.Fatal("buildBackgroundDrainFn returned nil — must return a no-op drain when vs.Repl is unset")
	}

	// Drain must be safe to invoke: it receives arbitrary tea.Msg and
	// silently drops them when no Repl is attached. Use a queryEndMsg to
	// exercise the default path.
	drain(queryEndMsg{})
}
