package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
	"modernc.org/sqlite"
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

// pinLocalCST fixes the local zone for timestamp rendering assertions:
// injectTimestamp renders restored messages through time.Local, whose offset
// otherwise varies by host.
func pinLocalCST(t *testing.T) {
	t.Helper()
	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })
}

func TestMarshalMessages_RestoredTimestampRenderedLocal(t *testing.T) {
	pinLocalCST(t)
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
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)},
	})
	eng.PersistNewMessages()

	eng2 := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(store, "")
	msgs, err := eng2.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("restored message count = %d, want 1", len(msgs))
	}
	eng2.SetMessages(msgs)

	got := eng2.marshalMessages()
	if len(got) != 1 {
		t.Fatalf("marshalMessages count = %d, want 1", len(got))
	}
	if len(got[0].Content) != 1 {
		t.Fatalf("content block count = %d, want 1", len(got[0].Content))
	}
	// 10:00 UTC == 18:00 in the pinned +08:00 zone; restored messages carry a
	// UTC location from the driver scan and must render in local wall clock
	// like in-session new messages.
	if got[0].Content[0].Text != "[2026-07-15 18:00:00 CST-TEST] hello" {
		t.Errorf("text = %q, want %q", got[0].Content[0].Text, "[2026-07-15 18:00:00 CST-TEST] hello")
	}
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

	// Non-existent session: must ERROR and leave the engine on the current
	// session. A silent switch into a ghost session bricked persistence for
	// that engine forever (every append hit the messages→sessions FK), so
	// restoreEngines' "switch failed → create new" fallback never fired.
	if _, err := eng.SwitchSession("nonexistent"); err == nil {
		t.Error("SwitchSession nonexistent: expected error, got nil")
	}
	eng.mu.RLock()
	sid2 := eng.sessionID
	eng.mu.RUnlock()
	if sid2 != session1.SessionID {
		t.Errorf("after failed switch sessionID = %q, want unchanged %q", sid2, session1.SessionID)
	}
}

