package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Chain 12: Sub-agent file edits are restored by rewind
//
// Simulates a sub-engine sharing the same Tracker as the main engine.
// The sub-engine creates its own snapshot with a different messageID and
// edits a file. Rewind to a main engine message should restore BOTH files:
//   - Main engine file: restored from target snapshot
//   - Sub-agent file: restored via getBackupFileNameFirstVersion fallback
//
// Production flow (matching engine.go timing):
//   Main Turn 1: MakeSnapshot(msgID1) → TrackEdit(fileA) → write fileA v2
//   Main Turn 2: MakeSnapshot(msgID2)
//   Sub Turn:    MakeSnapshot(subMsgID) → TrackEdit(fileB) → write fileB v2
//   Rewind to msgID1 → both files should be restored to v1
// ---------------------------------------------------------------------------

func TestIntegration_Rewind_SubAgentEditsRestored(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	// File A: edited by main engine
	fileA := filepath.Join(projectDir, "main.go")
	aV1 := []byte("package main\n\nfunc mainV1() {}\n")
	aV2 := []byte("package main\n\nfunc mainV2() {}\n")
	if err := os.WriteFile(fileA, aV1, 0o644); err != nil {
		t.Fatalf("write fileA v1: %v", err)
	}

	// File B: edited by sub-agent
	fileB := filepath.Join(projectDir, "feature.go")
	bV1 := []byte("package main\n\nfunc featureV1() {}\n")
	bV2 := []byte("package main\n\nfunc featureV2() {}\n")
	if err := os.WriteFile(fileB, bV1, 0o644); err != nil {
		t.Fatalf("write fileB v1: %v", err)
	}

	msgID1 := "sub11111-1111-1111-1111-111111111111"
	msgID2 := "sub22222-2222-2222-2222-222222222222"
	subMsgID := "sub33333-3333-3333-3333-333333333333"

	// === Turn 1: Main engine edits fileA ===
	// Production timing: MakeSnapshot(msgID1) → TrackEdit(fileA) → write fileA
	if err := tracker.TrackEdit(fileA); err != nil {
		t.Fatalf("TrackEdit fileA: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot msgID1: %v", err)
	}
	if err := os.WriteFile(fileA, aV2, 0o644); err != nil {
		t.Fatalf("write fileA v2: %v", err)
	}

	// === Turn 2: Main engine → sub-agent edits fileB ===
	// Production timing:
	//   Main engine: MakeSnapshot(msgID2) — records fileA current state (v2)
	//   Sub-engine:  MakeSnapshot(subMsgID) — creates its own snapshot
	//   Sub-engine:  TrackEdit(fileB) — records into mostRecentSnapshot (subMsgID)
	//   Sub-engine:  write fileB v1→v2
	if err := tracker.MakeSnapshot(msgID2); err != nil {
		t.Fatalf("MakeSnapshot msgID2: %v", err)
	}

	// Sub-engine creates its own snapshot (current behavior without !isSubagent guard)
	if err := tracker.MakeSnapshot(subMsgID); err != nil {
		t.Fatalf("MakeSnapshot subMsgID: %v", err)
	}

	// Sub-engine tracks its edit
	if err := tracker.TrackEdit(fileB); err != nil {
		t.Fatalf("TrackEdit fileB: %v", err)
	}

	// Sub-engine writes the file
	if err := os.WriteFile(fileB, bV2, 0o644); err != nil {
		t.Fatalf("write fileB v2: %v", err)
	}

	// Verify pre-rewind state
	if data, err := os.ReadFile(fileA); err != nil || string(data) != string(aV2) {
		t.Fatalf("pre-rewind fileA = %q, want aV2", string(data))
	}
	if data, err := os.ReadFile(fileB); err != nil || string(data) != string(bV2) {
		t.Fatalf("pre-rewind fileB = %q, want bV2", string(data))
	}

	// Log snapshot state for debugging
	state := tracker.State()
	t.Logf("TrackedFiles: %d, Snapshots: %d", len(state.TrackedFiles), len(state.Snapshots))
	for i, s := range state.Snapshots {
		t.Logf("  snapshot[%d] msgID=%q files=%d", i, s.MessageID, len(s.TrackedFileBackups))
		for fp, b := range s.TrackedFileBackups {
			t.Logf("    %s backup=%q v%d", filepath.Base(fp), b.BackupFileName, b.Version)
		}
	}

	// Messages
	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit main.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("use agent to edit feature.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// === Execute: /rewind to turn 1 (msgID1) ===
	_ = a.handleRewind(nil)
	if a.activeDialog == nil {
		t.Fatal("expected message picker dialog")
	}
	a.activeDialog.done = true
	a.activeDialog.cursor = 0 // select first user message (msgID1)
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	// Scope dialog should appear (has file changes)
	if app.activeDialog == nil {
		t.Fatal("expected scope dialog after message selection (has file changes)")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	_ = model.(*App)

	// === Verify: BOTH files should be restored ===

	// fileA: tracked in snapshot[msgID1] → should restore to aV1
	restoredA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("read fileA: %v", err)
	}
	if string(restoredA) != string(aV1) {
		t.Errorf("fileA NOT restored: got %q, want %q", string(restoredA), string(aV1))
	}

	// fileB: tracked in snapshot[subMsgID] → should restore to bV1 via fallback
	restoredB, err := os.ReadFile(fileB)
	if err != nil {
		t.Fatalf("read fileB: %v", err)
	}
	if string(restoredB) != string(bV1) {
		t.Errorf("SUB-AGENT FILE NOT RESTORED: got %q, want %q", string(restoredB), string(bV1))
	}

	// Messages should be rewound to 0
	if len(app.engine.Messages()) != 0 {
		t.Errorf("expected 0 messages after rewind, got %d", len(app.engine.Messages()))
	}
}

// ---------------------------------------------------------------------------
// Chain 13: Sub-agent creates new file → rewind deletes it
//
// Sub-agent creates a file that didn't exist before. Rewind should delete it.
// ---------------------------------------------------------------------------

func TestIntegration_Rewind_SubAgentCreatesFile_Deleted(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	// File A: existing file edited by main engine
	fileA := filepath.Join(projectDir, "main.go")
	aV1 := []byte("package main\n\nfunc mainV1() {}\n")
	aV2 := []byte("package main\n\nfunc mainV2() {}\n")
	if err := os.WriteFile(fileA, aV1, 0o644); err != nil {
		t.Fatalf("write fileA v1: %v", err)
	}

	// File C: NEW file created by sub-agent (doesn't exist before)
	fileC := filepath.Join(projectDir, "new_feature.go")
	cV1 := []byte("package main\n\nfunc newFeature() {}\n")

	msgID1 := "crt11111-1111-1111-1111-111111111111"
	msgID2 := "crt22222-2222-2222-2222-222222222222"
	subMsgID := "crt33333-3333-3333-3333-333333333333"

	// Turn 1: Main engine edits fileA
	if err := tracker.TrackEdit(fileA); err != nil {
		t.Fatalf("TrackEdit fileA: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot msgID1: %v", err)
	}
	if err := os.WriteFile(fileA, aV2, 0o644); err != nil {
		t.Fatalf("write fileA v2: %v", err)
	}

	// Turn 2: Sub-agent creates fileC
	if err := tracker.MakeSnapshot(msgID2); err != nil {
		t.Fatalf("MakeSnapshot msgID2: %v", err)
	}
	// Sub-engine creates its own snapshot
	if err := tracker.MakeSnapshot(subMsgID); err != nil {
		t.Fatalf("MakeSnapshot subMsgID: %v", err)
	}
	// Sub-agent tracks fileC BEFORE it exists → null backup (v1)
	// TrackEdit reads file from disk; file doesn't exist → BackupFileName=""
	if err := tracker.TrackEdit(fileC); err != nil {
		t.Fatalf("TrackEdit fileC (new): %v", err)
	}
	// Sub-agent creates the file
	if err := os.WriteFile(fileC, cV1, 0o644); err != nil {
		t.Fatalf("write fileC (new): %v", err)
	}

	// Verify fileC exists
	if _, err := os.Stat(fileC); os.IsNotExist(err) {
		t.Fatal("fileC should exist before rewind")
	}

	// Messages
	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit main.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("use agent to create new_feature.go")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Rewind to turn 1
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	if app.activeDialog == nil {
		t.Fatal("expected scope dialog")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0 // "Restore code and conversation"
	model, _ = app.onDialogDone(app.activeDialog)
	_ = model.(*App)

	// fileA should be restored to v1
	restoredA, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("read fileA: %v", err)
	}
	if string(restoredA) != string(aV1) {
		t.Errorf("fileA NOT restored: got %q, want %q", string(restoredA), string(aV1))
	}

	// fileC should be DELETED (sub-agent created it, rewind removes it)
	if _, err := os.Stat(fileC); !os.IsNotExist(err) {
		t.Errorf("SUB-AGENT CREATED FILE NOT DELETED after rewind: fileC still exists or stat error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Chain 14: Sub-agent edits same file as main engine → rewind restores correct version
//
// Both main engine and sub-agent edit the same file in the same turn.
// Rewind should restore to the version before EITHER edit.
// ---------------------------------------------------------------------------

func TestIntegration_Rewind_SubAgentEditsSameFile(t *testing.T) {
	a, _, tracker, projectDir := setupRewindIntegration(t)

	fileA := filepath.Join(projectDir, "shared.go")
	v1 := []byte("package main\n\nfunc v1() {}\n")
	v2 := []byte("package main\n\nfunc v2() {}\n")
	v3 := []byte("package main\n\nfunc v3() {}\n")

	if err := os.WriteFile(fileA, v1, 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	msgID1 := "same1111-1111-1111-1111-111111111111"
	msgID2 := "same2222-2222-2222-2222-222222222222"
	subMsgID := "same3333-3333-3333-3333-333333333333"

	// Turn 1: Main engine edits v1→v2
	if err := tracker.TrackEdit(fileA); err != nil {
		t.Fatalf("TrackEdit v1: %v", err)
	}
	if err := tracker.MakeSnapshot(msgID1); err != nil {
		t.Fatalf("MakeSnapshot msgID1: %v", err)
	}
	if err := os.WriteFile(fileA, v2, 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}

	// Turn 2: Sub-agent edits v2→v3
	if err := tracker.MakeSnapshot(msgID2); err != nil {
		t.Fatalf("MakeSnapshot msgID2: %v", err)
	}
	// Sub-engine snapshot
	if err := tracker.MakeSnapshot(subMsgID); err != nil {
		t.Fatalf("MakeSnapshot subMsgID: %v", err)
	}
	// Sub-agent tracks same file — already tracked in snapshot[subMsgID]
	// (dedup: alreadyTracked check should skip creating new v1 backup)
	if err := tracker.TrackEdit(fileA); err != nil {
		t.Fatalf("TrackEdit fileA from sub-agent: %v", err)
	}
	if err := os.WriteFile(fileA, v3, 0o644); err != nil {
		t.Fatalf("write v3: %v", err)
	}

	// Verify current state
	if data, _ := os.ReadFile(fileA); string(data) != string(v3) {
		t.Fatalf("pre-rewind: expected v3, got %q", string(data))
	}

	msgs := []types.Message{
		{ID: msgID1, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("edit shared.go to v2")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
		{ID: msgID2, Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("use agent to edit shared.go to v3")}, Timestamp: testTime},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done")}, Timestamp: testTime},
	}
	a.engine.SetMessages(msgs)

	// Rewind to turn 1 (msgID1)
	_ = a.handleRewind(nil)
	a.activeDialog.done = true
	a.activeDialog.cursor = 0
	model, _ := a.onDialogDone(a.activeDialog)
	app := model.(*App)

	if app.activeDialog == nil {
		t.Fatal("expected scope dialog")
	}
	app.activeDialog.done = true
	app.activeDialog.cursor = 0
	model, _ = app.onDialogDone(app.activeDialog)
	_ = model.(*App)

	// File should be restored to v1 (state at snapshot[msgID1])
	restored, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(restored) != string(v1) {
		t.Errorf("SAME FILE NOT RESTORED CORRECTLY: got %q, want %q (v1)", string(restored), string(v1))
	}
}
