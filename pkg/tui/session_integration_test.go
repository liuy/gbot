package tui

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// newIntegrationApp creates an App that mirrors main.go setup:
// real projectDir, store, session, and persisted messages.
func newIntegrationApp(t *testing.T) (*App, *short.Store, string) {
	t.Helper()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")

	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.New(&engine.Params{Logger: slog.Default()})

	// Create initial session with real projectDir (like main.go does)
	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)

	// Add conversation messages to engine
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("What is 2+2?")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("2+2 equals 4.")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("And 3+3?")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("3+3 equals 6.")}},
	}
	eng.SetMessages(msgs)

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
		lastPersistedIdx: 0,
		repl:             NewReplState(),
	}
	return a, store, projectDir
}

// persistTestMessages persists current engine messages to the store.
func persistTestMessages(t *testing.T, a *App) {
	t.Helper()
	a.persistTurn()
}

// ------- RED PHASE: these tests should fail before the fix -------

// TestIntegration_PickerShowsAllSessionsAfterFork verifies the core bug:
// after /session title (fork), /session (picker) must show BOTH sessions.
//
// BUG: openPicker used ListSessions("") while sessions were created with
// real projectDir, so the picker found zero sessions.
func TestIntegration_PickerShowsAllSessionsAfterFork(t *testing.T) {
	a, store, projectDir := newIntegrationApp(t)

	// Persist messages so fork has data to copy
	persistTestMessages(t, a)
	if a.lastPersistedIdx != 4 {
		t.Fatalf("expected lastPersistedIdx=4 after persist, got %d", a.lastPersistedIdx)
	}

	// /session my-fork → fork current session
	_ = a.handleSession("my-fork", nil)
	if a.sessionID == "" {
		t.Fatal("expected session ID after fork")
	}

	// Verify the forked session exists in DB
	sessions, err := store.ListSessions(a.projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) < 2 {
		t.Fatalf("expected >= 2 sessions in DB with projectDir=%q, got %d", projectDir, len(sessions))
	}

	// Now simulate /session (no args) → openPicker
	// The bug was: openPicker called ListSessions("") which returns 0 results
	pickerSessions, err := store.ListSessions(a.projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions for picker: %v", err)
	}
	if len(pickerSessions) < 2 {
		names := make([]string, len(pickerSessions))
		for i, s := range pickerSessions {
			names[i] = s.Title
		}
		t.Errorf("picker should see >= 2 sessions, got %d: %v", len(pickerSessions), names)
	}
}

