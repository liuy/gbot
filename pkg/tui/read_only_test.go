package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
)

// newReadOnlyIntegrationApp builds an App with a main engine and a read-only
// "wechat" engine. Returns the app so tests can drive switchEngine and
// handleSubmitRepl against the read-only guard.
func newReadOnlyIntegrationApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mainEng := engine.New(&engine.Params{
		Logger: nil, Model: "sonnet", EngineID: "main",
	})
	mainEng.SetStore(store, projectDir)
	if err := mainEng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("main NewSession: %v", err)
	}
	t.Cleanup(func() { mainEng.Close() })

	wcEng := engine.New(&engine.Params{
		Logger: nil, Model: "sonnet", EngineID: "wechat-acct",
	})
	wcEng.SetStore(store, projectDir)
	if err := wcEng.NewSession(projectDir, "WeChat"); err != nil {
		t.Fatalf("wechat NewSession: %v", err)
	}
	t.Cleanup(func() { wcEng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine:  mainEng,
		Handler: NewTUIHandlerForEngine("main", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "main", Name: "main",
		ActiveSessionID: mainEng.SessionID(), Model: mainEng.Model(),
	})
	mgr.Add(&engine.EngineViewState{
		Engine:  wcEng,
		Handler: NewTUIHandlerForEngine("wechat-acct", nil),
		Repl:    newReplAdapter(NewReplState()),
		ID:      "wechat-acct", Name: "WeChat",
		ReadOnly:        true,
		ActiveSessionID: wcEng.SessionID(), Model: wcEng.Model(),
	})

	a := NewAppWithManager(mgr, "", nil)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	a.projectDir = projectDir
	return a
}

// TestReadOnly_SubmitReplRejected verifies that handleSubmitRepl rejects a
// plain (non-slash) submission when the active engine is read-only, returning
// a showInfo cmd instead of starting a query.
func TestReadOnly_SubmitReplRejected(t *testing.T) {
	a := newReadOnlyIntegrationApp(t)

	// Switch to the read-only wechat engine.
	if _, cmd := a.switchEngine("wechat-acct"); cmd == nil {
		t.Fatal("switchEngine to wechat returned nil cmd")
	}
	if !a.inputReadOnly {
		t.Fatal("inputReadOnly should be true after switching to read-only engine")
	}

	// A plain submit must be rejected.
	cmd := a.handleSubmitRepl("hello there")
	if cmd == nil {
		t.Fatal("handleSubmitRepl on read-only engine should return a non-nil cmd (showInfo)")
	}
	// The cmd should produce an infoMsg mentioning read-only. Execute it.
	msg := cmd()
	if im, ok := msg.(infoMsg); !ok {
		t.Fatalf("expected infoMsg from read-only rejection, got %T", msg)
	} else if !strings.Contains(string(im), "read-only") {
		t.Fatalf("info message = %q, want to contain 'read-only'", string(im))
	}
}

// TestReadOnly_SlashCommandAllowed verifies that slash commands (e.g.
// /engine) are NOT rejected by the read-only guard, so the user can switch
// away from a read-only engine.
func TestReadOnly_SlashCommandAllowed(t *testing.T) {
	a := newReadOnlyIntegrationApp(t)

	if _, cmd := a.switchEngine("wechat-acct"); cmd == nil {
		t.Fatal("switchEngine to wechat returned nil cmd")
	}
	if !a.inputReadOnly {
		t.Fatal("inputReadOnly should be true")
	}

	// A slash command must bypass the guard (not return the read-only info).
	cmd := a.handleSubmitRepl("/engine main")
	if cmd == nil {
		t.Fatal("handleSubmitRepl with slash command should not return nil")
	}
	msg := cmd()
	if im, ok := msg.(infoMsg); ok && strings.Contains(string(im), "read-only") {
		t.Fatalf("slash command should bypass read-only guard, but got read-only info: %q", string(im))
	}
	// It's fine if it's a different infoMsg (e.g. "Switched to engine") or
	// another msg type — the point is it's not the read-only rejection.
}

// TestReadOnly_NonReadOnlyEngine_SubmitsAllowed verifies that a normal
// (non-read-only) engine does NOT trigger the guard.
func TestReadOnly_NonReadOnlyEngine_SubmitsAllowed(t *testing.T) {
	a := newReadOnlyIntegrationApp(t)

	// Main engine is not read-only.
	if a.inputReadOnly {
		t.Fatal("inputReadOnly should be false on main engine")
	}
	// The guard should not reject; handleSubmitRepl proceeds past it (it may
	// fail later for other reasons, but not with the read-only info).
	cmd := a.handleSubmitRepl("hello")
	// Whether cmd is nil or not depends on streaming state, but it must NOT
	// produce the read-only info message.
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if im, ok := msg.(infoMsg); ok && strings.Contains(string(im), "read-only") {
				t.Fatalf("non-read-only engine should not produce read-only rejection: %q", string(im))
			}
		}
	}
}

// TestReadOnly_SwitchEngineClearsFlag verifies that switching FROM a read-only
// engine back to a normal one clears inputReadOnly.
func TestReadOnly_SwitchEngineClearsFlag(t *testing.T) {
	a := newReadOnlyIntegrationApp(t)

	if _, cmd := a.switchEngine("wechat-acct"); cmd == nil {
		t.Fatal("switchEngine to wechat returned nil cmd")
	}
	if !a.inputReadOnly {
		t.Fatal("inputReadOnly should be true on wechat engine")
	}
	// Switch back to main.
	if _, cmd := a.switchEngine("main"); cmd == nil {
		t.Fatal("switchEngine to main returned nil cmd")
	}
	if a.inputReadOnly {
		t.Fatal("inputReadOnly should be false after switching back to main")
	}
}

// TestReadOnly_PlaceholderSet verifies the input placeholder reflects the
// read-only state after switching.
func TestReadOnly_PlaceholderSet(t *testing.T) {
	a := newReadOnlyIntegrationApp(t)

	if _, cmd := a.switchEngine("wechat-acct"); cmd == nil {
		t.Fatal("switchEngine to wechat returned nil cmd")
	}
	if a.input.placeholder == "" || !strings.Contains(strings.ToLower(a.input.placeholder), "read-only") {
		t.Fatalf("placeholder = %q, want to mention read-only", a.input.placeholder)
	}
	// Switch back to main: placeholder resets to the default.
	if _, cmd := a.switchEngine("main"); cmd == nil {
		t.Fatal("switchEngine to main returned nil cmd")
	}
	if a.input.placeholder != "Type a message..." {
		t.Fatalf("placeholder = %q, want default", a.input.placeholder)
	}
}
