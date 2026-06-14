package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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
	t.Cleanup(func() { eng.Close() })
	eng.PersistNewMessages() // should not panic
}

func TestPersistNewMessages_EmptySessionID(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
		}, strings.Repeat("a", 200) + "…"},
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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
	t.Cleanup(func() { eng.Close() })
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

func TestLoadMessages(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	// Empty session — should return nil, nil
	msgs, err := eng.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages empty: %v", err)
	}
	if msgs != nil {
		t.Errorf("expected nil for empty session, got %d messages", len(msgs))
	}

	// Persist a message then load it
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	loaded, err := eng.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages after persist: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded))
	}
	text := ""
	for _, b := range loaded[0].Content {
		if b.Type == types.ContentTypeText {
			text = b.Text
		}
	}
	if text != "hello" {
		t.Errorf("loaded text = %q, want %q", text, "hello")
	}

	// Non-existent session returns nil (store treats it like empty)
	empty, err := eng.LoadMessages("nonexistent")
	if err != nil {
		t.Errorf("LoadMessages nonexistent: unexpected error: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil for nonexistent session, got %d messages", len(empty))
	}
}

func TestNewSession(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	err := eng.NewSession("/tmp/project", "My Title")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	eng.mu.RLock()
	sid := eng.sessionID
	pd := eng.projectDir
	eng.mu.RUnlock()
	if sid == "" {
		t.Error("sessionID should be set")
	}
	if pd != "/tmp/project" {
		t.Errorf("projectDir = %q, want /tmp/project", pd)
	}

	// Verify title was set
	ses, err := store.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "My Title" {
		t.Errorf("title = %q, want %q", ses.Title, "My Title")
	}

	// Empty title should not error
	err = eng.NewSession("/tmp/other", "")
	if err != nil {
		t.Fatalf("NewSession no title: %v", err)
	}
}

func TestNewSession_NoStore(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	err := eng.NewSession("/tmp/project", "title")
	if err == nil {
		t.Fatal("expected error with no store")
	}
	if !strings.Contains(err.Error(), "no store") {
		t.Errorf("error = %q, want mention of no store", err.Error())
	}
}

func TestListSessions(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "/tmp/project")

	_, err := store.CreateSession("/tmp/project", "model1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_, err = store.CreateSession("/tmp/project", "model1")
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}

	sessions, err := eng.ListSessions(10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}

	// Limit works
	sessions, err = eng.ListSessions(1)
	if err != nil {
		t.Fatalf("ListSessions limit: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session with limit, got %d", len(sessions))
	}
}

func TestListSessions_NoStore(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	_, err := eng.ListSessions(10)
	if err == nil {
		t.Fatal("expected error with no store")
	}
	if !strings.Contains(err.Error(), "no store") {
		t.Errorf("error = %q, want mention of no store", err.Error())
	}
}