// An existing session with zero messages (fresh session_new row) must
// switch cleanly — the existence check may not over-tighten into
// rejecting legitimately empty sessions.
func TestSwitchSession_EmptyExistingSession(t *testing.T) {
	store := newTestStore(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")

	from, err := store.CreateSession("/tmp/p1", "model1")
	if err != nil {
		t.Fatalf("CreateSession from: %v", err)
	}
	eng.SetSessionID(from.SessionID)
	to, err := store.CreateSession("/tmp/p2", "model1")
	if err != nil {
		t.Fatalf("CreateSession to: %v", err)
	}

	msgs, err := eng.SwitchSession(to.SessionID)
	if err != nil {
		t.Fatalf("SwitchSession to empty session: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("messages = %d, want 0", len(msgs))
	}
	eng.mu.RLock()
	sid := eng.sessionID
	eng.mu.RUnlock()
	if sid != to.SessionID {
		t.Errorf("sessionID = %q, want %q", sid, to.SessionID)
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

// TestPersistNewMessages_JobNotificationStoredInFinalForm verifies the full
// production chain: background job completes → EnqueueAttachment →
// processAttachments → runTurns → PersistNewMessages → DB contains the LLM's
// response AND the drained job notification, stored once, in the assembled
// wire form, as a plain user message. The notification used to be ephemeral
// (attachment message dropped by the persist filter), which broke the
// prompt-cache prefix across restarts; the drain site now persists the final
// form directly.
func TestPersistNewMessages_JobNotificationStoredInFinalForm(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	// First query: normal user question
	mp.addResponse(textStreamEvents("test-model", "Sure, I'll help."), nil)
	// Second query: LLM processes the notification (triggered by processAttachments)
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
	//    The item is appended as a persisted plain user message in final wire
	//    form, then runTurns lets the LLM respond and PersistNewMessages
	//    commits the turn.
	eng.processAttachments(ctx, 0, eng.systemPrompt)

	// 4. Run a third query — verifies nothing was left in an uncommitted
	//    state (a second row for the notification would mean a re-drain).
	mp.addResponse(textStreamEvents("test-model", "Got it, thanks for letting me know."), nil)
	result2 := eng.QuerySync(ctx, "How did the background task go?", "")
	if result2.Error != nil {
		t.Fatalf("third query: %v", result2.Error)
	}

	// 5. Verify DB contents — decoded form for content assertions (the raw
	//    Content column stores json.Marshal output, so '<' is \u003c and a
	//    '<job-notification>' needle never matches it).
	storeMsgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(storeMsgs) == 0 {
		t.Fatal("expected messages in store, got 0")
	}
	engMsgs, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}

	// Exactly one row carries the notification envelope, in final wire form.
	var envelopeRows []int
	for i, m := range engMsgs {
		if utils.IsInjectedReminderEnvelope(utils.FirstTextBlockContent(m)) {
			envelopeRows = append(envelopeRows, i)
		}
	}
	if len(envelopeRows) != 1 {
		t.Fatalf("store holds %d job envelope rows, want exactly 1", len(envelopeRows))
	}
	row := engMsgs[envelopeRows[0]]
	if row.Role != types.RoleUser {
		t.Errorf("envelope row role = %q, want user", row.Role)
	}
	if row.MessageType != types.MessageTypeUser {
		t.Errorf("envelope row MessageType = %q, want %q (an unstamped row round-trips as \"\")",
			row.MessageType, types.MessageTypeUser)
	}
	if row.Attachment != nil {
		t.Errorf("envelope row still carries Attachment metadata: %+v", row.Attachment)
	}
	if row.HasFlag(types.FlagMeta) {
		t.Error("envelope row carries FlagMeta — the wui history filter would drop it (and the enqueued item's IsMeta must be ignored)")
	}
	wantText := "<system-reminder>\n" + wrapOriginText(jobXML, &types.MessageOrigin{Kind: types.OriginJob}) + "\n</system-reminder>"
	if text := utils.FirstTextBlockContent(row); text != wantText {
		t.Errorf("envelope row text = %q, want %q", text, wantText)
	}

	// Raw-column equivalent: the bare keyword (no angle brackets) appears in
	// exactly one row.
	rawHits := 0
	for _, m := range storeMsgs {
		if strings.Contains(m.Content, "job-notification") {
			rawHits++
		}
	}
	if rawHits != 1 {
		t.Errorf("raw Content rows containing 'job-notification' = %d, want 1", rawHits)
	}

	// The LLM's response to the notification must be in DB.
	var foundLLMResponse bool
	for _, m := range engMsgs {
		if strings.Contains(utils.FirstTextBlockContent(m), "Background task completed") {
			foundLLMResponse = true
		}
	}
	if !foundLLMResponse {
		t.Error("LLM response to the notification should be persisted in DB")
	}
}

// A nil store must yield a clean error from SwitchSession — the restore
// fallback and the WUI switch handler branch on this error.
func TestSwitchSession_NoStore(t *testing.T) {
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.SwitchSession("some-session"); err == nil {
		t.Error("SwitchSession with no store: expected error, got nil")
	}
}

// TestPersistNewMessages_MultiTurnWithToolUse verifies that multi-turn
// messages including tool_use/tool_result pairs survive persist+reload.
// This is the critical path for abort scenarios: when the user ESCs during
// the last turn, all successful prior turns must be persisted so they
// survive a restart.
func TestPersistNewMessages_MultiTurnWithToolUse(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)

	// Simulate a multi-turn query: user → assistant(tool_call) → user(tool_result) → assistant(partial)
	// The last assistant message has a tool_use that was interrupted (abort).
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("read the config")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolUse, ID: "toolu_1", Name: "Read", Input: json.RawMessage(`{"file_path":"/etc/config"}`)},
		}, Timestamp: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "toolu_1", Content: json.RawMessage(`[{"type":"text","text":"key=value"}]`)},
		}, Timestamp: time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)},

		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("[Request interrupted by user]")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	eng.PersistNewMessages()

	// Reload from store — simulates restart.
	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages in store after persist, got %d", len(msgs))
	}

	// Verify tool_use content survived the round-trip.
	engMsgs, err := short.StoreMessagesToEngine(msgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if engMsgs[1].Role != types.RoleAssistant {
		t.Errorf("msg[1].Role = %q, want assistant", engMsgs[1].Role)
	}
	foundToolUse := false
	for _, block := range engMsgs[1].Content {
		if block.Type == types.ContentTypeToolUse && block.Name == "Read" {
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Error("tool_use block lost after persist+reload — abort query content not preserved")
	}
}

// TestPersistNewMessages_PromptAttachmentPersisted verifies that queued
// prompt-mode attachments (user messages sent while a query is streaming)
// survive persist+reload as regular user messages. Without this, every
// message queued during an interruption vanishes from the DB and replay
// shows the assistant answering a question that is not there.
func TestPersistNewMessages_PromptAttachmentPersisted(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)

	promptMsg := types.Message{
		ID:          "att-prompt-1",
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("你怎么一直tool use？现在先复现问题")},
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "你怎么一直tool use？现在先复现问题",
			Mode:   types.ItemModePrompt,
		},
		Timestamp: time.Date(2026, 8, 16, 14, 11, 51, 0, time.UTC),
	}
	jobMsg := types.Message{
		ID:          "att-job-1",
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("<job>bg-1 done</job>")},
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "<job>bg-1 done</job>",
			Mode:   types.ItemModeJob,
		},
		Timestamp: time.Date(2026, 8, 16, 14, 11, 52, 0, time.UTC),
	}
	metaPromptMsg := types.Message{
		ID:          "att-meta-1",
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("(system-generated prompt)")},
		Attachment: &types.Attachment{
			Type:   types.AttachmentTypeQueued,
			Prompt: "(system-generated prompt)",
			Mode:   types.ItemModePrompt,
			IsMeta: true,
		},
		Timestamp: time.Date(2026, 8, 16, 14, 11, 53, 0, time.UTC),
	}
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("previous query")}, Timestamp: time.Date(2026, 8, 16, 14, 10, 0, 0, time.UTC)},
		promptMsg,
		jobMsg,
		metaPromptMsg,
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("收到，我先复现")}, Timestamp: time.Date(2026, 8, 16, 14, 12, 18, 0, time.UTC)},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	eng.PersistNewMessages()

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	// Prompt attachment must persist; job and meta-prompt attachments stay ephemeral.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages in store (user/prompt-as-user/assistant), got %d", len(msgs))
	}
	engMsgs, err := short.StoreMessagesToEngine(msgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if engMsgs[1].MessageType != types.MessageTypeUser {
		t.Errorf("prompt attachment reloaded as MessageType=%q, want %q (plain user)",
			engMsgs[1].MessageType, types.MessageTypeUser)
	}
	if engMsgs[1].Attachment != nil {
		t.Errorf("reloaded prompt attachment still carries Attachment metadata: %+v", engMsgs[1].Attachment)
	}
	text := textOfBlocks(engMsgs[1].Content)
	if text != "你怎么一直tool use？现在先复现问题" {
		t.Errorf("reloaded prompt attachment text = %q, want original user text", text)
	}
}

