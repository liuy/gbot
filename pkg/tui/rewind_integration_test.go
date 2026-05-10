package tui

import (
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

	a := &App{
		engine:           eng,
		store:            store,
		sessionID:        session.SessionID,
		projectDir:       projectDir,
		lastPersistedIdx: 0,
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
// 路径: fileHistory.RestoreToIndex → store.TruncateMessagesFromIndex
// 可观测输出: file content on disk, store.LoadMessages(), engine.Messages()
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_FileRestoreAndStoreCleanup(t *testing.T) {
	a, store, tracker, projectDir := setupRewindIntegration(t)

	// Create a real file in project dir
	testFile := filepath.Join(projectDir, "main.go")
	originalContent := []byte("package main\n\nfunc main() {}\n")
	if err := os.WriteFile(testFile, originalContent, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Edit the file (simulate Edit tool)
	editedContent := []byte("package main\n\nfunc main() { println(\"hello\") }\n")
	if err := os.WriteFile(testFile, editedContent, 0o644); err != nil {
		t.Fatalf("write edited file: %v", err)
	}

	// Record backup: pre-edit content at turn index 2 (after user message at idx 2)
	if err := tracker.RecordBackup(testFile, originalContent, 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	// Set up multi-turn conversation
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("What is 2+2?")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("4.")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("Edit main.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("Done.")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Persist to store
	a.persistTurn()
	if a.lastPersistedIdx != 4 {
		t.Fatalf("expected lastPersistedIdx=4, got %d", a.lastPersistedIdx)
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

	// Store should be truncated (no messages remain)
	storeMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after rewind: %v", err)
	}
	if len(storeMsgs) != 0 {
		t.Errorf("expected 0 store messages after rewind, got %d", len(storeMsgs))
	}

	// lastPersistedIdx should be updated
	if app.lastPersistedIdx != 0 {
		t.Errorf("expected lastPersistedIdx=0, got %d", app.lastPersistedIdx)
	}
}

// ---------------------------------------------------------------------------
// Chain 3: File Created → Rewind → File Deleted
//
// Tests that files created during rewound turns are deleted on rewind.
// ---------------------------------------------------------------------------
func TestIntegration_Rewind_DeletesCreatedFiles(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	// A file that didn't exist before the edit (new file creation)
	newFile := filepath.Join(projectDir, "new_feature.go")
	newContent := []byte("package main\n\nfunc newFeature() {}\n")
	if err := os.WriteFile(newFile, newContent, 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	// Record backup with nil original (file was created, not edited)
	if err := tracker.RecordBackup(newFile, nil, 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	// Set up conversation
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hello")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("hi")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("create new_feature.go")}, Timestamp: testTime},
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
	a.persistTurn()
	if a.lastPersistedIdx != 6 {
		t.Fatalf("expected lastPersistedIdx=6, got %d", a.lastPersistedIdx)
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

	// --- Recovery: simulate process restart by loading from store ---
	storeMsgsAfter, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages after rewind: %v", err)
	}
	if len(storeMsgsAfter) != 2 {
		t.Fatalf("expected 2 messages in store after rewind (recovery), got %d", len(storeMsgsAfter))
	}

	// Store messages should match engine messages
	if !strings.Contains(storeMsgsAfter[0].Content, "turn 1") {
		t.Errorf("store[0] should contain 'turn 1', got %q", storeMsgsAfter[0].Content)
	}
	if !strings.Contains(storeMsgsAfter[1].Content, "response 1") {
		t.Errorf("store[1] should contain 'response 1', got %q", storeMsgsAfter[1].Content)
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

	// Turn 2: edit v1 → v2
	if err := os.WriteFile(testFile, v2, 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := tracker.RecordBackup(testFile, v1, 2); err != nil {
		t.Fatalf("RecordBackup v1→v2: %v", err)
	}

	// Turn 4: edit v2 → v3
	if err := os.WriteFile(testFile, v3, 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	if err := tracker.RecordBackup(testFile, v2, 4); err != nil {
		t.Fatalf("RecordBackup v2→v3: %v", err)
	}

	// Set up conversation
	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("turn 1")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("response 1")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config to v2")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config to v3")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// --- Execute: /rewind to turn 2 (index 2, keeping messages 0-1) ---
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 1 // indices[1]=2
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Scope dialog appears because fileHistory has backups at turn 2
	if app.activeDialog == nil {
		t.Fatal("expected scope dialog after message selection (has file changes)")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	app = model.(*App)

	// --- Verify: file should be restored to v1 (pre-edit at turn 2) ---
	restored, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(restored) != string(v1) {
		t.Errorf("expected v1 content after rewind to turn 2, got %q", string(restored))
	}

	// Engine should have 2 messages
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
	a.persistTurn()

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

	// Store should be empty
	storeMsgs, err := store.LoadMessages(a.sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(storeMsgs) != 0 {
		t.Errorf("expected 0 store messages, got %d", len(storeMsgs))
	}

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
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit config")}, Timestamp: testTime},
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
	a.SetStore(store, session.SessionID, projectDir, len(msgs))

	// Verify SetStore created a Tracker and wired it to the engine.

	if a.fileHistory == nil {
		t.Fatalf("fileHistory is nil after SetStore")
	}

	// Simulate Edit: modify file on disk + record backup via tracker
	editedContent := []byte("package main\n\nconst Version = 2\n")
	if err := os.WriteFile(testFile, editedContent, 0o644); err != nil {
		t.Fatalf("write edited: %v", err)
	}
	if err := a.fileHistory.RecordBackup(testFile, originalContent, 0); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	// File should still be edited on disk
	current, _ := os.ReadFile(testFile)
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
	// DetectChanges should return BeforeContent with the ORIGINAL content
	// so RecordBackup can save it and RewindTo can restore.
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

	// 4. Record backup and verify rewind restores the file
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	if err := tracker.RecordBackup(testFile, changes[0].BeforeContent, 0); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	restored, err := tracker.RestoreToIndex(0)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
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

	// 4. Record backup with nil content, rewind should delete the file
	tracker := filehistory.NewTracker(filepath.Join(tmp, ".backups"))
	if err := tracker.RecordBackup(newFile, nil, 0); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	restored, err := tracker.RestoreToIndex(0)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}
	if len(restored) != 1 {
		t.Errorf("restored %d files, want 1", len(restored))
	}

	// File should be deleted after rewind
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Errorf("new file should be deleted after rewind, err=%v", err)
	}
}