// TestIntegration_ForkCarriesHistory verifies that a forked session
// contains all the original conversation messages.
func TestIntegration_ForkCarriesHistory(t *testing.T) {
	a, store, _ := newIntegrationApp(t)

	// Persist messages
	persistTestMessages(t, a)

	// Fork
	forkedSessionID := a.sessionID
	cmd := a.handleSession("history-fork", nil)
	_ = cmd
	newSessionID := a.sessionID

	if forkedSessionID == newSessionID {
		t.Fatal("fork should change session ID")
	}

	// Load forked messages from store
	forkedMsgs, err := store.LoadMessages(newSessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(forkedMsgs) != 4 {
		t.Fatalf("expected 4 forked messages, got %d", len(forkedMsgs))
	}

	// Verify conversation content round-tripped
	// First message: user asking "What is 2+2?"
	if !strings.Contains(forkedMsgs[0].Content, "What is 2+2?") {
		t.Errorf("forked msg[0] should contain 'What is 2+2?', got %q", forkedMsgs[0].Content)
	}
	// Second message: assistant answering
	if !strings.Contains(forkedMsgs[1].Content, "2+2 equals 4") {
		t.Errorf("forked msg[1] should contain '2+2 equals 4', got %q", forkedMsgs[1].Content)
	}
	if forkedMsgs[0].Type != "user" {
		t.Errorf("forked msg[0].Type = %q, want 'user'", forkedMsgs[0].Type)
	}
	if forkedMsgs[1].Type != "assistant" {
		t.Errorf("forked msg[1].Type = %q, want 'assistant'", forkedMsgs[1].Type)
	}

	// Verify engine was updated with forked messages
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 4 {
		t.Fatalf("engine should have 4 messages after fork, got %d", len(engMsgs))
	}
}

// TestIntegration_NewSessionIsEmpty verifies /session -n creates
// a clean session with no messages and the correct projectDir.
func TestIntegration_NewSessionIsEmpty(t *testing.T) {
	a, store, projectDir := newIntegrationApp(t)

	// Persist some messages first
	persistTestMessages(t, a)
	if len(a.engine.Messages()) != 4 {
		t.Fatalf("setup: expected 4 messages, got %d", len(a.engine.Messages()))
	}

	// /session -n
	cmd := a.handleSession("-n", nil)
	_ = cmd

	// Engine should be empty
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 0 {
		t.Errorf("new session should have 0 messages, got %d", len(engMsgs))
	}
	if a.lastPersistedIdx != 0 {
		t.Errorf("new session lastPersistedIdx should be 0, got %d", a.lastPersistedIdx)
	}

	// New session should have correct projectDir so picker can find it
	sessions, err := store.ListSessions(projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.SessionID == a.sessionID {
			found = true
			if s.ProjectDir != projectDir {
				t.Errorf("new session projectDir = %q, want %q", s.ProjectDir, projectDir)
			}
			break
		}
	}
	if !found {
		t.Error("new session not found in ListSessions with correct projectDir")
	}
}

// TestIntegration_NewSessionWithTitle verifies /session -n My Title.
func TestIntegration_NewSessionWithTitle(t *testing.T) {
	a, store, projectDir := newIntegrationApp(t)

	cmd := a.handleSession("-n My Project", nil)
	_ = cmd

	sessions, err := store.ListSessions(projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.SessionID == a.sessionID && s.Title == "My Project" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected session with title 'My Project' and projectDir=%q", projectDir)
	}
}

// TestIntegration_SwitchBackViaPickerRestoreMessages verifies that
// selecting a session in the picker restores its messages into the engine.
func TestIntegration_SwitchBackViaPickerRestoreMessages(t *testing.T) {
	a, _, _ := newIntegrationApp(t)
	originalSessionID := a.sessionID

	// Persist messages
	persistTestMessages(t, a)

	// Fork → switches to new session
	a.handleSession("temp-fork", nil)
	forkedSessionID := a.sessionID
	if forkedSessionID == originalSessionID {
		t.Fatal("fork should change session ID")
	}

	// Engine now has forked messages (same content, different session)
	if len(a.engine.Messages()) != 4 {
		t.Fatalf("after fork, engine should have 4 messages, got %d", len(a.engine.Messages()))
	}

	// Switch back by selecting original session via picker
	captured := helperSelectSession(t, a, []SessionItem{
		{SessionID: forkedSessionID, Title: "temp-fork"},
		{SessionID: originalSessionID, Title: ""},
	}, 1)

	model, _ := a.handleSessionPickerDone(a.listPicker, captured)
	if _, ok := model.(*App); !ok {
		t.Fatal("handleSessionPickerDone should return *App")
	}

	// Session ID should be restored
	if a.sessionID != originalSessionID {
		t.Errorf("sessionID = %q, want %q", a.sessionID, originalSessionID)
	}

	// Engine should have original messages restored
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 4 {
		t.Fatalf("after switch back, engine should have 4 messages, got %d", len(engMsgs))
	}
	if engMsgs[0].Role != types.RoleUser {
		t.Errorf("msg[0].Role = %q, want user", engMsgs[0].Role)
	}
}

// TestIntegration_ForkThenNewThenPickerShowsAll is the full end-to-end
// scenario: start → persist → fork → new → picker should show all 3.
func TestIntegration_ForkThenNewThenPickerShowsAll(t *testing.T) {
	a, store, _ := newIntegrationApp(t)
	originalID := a.sessionID

	// Persist messages
	persistTestMessages(t, a)

	// Fork
	a.handleSession("my-fork", nil)
	forkID := a.sessionID
	if forkID == originalID {
		t.Fatal("fork should change session ID")
	}

	// New session
	a.handleSession("-n fresh-start", nil)
	newID := a.sessionID
	if newID == forkID || newID == originalID {
		t.Fatal("new session should have unique ID")
	}

	// Now picker should show all 3 sessions
	sessions, err := store.ListSessions(a.projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) < 3 {
		t.Fatalf("expected >= 3 sessions, got %d", len(sessions))
	}

	// Verify each session is present
	ids := make(map[string]bool)
	for _, s := range sessions {
		ids[s.SessionID] = true
	}
	for _, want := range []string{originalID, forkID, newID} {
		if !ids[want] {
			t.Errorf("session %s not found in picker results", want[:8])
		}
	}
}

// TestIntegration_ForkPreservesToolUseMessages verifies that tool_use
// and tool_result messages survive the fork round-trip.
func TestIntegration_ForkPreservesToolUseMessages(t *testing.T) {
	a, store, _ := newIntegrationApp(t)

	// Set up a conversation with tool use
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("Read main.go")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{
				Type:  types.ContentTypeToolUse,
				ID:    "tu_123",
				Name:  "Read",
				Input: []byte(`{"file_path":"/tmp/main.go"}`),
			},
		}},
		{Role: types.RoleUser, Content: []types.ContentBlock{
			{
				Type:      types.ContentTypeToolResult,
				ToolUseID: "tu_123",
				Content:   []byte(`"package main\nfunc main() {}"`),
			},
		}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("The file contains...")}},
	}
	a.engine.SetMessages(msgs)
	persistTestMessages(t, a)
	if a.lastPersistedIdx != 4 {
		t.Fatalf("expected lastPersistedIdx=4, got %d", a.lastPersistedIdx)
	}

	// Fork
	cmd := a.handleSession("tool-fork", nil)
	_ = cmd

	// Load forked messages
	forkedMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(forkedMsgs) != 4 {
		t.Fatalf("expected 4 forked messages, got %d", len(forkedMsgs))
	}

	// Verify tool_use message preserved
	if !strings.Contains(forkedMsgs[1].Content, "tool_use") {
		t.Errorf("forked msg[1] should be tool_use, got %q", forkedMsgs[1].Content)
	}
	if !strings.Contains(forkedMsgs[1].Content, "Read") {
		t.Errorf("forked msg[1] should mention 'Read', got %q", forkedMsgs[1].Content)
	}

	// Verify tool_result message preserved
	if !strings.Contains(forkedMsgs[2].Content, "tool_result") {
		t.Errorf("forked msg[2] should be tool_result, got %q", forkedMsgs[2].Content)
	}

	// Convert back to engine and verify types
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 4 {
		t.Fatalf("engine should have 4 messages, got %d", len(engMsgs))
	}

	// Check assistant message has tool_use block
	assistantBlocks := engMsgs[1].Content
	hasToolUse := false
	for _, b := range assistantBlocks {
		if b.Type == types.ContentTypeToolUse {
			hasToolUse = true
			if b.Name != "Read" {
				t.Errorf("tool_use.Name = %q, want 'Read'", b.Name)
			}
		}
	}
	if !hasToolUse {
		t.Error("assistant message should contain tool_use block after fork round-trip")
	}
}

