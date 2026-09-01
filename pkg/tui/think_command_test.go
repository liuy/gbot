package tui

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
)

// TestHandleThink_ShowCurrent verifies the no-arg path displays the current
// effort without touching engine state.
func TestHandleThink_ShowCurrent(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleThink("", nil)
	if cmd == nil {
		t.Fatal("handleThink(\"\") returned nil cmd, want the show-info path")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "Thinking effort: auto") {
		t.Errorf("info = %q, want it to report the current effort auto", info)
	}
	if got := a.engine.Thinking(); got != llm.EffortAuto {
		t.Errorf("after no-arg /think engine.Thinking() = %q, want auto (display must be side-effect free)", got)
	}
}

// TestHandleThink_PersistsStickyOverride verifies /think mirrors the effort
// into the manager view-state and meta.json — restart must not drop it.
func TestHandleThink_PersistsStickyOverride(t *testing.T) {
	dir := t.TempDir()
	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{name: "openai"},
		Model:    "glm-5",
		Logger:   slog.Default(),
	})
	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{Engine: eng, ID: "main", Name: "main", Model: "openai/glm-5"})
	a := &App{
		engine:     eng,
		repl:       NewReplState(),
		engineMgr:  mgr,
		projectDir: dir,
	}
	if cmd := a.handleThink("none", nil); cmd == nil {
		t.Fatal("handleThink(\"none\") returned nil cmd")
	}
	if got := mgr.Active().Thinking; got != llm.EffortNone {
		t.Errorf("view-state effort = %q, want none", got)
	}
	meta, err := short.ReadWorkspaceMeta(dir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	if len(meta.Engines) != 1 || meta.Engines[0].Thinking != "none" {
		t.Fatalf("meta.json engines = %+v, want one engine with thinking none", meta.Engines)
	}
}

func TestHandleThink_SetEffort(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleThink("high", nil)
	if cmd == nil {
		t.Fatal("handleThink(\"high\") returned nil cmd")
	}
	if got := a.engine.Thinking(); got != llm.EffortHigh {
		t.Errorf("engine.Thinking() = %q, want high", got)
	}

	// Uppercase input normalizes to the axis value.
	_ = a.handleThink("NONE", nil)
	if got := a.engine.Thinking(); got != llm.EffortNone {
		t.Errorf("after /think NONE engine.Thinking() = %q, want none", got)
	}
}

func TestHandleThink_InvalidValue(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleThink("bogus", nil)
	if cmd == nil {
		t.Fatal("handleThink(\"bogus\") returned nil cmd, want error display")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "none|auto|low|medium|high|max") {
		t.Errorf("info = %q, want it to list the six valid values", info)
	}
	if got := a.engine.Thinking(); got != llm.EffortAuto {
		t.Errorf("after invalid /think engine.Thinking() = %q, want auto (rejection must not mutate state)", got)
	}
}

func TestHandleThink_StreamingGuard(t *testing.T) {
	a := newTestAppWithProviders(t)
	a.repl.streaming = true

	cmd := a.handleThink("high", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "Cannot change thinking effort while streaming") {
		t.Errorf("expected streaming guard message, got %q", info)
	}
	if got := a.engine.Thinking(); got != llm.EffortAuto {
		t.Errorf("guarded /think must not mutate state, got %q", got)
	}
}
