package tui

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	projectDir := filepath.Join(dir, "project")
	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
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
	sessions, err := store.ListSessions(a.projectDir, 100)
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
	session, _ := store.CreateSession(a.projectDir, "test-model")
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
		t.Fatal("infoMsg should not be empty for duplicate title")
	}
	if !strings.Contains(string(info), "already exists") {
		t.Errorf("info should mention already exists, got: %q", string(info))
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
	sessions, _ := store.ListSessions(a.projectDir, 100)
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

// ---------------------------------------------------------------------------
// /clear command tests
// ---------------------------------------------------------------------------

func TestHandleClear_Streaming(t *testing.T) {
	a, _ := newSwitchTestApp(t)
	a.repl.streaming = true

	cmd := a.handleClear(nil)
	if cmd == nil {
		t.Fatal("expected a command from streaming guard")
	}
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if string(info) != "Cannot clear while streaming" {
		t.Errorf("info = %q, want %q", info, "Cannot clear while streaming")
	}
}

func TestHandleClear_NoStore(t *testing.T) {
	a := &App{
		engine: engine.New(&engine.Params{Logger: slog.Default()}),
		repl:   NewReplState(),
	}

	cmd := a.handleClear(nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if string(info) != "Session storage not available" {
		t.Errorf("info = %q, want %q", info, "Session storage not available")
	}
}

func TestHandleClear_Success(t *testing.T) {
	a, _ := newSwitchTestApp(t)
	oldSessionID := a.sessionID

	// Set some state that should be cleared
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})
	a.lastPersistedIdx = 1
	a.displayedInputTokens = 500
	a.displayedOutputTokens = 100
	a.thinkingActive = true
	a.scrollOffset = 42
	a.committedCount = 1

	cmd := a.handleClear(nil)
	_ = cmd

	// Session ID should change
	if a.sessionID == oldSessionID {
		t.Error("expected session ID to change")
	}

	// Messages should be cleared
	msgs := a.engine.Messages()
	if len(msgs) != 0 {
		t.Errorf("expected empty messages, got %d", len(msgs))
	}

	// Persistence index should be 0
	if a.lastPersistedIdx != 0 {
		t.Errorf("lastPersistedIdx = %d, want 0", a.lastPersistedIdx)
	}

	// Display state should be reset
	if a.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0", a.displayedInputTokens)
	}
	if a.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0", a.displayedOutputTokens)
	}
	if a.thinkingActive {
		t.Error("thinkingActive should be false")
	}
	if a.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", a.scrollOffset)
	}
}

func TestResetDisplayState(t *testing.T) {
	a := &App{
		scrollOffset:         10,
		scrollTotal:          20,
		userScrolled:         true,
		contentCache:         "old content",
		contentDirty:         true,
		allToolsExpanded:     true,
		thinkingActive:       true,
		thinkingStart:        parseTime("2026-01-01T00:00:00Z"),
		thinkingDuration:     5 * time.Second,
		progressStart:        parseTime("2026-01-01T00:00:00Z"),
		responseCharCount:    100,
		displayedInputTokens: 1000,
		displayedOutputTokens: 200,
		outputTokenTarget:    300,
		inputTokenTarget:     1100,
		cacheReadTokens:      5000,
		cacheCreationTokens:  2000,
		toolBlink:            true,
		toolBlinkTick:        7,
	}

	a.resetDisplayState()

	if a.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", a.scrollOffset)
	}
	if a.scrollTotal != 0 {
		t.Errorf("scrollTotal = %d, want 0", a.scrollTotal)
	}
	if a.userScrolled {
		t.Error("userScrolled should be false")
	}
	if a.contentCache != "" {
		t.Errorf("contentCache = %q, want empty", a.contentCache)
	}
	if a.contentDirty {
		t.Error("contentDirty should be false")
	}
	if a.allToolsExpanded {
		t.Error("allToolsExpanded should be false")
	}
	if a.thinkingActive {
		t.Error("thinkingActive should be false")
	}
	if a.thinkingStart != (time.Time{}) {
		t.Errorf("thinkingStart = %v, want zero", a.thinkingStart)
	}
	if a.thinkingDuration != 0 {
		t.Errorf("thinkingDuration = %v, want 0", a.thinkingDuration)
	}
	if a.progressStart != (time.Time{}) {
		t.Errorf("progressStart = %v, want zero", a.progressStart)
	}
	if a.responseCharCount != 0 {
		t.Errorf("responseCharCount = %d, want 0", a.responseCharCount)
	}
	if a.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0", a.displayedInputTokens)
	}
	if a.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0", a.displayedOutputTokens)
	}
	if a.outputTokenTarget != 0 {
		t.Errorf("outputTokenTarget = %d, want 0", a.outputTokenTarget)
	}
	if a.inputTokenTarget != 0 {
		t.Errorf("inputTokenTarget = %d, want 0", a.inputTokenTarget)
	}
	if a.cacheReadTokens != 0 {
		t.Errorf("cacheReadTokens = %d, want 0", a.cacheReadTokens)
	}
	if a.cacheCreationTokens != 0 {
		t.Errorf("cacheCreationTokens = %d, want 0", a.cacheCreationTokens)
	}
	if a.toolBlink {
		t.Error("toolBlink should be false")
	}
	if a.toolBlinkTick != 0 {
		t.Errorf("toolBlinkTick = %d, want 0", a.toolBlinkTick)
	}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestForkCurrentSession_NoActiveSession(t *testing.T) {
	a := &App{
		engine: engine.New(&engine.Params{Logger: slog.Default()}),
		repl:   NewReplState(),
	}
	// No sessionID set
	cmd := a.forkCurrentSession("title", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "No active session") {
		t.Errorf("info = %q, should mention no active session", info)
	}
}

func TestWriteWorkspaceMeta_EmptyDir(t *testing.T) {
	err := WriteWorkspaceMeta("", "session-123")
	if err != nil {
		t.Errorf("empty dir should skip write and return nil, got: %v", err)
	}
}
