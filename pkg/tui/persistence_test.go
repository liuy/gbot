package tui

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
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

func TestPersistTurn_AutoTitle(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Session should have no title initially
	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "" {
		t.Fatalf("initial title should be empty, got %q", ses.Title)
	}

	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("help me fix a bug in auth.go")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("sure, let me look")}, Timestamp: time.Now()},
	})

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	a.persistTurn()

	// After first persist, session should be auto-titled with the first user prompt
	ses, err = store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession after persist: %v", err)
	}
	if ses.Title != "help me fix a bug in auth.go" {
		t.Errorf("auto-title = %q, want %q", ses.Title, "help me fix a bug in auth.go")
	}
}

func TestPersistTurn_AutoTitle_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Pre-set a custom title (simulates /session -n "my session")
	if err := store.UpdateSessionTitle(session.SessionID, "my custom title"); err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}

	eng := newTestEngine()
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("some prompt")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Now()},
	})

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	a.persistTurn()

	// Custom title should NOT be overwritten
	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "my custom title" {
		t.Errorf("title = %q, want %q (should not overwrite)", ses.Title, "my custom title")
	}
}

func TestExtractUserTitle(t *testing.T) {
	tests := []struct {
		name string
		msgs []types.Message
		want string
	}{
		{
			"first user text",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello world")}},
			},
			"hello world",
		},
		{
			"skips assistant messages",
			[]types.Message{
				{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("real prompt")}},
			},
			"real prompt",
		},
		{
			"skips XML tags",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<command-name>test</command-name>")}},
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("visible prompt")}},
			},
			"visible prompt",
		},
		{
			"truncates long text",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("a", 300))}},
			},
			strings.Repeat("a", 200) + "…",
		},
		{
			"skips empty text",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("")}},
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("actual")}},
			},
			"actual",
		},
		{
			"skips tool_result blocks",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewToolResultBlock("id1", json.RawMessage(`"result"`), false)}},
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("text after tool")}},
			},
			"text after tool",
		},
		{
			"empty messages",
			[]types.Message{},
			"",
		},
		{
			"only whitespace text",
			[]types.Message{
				{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("   \n  ")}},
			},
			"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUserTitle(tc.msgs)
			if got != tc.want {
				t.Errorf("extractUserTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPersistTurn_AutoTitle_SkipsSecondPersist(t *testing.T) {
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
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Now()},
	})

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		lastPersistedIdx: 0,
	}

	// First persist — auto-titles
	a.persistTurn()

	// Add more messages
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Now()},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second prompt")}, Timestamp: time.Now()},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply2")}, Timestamp: time.Now()},
	})

	// Second persist — should NOT change title
	a.persistTurn()

	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !strings.Contains(ses.Title, "first prompt") {
		t.Errorf("title = %q, should still contain first prompt", ses.Title)
	}
}
