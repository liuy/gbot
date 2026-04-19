package tui

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func newTestEngine() *engine.Engine {
	return engine.New(&engine.Params{
		Logger: slog.Default(),
	})
}

func TestPersistTurn_NilStore(t *testing.T) {
	a := &App{
		engine:    newTestEngine(),
		sessionID: "test-session",
	}
	a.persistTurn() // should not panic
}

func TestPersistTurn_EmptySessionID(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	a := &App{
		engine: newTestEngine(),
		store:  store,
	}
	a.persistTurn() // should not panic
}

func TestPersistTurn_NoUncommittedMessages(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := newTestEngine()
	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	a.persistTurn()

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages in store, got %d", len(msgs))
	}
}

func TestPersistTurn_SuccessfulPersist(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}, Timestamp: time.Now()},
	})

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	a.persistTurn()

	if a.lastPersistedIdx != 2 {
		t.Fatalf("expected lastPersistedIdx=2, got %d", a.lastPersistedIdx)
	}

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages in store, got %d", len(msgs))
	}
	if msgs[0].Type != "user" {
		t.Errorf("msg[0].Type = %q, want user", msgs[0].Type)
	}
	if msgs[1].Type != "assistant" {
		t.Errorf("msg[1].Type = %q, want assistant", msgs[1].Type)
	}

	// Persist again — should be a no-op (nothing new)
	a.persistTurn()
	msgs2, _ := store.LoadMessages(session.SessionID)
	if len(msgs2) != 2 {
		t.Fatalf("expected 2 messages after re-persist, got %d", len(msgs2))
	}
}

func TestPersistTurn_Incremental(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Now()},
	})

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	// First persist: 1 message
	a.persistTurn()
	if a.lastPersistedIdx != 1 {
		t.Fatalf("expected lastPersistedIdx=1, got %d", a.lastPersistedIdx)
	}

	// Add more messages
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply1")}, Timestamp: time.Now()},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}, Timestamp: time.Now()},
	})

	// Second persist: only the 2 new messages
	a.persistTurn()
	if a.lastPersistedIdx != 3 {
		t.Fatalf("expected lastPersistedIdx=3, got %d", a.lastPersistedIdx)
	}

	msgs, _ := store.LoadMessages(session.SessionID)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in store, got %d", len(msgs))
	}
}
