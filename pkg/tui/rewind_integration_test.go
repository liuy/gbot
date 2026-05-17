package tui

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Integration Test: Rewind System
//
// These tests exercise full call chains from entry point to observable output,
// using real file I/O, real SQLite store, and real file history backups.
// No mocking of the system under test.
// ---------------------------------------------------------------------------

// setupRewindIntegration creates a fully wired App for integration testing:
//   - Real Store (SQLite)
//   - Real fileHistory Tracker
//   - Real Engine with fileHistory set
//   - Temp project directory with test files
func setupRewindIntegration(t *testing.T) (*App, *short.Store, *filehistory.Tracker, string) {
	t.Helper()

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// Real Store
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Real Engine
	eng := engine.New(&engine.Params{Logger: slog.Default()})

	// Real File History Tracker
	trackerDir := filepath.Join(dir, "file-history")
	tracker := filehistory.NewTracker(trackerDir)
	eng.SetFileHistory(tracker)

	// Real Session
	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, projectDir)

	a := &App{
		engine:           eng,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
		repl:             NewReplState(),
		input:            NewInput(),
		history:          NewHistory(""),
		fileHistory:      tracker,
	}
	a.width = 80

	return a, store, tracker, projectDir
}

// ---------------------------------------------------------------------------
// Chain 1: Auto-rewind E2E
//
// 入口: tryAutoRewind (simulating ESC abort with no meaningful response)
// 路径: messagesAfterAreOnlySynthetic → engine.RewindTo → TUI state sync
// 可观测输出: engine messages, input value, history, TUI messages
// ---------------------------------------------------------------------------
func TestIntegration_AutoRewind_FullChain(t *testing.T) {
	a, _, _, _ := setupRewindIntegration(t)

	// Set up engine messages: user query + assistant thinking-only (synthetic)
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello world")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{
			{Type: types.ContentTypeThinking, Text: "let me think..."},
		}, Timestamp: testTime},
	})

	// Simulate: user message was committed, TUI has messages
	a.committedCount = 0 // no prior messages committed
	a.repl.messages = []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "hello world"}}},
	}
	// History has the query
	a.history.Add("hello world")

	// --- Execute: auto-rewind ---
	result := a.tryAutoRewind()

	// --- Verify observable output ---
	if !result {
		t.Fatal("expected auto-rewind to fire (only thinking after user message)")
	}

	// Engine messages should be empty (rewound to lastUserIdx=0, messages[:0])
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 0 {
		t.Fatalf("expected 0 engine messages after rewind, got %d", len(engMsgs))
	}

	// Input should be restored for resubmission
	if a.input.Value() != "hello world" {
		t.Errorf("expected input restored to 'hello world', got %q", a.input.Value())
	}

	// History entry should be removed
	if len(a.history.items) != 0 {
		t.Errorf("expected empty history after rewind, got %d items", len(a.history.items))
	}

	// TUI messages should be truncated to committedCount
	if len(a.repl.messages) != 0 {
		t.Errorf("expected 0 TUI messages after rewind, got %d", len(a.repl.messages))
	}

	// committedCount should be valid
	if a.committedCount > len(a.repl.messages) {
		t.Errorf("committedCount %d > repl.messages len %d", a.committedCount, len(a.repl.messages))
	}
}

// ---------------------------------------------------------------------------
// Chain 2: /rewind + File Restoration E2E
//
// 入口: handleRewind → Dialog selection → engine.RewindTo
// 路径: fileHistory.Rewind → store.TruncateMessagesFromIndex
// 可观测输出: file content on disk, store.LoadMessages(), engine.Messages()
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_FileRestoreAndStoreCleanup(t *testing.T) {
	a, store, tracker, projectDir := setupRewindIntegration(t)

	testFile := filepath.Join(projectDir, "main.go")
	originalContent := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	msgID1 := "ddd11111-1111-1111-1111-111111111111"
	msgID2 := "eee22222-2222-2222-2222-222222222222"

	// Production flow: TrackEdit before edit, MakeSnapshot after edit.
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	editedContent := []byte("package main\n\nfunc main() { println(\"hello\") }\n")
	if err := os.WriteFile(testFile, editedContent, 0o644); err != nil {
		t.Fatalf("write edited file: %v", err)
	}

	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("What is 2+2?")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("4.")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("Edit main.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("Done.")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Persist to store
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 4 {
		t.Fatalf("expected lastPersistedIdx=4, got %d", a.engine.LastPersistedIdx())
	}

	// --- Execute: /rewind to first user message ---
	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("expected dialog to open")
	}

	// Select first user message (cursor=0 → indices[0]=0)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Scope dialog appears because fileHistory has backups
	if app.activeDialog == nil {
		t.Fatal("expected scope dialog after message selection (has file changes)")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	app = model.(*App)

	// --- Verify observable output ---

	// Engine messages should be empty (rewound to idx 0)
	engMsgs := app.engine.Messages()
	if len(engMsgs) != 0 {
		t.Fatalf("expected 0 engine messages after rewind, got %d", len(engMsgs))
	}

	// File should be restored to original content on disk
	restored, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != string(originalContent) {
		t.Errorf("file not restored: got %q, want %q", string(restored), string(originalContent))
	}

	// Store should retain all messages (append-only — no truncation)
	storeMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after rewind: %v", err)
	}
	if len(storeMsgs) == 0 {
		t.Error("expected store to retain messages after rewind (append-only)")
	}

	// lastPersistedIdx should be updated to rewind point
	if app.engine.LastPersistedIdx() != 0 {
		t.Errorf("expected lastPersistedIdx=0, got %d", app.engine.LastPersistedIdx())
	}

	// forkParentUUID should be empty (rewound to beginning)
}