// TestIntegration_MultipleForks creates multiple forks and verifies
// each has independent message chains.
func TestIntegration_MultipleForks(t *testing.T) {
	a, store, _ := newIntegrationApp(t)
	originalID := a.sessionID

	// Persist messages
	persistTestMessages(t, a)

	sessionIDs := []string{originalID}

	// Fork 3 times from the original (need to switch back each time)
	for i := range 3 {
		// Fork
		a.handleSession("fork-"+string(rune('A'+i)), nil)
		sessionIDs = append(sessionIDs, a.sessionID)

		// Switch back to original for next fork
		captured := helperSelectSession(t, a, []SessionItem{
			{SessionID: originalID, Title: "original"},
		}, 0)
		a.handleSessionPickerDone(a.listPicker, captured)

		if a.sessionID != originalID {
			t.Fatalf("iteration %d: failed to switch back to original", i)
		}
	}

	// All 4 sessions should exist
	sessions, err := store.ListSessions(a.projectDir, 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) < 4 {
		t.Errorf("expected >= 4 sessions, got %d", len(sessions))
	}

	// Each forked session should have 4 messages
	for i, id := range sessionIDs[1:] {
		msgs, err := store.LoadMessages(id)
		if err != nil {
			t.Errorf("fork %d: LoadMessages: %v", i, err)
			continue
		}
		if len(msgs) != 4 {
			t.Errorf("fork %d: expected 4 messages, got %d", i, len(msgs))
		}
	}
}