func textOfBlocks(blocks []types.ContentBlock) string {
	for _, b := range blocks {
		if b.Type == types.ContentTypeText {
			return b.Text
		}
	}
	return ""
}

// TestListSessions_FilteredByEngineID verifies that Engine.ListSessions
// only returns sessions created by the same engine. Without the engine_id
// filter, agent-2 sees agent-1's sessions in the /session picker — which
// breaks the per-agent isolation contract.
func TestListSessions_FilteredByEngineID(t *testing.T) {
	store := newTestStore(t)

	// Engine 1 (main): creates 2 sessions.
	eng1 := New(&Params{Logger: slog.Default(), EngineID: "main"})
	t.Cleanup(func() { eng1.Close() })
	eng1.SetStore(store, "/tmp/project")
	if _, err := store.CreateSessionWithEngine("/tmp/project", "m1", "main"); err != nil {
		t.Fatalf("CreateSession main 1: %v", err)
	}
	if _, err := store.CreateSessionWithEngine("/tmp/project", "m1", "main"); err != nil {
		t.Fatalf("CreateSession main 2: %v", err)
	}

	// Engine 2 (e2): creates 1 session.
	eng2 := New(&Params{Logger: slog.Default(), EngineID: "e2"})
	t.Cleanup(func() { eng2.Close() })
	eng2.SetStore(store, "/tmp/project")
	if _, err := store.CreateSessionWithEngine("/tmp/project", "m2", "e2"); err != nil {
		t.Fatalf("CreateSession e2: %v", err)
	}

	// Engine 1 should only see its own 2 sessions.
	sessions1, err := eng1.ListSessions(100)
	if err != nil {
		t.Fatalf("eng1 ListSessions: %v", err)
	}
	if len(sessions1) != 2 {
		t.Errorf("eng1 ListSessions = %d sessions, want 2 (should not see e2's sessions)", len(sessions1))
	}
	for _, s := range sessions1 {
		if s.EngineID != "main" {
			t.Errorf("eng1 ListSessions returned session with EngineID=%q, want main", s.EngineID)
		}
	}

	// Engine 2 should only see its own 1 session.
	sessions2, err := eng2.ListSessions(100)
	if err != nil {
		t.Fatalf("eng2 ListSessions: %v", err)
	}
	if len(sessions2) != 1 {
		t.Fatalf("eng2 ListSessions = %d sessions, want 1 (should not see main's sessions)", len(sessions2))
	}
	if sessions2[0].EngineID != "e2" {
		t.Errorf("eng2 ListSessions returned session with EngineID=%q, want e2", sessions2[0].EngineID)
	}
}

// ---------------------------------------------------------------------------
// Three-tier persistence: query entry / turn boundary / query-exit defer
// ---------------------------------------------------------------------------

// newPersistTestEngine wires an engine for the three-tier persistence tests:
// real store, mock provider, mock tools. QuerySync runs the loop in the test
// goroutine, so callFn/post-turn-hook captures are deterministic mid-query
// observation points.
func newPersistTestEngine(t *testing.T, store *short.Store, sessionID string, mp *mockProvider, tools map[string]tool.Tool) *Engine {
	t.Helper()
	eng := New(&Params{
		Provider:      mp,
		ToolsProvider: func() map[string]tool.Tool { return tools },
		Model:         "test-model",
		Logger:        slog.Default(),
	})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(sessionID)
	eng.SetSystemPrompt(`{"role":"system","content":"You are a helpful assistant."}`)
	return eng
}

// TestRunTurns_EntryPersist_UserMessageBeforeFirstTurn verifies the query's
// user message reaches the DB at query entry, before the first turn completes.
// With only the query-exit defer, a process kill during the first LLM call
// lost the user's request entirely (the 2026-09 OOM incident).
func TestRunTurns_EntryPersist_UserMessageBeforeFirstTurn(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var snapshot []*short.TranscriptMessage
	var snapErr error
	toolA := &mockTool{
		name:    "tool_a",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			snapshot, snapErr = store.LoadMessages(session.SessionID)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-a", "tool_a", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "done"), nil)

	eng := newPersistTestEngine(t, store, session.SessionID, mp, map[string]tool.Tool{toolA.Name(): toolA})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "check the build", ""); result.Error != nil {
		t.Fatalf("query: %v", result.Error)
	}
	if snapErr != nil {
		t.Fatalf("mid-query LoadMessages: %v", snapErr)
	}
	if snapshot == nil {
		t.Fatal("tool callFn never ran — test setup broken")
	}
	if len(snapshot) != 1 {
		t.Fatalf("store during first turn has %d messages, want 1 (user message persisted at query entry)", len(snapshot))
	}
	snapEng, err := short.StoreMessagesToEngine(snapshot)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if snapEng[0].Role != types.RoleUser {
		t.Errorf("entry-persisted message role = %q, want user", snapEng[0].Role)
	}
	if text := textOfBlocks(snapEng[0].Content); text != "check the build" {
		t.Errorf("entry-persisted message text = %q, want %q", text, "check the build")
	}
}