// ---------------------------------------------------------------------------
// Chain 3: File Created → Rewind → File Deleted
//
// Tests that files created during rewound turns are deleted on rewind.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_DeletesCreatedFiles(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	// A file that didn't exist before the edit (new file creation).
	// TrackEdit before file exists → v1 backup = null (BackupFileName="").
	// MakeSnapshot before file exists → snapshot "0" records null backup.
	// THEN create file. HasChangesAtMessage("0") detects file exists but shouldn't.
	// Rewind("") (initial snapshot) → file deleted.
	msgID1 := "fff11111-1111-1111-1111-111111111111"
	msgID2 := "fff22222-2222-2222-2222-222222222222"

	newFile := filepath.Join(projectDir, "new_feature.go")
	if err := tracker.TrackEdit(newFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	newContent := []byte("package main\n\nfunc newFeature() {}\n")
	if err := os.WriteFile(newFile, newContent, 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("create new_feature.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("created")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// --- Execute: /rewind to first user message ---
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Scope dialog appears because fileHistory has backups
	if app.activeDialog == nil {
		t.Fatal("expected scope dialog after message selection (has file changes)")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	app = model.(*App)

	// --- Verify: created file should be deleted ---
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("expected created file to be deleted after rewind, but it exists or stat error: %v", err)
	}

	// Engine messages should be rewound
	if len(app.engine.Messages()) != 0 {
		t.Errorf("expected 0 messages after rewind, got %d", len(app.engine.Messages()))
	}
}

// ---------------------------------------------------------------------------
// Chain 4: Persistence Round-trip + Recovery
//
// Tests that after rewind + store truncation, a fresh store load produces
// the correct message set (simulating process restart).
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_PersistenceRoundtrip(t *testing.T) {
	a, store, _, _ := setupRewindIntegration(t)

	// Multi-turn conversation
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 1")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 1")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 2")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 2")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 3")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 3")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Persist all messages
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 6 {
		t.Fatalf("expected lastPersistedIdx=6, got %d", a.engine.LastPersistedIdx())
	}

	// Verify store has all messages before rewind
	storeMsgsBefore, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages before rewind: %v", err)
	}
	if len(storeMsgsBefore) != 6 {
		t.Fatalf("expected 6 messages in store before rewind, got %d", len(storeMsgsBefore))
	}

	// --- Execute: /rewind to turn 2 (index 2, keep messages 0-1) ---
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 1 // second user message → indices[1]=2
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Engine should have 2 messages
	engMsgs := app.engine.Messages()
	if len(engMsgs) != 2 {
		t.Fatalf("expected 2 engine messages after rewind, got %d", len(engMsgs))
	}
	if firstTextBlockContent(engMsgs[0]) != "turn 1" {
		t.Errorf("first message should be 'turn 1', got %q", firstTextBlockContent(engMsgs[0]))
	}
	if firstTextBlockContent(engMsgs[1]) != "response 1" {
		t.Errorf("second message should be 'response 1', got %q", firstTextBlockContent(engMsgs[1]))
	}

	// Store should retain all 6 messages (append-only — no truncation)
	storeMsgsAfter, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after rewind: %v", err)
	}
	if len(storeMsgsAfter) != 6 {
		t.Fatalf("expected 6 messages still in store after rewind (append-only), got %d", len(storeMsgsAfter))
	}

	// lastPersistedIdx should be at the rewind point
	if app.engine.LastPersistedIdx() != 2 {
		t.Errorf("expected lastPersistedIdx=2, got %d", app.engine.LastPersistedIdx())
	}
}

// ---------------------------------------------------------------------------
// Chain 5: Auto-rewind does NOT fire when assistant produced meaningful content
//
// Verifies the guard: if assistant has text or tool_use, no rewind happens.
// ---------------------------------------------------------------------------
func TestIntegration_AutoRewind_MeaningfulResponse_NoRewind(t *testing.T) {
	a, _, _, _ := setupRewindIntegration(t)

	// Assistant produced actual text — should NOT rewind
	a.engine.SetMessages([]types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("Here is your answer!")}, Timestamp: testTime},
	})
	a.history.Add("hello")

	result := a.tryAutoRewind()

	if result {
		t.Fatal("auto-rewind should NOT fire when assistant has meaningful text")
	}

	// Engine messages should be intact
	engMsgs := a.engine.Messages()
	if len(engMsgs) != 2 {
		t.Fatalf("expected 2 messages (no rewind), got %d", len(engMsgs))
	}

	// Input should NOT be changed
	if a.input.Value() != "" {
		t.Errorf("input should be empty (not restored), got %q", a.input.Value())
	}

	// History should NOT be modified
	if len(a.history.items) != 1 {
		t.Errorf("expected 1 history item, got %d", len(a.history.items))
	}
}

// ---------------------------------------------------------------------------
// Chain 6: Multiple file edits across turns → rewind restores correct version
//
// Tests that rewind to an intermediate turn restores files to the state at
// that turn (not the latest or earliest version).
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_MultipleEdits_CorrectVersion(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	testFile := filepath.Join(projectDir, "config.go")
	v1 := []byte("package main\n\nconst Version = 1\n")
	v2 := []byte("package main\n\nconst Version = 2\n")
	v3 := []byte("package main\n\nconst Version = 3\n")

	msgID1 := "aaa11111-1111-1111-1111-111111111111"
	msgID2 := "bbb22222-2222-2222-2222-222222222222"
	msgID3 := "ccc33333-3333-3333-3333-333333333333"

	// Production flow: TrackEdit before edit, MakeSnapshot after edit, per turn.
	// Turn 1 (msgID1): write v1, TrackEdit captures v1, snapshot msgID1 records v1 (unchanged)
	if err := os.WriteFile(testFile, v1, 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit v1: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot msgID1: %v", err)
	}

	// Turn 2 (msgID2): edit v1→v2, snapshot msgID2 detects change, records v2
	if err := os.WriteFile(testFile, v2, 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit v2: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID2); err != nil {
		t.Fatalf("MakeSnapshot msgID2: %v", err)
	}

	// Turn 3 (msgID3): edit v2→v3, snapshot msgID3 records v3
	if err := os.WriteFile(testFile, v3, 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID3); err != nil {
		t.Fatalf("MakeSnapshot msgID3: %v", err)
	}

	// Messages with UUID IDs
	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 1")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 1")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config to v2")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{ID: msgID3, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config to v3")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// --- Execute: /rewind to turn 2 (index 2) ---
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 1 // indices[1]=2 → msgID2
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	if app.activeDialog == nil {
		t.Fatal("expected scope dialog after message selection (has file changes)")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	app = model.(*App)

	// TS semantics: RewindTo(idx=2) uses msgs[2].ID=msgID2 as snapshotID.
	// Snapshot at msgID2 has v2 (end of turn 2). File restored to v2.
	restored, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != string(v2) {
		t.Errorf("expected v2 after rewind to turn 2, got %q", string(restored))
	}

	if len(app.engine.Messages()) != 2 {
		t.Errorf("expected 2 messages after rewind, got %d", len(app.engine.Messages()))
	}
}