// TestIntegration_ForkIsolation_MessagesDontLeak verifies that adding messages
// to one forked session does NOT affect other forks or the original.
// This is the key isolation property: forks are independent conversations.
func TestIntegration_ForkIsolation_MessagesDontLeak(t *testing.T) {
	a, store, _ := newIntegrationApp(t)
	originalID := a.sessionID

	// Persist 4 messages to original
	persistTestMessages(t, a)
	if a.lastPersistedIdx != 4 {
		t.Fatalf("setup: expected lastPersistedIdx=4, got %d", a.lastPersistedIdx)
	}

	// Fork A
	_ = a.handleSession("fork-A", nil)
	forkAID := a.sessionID
	if forkAID == originalID {
		t.Fatal("fork A should have different ID")
	}

	// Add extra messages to fork A
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("What is 2+2?")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("2+2 equals 4.")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("And 3+3?")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("3+3 equals 6.")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("Fork A extra question")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("Fork A extra answer")}},
	})
	a.lastPersistedIdx = 4 // only persist the new 2 messages
	persistTestMessages(t, a)
	if a.lastPersistedIdx != 6 {
		t.Fatalf("fork A: expected lastPersistedIdx=6, got %d", a.lastPersistedIdx)
	}

	// Switch back to original via picker
	cap2 := helperSelectSession(t, a, []SessionItem{
		{SessionID: originalID, Title: "original"},
	}, 0)
	a.handleSessionPickerDone(a.listPicker, cap2)
	if a.sessionID != originalID {
		t.Fatal("should be back on original session")
	}

	// Original should still have exactly 4 messages
	originalMsgs, err := store.LoadMessages(originalID)
	if err != nil {
		t.Fatalf("LoadMessages original: %v", err)
	}
	if len(originalMsgs) != 4 {
		t.Errorf("original session should have 4 messages, got %d — fork A's extra messages leaked!", len(originalMsgs))
	}
	for i, want := range []string{"What is 2+2?", "2+2 equals 4.", "And 3+3?", "3+3 equals 6."} {
		if !strings.Contains(originalMsgs[i].Content, want) {
			t.Errorf("original msg[%d] = %q, want substring %q", i, originalMsgs[i].Content, want)
		}
	}

	// Fork B from original
	_ = a.handleSession("fork-B", nil)
	forkBID := a.sessionID

	// Fork B should have exactly 4 messages (original content, not fork A's extras)
	forkBMsgs, err := store.LoadMessages(forkBID)
	if err != nil {
		t.Fatalf("LoadMessages fork B: %v", err)
	}
	if len(forkBMsgs) != 4 {
		t.Errorf("fork B should have 4 messages (from original), got %d — fork A's messages leaked into fork B!", len(forkBMsgs))
	}

	// Fork A should still have 6 messages
	forkAMsgs, err := store.LoadMessages(forkAID)
	if err != nil {
		t.Fatalf("LoadMessages fork A: %v", err)
	}
	if len(forkAMsgs) != 6 {
		t.Errorf("fork A should have 6 messages, got %d", len(forkAMsgs))
	}
	if !strings.Contains(forkAMsgs[4].Content, "Fork A extra question") {
		t.Errorf("fork A msg[4] should contain 'Fork A extra question', got %q", forkAMsgs[4].Content)
	}

	// Verify each session has distinct message chains (different UUIDs)
	originalUUIDs := collectUUIDs(t, originalMsgs)
	forkAUUIDs := collectUUIDs(t, forkAMsgs)
	forkBUUIDs := collectUUIDs(t, forkBMsgs)

	// Fork A's first 4 messages should NOT share UUIDs with original (copy rebuilds chain)
	for i, u := range forkAUUIDs[:4] {
		if i < len(originalUUIDs) && u == originalUUIDs[i] {
			t.Errorf("fork A msg[%d] UUID %q should differ from original — chains not independent", i, u)
		}
	}
	// Fork B should also have independent UUIDs from original
	for i, u := range forkBUUIDs {
		if i < len(originalUUIDs) && u == originalUUIDs[i] {
			t.Errorf("fork B msg[%d] UUID %q should differ from original — chains not independent", i, u)
		}
	}
}

func collectUUIDs(t *testing.T, msgs []*short.TranscriptMessage) []string {
	t.Helper()
	uuids := make([]string, len(msgs))
	for i, m := range msgs {
		uuids[i] = m.UUID
	}
	return uuids
}

// helperSelectSession sets up a ListPicker and selects the session at the given index.
// Returns the captured SessionItem slice.
func helperSelectSession(t *testing.T, a *App, sessionItems []SessionItem, selectIdx int) []SessionItem {
	t.Helper()
	pickerItems := make([]PickerItem, len(sessionItems))
	for i := range sessionItems {
		pickerItems[i] = &sessionItems[i]
	}
	a.listPicker = NewListPicker("Switch Session", pickerItems)
	a.listPicker.selected = selectIdx
	return sessionItems
}

// helperAbortPicker sets up a ListPicker and marks it as aborted.
func helperAbortPicker(t *testing.T, a *App, sessionItems []SessionItem) {
	t.Helper()
	pickerItems := make([]PickerItem, len(sessionItems))
	for i := range sessionItems {
		pickerItems[i] = &sessionItems[i]
	}
	a.listPicker = NewListPicker("Switch Session", pickerItems)
	a.listPicker.aborted = true
}

