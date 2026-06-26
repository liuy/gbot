package tui

import (
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

func newSessionTestApp(t *testing.T) (*App, *short.Store) {
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

	eng.SetStore(store, projectDir)

	a := &App{
		engine:     eng,
		sessionID:  session.SessionID,
		projectDir: projectDir,
		repl:       NewReplState(),
	}
	return a, store
}

func TestHandleSwitch_Streaming(t *testing.T) {
	a, _ := newSessionTestApp(t)
	a.repl.streaming = true

	cmd := a.handleSession("-n", nil)
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
	a, _ := newSessionTestApp(t)
	oldSessionID := a.sessionID

	_ = a.handleSession("-n", nil)

	if a.sessionID == oldSessionID {
		t.Error("expected session ID to change")
	}
	if a.sessionID == "" {
		t.Error("expected non-empty session ID")
	}
	if a.engine.LastPersistedIdx() != 0 {
		t.Errorf("expected lastPersistedIdx=0, got %d", a.engine.LastPersistedIdx())
	}
	msgs := a.engine.Messages()
	if len(msgs) != 0 {
		t.Errorf("expected empty messages after new session, got %d", len(msgs))
	}
}

func TestHandleSwitch_NewSessionWithTitle(t *testing.T) {
	a, store := newSessionTestApp(t)

	_ = a.handleSession("-n My New Session", nil)

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
	a, store := newSessionTestApp(t)

	// Create a session with a known title
	session, _ := store.CreateSession(a.projectDir, "test-model")
	if err := store.UpdateSessionTitle(session.SessionID, "existing-title"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Try to fork with duplicate title
	cmd := a.handleSession("existing-title", nil)
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
	a, store := newSessionTestApp(t)

	// Add and persist messages to the current session
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	})
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 2 {
		t.Fatalf("expected lastPersistedIdx=2 after persist, got %d", a.engine.LastPersistedIdx())
	}

	// Fork
	_ = a.handleSession("fork-title", nil)

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

	cmd := a.handleSession("-n", nil)
	msg := cmd()
	if _, ok := msg.(infoMsg); !ok {
		t.Fatalf("expected infoMsg about no store, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// /clear command tests
// ---------------------------------------------------------------------------

func TestHandleClear_Streaming(t *testing.T) {
	a, _ := newSessionTestApp(t)
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
	a, _ := newSessionTestApp(t)
	oldSessionID := a.sessionID

	// Set some state that should be cleared
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})
	a.repl.displayedInputTokens = 500
	a.repl.displayedOutputTokens = 100
	a.repl.StartThinkingAtForTest(time.Now()) // REAL-TIME: anchors thinkingStart assertion
	a.scrollOffset = 42
	a.repl.committedCount = 1

	_ = a.handleClear(nil)

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
	if a.engine.LastPersistedIdx() != 0 {
		t.Errorf("lastPersistedIdx = %d, want 0", a.engine.LastPersistedIdx())
	}

	// Display state should be reset
	if a.repl.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0", a.repl.displayedInputTokens)
	}
	if a.repl.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0", a.repl.displayedOutputTokens)
	}
	if a.repl.IsThinking() {
		t.Error("thinkingActive should be false")
	}
	if a.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", a.scrollOffset)
	}
}