// ---------------------------------------------------------------------------
// Chain 7: Rewind with no file edits — messages only
//
// Verifies rewind works cleanly when no files were modified.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_NoFileEdits(t *testing.T) {
	a, store, _, _ := setupRewindIntegration(t)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("bye")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("see you")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)
	a.engine.PersistNewMessages()

	// --- Execute: /rewind to first user message ---
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// --- Verify: messages rewound, no file side effects ---
	if len(app.engine.Messages()) != 0 {
		t.Errorf("expected 0 messages, got %d", len(app.engine.Messages()))
	}

	// Store should retain all messages (append-only)
	storeMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(storeMsgs) == 0 {
		t.Error("expected store to retain messages after rewind (append-only)")
	}

	// forkParentUUID should be empty (rewound to beginning, idx=0)

	// Input should be restored from the selected message
	if app.input.Value() != "hello" {
		t.Errorf("expected input 'hello', got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// Chain 8: SetStore wires file history → Edit → Rewind → File restored
//
// SetStore must create and wire filehistory.Tracker so /rewind can restore files.
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
func TestIntegration_SetStore_WiresFileHistory(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Real file to edit
	testFile := filepath.Join(projectDir, "config.go")
	originalContent := []byte("package main\n\nconst Version = 1\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Real Store + session
	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Set up engine with messages (simulating auto-resume)
	msgs := []types.Message{
		{ID: "setstore-uuid-0", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}

	eng := engine.New(&engine.Params{Logger: slog.Default()})
	eng.SetMessages(msgs)

	// Create App and call SetStore — THIS is the production wiring path
	a := &App{
		engine: eng,
		repl:   NewReplState(),
		input:  NewInput(),
		history: NewHistory(""),
	}
	a.width = 80
	a.SetStore(store, session.SessionID, projectDir)

	// Verify SetStore created a Tracker and wired it to the engine.

	if a.fileHistory == nil {
		t.Fatalf("fileHistory is nil after SetStore")
	}

	// Simulate Edit: TrackEdit captures "original" as v1, MakeSnapshot records state,
	// then modify AFTER snapshot so rewind can restore.
	if err := a.fileHistory.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := a.fileHistory.MakeSnapshot("setstore-uuid-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	editedContent := []byte("package main\n\nconst Version = 2\n")
	if err := os.WriteFile(testFile, editedContent, 0o644); err != nil {
		t.Fatalf("write edited: %v", err)
	}

	// File should still be edited on disk
	current, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read current file: %v", err)
	}
	if string(current) != string(editedContent) {
		t.Fatalf("file should still be edited before rewind")
	}

	// Execute: RewindTo(0) should restore the file
	result, err := a.engine.RewindTo(0)
	if err != nil {
		t.Fatalf("RewindTo: %v", err)
	}

	// Verify: file should be restored to original content
	restored, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != string(originalContent) {
		t.Errorf("file NOT restored after rewind!\n  got:  %q\n  want: %q", string(restored), string(originalContent))
	}

	// RewindResult should report the restored file
	if len(result.RestoredFiles) != 1 || result.RestoredFiles[0] != testFile {
		t.Errorf("RestoredFiles = %v, want [%s]", result.RestoredFiles, testFile)
	}
}

func TestIntegration_BashFileMonitor_ModifiedFile(t *testing.T) {
	// TakeSnapshot captures file state, then Bash modifies the file.
	// DetectChanges should return BeforeContent with the ORIGINAL content.
	// We verify the Bash monitor detects the change, then separately verify
	// that a tracker with the correct setup can restore the file on rewind.
	tmp := t.TempDir()
	testFile := filepath.Join(tmp, "config.txt")
	originalContent := []byte("v1\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// 1. Take snapshot before Bash modifies the file
	snap, err := filehistory.TakeSnapshot(tmp)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// 2. Bash "modifies" the file (different size to ensure mtime/size detection)
	modifiedContent := []byte("version-two\n")
	if err := os.WriteFile(testFile, modifiedContent, 0o644); err != nil {
		t.Fatalf("write modified: %v", err)
	}

	// 3. Detect changes
	changes, err := filehistory.DetectChanges(tmp, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}

	// BeforeContent must be the original content
	if changes[0].BeforeContent == nil {
		t.Fatalf("BeforeContent is nil, cannot restore modified file on rewind!")
	}
	if string(changes[0].BeforeContent) != string(originalContent) {
		t.Errorf("BeforeContent = %q, want %q", string(changes[0].BeforeContent), string(originalContent))
	}

	// 4. Verify rewind restores the file using snapshot API.
	// TrackEdit reads from disk BEFORE modification (we need to reset file first).
	// Since file is already modified, write original back, TrackEdit, MakeSnapshot,
	// then write modified again. Rewind to initial snapshot restores original.
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := os.WriteFile(testFile, modifiedContent, 0o644); err != nil {
		t.Fatalf("write modified: %v", err)
	}

	restored, err := tracker.Rewind("")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(restored) != 1 {
		t.Errorf("restored %d files, want 1", len(restored))
	}

	actual, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(actual) != string(originalContent) {
		t.Errorf("file = %q, want %q", string(actual), string(originalContent))
	}
}

func TestIntegration_BashFileMonitor_NewFile(t *testing.T) {
	// Bash creates a new file. DetectChanges should return BeforeContent=nil.
	// On rewind, the file should be deleted.
	tmp := t.TempDir()

	// 1. Snapshot of empty directory
	snap, err := filehistory.TakeSnapshot(tmp)
	if err != nil {
		t.Fatalf("TakeSnapshot: %v", err)
	}

	// 2. Bash "creates" a new file
	newFile := filepath.Join(tmp, "new.txt")
	if err := os.WriteFile(newFile, []byte("created\n"), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	// 3. Detect changes
	changes, err := filehistory.DetectChanges(tmp, snap)
	if err != nil {
		t.Fatalf("DetectChanges: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Path != newFile {
		t.Errorf("Path = %q, want %q", changes[0].Path, newFile)
	}
	if changes[0].BeforeContent != nil {
		t.Errorf("BeforeContent should be nil for new files, got %q", string(changes[0].BeforeContent))
	}

	// 4. TrackEdit before file exists (null backup), MakeSnapshot records null,
	// then create file. Rewind to initial snapshot → file should be deleted.
	// Remove the file first so TrackEdit sees it as non-existent.
	_ = os.Remove(newFile)
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	if err := tracker.TrackEdit(newFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tracker.MakeSnapshot("0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	// Now create the file AFTER snapshot
	if err := os.WriteFile(newFile, []byte("created\n"), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	restored, err := tracker.Rewind("")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(restored) != 1 {
		t.Errorf("restored %d files, want 1", len(restored))
	}

	// File should be deleted after rewind
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("new file should be deleted after rewind, err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Chain 9: Persistence — SetStore loads persisted file history state
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_PersistenceSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	testFile := filepath.Join(projectDir, "config.go")
	originalContent := []byte("v0\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Session 1: App with SetStore (wires fileHistoryWriter)
	eng1 := engine.New(&engine.Params{Logger: slog.Default()})
	app1 := &App{
		engine:           eng1,
		repl:             NewReplState(),
		input:            NewInput(),
		history:          NewHistory(""),
	}
	app1.width = 80
	app1.SetStore(store, session.SessionID, projectDir)

	if err := app1.fileHistory.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := os.WriteFile(testFile, []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := app1.fileHistory.MakeSnapshot("msg-0"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}
	if err := store.SaveFileHistoryState(session.SessionID, app1.fileHistory.State()); err != nil {
		t.Fatalf("SaveFileHistoryState: %v", err)
	}

	// Verify v1 is on disk
	if data, err := os.ReadFile(testFile); err != nil {
		t.Fatalf("read: %v", err)
	} else if string(data) != "v1\n" {
		t.Fatalf("expected v1, got %q", string(data))
	}

	// ---------------------------------------------------------------------------
	// Session restart: new App with SetStore (loads persisted state)
	// ---------------------------------------------------------------------------
	eng2 := engine.New(&engine.Params{Logger: slog.Default()})
	app2 := &App{
		engine:           eng2,
		repl:             NewReplState(),
		input:            NewInput(),
		history:          NewHistory(""),
	}
	app2.width = 80
	app2.SetStore(store, session.SessionID, projectDir)

	// Verify SetStore loaded the persisted state
	snapshots := app2.fileHistory.State().Snapshots
	t.Logf("loaded %d snapshots, tracker dir=%s", len(snapshots), app2.fileHistory.Dir())
	for i, s := range snapshots {
		for fp, b := range s.TrackedFileBackups {
			backupPath := filepath.Join(app2.fileHistory.Dir(), b.BackupFileName)
			_, statErr := os.Stat(backupPath)
			t.Logf("  snapshot[%d] msgID=%q: %s backup=%q (exists=%v)", i, s.MessageID, fp, b.BackupFileName, statErr == nil)
		}
	}
	if len(snapshots) < 2 {
		t.Errorf("expected at least 2 snapshots after LoadState, got %d", len(snapshots))
	}

	// Rewind to initial snapshot (MessageID="") — should restore v0.
	// RewindTo(0) would derive "" as snapshot ID (no user messages in messages[:0]).
	restored, err := app2.fileHistory.Rewind("")
	t.Logf("Rewind result: restored=%v err=%v", restored, err)

	// Verify backup file content
	for _, s := range app2.fileHistory.State().Snapshots {
		for fp, b := range s.TrackedFileBackups {
			if s.MessageID == "" && b.Version == 1 {
				v1BackupPath := filepath.Join(app2.fileHistory.Dir(), b.BackupFileName)
				t.Logf("v1 backup for %s: %s", fp, v1BackupPath)
				data, err := os.ReadFile(v1BackupPath)
				if err != nil {
					t.Fatalf("read v1 backup: %v", err)
				}
				t.Logf("v1 backup content: %q", string(data))
				break
			}
		}
	}
	if len(restored) == 0 {
		t.Errorf("REGRESSION: rewind restored 0 files — persistence broken, snapshot state was lost after session restart")
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "v0\n" {
		t.Errorf("REGRESSION: file = %q, want v0 — rewind did NOT restore!", string(data))
	}
}

// ---------------------------------------------------------------------------
// Chain 11: Scope dialog after session resume (message ID round-trip)
//
// Production failure: after restarting gbot, messages lose their UUID IDs because
// StoreMessagesToEngine does NOT map sm.UUID -> types.Message.ID.
// Rewind falls back to index-based IDs ("0", "2") which don't match
// UUID snapshot IDs -> HasChangesAtMessage returns false -> no scope dialog.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_ScopeDialogAfterResume(t *testing.T) {
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	testFile := filepath.Join(projectDir, "config.go")
	v1 := []byte("package main\n\nconst Version = 1\n")
	v2 := []byte("package main\n\nconst Version = 2\n")
	v3 := []byte("package main\n\nconst Version = 3\n")

	if err := os.WriteFile(testFile, v1, 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	store, err := short.NewStore(filepath.Join(dir, "memory", "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	msgID1 := "f47ac10b-58cc-4372-a567-0e02b2c3d479"
	msgID2 := "7c9e6679-7425-40de-944b-e07fc1f90ae7"

	// === Session 1: edit file v1→v2, create snapshots, persist ===
	//
	// Production timing matches engine.queryLoop:
	//   1. User sends message (msgID1)
	//   2. Tool execution: TrackEdit captures v1 BEFORE writing
	//   3. File edited to v2
	//   4. Turn ends: MakeSnapshot(msgID1) records post-edit state (v2)
	//   5. User sends message (msgID2) — no file edit this turn
	//   6. Turn ends: MakeSnapshot(msgID2)
	trackerDir := filepath.Join(dir, "file-history")
	tracker := filehistory.NewTracker(trackerDir)

	// Turn 1: edit v1 → v2
	if err := tracker.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := os.WriteFile(testFile, v2, 0o644); err != nil {
		t.Fatalf("WriteFile v2: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot msgID1: %v", err)
	}

	// Turn 2: no file edit, just MakeSnapshot
	if err := tracker.MakeSnapshot(msgID2); err != nil {
		t.Fatalf("MakeSnapshot msgID2: %v", err)
	}

	// Messages with UUID IDs (as created by engine.queryLoop)
	msgs1 := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("what else?")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("nothing")}, Timestamp: testTime},
	}

	// Persist messages and file history state
	storeMsgs, err := short.EngineMessagesToStore(msgs1)
	if err != nil {
		t.Fatalf("EngineMessagesToStore: %v", err)
	}
	for _, sm := range storeMsgs {
		if err := store.AppendMessage(session.SessionID, sm); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	if err := store.SaveFileHistoryState(session.SessionID, tracker.State()); err != nil {
		t.Fatalf("SaveFileHistoryState: %v", err)
	}

	// === Session 2: resume via store (matching production main.go flow) ===
	_, resumedMsgs, err := store.ResumeSession(session.SessionID)
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}

		engineMsgs, err := short.StoreMessagesToEngine(resumedMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}

	// RED CHECK: verify user message IDs survived the round-trip
	var origUserIDs []string
	for _, m := range msgs1 {
		if m.Role == types.RoleUser {
			origUserIDs = append(origUserIDs, m.ID)
		}
	}
	var resumedUserIdx int
	for _, msg := range engineMsgs {
		if msg.Role != types.RoleUser {
			continue
		}
		if resumedUserIdx < len(origUserIDs) {
			if msg.ID != origUserIDs[resumedUserIdx] {
				t.Errorf("user message %d ID lost in round-trip: got %q, want %q",
					resumedUserIdx, msg.ID, origUserIDs[resumedUserIdx])
			}
		}
		resumedUserIdx++
	}

	// Set up resumed App (matching production SetStore flow)
	eng2 := engine.New(&engine.Params{Logger: slog.Default()})
	eng2.SetMessages(engineMsgs)
	eng2.SetSessionID(session.SessionID)

	tracker2 := filehistory.NewTracker(trackerDir)
	if state, err := store.LoadFileHistoryState(session.SessionID); err == nil && state != nil {
		tracker2.LoadState(*state)
	}
	eng2.SetFileHistory(tracker2)

	app := &App{
		engine:           eng2,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
		repl:             NewReplState(),
		input:            NewInput(),
		history:          NewHistory(""),
		fileHistory:      tracker2,
	}
	app.width = 80
	app.repl.messages = engineMessagesToViews(engineMsgs)
	app.committedCount = len(app.repl.messages)

	// === Simulate post-resume edit: user asks to edit file again (v2 → v3) ===
	//
	// Production flow: user sends message, tool edits file, turn ends with MakeSnapshot.
	// We simulate this directly: TrackEdit + edit + MakeSnapshot for the new turn.
	msgID3 := "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	if err := tracker2.TrackEdit(testFile); err != nil {
		t.Fatalf("TrackEdit v2→v3: %v", err)
	}
	if err := os.WriteFile(testFile, v3, 0o644); err != nil {
		t.Fatalf("WriteFile v3: %v", err)
	}
	if err := tracker2.MakeSnapshot(msgID3); err != nil {
		t.Fatalf("MakeSnapshot msgID3: %v", err)
	}

	// Add the new turn's messages to engine
	newMsgs := append(engineMsgs, []types.Message{
		{ID: msgID3, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("change to v3")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}...)
	eng2.SetMessages(newMsgs)
	app.repl.messages = engineMessagesToViews(newMsgs)
	app.committedCount = len(app.repl.messages)

	// Verify current disk state is v3 before rewind
	current, _ := os.ReadFile(testFile)
	if string(current) != string(v3) {
		t.Fatalf("pre-rewind file should be v3, got %q", string(current))
	}

	// --- Execute: /rewind to first user message (msgID1, cursor=0) ---
	_ = app.handleRewind(nil)
	if app.activeDialog == nil {
		t.Fatal("expected message picker dialog")
	}

	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // select first user message (msgID1)
	model, _ := app.onDialogDone(app.activeDialog)
	app2 := model.(*App)

	// RED ASSERTION: scope dialog MUST appear after session resume.
	// HasChangesAtMessage(msgID1) compares snapshot at msgID1 (v2) vs current disk (v3)
	// → detects change → shows scope dialog.
	if app2.activeDialog == nil {
		t.Logf("DIAGNOSIS: scope dialog missing after resume")
		for i, msg := range newMsgs {
			if msg.Role == types.RoleUser {
				t.Logf("  msg[%d].ID = %q", i, msg.ID)
			}
		}
		state := tracker2.State()
		for i, s := range state.Snapshots {
			t.Logf("  snapshot[%d] msgID=%q", i, s.MessageID)
		}
		t.Fatalf("REGRESSION: scope dialog missing after session resume. " +
			"HasChangesAtMessage(msgID1) returned false — snapshot ID mismatch or no file change detected.")
	}

	if app2.activeDialog.title != "What do you want to restore?" {
		t.Errorf("scope dialog title = %q, want 'What do you want to restore?'", app2.activeDialog.title)
	}

	// Select "Restore code and conversation"
	app2.activeDialog.done = true
	app2.activeDialog.cursor = 0
	model, _ = app2.onDialogDone(app2.activeDialog)
	_ = model.(*App)

	// RewindTo(0) → snapshotID=msgs[0].ID=msgID1 → snapshot at msgID1 → v2
	restored, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(restored) != string(v2) {
		t.Errorf("file not restored to v2: got %q, want %q", string(restored), string(v2))
	}
}

// ---------------------------------------------------------------------------
// Chain 12: /rewind after ESC abort during tool use rounds
//
// User scenario: execute a query, a few rounds of tool use happen, user presses
// ESC before text response, normal interrupt (no autorewind), but /rewind doesn't
// show the interrupted query. This test verifies the interrupted query IS visible.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_AbortDuringToolUse(t *testing.T) {
	a, _, _, _ := setupRewindIntegration(t)

	// Simulate a conversation with prior history:
	// [0] user: "previous query"
	// [1] assistant: "previous response"
	prevMsgs := []types.Message{
		{
			ID:        "prev-user-1",
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock("previous query")},
			Timestamp: testTime,
		},
		{
			ID:        "prev-asst-1",
			Role:      types.RoleAssistant,
			Content:   []types.ContentBlock{types.NewTextBlock("previous response")},
			Timestamp: testTime,
		},
	}

	// Now the user submits a new query. Engine appends it immediately (queryLoop line 379).
	// Then tool use rounds happen. User presses ESC during streaming.
	//
	// Post-abort engine state (simulating callLLM ctx.Done path, lines 1029-1057):
	// [2] user: "read main.go"           ← the interrupted query
	// [3] assistant: [tool_use Read]      ← round 1 tool call
	// [4] user: [tool_result]             ← round 1 result
	// [5] assistant: [tool_use Grep]      ← round 2 tool call
	// [6] user: [tool_result + interrupt] ← round 2 result + appendInlineInterruptMessage
	abortedMsgs := []types.Message{
		{
			ID:        "abort-user-1",
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock("read main.go")},
			Timestamp: testTime,
		},
		{
			ID:   "abort-asst-1",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tool-1", Name: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)},
			},
			Timestamp: testTime,
		},
		{
			ID:   "abort-user-2",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tool-1", Text: "file contents here"},
			},
			Timestamp: testTime,
		},
		{
			ID:   "abort-asst-2",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tool-2", Name: "Grep", Input: json.RawMessage(`{"pattern":"func"}`)},
			},
			Timestamp: testTime,
		},
		{
			ID:   "abort-user-3",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tool-2", Text: "match1\nmatch2"},
				types.NewTextBlock(types.InterruptMessage), // appended by appendInlineInterruptMessage
			},
			Timestamp: testTime,
		},
	}

	allMsgs := append(prevMsgs, abortedMsgs...)
	a.engine.SetMessages(allMsgs)

	// Step 1: Verify auto-rewind does NOT fire (tool_use blocks exist)
	if a.tryAutoRewind() {
		t.Fatal("auto-rewind should NOT fire when tool_use blocks exist after user message")
	}

	// Step 2: Verify /rewind can see the interrupted query
	msgs := a.engine.Messages()
	var selectableIndices []int
	var selectableTexts []string
	for i, msg := range msgs {
		if isSelectableUserMessage(msg) {
			selectableIndices = append(selectableIndices, i)
			selectableTexts = append(selectableTexts, firstTextBlockContent(msg))
		}
	}

	// Should find both the previous query and the interrupted query
	if len(selectableIndices) != 2 {
		t.Fatalf("expected 2 selectable user messages, got %d: %v", len(selectableIndices), selectableTexts)
	}
	if selectableTexts[0] != "previous query" {
		t.Errorf("first selectable = %q, want 'previous query'", selectableTexts[0])
	}
	if selectableTexts[1] != "read main.go" {
		t.Errorf("second selectable = %q, want 'read main.go'", selectableTexts[1])
	}

	// Step 3: Verify the interrupted query is at the expected index
	if selectableIndices[1] != 2 {
		t.Errorf("interrupted query at index %d, want 2", selectableIndices[1])
	}

	// Step 4: Rewind to the interrupted query should work
	result, err := a.engine.RewindTo(2)
	if err != nil {
		t.Fatalf("RewindTo(2) error: %v", err)
	}
	if result.MessageCount != 2 {
		t.Errorf("after rewind, message count = %d, want 2", result.MessageCount)
	}
}

// ---------------------------------------------------------------------------
// Chain 13: isSyntheticMessage must NOT mark tool_use messages as synthetic
//
// Stage 18 abort (ESC after streaming, before tool execution): interrupt text
// appended to assistant message with tool_use blocks. isSyntheticMessage must
// not classify it as synthetic, preventing incorrect auto-rewind.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_Stage18AbortWithToolUse(t *testing.T) {
	a, _, _, _ := setupRewindIntegration(t)

	// Simulate Stage 18 abort state:
	// ESC pressed after LLM streamed tool_use response, before tool execution.
	// appendInlineInterruptMessage appended interrupt to the assistant message.
	// appendSyntheticToolResultsLocked added synthetic tool_results.
	//
	// [0] user: "read main.go"
	// [1] assistant: [tool_use Read, text "[Request interrupted by user]"]
	// [2] user: [synthetic tool_result]
	a.engine.SetMessages([]types.Message{
		{
			ID:        "user-1",
			Role:      types.RoleUser,
			Content:   []types.ContentBlock{types.NewTextBlock("read main.go")},
			Timestamp: testTime,
		},
		{
			ID:   "asst-1",
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolUse, ID: "tool-1", Name: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)},
				types.NewTextBlock(types.InterruptMessage),
			},
			Timestamp: testTime,
		},
		{
			ID:   "user-2",
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				{Type: types.ContentTypeToolResult, ToolUseID: "tool-1", Text: "synthetic error"},
			},
			Timestamp: testTime,
		},
	})

	// Auto-rewind should NOT fire — tool_use blocks exist (meaningful LLM output)
	if a.tryAutoRewind() {
		t.Fatal("auto-rewind should NOT fire when assistant message has tool_use blocks")
	}

	// /rewind should see the query
	msgs := a.engine.Messages()
	var found bool
	for _, msg := range msgs {
		if isSelectableUserMessage(msg) && firstTextBlockContent(msg) == "read main.go" {
			found = true
		}
	}
	if !found {
		t.Error("/rewind should find 'read main.go' as selectable, but it was not found")
	}
}

// ---------------------------------------------------------------------------
// Chain 8: Rewind -> New messages -> Store chain reload (Full lifecycle)
//
// The most critical append-only test. Exercises the complete lifecycle:
//  1. Multi-turn conversation -> persist to store
//  2. /rewind -> engine truncates, forkParentUUID captured
//  3. New messages -> persistTurn uses AppendMessagesWithForkPoint
//  4. Simulate restart: LoadChainMessages from store
//  5. Verify only active branch loaded (dead branches skipped)
//
// Call chain: persistTurn -> executeRewind -> persistTurn -> store.LoadChainMessages
// Observable output: store.LoadChainMessages returns only the active branch
//
// All messages have explicit IDs (production behavior) so forkParentUUID
// maps correctly to store UUIDs via EngineMessagesToStore.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_NewMessages_ChainReload(t *testing.T) {
	a, store, _, _ := setupRewindIntegration(t)

	// --- Phase 1: Build 3-turn conversation with explicit IDs ---
	msgs := []types.Message{
		{ID: "u1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 1")}, Timestamp: testTime},
		{ID: "a1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 1")}, Timestamp: testTime},
		{ID: "u2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 2")}, Timestamp: testTime},
		{ID: "a2", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 2")}, Timestamp: testTime},
		{ID: "u3", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 3")}, Timestamp: testTime},
		{ID: "a3", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 3")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Persist all 6 messages
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 6 {
		t.Fatalf("expected lastPersistedIdx=6, got %d", a.engine.LastPersistedIdx())
	}

	// Verify store has all 6
	allBefore, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages before rewind: %v", err)
	}
	if len(allBefore) != 6 {
		t.Fatalf("expected 6 messages in store, got %d", len(allBefore))
	}

	// --- Phase 2: /rewind to turn 2 (index=2, keep u1+a1) ---
	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("expected dialog to open")
	}

	// Select turn 2 user message (index 2 in the list)
	a.activeDialog.done = true
	a.activeDialog.cursor = 1 // second user message -> indices[1]=2
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Verify engine truncated to 2 messages
	engMsgs := app.engine.Messages()
	if len(engMsgs) != 2 {
		t.Fatalf("expected 2 engine messages after rewind, got %d", len(engMsgs))
	}
	if firstTextBlockContent(engMsgs[0]) != "turn 1" {
		t.Errorf("first msg = %q, want 'turn 1'", firstTextBlockContent(engMsgs[0]))
	}

	// Verify engine captured fork point (observable via persist)
	if app.engine.LastPersistedIdx() != 2 {
		t.Errorf("engine lastPersistedIdx = %d, want 2", app.engine.LastPersistedIdx())
	}
	if app.engine.LastPersistedIdx() != 2 {
		t.Errorf("TUI lastPersistedIdx = %d, want 2 (synced from engine)", app.engine.LastPersistedIdx())
	}

	// --- Phase 3: Send 2 new turns (simulating user continuing after rewind) ---
	newMsgs := []types.Message{
		{ID: "u4", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("new turn 1")}, Timestamp: testTime},
		{ID: "a4", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("new response 1")}, Timestamp: testTime},
		{ID: "u5", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("new turn 2")}, Timestamp: testTime},
		{ID: "a5", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("new response 2")}, Timestamp: testTime},
	}
	// Set engine messages to existing + new
	app.engine.SetMessages(append(engMsgs[:2], newMsgs...))

	// Persist new messages -- should use AppendMessagesWithForkPoint
	app.engine.PersistNewMessages()

	// Engine fork state is internal; verify persist succeeded by checking store count

	// Store should have 10 messages total (6 original + 4 new, dead branches kept)
	allAfter, err := store.LoadMessages(app.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after new persist: %v", err)
	}
	if len(allAfter) != 10 {
		t.Fatalf("expected 10 messages in store (append-only), got %d", len(allAfter))
	}

	// --- Phase 4: Simulate restart -- load chain from store ---
	chain, err := store.LoadChainMessages(app.sessionID)
	if err != nil {
		t.Fatalf("LoadChainMessages: %v", err)
	}

	// Active branch: u1 -> a1 -> u4 -> a4 -> u5 -> a5 (6 messages)
	// Dead branch: u2 -> a2 -> u3 -> a3 (skipped by chain-walk)
	if len(chain) != 6 {
		t.Fatalf("got %d chain messages, want 6 (active branch only)", len(chain))
	}

	wantUUIDs := []string{"u1", "a1", "u4", "a4", "u5", "a5"}
	for i, msg := range chain {
		if msg.UUID != wantUUIDs[i] {
			t.Errorf("chain[%d].UUID = %q, want %q", i, msg.UUID, wantUUIDs[i])
		}
	}

	// Verify dead branch messages are NOT in the chain
	chainUUIDs := make(map[string]bool, len(chain))
	for _, msg := range chain {
		chainUUIDs[msg.UUID] = true
	}
	for _, deadUUID := range []string{"u2", "a2", "u3", "a3"} {
		if chainUUIDs[deadUUID] {
			t.Errorf("dead branch message %q should not be in active chain", deadUUID)
		}
	}
}