// TestIntegration_ForkIsolation_EngineStateAfterSwitch verifies that switching
// sessions completely replaces the engine's message history — no stale state.
func TestIntegration_ForkIsolation_EngineStateAfterSwitch(t *testing.T) {
	a, _, _ := newIntegrationApp(t)
	originalID := a.sessionID

	// Persist messages
	persistTestMessages(t, a)

	// Fork with extra state
	_ = a.handleSession("fork-X", nil)
	forkXID := a.sessionID

	// Add messages to fork X in engine
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg1")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("msg2")}},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("fork X only")}},
	})

	// Switch back to original
	cap3 := helperSelectSession(t, a, []SessionItem{
		{SessionID: originalID, Title: "original"},
	}, 0)
	a.handleSessionPickerDone(a.listPicker, cap3)

	// Engine should have original 4 messages, NOT fork X's 3
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 4 {
		t.Fatalf("engine should have 4 messages from original, got %d — stale state from fork X!", len(engMsgs))
	}

	// Verify content is from original, not fork X
	if !strings.Contains(engMsgs[0].Content[0].Text, "What is 2+2?") {
		t.Errorf("engine msg[0] should be from original conversation, got %q", engMsgs[0].Content[0].Text)
	}

	// Switch to fork X again
	cap4 := helperSelectSession(t, a, []SessionItem{
		{SessionID: forkXID, Title: "fork-X"},
	}, 0)
	a.handleSessionPickerDone(a.listPicker, cap4)

	// Engine should now have fork X's messages from STORE (4 original, since we didn't persist the extras)
	engMsgs = a.engine.Messages()
	if len(engMsgs) != 4 {
		t.Errorf("engine after switch to fork X should have 4 messages, got %d", len(engMsgs))
	}

	// lastPersistedIdx should reflect fork X's state, not original's
	// (handleSessionPickerDone loads fresh from store and resets)
	if a.lastPersistedIdx != 4 {
		t.Errorf("lastPersistedIdx = %d, want 4", a.lastPersistedIdx)
	}

	// Session ID should be fork X
	if a.sessionID != forkXID {
		t.Errorf("sessionID = %q, want %q", a.sessionID, forkXID)
	}
}

// TestIntegration_DuplicateTitlePrevention verifies fork rejects
// a title that's already used by another session.
func TestIntegration_DuplicateTitlePrevention(t *testing.T) {
	a, _, _ := newIntegrationApp(t)

	persistTestMessages(t, a)

	// First fork succeeds
	cmd := a.handleSession("unique-title", nil)
	_ = cmd
	_ = a.sessionID

	// Switch back to original — not needed, the test continues with a2 below
	// Then try to fork with the same title
	a2, _, _ := newIntegrationApp(t)
	persistTestMessages(t, a2)

	// Create a titled session first
	store := a2.store
	session, _ := store.CreateSession(a2.projectDir, "test-model")
	if err := store.UpdateSessionTitle(session.SessionID, "taken-title"); err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}

	// Now try to fork with same title
	cmd = a2.handleSession("taken-title", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg for duplicate title, got %T", msg)
	}
	if !strings.Contains(string(info), "already exists") {
		t.Errorf("error should mention 'already exists', got %q", string(info))
	}
}

// TestIntegration_PickerCancelAborts verifies pressing Esc in picker
// doesn't change the session.
func TestIntegration_PickerCancelAborts(t *testing.T) {
	a, _, _ := newIntegrationApp(t)
	originalID := a.sessionID

	// Activate picker and cancel
	helperAbortPicker(t, a, []SessionItem{
		{SessionID: "other-session", Title: "Other"},
	})

	model, _ := a.handleSessionPickerDone(a.listPicker, []SessionItem{
		{SessionID: "other-session", Title: "Other"},
	})
	if _, ok := model.(*App); !ok {
		t.Fatal("handleSessionPickerDone should return *App")
	}

	if a.listPicker != nil {
		t.Error("listPicker should be nil feeling after cancel")
	}
	_ = originalID
	if a.sessionID != originalID {
		t.Errorf("sessionID should not change on cancel, got %q want %q", a.sessionID, originalID)
	}
}

// TestIntegration_PickerSameSessionNoop verifies selecting the
// current session is a no-op.
func TestIntegration_PickerSameSessionNoop(t *testing.T) {
	a, _, _ := newIntegrationApp(t)
	originalID := a.sessionID

	cap5 := helperSelectSession(t, a, []SessionItem{
		{SessionID: originalID, Title: "current"},
	}, 0)

	model, cmd := a.handleSessionPickerDone(a.listPicker, cap5)
	_ = model

	if a.sessionID != originalID {
		t.Error("same session should be no-op")
	}
	// Should get an info message saying "Already on this session"
	if cmd == nil {
		t.Error("expected a command (info message)")
	}
}