// TestRunTurns_TurnBoundaryPersist_MidQuery verifies a completed tool-use turn
// (assistant tool_use + tool_result) is persisted at the turn boundary, before
// the query finishes. Before turn-level persistence, the DB stayed empty for
// the whole query, so a mid-query kill lost every completed turn.
func TestRunTurns_TurnBoundaryPersist_MidQuery(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// tool_b runs in turn 2, i.e. strictly after the turn-1 boundary persist.
	var afterTurn1 []*short.TranscriptMessage
	var snapErr error
	toolA := &mockTool{name: "tool_a", enabled: true}
	toolB := &mockTool{
		name:    "tool_b",
		enabled: true,
		callFn: func(_ context.Context, _ json.RawMessage, _ *tool.ToolUseContext) (*tool.ToolResult, error) {
			afterTurn1, snapErr = store.LoadMessages(session.SessionID)
			return &tool.ToolResult{Data: "ok"}, nil
		},
	}

	mp := &mockProvider{}
	mp.addResponse(toolUseStreamEvents("test-model", "tu-a", "tool_a", `{}`), nil)
	mp.addResponse(toolUseStreamEvents("test-model", "tu-b", "tool_b", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "All done."), nil)

	eng := newPersistTestEngine(t, store, session.SessionID, mp, map[string]tool.Tool{
		toolA.Name(): toolA,
		toolB.Name(): toolB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "run the checks", ""); result.Error != nil {
		t.Fatalf("query: %v", result.Error)
	}
	if snapErr != nil {
		t.Fatalf("mid-query LoadMessages: %v", snapErr)
	}
	if afterTurn1 == nil {
		t.Fatal("tool_b callFn never ran — test setup broken")
	}
	if len(afterTurn1) != 3 {
		t.Fatalf("store during turn 2 has %d messages, want 3 (user + turn-1 assistant + tool_result)", len(afterTurn1))
	}
	turn1, err := short.StoreMessagesToEngine(afterTurn1)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	wantRoles := []string{string(types.RoleUser), string(types.RoleAssistant), string(types.RoleUser)}
	for i, want := range wantRoles {
		if string(turn1[i].Role) != want {
			t.Errorf("turn-1 persisted msg[%d].Role = %q, want %q", i, turn1[i].Role, want)
		}
	}
	var toolUseSeen, toolResultSeen bool
	for _, b := range turn1[1].Content {
		if b.Type == types.ContentTypeToolUse && b.Name == "tool_a" {
			toolUseSeen = true
		}
	}
	for _, b := range turn1[2].Content {
		if b.Type == types.ContentTypeToolResult && b.ToolUseID == "tu-a" {
			toolResultSeen = true
		}
	}
	if !toolUseSeen {
		t.Error("turn-1 persisted assistant message has no tool_use block for tool_a")
	}
	if !toolResultSeen {
		t.Error("turn-1 persisted messages have no tool_result for tu-a — completed turn lost mid-query")
	}

	// Query finished: user + 2 tool turns + terminal answer = 6 messages.
	final, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("final LoadMessages: %v", err)
	}
	if len(final) != 6 {
		t.Fatalf("store after query has %d messages, want 6", len(final))
	}
}

// TestRunTurns_TerminalTurnPersistedByDefer verifies the terminal text-only
// answer is persisted by the query-exit defer: the terminal path returns from
// a branch that never reaches the loop-continue turn boundary.
func TestRunTurns_TerminalTurnPersistedByDefer(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	mp := &mockProvider{}
	mp.addResponse(textStreamEvents("test-model", "The final answer is 42."), nil)
	eng := newPersistTestEngine(t, store, session.SessionID, mp, nil)

	// firePostTurnHooks only runs when ContextWindow > 0; compactor stays nil
	// so shouldAutoCompact never fires.
	eng.SetCompactor(nil, AutoCompactConfig{ContextWindow: 128000})
	var hookSnapshot []*short.TranscriptMessage
	var hookLoadErr error
	var hookRan bool
	eng.RegisterPostTurnHook(func(_ context.Context, _ []types.Message, _ int, _ string) {
		if hookRan {
			return
		}
		hookRan = true
		hookSnapshot, hookLoadErr = store.LoadMessages(session.SessionID)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "meaning of life", ""); result.Error != nil {
		t.Fatalf("query: %v", result.Error)
	}
	if !hookRan {
		t.Fatal("post-turn hook never fired — test setup broken")
	}
	if hookLoadErr != nil {
		t.Fatalf("post-turn hook LoadMessages: %v", hookLoadErr)
	}
	// Terminal-path post-turn hook runs before the return+defer, so at that
	// point the DB must hold exactly the entry-persisted user message.
	if len(hookSnapshot) != 1 {
		t.Fatalf("store at terminal post-turn hook has %d messages, want 1 (user only, final answer not yet persisted)", len(hookSnapshot))
	}

	final, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("final LoadMessages: %v", err)
	}
	if len(final) != 2 {
		t.Fatalf("store after query has %d messages, want 2 (user + final answer)", len(final))
	}
	finalEng, err := short.StoreMessagesToEngine(final)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}
	if finalEng[1].Role != types.RoleAssistant {
		t.Errorf("final msg role = %q, want assistant", finalEng[1].Role)
	}
	if text := textOfBlocks(finalEng[1].Content); text != "The final answer is 42." {
		t.Errorf("final answer text = %q, want %q", text, "The final answer is 42.")
	}
	if idx := eng.LastPersistedIdx(); idx != 2 {
		t.Errorf("LastPersistedIdx after query = %d, want 2", idx)
	}
}

