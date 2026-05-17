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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")

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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi there")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply1")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("help me fix a bug in auth.go")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("sure, let me look")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("some prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("first prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("second prompt")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("reply2")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},  // REAL-TIME: timestamp for persistence
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

// ---------------------------------------------------------------------------
// SetStore must sync repl.messages from engine on resume.
// ---------------------------------------------------------------------------

func TestSetStore_SyncsReplMessagesFromEngine(t *testing.T) {
	dir := t.TempDir()

	// Create real store + session
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(dir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Persist a multi-turn conversation
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("world")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("how are you?")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("fine")}, Timestamp: testTime},
	}

	// Simulate auto-resume: load messages from store into engine
	eng := newTestEngine()
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, "")
	eng.SetMessages(msgs)

	// Create App and call SetStore (as main.go does after resume)
	a := &App{
		engine: eng,
		repl:   NewReplState(),
		input:  NewInput(),
	}
	a.SetStore(store, session.SessionID, dir, len(msgs))

	// repl.messages must be populated from engine
	if len(a.repl.messages) == 0 {
		t.Fatalf("expected repl.messages synced from engine, got 0")
	}

	// Verify message count matches
	if len(a.repl.messages) != len(msgs) {
		t.Errorf("expected %d repl.messages, got %d", len(msgs), len(a.repl.messages))
	}

	// Verify first user message content
	first := a.repl.messages[0]
	if first.Role != "user" {
		t.Errorf("first message role = %q, want user", first.Role)
	}
	if len(first.Blocks) == 0 || first.Blocks[0].Text != "hello" {
		t.Errorf("first message text mismatch, got %v", first.Blocks)
	}

	// committedCount should equal message count
	if a.committedCount != len(msgs) {
		t.Errorf("committedCount = %d, want %d", a.committedCount, len(msgs))
	}
}

// TestStoreRoundTrip_PreservesUsage verifies that Usage/Model/StopReason survive
// the engine→store→engine round-trip. This is critical for TokenCountWithEstimation
// which depends on per-message Usage after session resume.
func TestStoreRoundTrip_PreservesUsage(t *testing.T) {
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	session, err := store.CreateSession("", "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Engine messages with full metadata
	engineMsgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{
			Role:       types.RoleAssistant,
			Content:    []types.ContentBlock{types.NewTextBlock("hi there")},
			Model:      "minimax-2.7",
			StopReason: "end_turn",
			Usage: &types.Usage{
				InputTokens:          50000,
				OutputTokens:         100,
				CacheReadInputTokens: 30000,
			},
			Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("follow up")}, Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{
			Role:       types.RoleAssistant,
			Content:    []types.ContentBlock{types.NewTextBlock("response")},
			Model:      "minimax-2.7",
			StopReason: "tool_use",
			Usage: &types.Usage{
				InputTokens:          80000,
				OutputTokens:         200,
				CacheReadInputTokens: 50000,
			},
			Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	// Engine → Store
	storeMsgs, err := EngineMessagesToStore(engineMsgs)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	storePtrs := make([]*short.TranscriptMessage, len(storeMsgs))
	for i := range storeMsgs { storePtrs[i] = &storeMsgs[i] }
	if err := store.AppendMessages(session.SessionID, storePtrs); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Store → Engine (simulate resume)
	loaded, err := loadAndConvertMessages(store, session.SessionID)
	if err != nil {
		t.Fatalf("loadAndConvertMessages: %v", err)
	}
	if len(loaded) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(loaded))
	}

	// Verify Usage preserved on assistant messages
	msg1 := loaded[1]
	if msg1.Usage == nil {
		t.Fatal("msg[1] Usage is nil — metadata lost during round-trip")
	}
	if msg1.Usage.InputTokens != 50000 {
		t.Errorf("msg[1] Usage.InputTokens = %d, want 50000", msg1.Usage.InputTokens)
	}
	if msg1.Usage.OutputTokens != 100 {
		t.Errorf("msg[1] Usage.OutputTokens = %d, want 100", msg1.Usage.OutputTokens)
	}
	if msg1.Usage.CacheReadInputTokens != 30000 {
		t.Errorf("msg[1] Usage.CacheReadInputTokens = %d, want 30000", msg1.Usage.CacheReadInputTokens)
	}
	if msg1.Model != "minimax-2.7" {
		t.Errorf("msg[1] Model = %q, want %q", msg1.Model, "minimax-2.7")
	}
	if msg1.StopReason != "end_turn" {
		t.Errorf("msg[1] StopReason = %q, want %q", msg1.StopReason, "end_turn")
	}

	msg3 := loaded[3]
	if msg3.Usage == nil {
		t.Fatal("msg[3] Usage is nil — metadata lost during round-trip")
	}
	if msg3.Usage.InputTokens != 80000 {
		t.Errorf("msg[3] Usage.InputTokens = %d, want 80000", msg3.Usage.InputTokens)
	}
	if msg3.Model != "minimax-2.7" {
		t.Errorf("msg[3] Model = %q, want %q", msg3.Model, "minimax-2.7")
	}
	if msg3.StopReason != "tool_use" {
		t.Errorf("msg[3] StopReason = %q, want %q", msg3.StopReason, "tool_use")
	}

	// Verify user messages have no Usage
	if loaded[0].Usage != nil {
		t.Error("msg[0] (user) should not have Usage")
	}

	// Verify TokenCountWithEstimation works with restored messages
	tokens := engine.TokenCountWithEstimation(loaded)
	// Should use msg[3]'s usage: 80000+50000+200 = 130200
	// (no messages after it, so no delta)
	if tokens != 130200 {
		t.Errorf("TokenCountWithEstimation = %d, want 130200", tokens)
	}
}