func TestResetDisplayState(t *testing.T) {
	a := &App{
		repl:             NewReplState(),
		scrollOffset:     10,
		scrollTotal:      20,
		userScrolled:     true,
		contentCache:     "old content",
		contentDirty:     true,
		allToolsExpanded: true,
		toolBlink:        true,
		toolBlinkTick:    7,
	}
	a.repl.StartThinkingAtForTest(parseTime("2026-01-01T00:00:00Z"))
	a.repl.displayedInputTokens = 1000
	a.repl.displayedOutputTokens = 200
	a.repl.outputTokenTarget = 300
	a.repl.inputTokenTarget = 1100

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
	if a.repl.IsThinking() {
		t.Error("thinkingActive should be false")
	}
	if a.repl.ThinkingStart() != (time.Time{}) {
		t.Errorf("thinkingStart = %v, want zero", a.repl.ThinkingStart())
	}
	if a.repl.StreamingStart() != (time.Time{}) {
		t.Errorf("progressStart = %v, want zero", a.repl.StreamingStart())
	}
	if a.repl.ResponseCharCount() != 0 {
		t.Errorf("responseCharCount = %d, want 0", a.repl.ResponseCharCount())
	}
	if a.repl.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0", a.repl.displayedInputTokens)
	}
	if a.repl.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0", a.repl.displayedOutputTokens)
	}
	if a.repl.outputTokenTarget != 0 {
		t.Errorf("outputTokenTarget = %d, want 0", a.repl.outputTokenTarget)
	}
	if a.repl.inputTokenTarget != 0 {
		t.Errorf("inputTokenTarget = %d, want 0", a.repl.inputTokenTarget)
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

func TestPersistWorkspaceMeta_EmptyDir(t *testing.T) {
	// App.persistWorkspaceMeta is a no-op when projectDir == "" — no error,
	// no file created. Verifies the guard added when the tui.WriteWorkspaceMeta
	// wrapper was removed (Step 2b).
	app := newTestApp(&tuiMockProvider{})
	if err := app.persistWorkspaceMeta(); err != nil {
		t.Errorf("persistWorkspaceMeta on empty projectDir = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Session switch (/clear, resume, fork) must clear screen and reset
// committedCount so WindowSizeMsg can re-commit new session messages.
// ---------------------------------------------------------------------------

func containsClearScreen(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	return msgContainsClearScreen(t, cmd())
}

func msgContainsClearScreen(t *testing.T, msg tea.Msg) bool {
	t.Helper()
	if msg == nil {
		return false
	}
	if reflect.TypeOf(msg).Name() == "clearScreenMsg" {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if containsClearScreen(t, sub) {
				return true
			}
		}
	}
	return false
}

func TestClear_EmitsClearScreen(t *testing.T) {
	a, _ := newSessionTestApp(t)
	cmd := a.handleClear(nil)
	if !containsClearScreen(t, cmd) {
		t.Error("handleClear should emit tea.ClearScreen to wipe terminal, but cmd produced no clearScreenMsg")
	}
}

// TestClear_RestartsReadEvents verifies that /clear re-includes readEvents in
// its returned Cmd batch. Without this, the old idleStop goroutine (closed by
// createNewSession) is gone but no new readEvents is launched — subsequent
// events from the engine (e.g. WeChat connector messages) pile up in appCh
// with no consumer, and the TUI never renders them.
func TestClear_RestartsReadEvents(t *testing.T) {
	a, _ := newSessionTestApp(t)
	cmd := a.handleClear(nil)
	if cmd == nil {
		t.Fatal("handleClear returned nil cmd")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("handleClear cmd produced nil msg")
	}
	// The batch should eventually produce a readEvents message — either
	// directly (from appCh) or an idleAbortedMsg/queryEndMsg (from idleStop).
	// Verify the batch contains a command whose result is one of these
	// readEvents-origin types by checking the batch's type.
	if batch, ok := msg.(tea.BatchMsg); ok {
		if len(batch) < 3 {
			t.Errorf("batch has %d cmds, want at least 3 (clearScreen + showInfo + readEvents)", len(batch))
		}
	} else {
		t.Errorf("handleClear should return a tea.Batch, got %T", msg)
	}
}

// handleSubmitRepl packages old uncommitted messages as a tea.Println commitCmd
// before dispatching the slash command. Batch runs concurrently, so Println can
// land after ClearScreen and re-print old content — visually no clear happened.
func TestClear_DiscardsCommitCmd(t *testing.T) {
	a, _ := newSessionTestApp(t)
	commitCmd := tea.Println("OLD CONTENT THAT SHOULD BE WIPED")
	cmd := a.handleClear(commitCmd)
	if cmdContainsPrintln(t, cmd) {
		t.Error("handleClear should discard commitCmd (tea.Println of old messages); " +
			"passing it through races with ClearScreen and re-prints old content")
	}
}

func cmdContainsPrintln(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	return msgContainsPrintln(t, cmd())
}

func msgContainsPrintln(t *testing.T, msg tea.Msg) bool {
	t.Helper()
	if msg == nil {
		return false
	}
	if reflect.TypeOf(msg).Name() == "printLineMessage" {
		return true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if cmdContainsPrintln(t, sub) {
				return true
			}
		}
	}
	return false
}

func TestResumePicker_ResetsDisplayStateAndCommittedCount(t *testing.T) {
	a, store := newSessionTestApp(t)

	a.repl.committedCount = 3
	a.repl.displayedInputTokens = 999
	a.repl.displayedOutputTokens = 888
	a.scrollOffset = 15
	a.repl.StartThinkingAtForTest(time.Now()) // REAL-TIME: anchors thinkingStart assertion

	session2, err := store.CreateSession(a.projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg from session2")}},
	})
	a.engine.SetSessionID(session2.SessionID)
	a.engine.PersistNewMessages()

	items := []SessionItem{{SessionID: session2.SessionID, Title: "session2"}}
	_, cmd := a.handleSessionPickerDone(newSelectedDialog(0), items)

	if a.repl.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0 (resetDisplayState not called on resume)", a.repl.displayedInputTokens)
	}
	if a.repl.IsThinking() {
		t.Error("thinkingActive should be false after resume (resetDisplayState not called)")
	}
	if a.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after resume", a.scrollOffset)
	}
	// committedCount=0 lets WindowSizeMsg re-commit the resumed messages; setting
	// it to len(messages) would hide them from View() since uncommitted is empty.
	if a.repl.committedCount != 0 {
		t.Errorf("committedCount = %d, want 0 (must be 0 so WindowSizeMsg commits resumed messages)", a.repl.committedCount)
	}
	if !containsClearScreen(t, cmd) {
		t.Error("resume should emit tea.ClearScreen to wipe old session content")
	}
}