// TestRunTurns_SubagentGuardSkipsPersistPoints verifies a sub-agent engine
// skips all three persistence call points — sub-agent transcripts are
// ephemeral, and the call-site guard keeps the per-turn skip INFO log out of
// sub-agent runs.
func TestRunTurns_SubagentGuardSkipsPersistPoints(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	toolA := &mockTool{name: "tool_a", enabled: true}
	mp := &mockProvider{}
	// Tool turn exercises the turn-boundary call site; terminal text exercises
	// entry + defer; all three must be no-ops for a sub-agent.
	mp.addResponse(toolUseStreamEvents("test-model", "tu-a", "tool_a", `{}`), nil)
	mp.addResponse(textStreamEvents("test-model", "sub-agent done"), nil)

	eng := newPersistTestEngine(t, store, session.SessionID, mp, map[string]tool.Tool{toolA.Name(): toolA})
	eng.isSubagent = true

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result := eng.QuerySync(ctx, "sub-agent task", ""); result.Error != nil {
		t.Fatalf("query: %v", result.Error)
	}

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("sub-agent wrote %d messages to the session store, want 0 (all persist points guarded)", len(msgs))
	}
	if idx := eng.LastPersistedIdx(); idx != 0 {
		t.Errorf("sub-agent LastPersistedIdx = %d, want 0", idx)
	}
}

// --- TestPersist*: second-round mutation gaps in PersistNewMessages /
// LoadMessages / extractUserTitle.

