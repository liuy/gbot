package engine

import (
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func newTestStore(t *testing.T) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPersistNewMessages_NilStore(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	eng.PersistNewMessages() // should not panic
}

func TestPersistNewMessages_EmptySessionID(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(newTestStore(t), "")
	eng.PersistNewMessages() // should not panic
}

func TestPersistNewMessages_NoUncommittedMessages(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)

	eng.PersistNewMessages()

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages in store, got %d", len(msgs))
	}
}

func TestPersistNewMessages_SuccessfulPersist(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	// SetStore computed lastPersistedIdx = 0 (empty messages at that time).
	// After SetMessages, we need to NOT update lastPersistedIdx so the new messages are "uncommitted".
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	eng.PersistNewMessages()

	if eng.LastPersistedIdx() != 2 {
		t.Fatalf("expected lastPersistedIdx=2, got %d", eng.LastPersistedIdx())
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
	eng.PersistNewMessages()
	msgs2, _ := store.LoadMessages(session.SessionID)
	if len(msgs2) != 2 {
		t.Fatalf("expected 2 messages after re-persist, got %d", len(msgs2))
	}
}

func TestPersistNewMessages_Incremental(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	// First persist: 1 message
	eng.PersistNewMessages()
	if eng.LastPersistedIdx() != 1 {
		t.Fatalf("expected lastPersistedIdx=1, got %d", eng.LastPersistedIdx())
	}

	// Add more messages
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply1")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	// lastPersistedIdx is 1, so messages[1:] ("reply1", "second") are uncommitted

	// Second persist: only the 2 new messages
	eng.PersistNewMessages()
	if eng.LastPersistedIdx() != 3 {
		t.Fatalf("expected lastPersistedIdx=3, got %d", eng.LastPersistedIdx())
	}

	msgs, _ := store.LoadMessages(session.SessionID)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in store, got %d", len(msgs))
	}
}

func TestPersistNewMessages_AutoTitle(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "" {
		t.Fatalf("initial title should be empty, got %q", ses.Title)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("help me fix a bug in auth.go")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("sure, let me look")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	eng.PersistNewMessages()

	ses, err = store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession after persist: %v", err)
	}
	if ses.Title != "help me fix a bug in auth.go" {
		t.Errorf("auto-title = %q, want %q", ses.Title, "help me fix a bug in auth.go")
	}
}

func TestPersistNewMessages_AutoTitle_DoesNotOverwrite(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := store.UpdateSessionTitle(session.SessionID, "my custom title"); err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("some prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	eng.PersistNewMessages()

	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "my custom title" {
		t.Errorf("title = %q, want %q (should not overwrite)", ses.Title, "my custom title")
	}
}

func TestPersistNewMessages_AutoTitle_SkipsSecondPersist(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	// First persist — auto-titles
	eng.PersistNewMessages()

	// Add more messages
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply2")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})

	// Second persist — should NOT change title
	eng.PersistNewMessages()

	ses, err := store.GetSession(session.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !strings.Contains(ses.Title, "first prompt") {
		t.Errorf("title = %q, should still contain first prompt", ses.Title)
	}
}

func TestExtractUserTitle_Engine(t *testing.T) {
	tests := []struct {
		name string
		msgs []types.Message
		want string
	}{
		{"first user text", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello world")}},
		}, "hello world"},
		{"skips assistant messages", []types.Message{
			{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("real prompt")}},
		}, "real prompt"},
		{"skips XML tags", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("<command-name>test</command-name>")}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("visible prompt")}},
		}, "visible prompt"},
		{"truncates long text", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock(strings.Repeat("a", 300))}},
		}, strings.Repeat("a", 200)+"…"},
		{"skips empty text", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("")}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("actual")}},
		}, "actual"},
		{"skips tool_result blocks", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewToolResultBlock("id1", json.RawMessage(`"result"`), false)}},
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("text after tool")}},
		}, "text after tool"},
		{"empty messages", []types.Message{}, ""},
		{"only whitespace text", []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("   \n  ")}},
		}, ""},
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

func TestSetStore_ComputesLastPersistedIdx(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("world")}},
	})

	eng.SetStore(store, "/tmp/project")

	if eng.LastPersistedIdx() != 2 {
		t.Errorf("lastPersistedIdx = %d, want 2 (len of messages)", eng.LastPersistedIdx())
	}
	if !eng.HasStore() {
		t.Error("HasStore() = false, want true")
	}
}

func TestReset_ClearsPersistState(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	eng.SetStore(store, "/tmp/project")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})

	// Simulate fork state
	eng.mu.Lock()
	eng.forkParentUUID = "some-uuid"
	eng.lastPersistedIdx = 1
	eng.mu.Unlock()

	eng.Reset()

	if eng.LastPersistedIdx() != 0 {
		t.Errorf("lastPersistedIdx after Reset = %d, want 0", eng.LastPersistedIdx())
	}
	eng.mu.RLock()
	fork := eng.forkParentUUID
	eng.mu.RUnlock()
	if fork != "" {
		t.Errorf("forkParentUUID after Reset = %q, want empty", fork)
	}
}

func TestMarkAllPersisted_AfterCompact(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	eng.SetMessages(makeLargeMessages(20, 100))
	eng.mu.Lock()
	eng.lastPersistedIdx = 20
	eng.mu.Unlock()

	// Simulate compact replacing messages
	compacted := makeLargeMessages(5, 100)
	eng.mu.Lock()
	eng.messages = compacted
	eng.markAllPersisted()
	eng.mu.Unlock()

	if eng.LastPersistedIdx() != 5 {
		t.Errorf("lastPersistedIdx after compact = %d, want 5", eng.LastPersistedIdx())
	}
}