func TestFork_ResetsDisplayStateAndCommittedCount(t *testing.T) {
	a, _ := newSessionTestApp(t)

	a.repl.committedCount = 2
	a.repl.displayedInputTokens = 777
	a.repl.StartThinkingAtForTest(time.Now()) // REAL-TIME: anchors thinkingStart assertion
	a.scrollOffset = 10

	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
	})
	a.engine.PersistNewMessages()

	cmd := a.forkCurrentSession("fork-test", nil)

	if a.repl.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0 after fork", a.repl.displayedInputTokens)
	}
	if a.repl.IsThinking() {
		t.Error("thinkingActive should be false after fork")
	}
	if a.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0 after fork", a.scrollOffset)
	}
	if a.repl.committedCount != 0 {
		t.Errorf("committedCount = %d, want 0 (forked messages must be re-committed via WindowSizeMsg)", a.repl.committedCount)
	}
	if !containsClearScreen(t, cmd) {
		t.Error("fork should emit tea.ClearScreen to wipe old session content")
	}
}

// TestFork_RestartsReadEvents verifies that forkCurrentSession includes
// readEvents in its returned Cmd batch. Without this, engine events after
// forking pile up in appCh with no consumer.
func TestFork_RestartsReadEvents(t *testing.T) {
	a, _ := newSessionTestApp(t)
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}},
	})
	a.engine.PersistNewMessages()
	cmd := a.forkCurrentSession("fork-test", nil)
	if cmd == nil {
		t.Fatal("forkCurrentSession returned nil cmd")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		if len(batch) < 3 {
			t.Errorf("batch has %d cmds, want at least 3 (clearScreen + showInfo + readEvents)", len(batch))
		}
	} else {
		t.Errorf("forkCurrentSession should return tea.Batch, got %T", msg)
	}
}

func newSelectedDialog(idx int) *Dialog {
	d := NewDialog("test", []DialogOption{{Label: "item0"}})
	d.cursor = idx
	d.done = true
	return d
}