// blockSessionUpdates makes every UPDATE on the sessions table fail, which is
// the only way to reach the post-append store-error branches (timestamp,
// context tokens, title) while the message INSERT path still succeeds.
func blockSessionUpdates(t *testing.T, store *short.Store) {
	t.Helper()
	if _, err := store.DB().Exec(
		`CREATE TRIGGER mutf_block_sessions_update BEFORE UPDATE ON sessions
		 BEGIN SELECT RAISE(ABORT, 'mutf: sessions update blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

func TestPersist_SkipGuardMatrix(t *testing.T) {
	// store==nil + sessionID=="" — both guard operands true.
	rec := captureSlog(t)
	engNil := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engNil.Close() })
	engNil.PersistNewMessages()
	if _, ok := rec.find("PersistNewMessages: skip persistence", func(a map[string]any) bool {
		return a["store_nil"] == true && a["sessionID"] == "" && a["subagent"] == false
	}); !ok {
		t.Fatalf("nil-store skip record missing or wrong attrs: %+v", rec.calls)
	}

	// store set + sessionID=="" — only the sessionID operand is true.
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	engNoSID := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engNoSID.Close() })
	engNoSID.SetStore(store, "")
	engNoSID.SetMessages([]types.Message{CreateUserMessage("must not persist")})
	engNoSID.PersistNewMessages()
	if _, ok := rec.find("PersistNewMessages: skip persistence", func(a map[string]any) bool {
		return a["store_nil"] == false && a["sessionID"] == ""
	}); !ok {
		t.Fatalf("empty-sessionID skip record missing or wrong attrs: %+v", rec.calls)
	}
	if msgs, err := store.LoadMessages(session.SessionID); err != nil || len(msgs) != 0 {
		t.Fatalf("store messages after empty-sessionID persist = %d (err %v), want 0", len(msgs), err)
	}

	// store==nil + sessionID set — only the store operand is true.
	engNilSID := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engNilSID.Close() })
	engNilSID.SetSessionID("mutf-nil-store")
	engNilSID.SetMessages([]types.Message{CreateUserMessage("x")})
	engNilSID.PersistNewMessages()
	if _, ok := rec.find("PersistNewMessages: skip persistence", func(a map[string]any) bool {
		return a["store_nil"] == true && a["sessionID"] == "mutf-nil-store"
	}); !ok {
		t.Fatalf("nil-store-with-sessionID skip record missing: %+v", rec.calls)
	}
}

func TestPersist_NothingNewLog(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{CreateUserMessage("one")})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	rec := captureSlog(t)
	eng.PersistNewMessages()
	if _, ok := rec.find("PersistNewMessages: nothing new", func(a map[string]any) bool {
		return a["messages"] == int64(1) && a["lastPersistedIdx"] == int64(1) &&
			a["engine"] == eng.engineID && a["session"] == session.SessionID[:8]
	}); !ok {
		t.Fatalf("nothing-new record missing or wrong attrs: %+v", rec.calls)
	}
	if got := eng.LastPersistedIdx(); got != 1 {
		t.Errorf("lastPersistedIdx = %d, want 1 (nothing-new path must not advance the cursor)", got)
	}
	if msgs, err := store.LoadMessages(session.SessionID); err != nil || len(msgs) != 1 {
		t.Fatalf("store messages = %d (err %v), want 1", len(msgs), err)
	}
}

func TestPersist_AttachmentFilterNilAttachment(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	// An attachment-typed message with no Attachment payload: the nil check is
	// what keeps the mode/meta probes from dereferencing nil.
	eng.SetMessages([]types.Message{{
		ID:          "mutf-att-nil",
		Role:        types.RoleUser,
		MessageType: types.MessageTypeAttachment,
		Content:     []types.ContentBlock{types.NewTextBlock("payload-less attachment")},
	}})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()
	if msgs, err := store.LoadMessages(session.SessionID); err != nil || len(msgs) != 0 {
		t.Fatalf("store messages = %d (err %v), want 0 (nil-attachment message stays ephemeral)", len(msgs), err)
	}
}

func TestPersist_AttachmentFilterMetaAndMode(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	att := func(id, text string, mode types.ItemMode, isMeta bool) types.Message {
		return types.Message{
			ID:          id,
			Role:        types.RoleUser,
			MessageType: types.MessageTypeAttachment,
			Content:     []types.ContentBlock{types.NewTextBlock(text)},
			Attachment:  &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: text, Mode: mode, IsMeta: isMeta},
		}
	}
	eng.SetMessages([]types.Message{
		att("mutf-att-prompt", "keep me", types.ItemModePrompt, false),
		att("mutf-att-meta", "meta prompt", types.ItemModePrompt, true),
		att("mutf-att-job", "job notification text", types.ItemModeJob, false),
		att("mutf-att-note", "plain notification", types.ItemModeNotification, false),
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	msgs, err := store.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("store messages = %d, want 1 (only the non-meta prompt attachment persists): %+v",
			len(msgs), messageUUIDs(msgs))
	}
	if msgs[0].UUID != "mutf-att-prompt" {
		t.Errorf("persisted uuid = %q, want mutf-att-prompt", msgs[0].UUID)
	}
}

func messageUUIDs(msgs []*short.TranscriptMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.UUID)
	}
	return out
}

func TestPersist_ForkPointAppliedAndCleared(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{{ID: "mutf-fork-root", Role: types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("root")}}})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	// Rewind semantics: the next batch must chain off the explicit fork parent.
	eng.mu.Lock()
	eng.forkParentUUID = "mutf-fork-root"
	eng.mu.Unlock()
	eng.SetMessages([]types.Message{
		{ID: "mutf-fork-root", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("root")}},
		{ID: "mutf-fork-child", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("branch")}},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 1
	eng.mu.Unlock()
	eng.PersistNewMessages()

	child, err := store.GetMessageByUUID("mutf-fork-child")
	if err != nil {
		t.Fatalf("GetMessageByUUID: %v", err)
	}
	if child == nil {
		t.Fatal("fork-point batch was not appended")
	}
	if child.ParentUUID != "mutf-fork-root" {
		t.Fatalf("fork batch parent_uuid = %q, want mutf-fork-root", child.ParentUUID)
	}

	// The fork point is single-use: the following batch chains normally.
	eng.SetMessages([]types.Message{
		{ID: "mutf-fork-root", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("root")}},
		{ID: "mutf-fork-child", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("branch")}},
		{ID: "mutf-fork-next", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("next")}},
	})
	eng.mu.Lock()
	eng.lastPersistedIdx = 2
	eng.mu.Unlock()
	eng.PersistNewMessages()

	next, err := store.GetMessageByUUID("mutf-fork-next")
	if err != nil {
		t.Fatalf("GetMessageByUUID next: %v", err)
	}
	if next == nil {
		t.Fatal("post-fork batch was not appended")
	}
	if next.ParentUUID != "mutf-fork-child" {
		t.Errorf("post-fork parent_uuid = %q, want mutf-fork-child (fork point must be cleared after use)", next.ParentUUID)
	}
}

func TestPersist_AppendFailureKeepsCursor(t *testing.T) {
	store := newTestStore(t)
	rec := captureSlog(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	// Session row does not exist: the messages INSERT hits the FK constraint.
	eng.SetSessionID("mutf-missing-session")
	eng.SetMessages([]types.Message{CreateUserMessage("orphan")})
	eng.PersistNewMessages()

	if _, ok := rec.find("PersistNewMessages: append messages", func(a map[string]any) bool {
		e, _ := a["error"].(error)
		return e != nil && strings.Contains(e.Error(), "FOREIGN KEY")
	}); !ok {
		t.Fatalf("append-error record missing: %+v", rec.calls)
	}
	want := 0 // the failed append must not advance the cursor
	if got := eng.LastPersistedIdx(); got != want {
		t.Errorf("lastPersistedIdx = %d, want %d", got, want)
	}
}

func TestPersist_SessionUpdateFailuresLogged(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	blockSessionUpdates(t, store)

	rec := captureSlog(t)
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.ContextTokens = 5
	eng.SetMessages([]types.Message{CreateUserMessage("title source")})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()
	eng.PersistNewMessages()

	for _, msg := range []string{
		"PersistNewMessages: update session timestamp",
		"PersistNewMessages: update context tokens",
		"PersistNewMessages: auto-title",
	} {
		if _, ok := rec.find(msg, func(a map[string]any) bool {
			e, _ := a["error"].(error)
			return e != nil && strings.Contains(e.Error(), "mutf: sessions update blocked")
		}); !ok {
			t.Errorf("%q error record missing: %+v", msg, rec.calls)
		}
	}
	// The messages themselves still land, and the cursor advances.
	if _, ok := rec.find("PersistNewMessages: persisted messages", func(a map[string]any) bool {
		return a["count"] == int64(1) && a["total"] == int64(1)
	}); !ok {
		t.Errorf("persisted-messages record missing: %+v", rec.calls)
	}
	if msgs, err := store.LoadMessages(session.SessionID); err != nil || len(msgs) != 1 {
		t.Fatalf("store messages = %d (err %v), want 1", len(msgs), err)
	}
}

func TestPersist_ContextTokensGate(t *testing.T) {
	cases := []struct {
		name string
		eng  int
		want int
	}{
		{"zero skips write", 0, 7},
		{"one writes", 1, 1},
		{"positive writes", 5, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			session, err := store.CreateSession("", "test-model")
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			if err := store.UpdateContextTokens(session.SessionID, 7); err != nil {
				t.Fatalf("seed context tokens: %v", err)
			}
			eng := New(&Params{Logger: slog.Default()})
			t.Cleanup(func() { eng.Close() })
			eng.SetStore(store, "")
			eng.SetSessionID(session.SessionID)
			eng.ContextTokens = tc.eng
			eng.SetMessages([]types.Message{CreateUserMessage("hello")})
			eng.mu.Lock()
			eng.lastPersistedIdx = 0
			eng.mu.Unlock()
			eng.PersistNewMessages()

			ses, err := store.GetSession(session.SessionID)
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if ses.ContextTokens != tc.want {
				t.Errorf("stored context_tokens = %d, want %d (engine ContextTokens %d)", ses.ContextTokens, tc.want, tc.eng)
			}
		})
	}
}

func TestPersist_AutoTitleGates(t *testing.T) {
	// First persist with an empty store batch must not title the session.
	storeEmpty := newTestStore(t)
	sessEmpty, err := storeEmpty.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	engEmpty := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engEmpty.Close() })
	engEmpty.SetStore(storeEmpty, "")
	engEmpty.SetSessionID(sessEmpty.SessionID)
	engEmpty.SetMessages([]types.Message{{
		ID: "mutf-title-job", Role: types.RoleUser, MessageType: types.MessageTypeAttachment,
		Content:    []types.ContentBlock{types.NewTextBlock("notification text")},
		Attachment: &types.Attachment{Type: types.AttachmentTypeQueued, Prompt: "notification text", Mode: types.ItemModeJob},
	}})
	engEmpty.mu.Lock()
	engEmpty.lastPersistedIdx = 0
	engEmpty.mu.Unlock()
	engEmpty.PersistNewMessages()
	if ses, err := storeEmpty.GetSession(sessEmpty.SessionID); err != nil {
		t.Fatalf("GetSession: %v", err)
	} else if ses.Title != "" {
		t.Errorf("title = %q, want empty (no persistable message, so no auto-title)", ses.Title)
	}

	// A later persist (cursor already past 0) must not title either.
	storeLate := newTestStore(t)
	sessLate, err := storeLate.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	engLate := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engLate.Close() })
	engLate.SetStore(storeLate, "")
	engLate.SetSessionID(sessLate.SessionID)
	engLate.SetMessages([]types.Message{{ID: "mutf-late-1", Role: types.RoleAssistant,
		Content: []types.ContentBlock{types.NewTextBlock("no title here")}}})
	engLate.mu.Lock()
	engLate.lastPersistedIdx = 0
	engLate.mu.Unlock()
	engLate.PersistNewMessages()
	engLate.SetMessages([]types.Message{
		{ID: "mutf-late-1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("no title here")}},
		{ID: "mutf-late-2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("should not become title")}},
	})
	engLate.mu.Lock()
	engLate.lastPersistedIdx = 1
	engLate.mu.Unlock()
	engLate.PersistNewMessages()
	if ses, err := storeLate.GetSession(sessLate.SessionID); err != nil {
		t.Fatalf("GetSession late: %v", err)
	} else if ses.Title != "" {
		t.Errorf("title = %q, want empty (auto-title runs on the first persist only)", ses.Title)
	}

	// The first persist with a real user prompt does title the session, and
	// logs no error when the update succeeds.
	storeOK := newTestStore(t)
	sessOK, err := storeOK.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rec := captureSlog(t)
	engOK := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { engOK.Close() })
	engOK.SetStore(storeOK, "")
	engOK.SetSessionID(sessOK.SessionID)
	engOK.SetMessages([]types.Message{CreateUserMessage("first real prompt")})
	engOK.mu.Lock()
	engOK.lastPersistedIdx = 0
	engOK.mu.Unlock()
	engOK.PersistNewMessages()
	if ses, err := storeOK.GetSession(sessOK.SessionID); err != nil {
		t.Fatalf("GetSession ok: %v", err)
	} else if ses.Title != "first real prompt" {
		t.Errorf("title = %q, want %q", ses.Title, "first real prompt")
	}
	if _, ok := rec.find("PersistNewMessages: auto-titled session", func(a map[string]any) bool {
		return a["title"] == "first real prompt"
	}); !ok {
		t.Errorf("auto-titled record missing: %+v", rec.calls)
	}
	for _, c := range rec.calls {
		if c.msg == "PersistNewMessages: auto-title" {
			t.Errorf("unexpected auto-title error record on success: %+v", c.attrs)
		}
	}
}

func TestLoadMessages_LoadError(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	// Drop the table the chain query reads so the store call fails with a typed
	// driver error the engine must wrap (not stringify).
	if _, err := store.DB().Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("drop messages table: %v", err)
	}
	msgs, err := eng.LoadMessages(session.SessionID)
	if err == nil {
		t.Fatalf("LoadMessages on broken store returned %v, want error", msgs)
	}
	if _, ok := errors.AsType[*sqlite.Error](err); !ok {
		t.Errorf("LoadMessages error = %v, want chain to carry the driver error", err)
	}
	if !strings.HasPrefix(err.Error(), "load messages: ") {
		t.Errorf("LoadMessages error = %q, want prefix %q", err.Error(), "load messages: ")
	}
	if msgs != nil {
		t.Errorf("LoadMessages messages = %v, want nil on error", msgs)
	}
}

func TestLoadMessages_EmptySession(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	msgs, err := eng.LoadMessages(session.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages empty session: %v", err)
	}
	if msgs != nil {
		t.Errorf("LoadMessages empty session = %v, want nil", msgs)
	}
}

func TestLoadMessages_ConvertError(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// A stored row whose role is not user/assistant/system makes the store→engine
	// conversion fail; the engine must surface that as a wrapped error.
	if err := store.AppendMessages(session.SessionID, []*short.TranscriptMessage{{
		UUID: "mutf-bogus-role", Type: "bogus_role", Content: "[]",
	}}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	msgs, err := eng.LoadMessages(session.SessionID)
	if err == nil {
		t.Fatalf("LoadMessages with unknown role returned %v, want error", msgs)
	}
	if !strings.Contains(err.Error(), "unknown message role") {
		t.Errorf("LoadMessages error = %q, want it to name the unknown role", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "convert messages: ") {
		t.Errorf("LoadMessages error = %q, want prefix %q", err.Error(), "convert messages: ")
	}
	// The prefix and the role name are both still there with a %v verb, so only
	// the unwrap chain pins the wrap. The cause must survive LoadMessages' %w
	// layer so a classifier can reach the corrupt-record detail (which seq,
	// which role) instead of re-parsing the joined context string.
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("LoadMessages error = %v, want it to wrap the conversion cause", err)
	}
	if want := `unknown message role "bogus_role" in store message seq=1`; cause.Error() != want {
		t.Errorf("unwrapped cause = %q, want %q", cause.Error(), want)
	}
	if msgs != nil {
		t.Errorf("LoadMessages messages = %v, want nil on error", msgs)
	}
}

func TestExtractUserTitle_NonTextBlocks(t *testing.T) {
	msgs := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			{Type: types.ContentTypeToolResult, ToolUseID: "t1", Text: "tool result text"},
			{Type: types.ContentTypeText, Text: "real prompt"},
		},
	}}
	if got := extractUserTitle(msgs); got != "real prompt" {
		t.Errorf("extractUserTitle = %q, want %q (non-text blocks are skipped)", got, "real prompt")
	}
}

func TestExtractUserTitle_SkipContinue(t *testing.T) {
	emptyFirst := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("   \n  "),
			types.NewTextBlock("after blank"),
		},
	}}
	if got := extractUserTitle(emptyFirst); got != "after blank" {
		t.Errorf("extractUserTitle blank-first = %q, want %q (blank text continues, not breaks)", got, "after blank")
	}
	angleFirst := []types.Message{{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock("<system-reminder>ignore</system-reminder>"),
			types.NewTextBlock("after tag"),
		},
	}}
	if got := extractUserTitle(angleFirst); got != "after tag" {
		t.Errorf("extractUserTitle tag-first = %q, want %q (tagged text continues, not breaks)", got, "after tag")
	}
}

func TestExtractUserTitle_TruncationBoundary(t *testing.T) {
	at200 := strings.Repeat("a", 200)
	if got := extractUserTitle([]types.Message{{Role: types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(at200)}}}); got != at200 {
		t.Errorf("extractUserTitle(200 chars) = %q len=%d, want the string unchanged", got, len([]rune(got)))
	}
	at201 := strings.Repeat("b", 201)
	want := strings.Repeat("b", 200) + "…"
	if got := extractUserTitle([]types.Message{{Role: types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock(at201)}}}); got != want {
		t.Errorf("extractUserTitle(201 chars) = %q len=%d, want 200 chars + ellipsis", got, len([]rune(got)))
	}
}

// recordGate parks the goroutine emitting one matching record inside Handle,
// letting a test inspect engine state at an exact mid-function point.
type recordGate struct {
	msg     string
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *recordGate) Enabled(context.Context, slog.Level) bool { return true }

func (g *recordGate) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message == g.msg {
		g.once.Do(func() { close(g.arrived) })
		<-g.release
	}
	return nil
}

func (g *recordGate) WithAttrs([]slog.Attr) slog.Handler { return g }
func (g *recordGate) WithGroup(string) slog.Handler      { return g }

// TestPersist_WriteLockHeldAcrossBody pins the mutual-exclusion contract of
// PersistNewMessages: the write lock covers the entire body and is released
// only at return. Turning the deferred Unlock into an immediate call would let
// a concurrent SetMessages or rewind swap the message slice while the body is
// still reading the uncommitted tail — a tear no single-threaded test can
// observe, so the gate parks the persist goroutine on its final log record
// (still under the lock) and probes the mutex from outside.
func TestPersist_WriteLockHeldAcrossBody(t *testing.T) {
	store := newTestStore(t)
	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng := New(&Params{Logger: slog.Default()})
	t.Cleanup(func() { eng.Close() })
	eng.SetStore(store, "")
	eng.SetSessionID(session.SessionID)
	eng.SetMessages([]types.Message{CreateUserMessage("hello")})
	eng.mu.Lock()
	eng.lastPersistedIdx = 0
	eng.mu.Unlock()

	arrived := make(chan struct{})
	release := make(chan struct{})
	attachSlogCapture(t, &recordGate{
		msg:     "PersistNewMessages: persisted messages",
		arrived: arrived,
		release: release,
	})
	openGate := sync.OnceFunc(func() { close(release) })
	t.Cleanup(openGate)

	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.PersistNewMessages()
	}()

	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("PersistNewMessages never emitted its final record — gate setup broken")
	}

	if eng.mu.TryLock() {
		eng.mu.Unlock()
		t.Fatal("write lock was free while PersistNewMessages was parked mid-body — Unlock must stay deferred to function exit")
	}

	openGate()
	<-done

	if got := eng.LastPersistedIdx(); got != 1 {
		t.Errorf("lastPersistedIdx = %d, want 1 (parked persist completed)", got)
	}
}