func TestSwitchSession(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	// Create and populate session 1
	session1, err := store.CreateSession("/tmp/p1", "model1")
	if err != nil {
		t.Fatalf("CreateSession 1: %v", err)
	}
	eng.SetSessionID(session1.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	// Create session 2
	session2, err := store.CreateSession("/tmp/p2", "model1")
	if err != nil {
		t.Fatalf("CreateSession 2: %v", err)
	}
	eng.SetSessionID(session2.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg2")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	// Switch back to session 1
	msgs, err := eng.SwitchSession(session1.SessionID)
	if err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message from session1, got %d", len(msgs))
	}
	text := ""
	for _, b := range msgs[0].Content {
		if b.Type == types.ContentTypeText {
			text = b.Text
		}
	}
	if text != "msg1" {
		t.Errorf("switched message text = %q, want %q", text, "msg1")
	}

	eng.mu.RLock()
	sid := eng.sessionID
	eng.mu.RUnlock()
	if sid != session1.SessionID {
		t.Errorf("sessionID = %q, want %q", sid, session1.SessionID)
	}

	// Non-existent session: store returns nil, SwitchSession succeeds with empty messages
	emptyMsgs, err := eng.SwitchSession("nonexistent")
	if err != nil {
		t.Errorf("SwitchSession nonexistent: unexpected error: %v", err)
	}
	if emptyMsgs != nil {
		t.Errorf("expected nil messages for nonexistent session, got %d", len(emptyMsgs))
	}
}

func TestForkSession(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	session, err := store.CreateSession("/tmp/project", "model1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("original")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	// Fork with title
	forkedMsgs, err := eng.ForkSession("Forked Title")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if len(forkedMsgs) != 2 {
		t.Fatalf("expected 2 forked messages, got %d", len(forkedMsgs))
	}

	// Engine should now be on the forked session
	eng.mu.RLock()
	sid := eng.sessionID
	fork := eng.forkParentUUID
	eng.mu.RUnlock()
	if sid == session.SessionID {
		t.Error("sessionID should have changed to forked session")
	}
	if fork != "" {
		t.Errorf("forkParentUUID = %q, want empty (cleared by ForkSession)", fork)
	}

	// Verify title was set on the forked session
	ses, err := store.GetSession(sid)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Title != "Forked Title" {
		t.Errorf("forked title = %q, want %q", ses.Title, "Forked Title")
	}
}

func TestForkSession_NoTitle(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	session, err := store.CreateSession("/tmp/project", "model1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)

	_, err = eng.ForkSession("")
	if err != nil {
		t.Fatalf("ForkSession no title: %v", err)
	}
}

// TestPersistNewMessages_AttachmentNotStored verifies the full production chain:
// background job completes → EnqueueAttachment → processAttachments → runTurns
// → PersistNewMessages → DB should contain the LLM's response but NOT the
// raw attachment XML.
//
// Regression: attachment messages (job notifications, scope info) were being
// written to the conversation DB as user messages. They are ephemeral context
// for the current LLM call only and should never be persisted.
func TestPersistNewMessages_AttachmentNotStored(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	// First query: normal user question
	mp.addResponse(textStreamEvents("test-model", "Sure, I'll help."), nil)
	// Second query: LLM processes the attachment (triggered by processAttachments)
	mp.addResponse(textStreamEvents("test-model", "Background task completed successfully."), nil)

	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
		Logger:   slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetSystemPrompt(`{"role":"system","content":"You are a helpful assistant."}`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Run a normal query to establish the conversation
	result := eng.QuerySync(ctx, "Run a background task", "")
	if result.Error != nil {
		t.Fatalf("first query: %v", result.Error)
	}

	// 2. Background job completes — enqueue attachment into the queue directly
	//    (same as EnqueueAttachment but without triggering the async goroutine)
	jobXML := `<job-notification><job-id>bg-1</job-id><status>completed</status><summary>echo done</summary></job-notification>`
	eng.attachments.Enqueue(types.QueuedItem{
		Value:     jobXML,
		Mode:      types.ItemModeJob,
		IsMeta:    true,
		Origin:    &types.MessageOrigin{Kind: types.OriginJob},
		Timestamp: time.Date(2026, 5, 19, 20, 0, 0, 0, time.UTC),
	})

	// 3. Call processAttachments synchronously (same package, no goroutine).
	//    This creates the attachment message, appends it to e.messages,
	//    calls runTurns (LLM responds), and calls PersistNewMessages.
	eng.processAttachments(ctx, eng.systemPrompt)

	// 4. Run a third query — PersistNewMessages will persist the new user
	//    message + LLM response, but the attachment message should have been
	//    filtered during step 3's PersistNewMessages already.
	//    This step verifies the attachment wasn't left in an uncommitted state.
	mp.addResponse(textStreamEvents("test-model", "Got it, thanks for letting me know."), nil)
	result2 := eng.QuerySync(ctx, "How did the background task go?", "")
	if result2.Error != nil {
		t.Fatalf("third query: %v", result2.Error)
	}

	// 5. Verify DB contents
	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected messages in store, got 0")
	}

	// The LLM's response to the notification must be in DB
	var foundLLMResponse bool
	for _, m := range msgs {
		if strings.Contains(m.Content, "Background task completed") {
			foundLLMResponse = true
		}
	}
	if !foundLLMResponse {
		t.Error("LLM response to attachment should be persisted in DB")
	}

	// KEY CHECK: the raw attachment XML must NOT be in DB.
	// json.Marshal escapes < to \u003c, so check the unescaped keyword.
	for _, m := range msgs {
		if strings.Contains(m.Content, "job-notification") {
			t.Errorf("attachment XML should NOT be in DB, found: %s", m.Content[:min(len(m.Content), 200)])
		}
	}
}

func TestResumeOrInitSession_NoStore(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	sid, err := eng.ResumeOrInitSession("/tmp/project", "model1")
	if err != nil {
		t.Fatalf("ResumeOrInitSession no store: %v", err)
	}
	if sid != "" {
		t.Errorf("expected empty session ID with no store, got %q", sid)
	}
}

func TestResumeOrInitSession_NewSession(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	dir := t.TempDir()
	sid, err := eng.ResumeOrInitSession(dir, "model1")
	if err != nil {
		t.Fatalf("ResumeOrInitSession: %v", err)
	}
	if sid == "" {
		t.Fatal("expected non-empty session ID")
	}

	eng.mu.RLock()
	pd := eng.projectDir
	eng.mu.RUnlock()
	if pd != dir {
		t.Errorf("projectDir = %q, want %q", pd, dir)
	}
}

func TestResumeOrInitSession_ResumesExisting(t *testing.T) {
	store := newTestStore(t)
	dir := t.TempDir()

	// Create a session and persist a message
	session, err := store.CreateSession(dir, "model1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, dir)
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	// Write workspace meta pointing to this session
	metaDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	metaData, _ := json.Marshal(short.WorkspaceMeta{CurrentSessionID: session.SessionID})
	if err := os.WriteFile(filepath.Join(metaDir, "meta.json"), metaData, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// New engine should resume the session
	eng2 := New(&Params{Logger: slog.Default()})
	eng2.SetStore(store, "")

	sid, err := eng2.ResumeOrInitSession(dir, "model1")
	if err != nil {
		t.Fatalf("ResumeOrInitSession: %v", err)
	}
	if sid != session.SessionID {
		t.Errorf("resumed sessionID = %q, want %q", sid, session.SessionID)
	}

	eng2.mu.RLock()
	msgCount := len(eng2.messages)
	eng2.mu.RUnlock()
	if msgCount != 1 {
		t.Errorf("resumed messages count = %d, want 1", msgCount)
	}
}