// ---------------------------------------------------------------------------
// E2E Test: Full Lifecycle — TUI + Engine + Store
//
// Simulates a real user session from start to finish:
//  1. Multi-turn conversation (send → persist)
//  2. Rewind + new messages (fork → persist)
//  3. Compact boundary (insert boundary + post-compact messages)
//  4. Process restart (new App instance from same DB → resume → verify)
//
// Covers the complete append-only + chain-walk lifecycle end-to-end.
// Uses real SQLite, real Engine, real file I/O. No mocking.
// ---------------------------------------------------------------------------

// setupE2E creates a fully wired App for E2E testing.
// Returns the App, store DB path (for restart), and cleanup.
func setupE2E(t *testing.T) (*App, *short.Store, string) {
	t.Helper()

	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	store, err := short.NewStore(filepath.Join(dir, "memory", "e2e.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.New(&engine.Params{Logger: slog.Default()})

	session, err := store.CreateSession(projectDir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	eng.SetSessionID(session.SessionID)
	eng.SetStore(store, projectDir)

	a := &App{
		engine:           eng,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
		repl:             NewReplState(),
		input:            NewInput(),
		history:          NewHistory(""),
	}
	a.width = 80

	return a, store, filepath.Join(dir, "memory", "e2e.db")
}

func TestE2E_RewindCompactResume(t *testing.T) {
	a, store, dbPath := setupE2E(t)

	// ================================================================
	// Phase 1: Multi-turn conversation — 3 turns, all persisted
	// ================================================================
	turn1 := []types.Message{
		{ID: "e2e-u1", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("what is Go?")}, Timestamp: testTime},
		{ID: "e2e-a1", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("a compiled language")}, Timestamp: testTime},
	}
	turn2 := []types.Message{
		{ID: "e2e-u2", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("show hello world")}, Timestamp: testTime},
		{ID: "e2e-a2", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock(`fmt.Println("hello")`)}, Timestamp: testTime},
	}
	turn3 := []types.Message{
		{ID: "e2e-u3", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("explain goroutines")}, Timestamp: testTime},
		{ID: "e2e-a3", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("lightweight threads")}, Timestamp: testTime},
	}

	allMsgs := append(append(turn1, turn2...), turn3...)
	a.engine.SetMessages(allMsgs)
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != 6 {
		t.Fatalf("Phase 1: lastPersistedIdx=%d, want 6", a.engine.LastPersistedIdx())
	}

	// ================================================================
	// Phase 2: Rewind to turn 2 (keep u1+a1+u2+a2), send new messages
	// ================================================================
	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("Phase 2: expected rewind dialog")
	}
	// Selectable user messages at indices: 0(u1), 2(u2), 4(u3)
	// cursor=2 -> index=4 -> rewind to turn 3
	a.activeDialog.done = true
	a.activeDialog.cursor = 2
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Engine should have 4 messages (u1, a1, u2, a2)
	engMsgs := app.engine.Messages()
	if len(engMsgs) != 4 {
		t.Fatalf("Phase 2: engine has %d messages after rewind, want 4", len(engMsgs))
	}
	if app.engine.LastPersistedIdx() != 4 {
		t.Errorf("Phase 2: engine lastPersistedIdx=%d, want 4", app.engine.LastPersistedIdx())
	}

	// Send 1 new turn after rewind
	newTurns := []types.Message{
		{ID: "e2e-u4", Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("tell me about interfaces")}, Timestamp: testTime},
		{ID: "e2e-a4", Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("interfaces define behavior")}, Timestamp: testTime},
	}
	app.engine.SetMessages(append(engMsgs[:4], newTurns...))
	app.engine.PersistNewMessages()

	// Store: 6 original + 2 new = 8 total (append-only)
	allAfter, err := store.LoadMessages(app.sessionID)
	if err != nil {
		t.Fatalf("Phase 2: LoadMessages: %v", err)
	}
	if len(allAfter) != 8 {
		t.Fatalf("Phase 2: store has %d messages, want 8 (append-only)", len(allAfter))
	}

	// Chain: u1->a1->u2->a2->u4->a4 (6 messages, dead branch u3/a3 skipped)
	chain, err := store.LoadChainMessages(app.sessionID)
	if err != nil {
		t.Fatalf("Phase 2: LoadChainMessages: %v", err)
	}
	if len(chain) != 6 {
		t.Fatalf("Phase 2: chain has %d messages, want 6", len(chain))
	}
	for _, msg := range chain {
		for _, dead := range []string{"e2e-u3", "e2e-a3"} {
			if msg.UUID == dead {
				t.Errorf("Phase 2: dead branch message %q in chain", dead)
			}
		}
	}

	// ================================================================
	// Phase 3: Compact — insert boundary, then add post-boundary msgs
	// ================================================================
	boundary := &short.TranscriptMessage{
		Type:      "system",
		Subtype:   "compact_boundary",
		UUID:      "e2e-boundary",
		Content:   `[{"type":"text","text":"[Compact summary: Go basics, hello world]"}]`,
		CreatedAt: testTime,
	}
	if err := store.AppendMessage(app.sessionID, boundary); err != nil {
		t.Fatalf("Phase 3: AppendMessage boundary: %v", err)
	}

	// Add post-boundary messages (chain from boundary)
	postCompactMsgs := []*short.TranscriptMessage{
		{Type: "user", UUID: "e2e-u5", Content: `[{"type":"text","text":"what about generics?"}]`, CreatedAt: testTime},
		{Type: "assistant", UUID: "e2e-a5", Content: `[{"type":"text","text":"type parameters"}]`, CreatedAt: testTime},
	}
	if err := store.AppendMessagesWithForkPoint(app.sessionID, postCompactMsgs, "e2e-boundary"); err != nil {
		t.Fatalf("Phase 3: AppendMessagesWithForkPoint: %v", err)
	}

	// Verify LoadPostCompactChainMessages returns post-boundary chain
	postCompact, err := store.LoadPostCompactChainMessages(app.sessionID)
	if err != nil {
		t.Fatalf("Phase 3: LoadPostCompactChainMessages: %v", err)
	}
	wantPostCompact := []string{"e2e-boundary", "e2e-u5", "e2e-a5"}
	if len(postCompact) != len(wantPostCompact) {
		t.Fatalf("Phase 3: got %d post-compact messages, want %d", len(postCompact), len(wantPostCompact))
	}
	for i, msg := range postCompact {
		if msg.UUID != wantPostCompact[i] {
			t.Errorf("Phase 3: postCompact[%d]=%q, want %q", i, msg.UUID, wantPostCompact[i])
		}
	}

	// ================================================================
	// Phase 4: Restart — new store + engine from same DB
	// ================================================================
	if err := store.Close(); err != nil {
		t.Fatalf("Phase 4: close store: %v", err)
	}

	store2, err := short.NewStore(dbPath)
	if err != nil {
		t.Fatalf("Phase 4: NewStore reopen: %v", err)
	}
	defer store2.Close()

	// Resume via store.ResumeSession (chain-walk + boundary filtering)
	_, resumedMsgs, err := store2.ResumeSession(app.sessionID)
	if err != nil {
		t.Fatalf("Phase 4: ResumeSession: %v", err)
	}
	if len(resumedMsgs) != len(wantPostCompact) {
		t.Fatalf("Phase 4: ResumeSession returned %d messages, want %d", len(resumedMsgs), len(wantPostCompact))
	}
	for i, msg := range resumedMsgs {
		if msg.UUID != wantPostCompact[i] {
			t.Errorf("Phase 4: resumed[%d]=%q, want %q", i, msg.UUID, wantPostCompact[i])
		}
	}

	// Convert to engine messages via TUI layer
	// After compact, chain-walk stops at boundary (parent_uuid="") so
	// only post-boundary chain is returned: boundary->u5->a5
	storeMsgs2, err := store2.LoadChainMessages(app.sessionID)
	if err != nil {
		t.Fatalf("Phase 4: LoadChainMessages: %v", err)
	}
	engMsgs2, err := short.StoreMessagesToEngine(storeMsgs2)
	if err != nil {
		t.Fatalf("Phase 4: StoreMessagesToEngine: %v", err)
	}
	// Chain: boundary(system) + u5(user) + a5(assistant) = 3
	if len(engMsgs2) != 3 {
		t.Fatalf("Phase 4: loadAndConvert returned %d engine messages, want 3", len(engMsgs2))
	}
	// Boundary summary is first (system message)
	if engMsgs2[0].Role != types.RoleSystem {
		t.Errorf("Phase 4: first msg role = %q, want system", engMsgs2[0].Role)
	}
	if firstTextBlockContent(engMsgs2[1]) != "what about generics?" {
		t.Errorf("Phase 4: second msg = %q, want 'what about generics?'", firstTextBlockContent(engMsgs2[1]))
	}
	if firstTextBlockContent(engMsgs2[2]) != "type parameters" {
		t.Errorf("Phase 4: third msg = %q, want 'type parameters'", firstTextBlockContent(engMsgs2[2]))
	}

	// Pre-boundary and dead branch messages not in engine
	for _, msg := range engMsgs2 {
		text := firstTextBlockContent(msg)
		for _, dead := range []string{"what is Go?", "explain goroutines", "lightweight threads"} {
			if text == dead {
				t.Errorf("Phase 4: pre-boundary content %q should not be in engine messages", text)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Chain: Rewind Menu Filter — Attachment & Skill Messages
//
// Tests that MessageTypeAttachment and FlagMeta messages are correctly
// filtered from the rewind picker after persist→load (simulating restart).
// Fixed: queued messages and skill content no longer appear in /rewind menu.
// ---------------------------------------------------------------------------

func TestIntegration_Rewind_FilterAttachmentAndSkillMessages(t *testing.T) {
	a, store, _, _ := setupRewindIntegration(t)

	// Simulate a conversation with mixed message types:
	// - normal user messages (selectable)
	// - queued/attachment messages (NOT selectable — MessageTypeAttachment)
	// - skill messages with FlagMeta (NOT selectable)
	msgID1 := "aaa11111-1111-1111-1111-111111111111"
	msgID2 := "bbb22222-2222-2222-2222-222222222222"
	msgID3 := "ccc33333-3333-3333-3333-333333333333"
	msgID4 := "ddd44444-4444-4444-4444-444444444444"

	msgs := []types.Message{
		// Normal user message → selectable
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		// Queued message (prompt mode attachment) → NOT selectable
		{ID: msgID2, Role: types.RoleUser, MessageType: types.MessageTypeAttachment, Content: []types.ContentBlock{types.NewTextBlock("123")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("got your message")}, Timestamp: testTime},
		// Skill message (FlagMeta) → NOT selectable
		{ID: msgID3, Role: types.RoleUser, Flags: types.FlagMeta, Content: []types.ContentBlock{types.NewTextBlock("<command-message>test-design</command-message>\nskill content here")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("ok")}, Timestamp: testTime},
		// Another normal user message → selectable
		{ID: msgID4, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("goodbye")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("bye")}, Timestamp: testTime},
	}

	// --- Phase 1: Verify in-memory filter works ---
	a.engine.SetMessages(msgs)
	for i, msg := range msgs {
		if msg.Role != types.RoleUser {
			continue
		}
		selectable := isSelectableUserMessage(msg)
		text := firstTextBlockContent(msg)
		switch text {
		case "hello", "goodbye":
			if !selectable {
				t.Errorf("Phase 1: msg[%d] %q should be selectable", i, text)
			}
		case "123", "<command-message>test-design</command-message>\nskill content here":
			if selectable {
				t.Errorf("Phase 1: msg[%d] %q should NOT be selectable (flags=%v, type=%q)", i, text, msg.Flags, msg.MessageType)
			}
		}
	}

	// --- Phase 2: Persist → Load → Verify filter still works ---
	a.engine.SetMessages(msgs)
	a.engine.PersistNewMessages()
	if a.engine.LastPersistedIdx() != len(msgs) {
		t.Fatalf("expected lastPersistedIdx=%d, got %d", len(msgs), a.engine.LastPersistedIdx())
	}

	// Simulate restart: load from store
	storeMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(storeMsgs) == 0 {
		t.Fatal("expected messages in store after persist")
	}

	// Convert back to engine messages
		loadedMsgs, err := short.StoreMessagesToEngine(storeMsgs)
	if err != nil {
		t.Fatalf("StoreMessagesToEngine: %v", err)
	}

	// Verify MessageType and Flags survived the round-trip
	for i, msg := range loadedMsgs {
		if msg.Role != types.RoleUser {
			continue
		}
		selectable := isSelectableUserMessage(msg)
		text := firstTextBlockContent(msg)
		switch text {
		case "hello", "goodbye":
			if !selectable {
				t.Errorf("Phase 2: loaded msg[%d] %q should be selectable (flags=%v, type=%q)", i, text, msg.Flags, msg.MessageType)
			}
		case "123":
			if selectable {
				t.Errorf("Phase 2: loaded msg[%d] queued message %q should NOT be selectable (flags=%v, type=%q)", i, text, msg.Flags, msg.MessageType)
			}
			if msg.MessageType != types.MessageTypeAttachment {
				t.Errorf("Phase 2: queued msg MessageType = %q, want %q", msg.MessageType, types.MessageTypeAttachment)
			}
		case "<command-message>test-design</command-message>\nskill content here":
			if selectable {
				t.Errorf("Phase 2: loaded msg[%d] skill message should NOT be selectable (flags=%v, type=%q)", i, msg.Flags, msg.MessageType)
			}
			if !msg.HasFlag(types.FlagMeta) {
				t.Errorf("Phase 2: skill msg Flags=%v, want FlagMeta(%v)", msg.Flags, types.FlagMeta)
			}
		}
	}

	// --- Phase 3: handleRewind picker only shows selectable messages ---
	a.engine.SetMessages(loadedMsgs)
	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("Phase 3: expected rewind dialog to open")
	}

	// Extract option labels
	labels := make([]string, len(a.activeDialog.options))
	for i, opt := range a.activeDialog.options {
		labels[i] = opt.Label
	}

	// Should only show "hello" and "goodbye"
	for _, label := range labels {
		if strings.Contains(label, "123") {
			t.Errorf("Phase 3: rewind picker should NOT show queued message '123', got label %q", label)
		}
		if strings.Contains(label, "command-message") {
			t.Errorf("Phase 3: rewind picker should NOT show skill message, got label %q", label)
		}
	}

	// Should have exactly 2 selectable options
	if len(labels) != 2 {
		t.Errorf("Phase 3: expected 2 rewind options, got %d: %v", len(labels), labels)
	}
}
