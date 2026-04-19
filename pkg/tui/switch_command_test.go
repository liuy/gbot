package tui

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func newSwitchTestApp(t *testing.T) (*App, *short.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	eng := engine.New(&engine.Params{Logger: slog.Default()})
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
		repl:             NewReplState(),
	}
	return a, store
}

func TestHandleSwitch_Streaming(t *testing.T) {
	a, _ := newSwitchTestApp(t)
	a.repl.streaming = true

	cmd := a.handleSwitch("-n", nil)
	if cmd == nil {
		t.Fatal("expected a command from streaming guard")
	}
	// The command should produce an infoMsg about not switching while streaming
	msg := cmd()
	if _, ok := msg.(infoMsg); !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
}

func TestHandleSwitch_NewSessionNoTitle(t *testing.T) {
	a, _ := newSwitchTestApp(t)
	oldSessionID := a.sessionID

	cmd := a.handleSwitch("-n", nil)
	_ = cmd

	if a.sessionID == oldSessionID {
		t.Error("expected session ID to change")
	}
	if a.sessionID == "" {
		t.Error("expected non-empty session ID")
	}
	if a.lastPersistedIdx != 0 {
		t.Errorf("expected lastPersistedIdx=0, got %d", a.lastPersistedIdx)
	}
	msgs := a.engine.Messages()
	if len(msgs) != 0 {
		t.Errorf("expected empty messages after new session, got %d", len(msgs))
	}
}

func TestHandleSwitch_NewSessionWithTitle(t *testing.T) {
	a, store := newSwitchTestApp(t)

	cmd := a.handleSwitch("-n My New Session", nil)
	_ = cmd

	// Verify session was created with title
	sessions, err := store.ListSessions("", 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.Title == "My New Session" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected session with title 'My New Session'")
	}
}

func TestHandleSwitch_ForkWithDuplicateTitle(t *testing.T) {
	a, store := newSwitchTestApp(t)

	// Create a session with a known title
	session, _ := store.CreateSession("", "test-model")
	if err := store.UpdateSessionTitle(session.SessionID, "existing-title"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Try to fork with duplicate title
	cmd := a.handleSwitch("existing-title", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if string(info) == "" {
		t.Error("expected error message about duplicate title")
	}
}

func TestHandleSwitch_ForkSuccess(t *testing.T) {
	a, store := newSwitchTestApp(t)

	// Add and persist messages to the current session
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	})
	a.lastPersistedIdx = 0
	a.persistTurn()
	if a.lastPersistedIdx != 2 {
		t.Fatalf("expected lastPersistedIdx=2 after persist, got %d", a.lastPersistedIdx)
	}

	// Fork
	cmd := a.handleSwitch("fork-title", nil)
	_ = cmd

	if a.sessionID == "" {
		t.Error("expected session ID after fork")
	}

	// Verify forked messages
	forkedMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(forkedMsgs) != 2 {
		t.Errorf("expected 2 messages in forked session, got %d", len(forkedMsgs))
	}

	// Verify title
	sessions, _ := store.ListSessions("", 100)
	for _, s := range sessions {
		if s.SessionID == a.sessionID {
			if s.Title != "fork-title" {
				t.Errorf("fork title = %q, want %q", s.Title, "fork-title")
			}
			break
		}
	}
}
func TestHandleSwitch_NoStore(t *testing.T) {
	a := &App{
		engine: engine.New(&engine.Params{Logger: slog.Default()}),
		repl:   NewReplState(),
	}

	cmd := a.handleSwitch("-n", nil)
	msg := cmd()
	if _, ok := msg.(infoMsg); !ok {
		t.Fatalf("expected infoMsg about no store, got %T", msg)
	}
}
