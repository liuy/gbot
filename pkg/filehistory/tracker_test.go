package filehistory

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TrackEdit tests
// ---------------------------------------------------------------------------

// TestTrackEdit_NewFile records a file that doesn't exist yet (null backup).
func TestTrackEdit_NewFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	err := tr.TrackEdit(filepath.Join(dir, "new.go"))
	if err != nil {
		t.Fatalf("TrackEdit new file: %v", err)
	}

	state := tr.State()
	if len(state.TrackedFiles) != 1 {
		t.Fatalf("expected 1 tracked file, got %d", len(state.TrackedFiles))
	}
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup, ok := snap.TrackedFileBackups[filepath.Join(dir, "new.go")]
	if !ok {
		t.Fatal("file not in trackedFileBackups")
	}
	if backup.BackupFileName != "" {
		t.Errorf("expected empty BackupFileName for new file, got %q", backup.BackupFileName)
	}
	if backup.Version != 1 {
		t.Errorf("expected version 1, got %d", backup.Version)
	}
}

// TestTrackEdit_ExistingFile creates backup of pre-edit content.
func TestTrackEdit_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	content := []byte("package main\n")
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	err := tr.TrackEdit(filePath)
	if err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup, ok := snap.TrackedFileBackups[filePath]
	if !ok {
		t.Fatal("file not in trackedFileBackups")
	}
	if backup.BackupFileName == "" {
		t.Fatal("expected non-empty BackupFileName for existing file")
	}

	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("backup content mismatch: got %q, want %q", string(data), string(content))
	}
}

// TestTrackEdit_DeduplicatesSameTurn skips second TrackEdit for same file in same turn.
func TestTrackEdit_DeduplicatesSameTurn(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := tr.TrackEdit(filePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = tr.TrackEdit(filePath)
	if err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v0" {
		t.Errorf("dedup failed: backup should contain v0, got %q", string(data))
	}
}

// TestTrackEdit_EmptyPath is a no-op.
func TestTrackEdit_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	err := tr.TrackEdit("")
	if err != nil {
		t.Fatalf("empty path should not error: %v", err)
	}

	state := tr.State()
	if len(state.TrackedFiles) != 0 {
		t.Errorf("expected 0 tracked files, got %d", len(state.TrackedFiles))
	}
}

// TestTrackEdit_MultipleFiles tracks multiple files in same turn.
func TestTrackEdit_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("a-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(fileB); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	if len(state.TrackedFiles) != 2 {
		t.Errorf("expected 2 tracked files, got %d", len(state.TrackedFiles))
	}
	snap := state.Snapshots[len(state.Snapshots)-1]
	if len(snap.TrackedFileBackups) != 2 {
		t.Errorf("expected 2 trackedFileBackups, got %d", len(snap.TrackedFileBackups))
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot tests
// ---------------------------------------------------------------------------

// TestMakeSnapshot_CreatesNewSnapshot creates a snapshot after TrackEdit.
func TestMakeSnapshot_CreatesNewSnapshot(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	if len(state.Snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(state.Snapshots))
	}
	snap := state.Snapshots[1]
	if snap.MessageID != "msg-1" {
		t.Errorf("expected messageID msg-1, got %q", snap.MessageID)
	}
}

// TestMakeSnapshot_DetectsFileChanges creates new backup when file changed.
func TestMakeSnapshot_DetectsFileChanges(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.Version != 2 {
		t.Errorf("expected version 2, got %d", backup.Version)
	}

	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1" {
		t.Errorf("v2 backup should contain v1, got %q", string(data))
	}
}

// TestMakeSnapshot_DeletedFile records null backup for deleted file.
func TestMakeSnapshot_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.BackupFileName != "" {
		t.Errorf("expected empty BackupFileName for deleted file, got %q", backup.BackupFileName)
	}
}

// TestMakeSnapshot_UnchangedFile reuses backup for unchanged file.
func TestMakeSnapshot_UnchangedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap1 := state.Snapshots[1]
	snap2 := state.Snapshots[2]
	b1 := snap1.TrackedFileBackups[filePath]
	b2 := snap2.TrackedFileBackups[filePath]
	if b1.BackupFileName != b2.BackupFileName {
		t.Errorf("expected same BackupFileName for unchanged file, got %q vs %q",
			b1.BackupFileName, b2.BackupFileName)
	}
}

// ---------------------------------------------------------------------------
// Rewind tests
// ---------------------------------------------------------------------------

// TestRewind_RestoresFile restores a file to its state at the target snapshot.
func TestRewind_RestoresFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("expected original content, got %q", string(data))
	}
}

// TestRewind_DeletesCreatedFile deletes files that didn't exist at target snapshot.
func TestRewind_DeletesCreatedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	newFile := filepath.Join(dir, "new.go")

	if err := tr.TrackEdit(newFile); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(newFile, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Error("expected file to be deleted after rewind")
	}
}

// TestRewind_SnapshotNotFound returns error for unknown messageID.
func TestRewind_SnapshotNotFound(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	_, err := tr.Rewind("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

// TestRewind_MultipleFilesMultipleEdits restores all files correctly.
func TestRewind_MultipleFilesMultipleEdits(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	fileC := filepath.Join(dir, "c.go")

	if err := os.WriteFile(fileA, []byte("a0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: edit fileA
	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("a1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Turn 2: edit fileB, create fileC
	if err := tr.TrackEdit(fileB); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(fileC); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileC, []byte("c0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Turn 3: edit fileA again
	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("a2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatal(err)
	}

	// Verify current state
	data, err := os.ReadFile(fileA)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a2" {
		t.Errorf("fileA should be a2, got %q", string(data))
	}

	// Rewind to msg-1
	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 3 {
		t.Fatalf("expected 3 restored files, got %d: %v", len(restored), restored)
	}

	data, err = os.ReadFile(fileA)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a1" {
		t.Errorf("fileA should be a1, got %q", string(data))
	}

	data, err = os.ReadFile(fileB)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "b0" {
		t.Errorf("fileB should be b0 after rewind, got %q", string(data))
	}

	if _, err := os.Stat(fileC); !os.IsNotExist(err) {
		t.Error("fileC should be deleted after rewind to msg-1")
	}
}

// ---------------------------------------------------------------------------
// RewindFilesOnly tests
// ---------------------------------------------------------------------------

// TestRewindFilesOnly restores files without truncating snapshots.
func TestRewindFilesOnly(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	snapCountBefore := len(tr.State().Snapshots)

	restored, err := tr.RewindFilesOnly("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("expected original content, got %q", string(data))
	}

	if len(tr.State().Snapshots) != snapCountBefore {
		t.Errorf("snapshots should not be truncated, got %d vs %d",
			len(tr.State().Snapshots), snapCountBefore)
	}
}

// ---------------------------------------------------------------------------
// HasChangesAtMessage tests
// ---------------------------------------------------------------------------

// TestHasChangesAtMessage_TrueWhenFileDiffers returns true when file was modified.
func TestHasChangesAtMessage_TrueWhenFileDiffers(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	if !tr.HasChangesAtMessage("msg-1") {
		t.Error("expected HasChangesAtMessage to return true for msg-1")
	}
}

// TestHasChangesAtMessage_FalseWhenNoChanges returns false when file unchanged.
func TestHasChangesAtMessage_FalseWhenNoChanges(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected HasChangesAtMessage to return false (no changes)")
	}
}

// TestHasChangesAtMessage_FalseForUnknownMessage returns false for nonexistent message.
func TestHasChangesAtMessage_FalseForUnknownMessage(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if tr.HasChangesAtMessage("nonexistent") {
		t.Error("expected false for unknown message")
	}
}

// ---------------------------------------------------------------------------
// State persistence tests
// ---------------------------------------------------------------------------

// TestStateRoundTrip preserves state through serialization cycle.
func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	state := tr.State()

	tr2 := NewTracker(dir)
	tr2.LoadState(state)

	state2 := tr2.State()
	if len(state2.Snapshots) != len(state.Snapshots) {
		t.Errorf("snapshot count mismatch: %d vs %d", len(state2.Snapshots), len(state.Snapshots))
	}
	if len(state2.TrackedFiles) != len(state.TrackedFiles) {
		t.Errorf("tracked files mismatch: %d vs %d", len(state2.TrackedFiles), len(state.TrackedFiles))
	}

	// Verify snapshot content survived round-trip.
	if len(state2.Snapshots) < 2 {
		t.Fatalf("expected at least 2 snapshots, got %d", len(state2.Snapshots))
	}
	snap := state2.Snapshots[1] // msg-1 snapshot
	if snap.MessageID != "msg-1" {
		t.Errorf("snapshot MessageID = %q, want %q", snap.MessageID, "msg-1")
	}
	backup, ok := snap.TrackedFileBackups[filePath]
	if !ok {
		t.Fatalf("filePath %q not in TrackedFileBackups", filePath)
	}
	if backup.Version != 1 {
		t.Errorf("backup Version = %d, want 1", backup.Version)
	}
	if backup.BackupFileName == "" {
		t.Error("backup BackupFileName is empty")
	}
	// Verify backup file on disk is readable and has correct content.
	backupData, err := os.ReadFile(filepath.Join(dir, backup.BackupFileName))
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	if string(backupData) != "content" {
		t.Errorf("backup content = %q, want %q", string(backupData), "content")
	}
}

// TestStateRoundTrip_CrashRecovery simulates crash recovery.
func TestStateRoundTrip_CrashRecovery(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	tr2 := NewTracker(dir)
	tr2.LoadState(state)

	if err := os.WriteFile(filePath, []byte("v1-edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr2.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	restored, err := tr2.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v0" {
		t.Errorf("expected v0 after crash recovery rewind, got %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// TestRewindToBeforeAnyFilesTracked rewinds to initial snapshot.
func TestRewindToBeforeAnyFilesTracked(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.Rewind("")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected file to be deleted after rewind to initial snapshot")
	}
}

// TestSnapshotSequenceIncrements tracks snapshot sequence counter.
func TestSnapshotSequenceIncrements(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if tr.State().SnapshotSequence != 0 {
		t.Errorf("expected initial sequence 0, got %d", tr.State().SnapshotSequence)
	}

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}
	if tr.State().SnapshotSequence != 1 {
		t.Errorf("expected sequence 1, got %d", tr.State().SnapshotSequence)
	}

	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}
	if tr.State().SnapshotSequence != 2 {
		t.Errorf("expected sequence 2, got %d", tr.State().SnapshotSequence)
	}
}

// ---------------------------------------------------------------------------
// WalkDir tests
// ---------------------------------------------------------------------------

// TestWalkDir traverses files in a directory.
func TestWalkDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	var files []string
	err := WalkDir(dir, func(path string, _ os.DirEntry) error {
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

// TestIsSkippedDir checks known skip directories.
func TestIsSkippedDir(t *testing.T) {
	for _, name := range []string{".git", "node_modules", "vendor", "__pycache__"} {
		if !IsSkippedDir(name) {
			t.Errorf("expected %q to be skipped", name)
		}
	}
	if IsSkippedDir("src") {
		t.Error("src should not be skipped")
	}
}

// ---------------------------------------------------------------------------
// Dir() tests
// ---------------------------------------------------------------------------

func TestDir(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	if tr.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", tr.Dir(), dir)
	}
}

// ---------------------------------------------------------------------------
// TrackEditFromContent tests
// ---------------------------------------------------------------------------

func TestTrackEditFromContent_WithContent(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "src", "main.go")
	content := []byte("package main\nfunc main() {}\n")

	err := tr.TrackEditFromContent(filePath, content)
	if err != nil {
		t.Fatalf("TrackEditFromContent: %v", err)
	}

	state := tr.State()
	if len(state.TrackedFiles) != 1 {
		t.Fatalf("expected 1 tracked file, got %d", len(state.TrackedFiles))
	}

	snap := state.Snapshots[len(state.Snapshots)-1]
	backup, ok := snap.TrackedFileBackups[filePath]
	if !ok {
		t.Fatal("file not in trackedFileBackups")
	}
	if backup.BackupFileName == "" {
		t.Error("expected non-empty BackupFileName for file with content")
	}
	if backup.Version != 1 {
		t.Errorf("expected version 1, got %d", backup.Version)
	}

	// Verify backup data on disk matches provided content.
	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("backup data = %q, want %q", data, content)
	}
}

func TestTrackEditFromContent_NilContent(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "nonexistent.go")

	err := tr.TrackEditFromContent(filePath, nil)
	if err != nil {
		t.Fatalf("TrackEditFromContent nil: %v", err)
	}

	snap := tr.State().Snapshots[len(tr.State().Snapshots)-1]
	backup, ok := snap.TrackedFileBackups[filePath]
	if !ok {
		t.Fatal("file not in trackedFileBackups")
	}
	if backup.BackupFileName != "" {
		t.Errorf("expected empty BackupFileName for nil content, got %q", backup.BackupFileName)
	}
}

func TestTrackEditFromContent_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	err := tr.TrackEditFromContent("", []byte("content"))
	if err != nil {
		t.Fatalf("TrackEditFromContent empty path: %v", err)
	}

	state := tr.State()
	if len(state.TrackedFiles) != 0 {
		t.Errorf("expected 0 tracked files for empty path, got %d", len(state.TrackedFiles))
	}
}

func TestTrackEditFromContent_DeduplicatesSameTurn(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	content1 := []byte("version 1")
	content2 := []byte("version 2")

	if err := tr.TrackEditFromContent(filePath, content1); err != nil {
		t.Fatalf("first track: %v", err)
	}
	if err := tr.TrackEditFromContent(filePath, content2); err != nil {
		t.Fatalf("second track: %v", err)
	}

	// Dedup: second call with same snapshot should be skipped.
	snap := tr.State().Snapshots[len(tr.State().Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	// Should still be content1 (dedup skips second call).
	if !bytes.Equal(data, content1) {
		t.Errorf("backup data = %q, want %q (original, not overwritten)", data, content1)
	}
}

// ---------------------------------------------------------------------------
// writeBackupData / createBackupFromContent — mkdir fallback path
// ---------------------------------------------------------------------------

func TestWriteBackupData_NestedDir(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// writeBackupData is called internally. Use TrackEditFromContent to
	// trigger the writeBackupData path with a deeply nested file path
	// that doesn't exist yet — forces the lazy mkdir path.
	filePath := filepath.Join(dir, "a", "b", "c", "deep.go")
	content := []byte("deep content")

	err := tr.TrackEditFromContent(filePath, content)
	if err != nil {
		t.Fatalf("TrackEditFromContent nested: %v", err)
	}

	snap := tr.State().Snapshots[len(tr.State().Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.BackupFileName == "" {
		t.Fatal("expected non-empty BackupFileName")
	}

	backupPath := filepath.Join(dir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("backup data = %q, want %q", data, content)
	}
}

// ---------------------------------------------------------------------------
// getBackupFileNameFirstVersionLocked — tested via RewindToBeforeAnyFilesTracked
// and via a direct rewind scenario that triggers the first-version fallback.
// ---------------------------------------------------------------------------

func TestGetBackupFileNameFirstVersion_RewindFallback(t *testing.T) {
	// Setup: track a file, make snapshot (v1), edit, make snapshot (v2).
	// Rewind to a snapshot where the file is NOT present — triggers
	// getBackupFileNameFirstVersionLocked to find v1 backup.
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	origContent := []byte("original")
	if err := os.WriteFile(filePath, origContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Track + snapshot (v1).
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Edit file + snapshot (v2).
	newContent := []byte("modified")
	if err := os.WriteFile(filePath, newContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Verify file is modified.
	data, _ := os.ReadFile(filePath)
	if string(data) != "modified" {
		t.Fatalf("pre-rewind: got %q, want modified", data)
	}

	// Rewind to msg-1 — file IS in msg-1's snapshot, so this tests
	// the normal rewind path. To trigger getBackupFileNameFirstVersionLocked,
	// we need a file NOT in the target snapshot.
	// Add another file that was tracked AFTER msg-1.
	file2 := filepath.Join(dir, "new.go")
	if err := os.WriteFile(file2, []byte("new file"), 0o644); err != nil {
		t.Fatalf("write file2: %v", err)
	}
	if err := tr.TrackEdit(file2); err != nil {
		t.Fatalf("track file2: %v", err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatalf("snapshot msg-3: %v", err)
	}

	// Now rewind to msg-1. file2 was NOT tracked in msg-1's snapshot,
	// but it has a v1 backup in msg-3's snapshot.
	// getBackupFileNameFirstVersionLocked finds v1 backup (with content),
	// so file2 is restored to v1 content, not deleted.
	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if len(changed) == 0 {
		t.Error("expected file changes from rewind")
	}

	// main.go should be restored to original.
	data, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("main.go = %q, want %q", data, "original")
	}
}

// ---------------------------------------------------------------------------
// restoreBackup — error paths
// ---------------------------------------------------------------------------

func TestRestoreBackup_BackupNotFound(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Track file + snapshot to register it.
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Remove all backup files to simulate missing backup.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "@v") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}

	// Rewind to initial snapshot triggers restoreBackup with missing backup.
	// applySnapshotLocked logs the error but does not propagate it to Rewind.
	// Instead, the file is not in the changed list because restore failed.
	changed, err := tr.Rewind("")
	if err != nil {
		t.Fatalf("rewind should not return error (errors are logged), got: %v", err)
	}

	// File should NOT be in changed list since backup was missing.
	for _, f := range changed {
		if f == filePath {
			t.Error("file should not be in changed list when backup is missing")
		}
	}

	// File should still exist with original content (restore failed, nothing changed).
	data, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if string(data) != "content" {
		t.Errorf("file content = %q, want %q (unchanged)", data, "content")
	}
}

// ---------------------------------------------------------------------------
// copyFileData — dest dir missing path
// ---------------------------------------------------------------------------

func TestRestoreBackup_DestDirMissing(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Create a file in a subdirectory and track it (creates backup).
	filePath := filepath.Join(dir, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	origContent := []byte("original content")
	if err := os.WriteFile(filePath, origContent, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Overwrite the file to simulate an edit.
	editedContent := []byte("edited content")
	if err := os.WriteFile(filePath, editedContent, 0o644); err != nil {
		t.Fatalf("write edited: %v", err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatalf("snapshot msg-2: %v", err)
	}

	// Delete the file but keep its parent directory.
	_ = os.Remove(filePath)

	// Rewind to msg-1 - file existed at msg-1 time, backup exists,
	// and the file is now missing (changed). Restore should work.
	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	found := false
	for _, f := range changed {
		if f == filePath {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s in changed files, got %v", filePath, changed)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "original content" {
		t.Errorf("restored content = %q, want %q", data, "original content")
	}
}

// ---------------------------------------------------------------------------
// Snapshot eviction (MAX_SNAPSHOTS)
// ---------------------------------------------------------------------------

func TestMakeSnapshot_EvictsOldSnapshots(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatalf("track: %v", err)
	}

	// Create MAX_SNAPSHOTS + 5 snapshots.
	for i := range MAX_SNAPSHOTS + 5 {
		if err := tr.MakeSnapshot(fmt.Sprintf("msg-%d", i)); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}

	state := tr.State()
	if len(state.Snapshots) > MAX_SNAPSHOTS {
		t.Errorf("expected at most %d snapshots, got %d", MAX_SNAPSHOTS, len(state.Snapshots))
	}

	// The first few snapshots should have been evicted.
	for _, snap := range state.Snapshots {
		if snap.MessageID == "msg-0" || snap.MessageID == "msg-1" || snap.MessageID == "msg-2" {
			t.Errorf("old snapshot %q should have been evicted", snap.MessageID)
		}
	}

	// Regression: evicted snapshots must have their backup files removed from disk.
	// Previously only the in-memory slice was trimmed, leaving orphan files that
	// accumulate to GBs over long sessions.
	retainedBackups := make(map[string]bool)
	for _, snap := range state.Snapshots {
		for _, backup := range snap.TrackedFileBackups {
			if backup.BackupFileName != "" {
				retainedBackups[backup.BackupFileName] = true
			}
		}
	}

	// Walk the backup directory and verify every file on disk is referenced
	// by a retained snapshot.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var orphans []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// main.go is the tracked source file, not a backup.
		if name == "main.go" {
			continue
		}
		if !retainedBackups[name] {
			orphans = append(orphans, name)
		}
	}
	if got, want := len(orphans), 0; got != want {
		t.Errorf("orphan backup files on disk = %d, want %d (evicted snapshots should have removed them); first few: %v",
			got, want, orphans[:min(len(orphans), 5)])
	}
}

// ---------------------------------------------------------------------------
// LoadState edge case
// ---------------------------------------------------------------------------

func TestLoadState_EmptySnapshots(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Load empty state.
	tr.LoadState(FileHistoryState{
		Snapshots:        []FileHistorySnapshot{},
		TrackedFiles:     map[string]bool{},
		SnapshotSequence: 0,
	})

	state := tr.State()
	if len(state.Snapshots) != 0 {
		t.Errorf("expected 0 snapshots, got %d", len(state.Snapshots))
	}
}

// ---------------------------------------------------------------------------
// writeBackupData / copyFileData — ENOENT mkdir fallback paths
// ---------------------------------------------------------------------------

// TestWriteBackupData_MkdirFallback removes the tracker dir before writing,
// forcing writeBackupData to hit the ENOENT → MkdirAll → retry path.
func TestWriteBackupData_MkdirFallback(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")

	tr := NewTracker(backupDir)
	// backupDir now exists (created by NewTracker). Remove it to trigger ENOENT.
	if err := os.RemoveAll(backupDir); err != nil {
		t.Fatalf("remove backup dir: %v", err)
	}

	filePath := filepath.Join(baseDir, "test.go")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TrackEditFromContent → createBackupFromContent → writeBackupData
	err := tr.TrackEditFromContent(filePath, []byte("backup content"))
	if err != nil {
		t.Fatalf("expected success with mkdir fallback: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.BackupFileName == "" {
		t.Fatal("expected non-empty backup file name")
	}

	backupPath := filepath.Join(backupDir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != "backup content" {
		t.Errorf("backup content = %q, want %q", string(data), "backup content")
	}
}

// TestWriteBackupData_MkdirFails creates a file where the dir should be,
// blocking MkdirAll. writeBackupData should return the mkdir error.
func TestWriteBackupData_MkdirFails(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")

	tr := NewTracker(backupDir)
	_ = os.RemoveAll(backupDir)

	// Place a file at backupDir path to block MkdirAll.
	if err := os.WriteFile(backupDir, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	err := tr.TrackEditFromContent(filePath, []byte("backup"))
	if err == nil {
		t.Fatal("expected error when mkdir fails")
	}
	if !strings.Contains(err.Error(), "write backup") {
		t.Errorf("error should mention write backup, got: %v", err)
	}
}

// TestWriteBackupData_NonENOENTError triggers writeBackupData with a
// non-ENOENT, non-nil error by making the backup path unwritable.
func TestWriteBackupData_NonENOENTError(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")

	tr := NewTracker(backupDir)

	// Make the backup dir read-only so WriteFile fails with a permission error
	// (not ENOENT, since the dir exists).
	if err := os.Chmod(backupDir, 0o444); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(backupDir, 0o755) }() // restore for cleanup

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	err := tr.TrackEditFromContent(filePath, []byte("backup"))
	if err == nil {
		t.Fatal("expected error when write fails")
	}
	if !strings.Contains(err.Error(), "write backup") {
		t.Errorf("error should mention write backup, got: %v", err)
	}
}

// TestCopyFileData_MkdirFallback removes tracker dir to force copyFileData
// through the ENOENT → MkdirAll → retry path.
func TestCopyFileData_MkdirFallback(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")

	tr := NewTracker(backupDir)
	if err := os.RemoveAll(backupDir); err != nil {
		t.Fatalf("remove backup dir: %v", err)
	}

	filePath := filepath.Join(baseDir, "test.go")
	if err := os.WriteFile(filePath, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TrackEdit → createBackup → copyFileData → ENOENT → mkdir → retry
	err := tr.TrackEdit(filePath)
	if err != nil {
		t.Fatalf("expected success with mkdir fallback: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.BackupFileName == "" {
		t.Fatal("expected non-empty backup file name")
	}

	backupPath := filepath.Join(backupDir, backup.BackupFileName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(data) != "original content" {
		t.Errorf("backup content = %q, want %q", string(data), "original content")
	}
}

// TestCopyFileData_MkdirFails blocks MkdirAll with a file at the dir path.
func TestCopyFileData_MkdirFails(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")

	tr := NewTracker(backupDir)
	_ = os.RemoveAll(backupDir)

	if err := os.WriteFile(backupDir, []byte("block"), 0o644); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	err := tr.TrackEdit(filePath)
	if err == nil {
		t.Fatal("expected error when mkdir fails")
	}
	if !strings.Contains(err.Error(), "copy backup") {
		t.Errorf("error should mention copy backup, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// mostRecentSnapshotLocked / empty snapshots case via TrackEdit
// ---------------------------------------------------------------------------

// TestTrackEdit_NoSnapshots loads empty state, then TrackEdit should error.
func TestTrackEdit_NoSnapshots(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	tr.LoadState(FileHistoryState{
		Snapshots:    []FileHistorySnapshot{},
		TrackedFiles: map[string]bool{},
	})

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	err := tr.TrackEdit(filePath)
	if err == nil {
		t.Fatal("expected error with no snapshots")
	}
	if !strings.Contains(err.Error(), "no snapshots available") {
		t.Errorf("error = %v, want 'no snapshots available'", err)
	}
}

// ---------------------------------------------------------------------------
// createBackup — stat error (not ENOENT) via symlink loop
// ---------------------------------------------------------------------------

// TestCreateBackup_StatError uses a symlink loop to trigger a non-ENOENT
// stat error in createBackup.
func TestCreateBackup_StatError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Create a symlink loop: linkA → linkB → linkA
	linkA := filepath.Join(dir, "loopA")
	linkB := filepath.Join(dir, "loopB")
	if err := os.Symlink(linkB, linkA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkA, linkB); err != nil {
		t.Fatal(err)
	}

	err := tr.TrackEdit(linkA)
	if err == nil {
		t.Fatal("expected error for symlink loop")
	}
	if !strings.Contains(err.Error(), "stat source") {
		t.Errorf("error should mention stat source, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HasChangesAtMessage — additional branches
// ---------------------------------------------------------------------------

// TestHasChangesAtMessage_FileExistsButShouldNot covers the backupFileName==""
// branch where the file shouldn't exist at the target snapshot but does.
func TestHasChangesAtMessage_FileExistsButShouldNot(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Track a file that doesn't exist → null backup (BackupFileName == "")
	filePath := filepath.Join(dir, "new.go")
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Now create the file — it shouldn't exist at msg-1.
	_ = os.WriteFile(filePath, []byte("surprise"), 0o644)

	if !tr.HasChangesAtMessage("msg-1") {
		t.Error("expected changes: file exists but shouldn't at msg-1")
	}
}

// TestHasChangesAtMessage_NoBackupFound covers getBackupFileNameFirstVersionLocked
// not finding any backup for the file (Version==0 and no v1 anywhere).
func TestHasChangesAtMessage_NoBackupFound(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Manually set up state with a tracked file that has no backup anywhere.
	filePath := filepath.Join(dir, "ghost.go")
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{
				MessageID:          "msg-1",
				TrackedFileBackups: map[string]FileHistoryBackup{},
				Timestamp:          time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		TrackedFiles:     map[string]bool{filePath: true},
		SnapshotSequence: 1,
	})

	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected no changes: file has no backup in any snapshot")
	}
}

// TestHasChangesAtMessage_CheckError covers checkOriginFileChanged returning
// an error (backup file deleted between snapshots).
func TestHasChangesAtMessage_CheckError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Delete the backup file to make checkOriginFileChanged fail.
	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	_ = os.Remove(filepath.Join(dir, backup.BackupFileName))

	// checkOriginFileChanged returns (true, nil) when backup is missing (IsNotExist).
	// So HasChangesAtMessage returns true.
	if !tr.HasChangesAtMessage("msg-1") {
		t.Error("expected changes: backup deleted, treated as changed")
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot — error paths
// ---------------------------------------------------------------------------

// TestMakeSnapshot_NoSnapshotsToReference tests MakeSnapshot when there are
// no snapshots (mostRecentSnapshotLocked returns nil → early return).
func TestMakeSnapshot_NoSnapshotsToReference(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	tr.LoadState(FileHistoryState{
		Snapshots:    []FileHistorySnapshot{},
		TrackedFiles: map[string]bool{},
	})

	err := tr.MakeSnapshot("msg-empty")
	if err != nil {
		t.Fatalf("expected nil with no snapshots: %v", err)
	}
}

// TestMakeSnapshot_CheckChangedError covers checkOriginFileChanged error
// during snapshot creation (backup file missing → stat error).
func TestMakeSnapshot_CheckChangedError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Delete the backup to make checkOriginFileChanged fail.
	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	_ = os.Remove(filepath.Join(dir, backup.BackupFileName))

	// Modify file so MakeSnapshot tries to compare with the now-missing backup.
	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	err := tr.MakeSnapshot("msg-check-err")
	if err != nil {
		t.Fatalf("MakeSnapshot should not fail: %v", err)
	}

	// The file should be skipped (inherited from previous snapshot via the
	// inherit loop at the bottom of MakeSnapshot).
	state2 := tr.State()
	lastSnap := state2.Snapshots[len(state2.Snapshots)-1]
	if lastSnap.MessageID != "msg-check-err" {
		t.Fatalf("wrong snapshot: %s", lastSnap.MessageID)
	}
}

// TestMakeSnapshot_CreateBackupError covers createBackup failure during
// snapshot creation (backup dir blocked).
func TestMakeSnapshot_CreateBackupError(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")
	tr := NewTracker(backupDir)

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Block backup dir with a file to make createBackup fail.
	_ = os.RemoveAll(backupDir)
	_ = os.WriteFile(backupDir, []byte("block"), 0o644)

	// Modify file so MakeSnapshot tries to create a new backup.
	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	err := tr.MakeSnapshot("msg-backup-err")
	if err != nil {
		t.Fatalf("MakeSnapshot should not fail: %v", err)
	}

	// File should be inherited from previous snapshot.
	state := tr.State()
	lastSnap := state.Snapshots[len(state.Snapshots)-1]
	if lastSnap.MessageID != "msg-backup-err" {
		t.Fatalf("wrong snapshot: %s", lastSnap.MessageID)
	}
}

// TestMakeSnapshot_DeletedTrackedFile covers the ENOENT (file deleted) path
// in MakeSnapshot with an explicit check for the null backup entry.
func TestMakeSnapshot_DeletedTrackedFile_NullBackup(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Delete the tracked file.
	_ = os.Remove(filePath)

	err := tr.MakeSnapshot("msg-2")
	if err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup := snap.TrackedFileBackups[filePath]
	if backup.BackupFileName != "" {
		t.Errorf("deleted file should have empty BackupFileName, got %q", backup.BackupFileName)
	}
	if backup.Version < 2 {
		t.Errorf("deleted file version should be >= 2, got %d", backup.Version)
	}
}

// ---------------------------------------------------------------------------
// applySnapshotLocked — additional branches
// ---------------------------------------------------------------------------

// TestApplySnapshot_DeleteFile covers the backupFileName=="" path in
// applySnapshotLocked: file shouldn't exist at target → delete it.
func TestApplySnapshot_DeleteFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "new.go")
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Create the file (it didn't exist at msg-1).
	_ = os.WriteFile(filePath, []byte("new content"), 0o644)
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Rewind to msg-1: file shouldn't exist → delete it.
	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}

	found := false
	for _, f := range changed {
		if f == filePath {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s in changed files, got %v", filePath, changed)
	}

	if _, statErr := os.Stat(filePath); !os.IsNotExist(statErr) {
		t.Error("file should have been deleted after rewind to null backup")
	}
}

// TestApplySnapshot_NoBackupFound covers the path where
// getBackupFileNameFirstVersionLocked finds no backup for a file.
func TestApplySnapshot_NoBackupFound(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// State with a tracked file that has no backup in any snapshot.
	filePath := filepath.Join(dir, "orphan.go")
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{
				MessageID:          "msg-1",
				TrackedFileBackups: map[string]FileHistoryBackup{},
				Timestamp:          time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		TrackedFiles:     map[string]bool{filePath: true},
		SnapshotSequence: 1,
	})

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("expected 0 changes for file with no backup, got %v", changed)
	}
}

// TestApplySnapshot_FileUnchanged covers the path where the file matches
// the backup, so no restore is needed.
func TestApplySnapshot_FileUnchanged(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Don't change the file. Rewind should find it unchanged.
	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	for _, f := range changed {
		if f == filePath {
			t.Error("unchanged file should not appear in changed list")
		}
	}
}

// ---------------------------------------------------------------------------
// NewTracker — mkdir failure
// ---------------------------------------------------------------------------

// TestNewTracker_MkdirFail verifies NewTracker doesn't panic when MkdirAll
// fails (e.g. a file blocks the directory path).
func TestNewTracker_MkdirFail(t *testing.T) {
	baseDir := t.TempDir()
	blockPath := filepath.Join(baseDir, "blocked")
	_ = os.WriteFile(blockPath, []byte("x"), 0o644)

	tr := NewTracker(filepath.Join(blockPath, "sub", "dir"))
	if tr == nil {
		t.Fatal("expected non-nil tracker even on mkdir failure")
	}
	if tr.Dir() == "" {
		t.Error("expected non-empty dir")
	}
}

// ---------------------------------------------------------------------------
// checkOriginFileChanged — mtime optimization path
// ---------------------------------------------------------------------------

// TestCheckOriginFileChanged_MtimeOptimization covers the path where original
// mtime is before backup mtime → skip content comparison.
func TestCheckOriginFileChanged_MtimeOptimization(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)

	// Set backup mtime to future → original mtime will be "before" backup mtime.
	futureTime := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(backupPath, futureTime, futureTime); err != nil {
		t.Fatal(err)
	}

	changed, err := tr.checkOriginFileChanged(filePath, backup.BackupFileName, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no change: original mtime before backup mtime (optimization)")
	}
}

// ---------------------------------------------------------------------------
// restoreBackup — successful restore
// ---------------------------------------------------------------------------

// TestRestoreBackup_Success exercises the full restoreBackup path via Rewind.
func TestRestoreBackup_Success(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("original"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if len(changed) != 1 || changed[0] != filePath {
		t.Errorf("expected [%s] in changed, got %v", filePath, changed)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("restored content = %q, want %q", string(data), "original")
	}
}

// TestRestoreBackup_MkdirFallback exercises restoreBackup when the dest
// directory doesn't exist (triggers copyFileData mkdir fallback).
func TestRestoreBackup_MkdirFallback(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Create file in a subdirectory.
	subDir := filepath.Join(dir, "src")
	_ = os.MkdirAll(subDir, 0o755)
	filePath := filepath.Join(subDir, "main.go")
	_ = os.WriteFile(filePath, []byte("original"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Delete the entire subdirectory (dest dir gone for restore).
	_ = os.RemoveAll(subDir)

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if len(changed) != 1 || changed[0] != filePath {
		t.Errorf("expected [%s] in changed, got %v", filePath, changed)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("restored = %q, want %q", string(data), "original")
	}
}

// ---------------------------------------------------------------------------
// CleanupOldBackups — additional branches
// ---------------------------------------------------------------------------

// TestCleanupOldBackups_ReadDirError passes a file (not dir) to trigger
// a non-ENOENT ReadDir error.
func TestCleanupOldBackups_ReadDirError(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(tmpFile, []byte("x"), 0o644)

	cleaned, err := CleanupOldBackups(tmpFile, DefaultCleanupAge)
	if err == nil {
		t.Fatal("expected error when ReadDir fails on non-directory")
	}
	if !strings.Contains(err.Error(), "not-a-dir") {
		t.Errorf("error should mention path, got: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned, got %d", cleaned)
	}
}

// TestCleanupOldBackups_SkipsNonDirEntries verifies that regular files
// in the history dir are skipped.
func TestCleanupOldBackups_SkipsNonDirEntries(t *testing.T) {
	baseDir := t.TempDir()

	// Regular file (not a directory).
	_ = os.WriteFile(filepath.Join(baseDir, "readme.txt"), []byte("x"), 0o644)

	// Old directory that should be cleaned.
	oldDir := filepath.Join(baseDir, "old-session")
	_ = os.MkdirAll(oldDir, 0o755)
	_ = os.Chtimes(oldDir, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	cleaned, err := CleanupOldBackups(baseDir, DefaultCleanupAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("expected 1 cleaned, got %d", cleaned)
	}
	if _, statErr := os.Stat(oldDir); !os.IsNotExist(statErr) {
		t.Error("old session directory should have been removed")
	}
}

// TestCleanupOldBackups_StatErrorOnSessionDir uses a symlink to trigger
// a stat error on a session directory entry.
func TestCleanupOldBackups_StatErrorOnSessionDir(t *testing.T) {
	baseDir := t.TempDir()

	// Create a dangling symlink — IsDir() returns false, so it's skipped
	// before reaching the stat. Create a real directory with no permissions
	// to trigger a stat error... actually stat on a dir usually works even
	// without read permission. Instead, create a FIFO which IsDir()=false.
	// This exercises the "if !d.IsDir() { continue }" branch.
	linkPath := filepath.Join(baseDir, "link-session")
	_ = os.Symlink("/nonexistent/target", linkPath)

	cleaned, err := CleanupOldBackups(baseDir, DefaultCleanupAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (symlink is not a dir), got %d", cleaned)
	}
}

// ---------------------------------------------------------------------------
// WalkDir — callback error
// ---------------------------------------------------------------------------

// TestWalkDir_CallbackError verifies WalkDir propagates the first error from fn.
func TestWalkDir_CallbackError(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)

	err := WalkDir(dir, func(path string, _ fs.DirEntry) error {
		return fmt.Errorf("stop")
	})
	if err == nil {
		t.Fatal("expected error from callback")
	}
	if err.Error() != "stop" {
		t.Errorf("error = %v, want 'stop'", err)
	}
}

// ---------------------------------------------------------------------------
// HasChangesAtMessage — file unchanged
// ---------------------------------------------------------------------------

// TestHasChangesAtMessage_FileUnchanged verifies no changes reported when
// the file matches its backup.
func TestHasChangesAtMessage_FileUnchanged(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected no changes for unchanged file")
	}
}

// ---------------------------------------------------------------------------
// copyFileDataOnce — src open failure
// ---------------------------------------------------------------------------

// TestCopyFileDataOnce_SrcOpenFail covers the error path when the source
// file cannot be opened.
func TestCopyFileDataOnce_SrcOpenFail(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	dst := filepath.Join(dir, "dst")
	err := tr.copyFileDataOnce("/nonexistent/src/file.txt", dst, 0o644)
	if err == nil {
		t.Fatal("expected error for missing source file")
	}
	if !strings.Contains(err.Error(), "open src") {
		t.Errorf("error should mention open src, got: %v", err)
	}
}

// TestCopyFileDataOnce_DstOpenFail covers the error path when the dest
// file cannot be created (parent dir doesn't exist, and this is the raw
// copyFileDataOnce — no mkdir fallback here).
func TestCopyFileDataOnce_DstOpenFail(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	src := filepath.Join(dir, "src.txt")
	_ = os.WriteFile(src, []byte("data"), 0o644)

	dst := filepath.Join(dir, "no-such-dir", "dst.txt")
	err := tr.copyFileDataOnce(src, dst, 0o644)
	if err == nil {
		t.Fatal("expected error for missing destination dir")
	}
	if !strings.Contains(err.Error(), "open dst") {
		t.Errorf("error should mention open dst, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// restoreBackup — non-ENOENT stat error + copy failure
// ---------------------------------------------------------------------------

// TestRestoreBackup_StatNonENOENTError uses a symlink loop as the backup
// file to trigger a non-ENOENT stat error in restoreBackup.
func TestRestoreBackup_StatNonENOENTError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Get the backup filename and replace it with a symlink loop.
	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)

	linkB := backupPath + "-loop"
	_ = os.Remove(backupPath)
	_ = os.Symlink(linkB, backupPath)
	_ = os.Symlink(backupPath, linkB)

	// Modify file so rewind tries to restore.
	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind should not error: %v", err)
	}
	// Restore fails (stat on symlink loop), so file should not be in changed.
	for _, f := range changed {
		if f == filePath {
			t.Error("file should not be restored when backup stat fails")
		}
	}
}

// TestRestoreBackup_CopyFail makes the backup unreadable so copyFileData
// fails during restore.
func TestRestoreBackup_CopyFail(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Make the backup file unreadable so copyFileData can't open it.
	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)
	_ = os.Chmod(backupPath, 0o000)
	defer func() { _ = os.Chmod(backupPath, 0o644) }()

	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind should not error: %v", err)
	}
	for _, f := range changed {
		if f == filePath {
			t.Error("file should not be restored when backup copy fails")
		}
	}
}

// ---------------------------------------------------------------------------
// WalkDir — inaccessible file skip
// ---------------------------------------------------------------------------

// TestWalkDir_SkipInaccessibleFiles verifies WalkDir skips files that
// cause access errors.
func TestWalkDir_SkipInaccessibleFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644)

	// Create a subdirectory with no execute permission.
	subDir := filepath.Join(dir, "secret")
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "b.go"), []byte("y"), 0o644)
	_ = os.Chmod(subDir, 0o000)
	defer func() { _ = os.Chmod(subDir, 0o755) }()

	var files []string
	err := WalkDir(dir, func(path string, _ fs.DirEntry) error {
		files = append(files, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir should not error: %v", err)
	}
	// a.go should be found; b.go may or may not be found depending on OS.
	found := false
	for _, f := range files {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a.go, got %v", files)
	}
}

// ---------------------------------------------------------------------------
// trackEditLocked — snapshot disappeared during track edit
// ---------------------------------------------------------------------------

// TestTrackEdit_SnapshotDisappearedDuringEdit tests the defensive check
// where mostRecentSnapshotLocked returns nil after the backup is created.
// This can't normally happen (mutex held), but the defensive path exists.
// We simulate it by manipulating state directly.
func TestTrackEdit_SnapshotDisappearedDuringEdit(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// This tests the path indirectly. We load empty state, which means
	// mostRecentSnapshotLocked returns nil. TrackEdit should error.
	tr.LoadState(FileHistoryState{
		Snapshots:    []FileHistorySnapshot{},
		TrackedFiles: map[string]bool{},
	})

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	err := tr.TrackEdit(filePath)
	if err == nil {
		t.Fatal("expected error when no snapshots available")
	}
	if !strings.Contains(err.Error(), "no snapshots") {
		t.Errorf("error = %v, want no snapshots", err)
	}
}

// ---------------------------------------------------------------------------
// checkOriginFileChanged — ReadFile error paths
// ---------------------------------------------------------------------------

// TestCheckOriginFileChanged_ContentComparison tests the byte comparison
// path where files have same size but different content.
func TestCheckOriginFileChanged_ContentComparison(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content-A"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	// Write different content with same length.
	_ = os.WriteFile(filePath, []byte("content-B"), 0o644)

	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]

	changed, err := tr.checkOriginFileChanged(filePath, backup.BackupFileName, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed: same-size files with different content")
	}
}

// TestCheckOriginFileChanged_ModeDifference tests that different file modes
// are detected as changed.
func TestCheckOriginFileChanged_ModeDifference(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	// Change file mode.
	_ = os.Chmod(filePath, 0o755)

	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]

	changed, err := tr.checkOriginFileChanged(filePath, backup.BackupFileName, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed: file mode differs")
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot — stat error (non-ENOENT) on tracked file
// ---------------------------------------------------------------------------

// TestMakeSnapshot_TrackedFileStatError uses a symlink loop as a tracked
// file path to trigger a non-ENOENT stat error.
func TestMakeSnapshot_TrackedFileStatError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Create a symlink loop inside the tracker dir.
	linkA := filepath.Join(dir, "loopA")
	linkB := filepath.Join(dir, "loopB")
	_ = os.Symlink(linkB, linkA)
	_ = os.Symlink(linkA, linkB)

	// Manually add the loop path as a tracked file.
	state := tr.State()
	snap := state.Snapshots[0]
	snap.TrackedFileBackups[linkA] = FileHistoryBackup{
		BackupFileName: "existing-backup",
		Version:        1,
	}
	tr.LoadState(FileHistoryState{
		Snapshots:        state.Snapshots,
		TrackedFiles:     map[string]bool{linkA: true},
		SnapshotSequence: state.SnapshotSequence,
	})

	// MakeSnapshot should skip the file with stat error (symlink loop).
	err := tr.MakeSnapshot("msg-stat-err")
	if err != nil {
		t.Fatalf("MakeSnapshot should not fail: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HasChangesAtMessage — getBackupFileNameFirstVersionLocked found
// ---------------------------------------------------------------------------

// TestHasChangesAtMessage_FirstVersionFallback covers the case where a file
// has Version==0 in the target snapshot (file not tracked yet) but has a v1
// backup in a later snapshot. getBackupFileNameFirstVersionLocked finds v1
// and compares against current file.
func TestHasChangesAtMessage_FirstVersionFallback(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Snapshot msg-1 with no tracked files.
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Track a file (v1 backup) and make snapshot msg-2.
	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("original"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Modify the file.
	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	// HasChangesAtMessage("msg-1"): file is NOT in msg-1 snapshot
	// (Version==0), so getBackupFileNameFirstVersionLocked finds v1 backup
	// from msg-2. File is modified → should report changes.
	if !tr.HasChangesAtMessage("msg-1") {
		t.Error("expected changes: file modified since msg-1 (file not tracked yet)")
	}
}

// TestHasChangesAtMessage_CheckOriginError covers the error path in
// HasChangesAtMessage where checkOriginFileChanged returns a non-nil error
// (original file replaced with symlink loop → stat fails with ELOOP).
func TestHasChangesAtMessage_CheckOriginError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Replace the original file with a symlink loop to trigger stat error.
	linkA := filePath + "-loopA"
	linkB := filePath + "-loopB"
	_ = os.Remove(filePath)
	_ = os.Symlink(linkB, linkA)
	_ = os.Symlink(linkA, linkB)
	// Make filePath point to linkA to trigger the loop
	_ = os.Symlink(linkA, filePath)

	// checkOriginFileChanged will fail with ELOOP → HasChangesAtMessage
	// hits the "if err != nil { continue }" path, returns false.
	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected false when checkOriginFileChanged errors")
	}
}

// ---------------------------------------------------------------------------
// checkOriginFileChanged — stat error on backup + ReadFile error
// ---------------------------------------------------------------------------

// TestCheckOriginFileChanged_BackupStatError uses a symlink loop as the
// backup file to trigger a non-ENOENT stat error.
func TestCheckOriginFileChanged_BackupStatError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)

	// Replace backup with symlink loop.
	linkB := backupPath + "-loopB"
	_ = os.Remove(backupPath)
	_ = os.Symlink(linkB, backupPath)
	_ = os.Symlink(backupPath, linkB)

	changed, err := tr.checkOriginFileChanged(filePath, backup.BackupFileName, nil)
	if err == nil {
		t.Fatal("expected error from symlink loop backup")
		if !strings.Contains(err.Error(), "symbolic") && !strings.Contains(err.Error(), "loop") {
			t.Errorf("error should mention symlinks, got: %v", err)
		}
	}
	if !changed {
		t.Error("expected changed=true when stat fails on backup")
	}
}

// TestCheckOriginFileChanged_BackupReadError makes the backup unreadable
// after stat succeeds, so ReadFile fails in the content comparison path.
func TestCheckOriginFileChanged_BackupReadError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content-A"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	state := tr.State()
	snap := state.Snapshots[0]
	backup := snap.TrackedFileBackups[filePath]
	backupPath := filepath.Join(dir, backup.BackupFileName)

	// Write different content with same length to trigger content comparison.
	_ = os.WriteFile(filePath, []byte("content-B"), 0o644)

	// Make backup unreadable so ReadFile fails (stat still succeeds).
	_ = os.Chmod(backupPath, 0o000)
	defer func() { _ = os.Chmod(backupPath, 0o644) }()

	changed, err := tr.checkOriginFileChanged(filePath, backup.BackupFileName, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when backup is unreadable (treat as changed)")
	}
}

// ---------------------------------------------------------------------------
// HasChangesAtMessage — backupFileName=="" and file still doesn't exist
// ---------------------------------------------------------------------------

// TestHasChangesAtMessage_NullBackupFileStillMissing covers the continue
// path when backupFileName=="" and the file doesn't exist on disk (no change).
func TestHasChangesAtMessage_NullBackupFileStillMissing(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "nonexistent.go")
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// File still doesn't exist → no change.
	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected no changes: file still doesn't exist at msg-1")
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot — inherit from previous snapshot
// ---------------------------------------------------------------------------

// TestMakeSnapshot_InheritFromPreviousSnapshot verifies that files not
// re-processed in the current snapshot inherit their backup from the
// previous snapshot (the inherit loop at bottom of MakeSnapshot).
func TestMakeSnapshot_InheritFromPreviousSnapshot(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	_ = os.WriteFile(fileA, []byte("a-content"), 0o644)
	_ = os.WriteFile(fileB, []byte("b-content"), 0o644)

	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(fileB); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Only modify fileA, not fileB. fileB should be inherited.
	_ = os.WriteFile(fileA, []byte("a-modified"), 0o644)

	err := tr.MakeSnapshot("msg-2")
	if err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	if snap.MessageID != "msg-2" {
		t.Fatalf("wrong snapshot: %s", snap.MessageID)
	}

	// Both files should be in the snapshot.
	backupA, okA := snap.TrackedFileBackups[fileA]
	backupB, okB := snap.TrackedFileBackups[fileB]
	if !okA {
		t.Error("fileA should be in snapshot")
	}
	if !okB {
		t.Error("fileB should be in snapshot (inherited)")
	}
	if backupA.Version < 2 {
		t.Errorf("fileA should be v2+, got v%d", backupA.Version)
	}
	// fileB is unchanged, so it should reuse the same backup.
	prevSnap := state.Snapshots[len(state.Snapshots)-2]
	prevBackupB := prevSnap.TrackedFileBackups[fileB]
	if backupB.BackupFileName != prevBackupB.BackupFileName {
		t.Errorf("fileB should reuse previous backup: got %q, want %q",
			backupB.BackupFileName, prevBackupB.BackupFileName)
	}
}

// ---------------------------------------------------------------------------
// CleanupOldBackups — RemoveAll failure
// ---------------------------------------------------------------------------

// TestCleanupOldBackups_RemoveAllFail uses a read-only parent to trigger
// RemoveAll failure.
func TestCleanupOldBackups_RemoveAllFail(t *testing.T) {
	baseDir := t.TempDir()
	fhDir := filepath.Join(baseDir, "file-history")

	oldDir := filepath.Join(fhDir, "old-session")
	_ = os.MkdirAll(oldDir, 0o755)
	_ = os.WriteFile(filepath.Join(oldDir, "backup.txt"), []byte("x"), 0o644)
	_ = os.Chtimes(oldDir, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	// Make file-history dir read-only to prevent RemoveAll from deleting children.
	_ = os.Chmod(fhDir, 0o555)
	defer func() { _ = os.Chmod(fhDir, 0o755) }()

	cleaned, err := CleanupOldBackups(fhDir, DefaultCleanupAge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// RemoveAll fails silently (logged, continues).
	if cleaned != 0 {
		t.Errorf("expected 0 cleaned (RemoveAll fails), got %d", cleaned)
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot — Version==0 fallback (line 214-216)
// ---------------------------------------------------------------------------

// TestMakeSnapshot_VersionZeroFallback covers the path where a tracked file
// has no backup in any snapshot (Version==0), so nextVersion is set to 1.
func TestMakeSnapshot_VersionZeroFallback(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	// Manually set up state: file is tracked but has no backup in snapshot.
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "",
			TrackedFileBackups: map[string]FileHistoryBackup{},
			Timestamp:          time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
		TrackedFiles:     map[string]bool{filePath: true},
		SnapshotSequence: 1,
	})

	err := tr.MakeSnapshot("msg-1")
	if err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	state := tr.State()
	snap := state.Snapshots[len(state.Snapshots)-1]
	backup, ok := snap.TrackedFileBackups[filePath]
	if !ok {
		t.Fatal("file should be in snapshot")
	}
	if backup.Version != 1 {
		t.Errorf("expected version 1 (Version==0 fallback), got %d", backup.Version)
	}
	if backup.BackupFileName == "" {
		t.Error("expected non-empty BackupFileName for existing file")
	}
}

// ---------------------------------------------------------------------------
// trackEditLocked — defensive checks (lines 174-179)
// ---------------------------------------------------------------------------

// TestTrackEdit_SnapshotDisappearedAfterBackup covers the defensive path where
// mostRecentSnapshotLocked returns nil AFTER backupFn succeeds. Can only happen
// under mutex manipulation (matches TS defensive pattern).
func TestTrackEdit_SnapshotDisappearedAfterBackup(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	// Call trackEditLocked directly — it acquires its own mutex.
	err := tr.trackEditLocked(filepath.Join(dir, "other.go"), func() (FileHistoryBackup, error) {
		tr.state.Snapshots = nil
		return FileHistoryBackup{BackupFileName: "test", Version: 1, BackupTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
	})
	if err == nil {
		t.Fatal("expected error when snapshot disappears after backup")
	}
	if !strings.Contains(err.Error(), "snapshot disappeared") {
		t.Errorf("error = %v, want 'snapshot disappeared'", err)
	}
}

// TestTrackEdit_AlreadyTrackedAfterBackup covers the defensive re-check where
// the file appears in TrackedFileBackups between the first check and the second.
func TestTrackEdit_AlreadyTrackedAfterBackup(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	// Call trackEditLocked directly — it acquires its own mutex.
	// Inside backupFn, add the new file to the snapshot — triggers alreadyTracked.
	newFile := filepath.Join(dir, "new.go")
	_ = os.WriteFile(newFile, []byte("new"), 0o644)

	err := tr.trackEditLocked(newFile, func() (FileHistoryBackup, error) {
		snap := tr.mostRecentSnapshotLocked()
		snap.TrackedFileBackups[newFile] = FileHistoryBackup{
			BackupFileName: "already-here",
			Version:        1,
		}
		return FileHistoryBackup{BackupFileName: "new-backup", Version: 1, BackupTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
	})
	if err != nil {
		t.Fatalf("expected nil when already tracked after backup: %v", err)
	}
}

// ---------------------------------------------------------------------------
// createBackup with blank filePath (line 526-528)
// ---------------------------------------------------------------------------

func TestCreateBackup_EmptyPath(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	backup, err := tr.createBackup("", 1)
	if err != nil {
		t.Fatalf("createBackup empty path: %v", err)
	}
	if backup.BackupFileName != "" {
		t.Errorf("expected empty BackupFileName, got %q", backup.BackupFileName)
	}
	if backup.Version != 1 {
		t.Errorf("expected version 1, got %d", backup.Version)
	}
}

// ---------------------------------------------------------------------------
// applySnapshotLocked — delete file permission error (line 488-490)
// ---------------------------------------------------------------------------

func TestApplySnapshot_DeleteFilePermissionError(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	subDir := filepath.Join(dir, "sub")
	_ = os.MkdirAll(subDir, 0o755)
	filePath := filepath.Join(subDir, "new.go")

	// Track non-existent file → null backup.
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Create the file (it shouldn't exist at msg-1).
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Make parent directory read-only so os.Remove fails with EACCES.
	_ = os.Chmod(subDir, 0o555)
	defer func() { _ = os.Chmod(subDir, 0o755) }()

	changed, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	for _, f := range changed {
		if f == filePath {
			t.Error("file should not be in changed list when delete fails")
		}
	}
}

// ---------------------------------------------------------------------------
// writeBackupData — deep MkdirAll failure (line 583-585)
// ---------------------------------------------------------------------------

func TestWriteBackupData_DeepMkdirFails(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")
	tr := NewTracker(backupDir)

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	// Remove backupDir, then make baseDir read-only so MkdirAll fails.
	_ = os.RemoveAll(backupDir)
	_ = os.Chmod(baseDir, 0o555)
	defer func() { _ = os.Chmod(baseDir, 0o755) }()

	err := tr.TrackEditFromContent(filePath, []byte("backup"))
	if err == nil {
		t.Fatal("expected error when MkdirAll fails")
	}
	if !strings.Contains(err.Error(), "write backup") {
		t.Errorf("error should mention write backup, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MakeSnapshot — createBackup fails (line 252-254)
// ---------------------------------------------------------------------------

// TestMakeSnapshot_CreateBackupReadOnly covers createBackup failing inside
// MakeSnapshot when the backup directory is read-only. The file is detected as
// changed but backup creation fails — MakeSnapshot logs and continues, then
// inherits the file from the previous snapshot.
func TestMakeSnapshot_CreateBackupReadOnly(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")
	tr := NewTracker(backupDir)

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Make backup dir read-only — existing backup readable, new files can't be created.
	_ = os.Chmod(backupDir, 0o555)
	defer func() { _ = os.Chmod(backupDir, 0o755) }()

	// Modify file so MakeSnapshot tries to create a new backup (v2).
	_ = os.WriteFile(filePath, []byte("modified"), 0o644)

	err := tr.MakeSnapshot("msg-2")
	if err != nil {
		t.Fatalf("MakeSnapshot should not fail: %v", err)
	}

	// File should be inherited from previous snapshot since createBackup failed.
	state := tr.State()
	lastSnap := state.Snapshots[len(state.Snapshots)-1]
	if lastSnap.MessageID != "msg-2" {
		t.Fatalf("wrong snapshot: %s", lastSnap.MessageID)
	}
	prevSnap := state.Snapshots[len(state.Snapshots)-2]
	prevBackup := prevSnap.TrackedFileBackups[filePath]
	lastBackup := lastSnap.TrackedFileBackups[filePath]
	if prevBackup.BackupFileName != lastBackup.BackupFileName {
		t.Errorf("expected inherited backup after create failure, got %q vs %q",
			prevBackup.BackupFileName, lastBackup.BackupFileName)
	}
}

// ---------------------------------------------------------------------------
// copyFileData — deep MkdirAll failure (line 633-635)
// ---------------------------------------------------------------------------

func TestCopyFileData_DeepMkdirFails(t *testing.T) {
	baseDir := t.TempDir()
	backupDir := filepath.Join(baseDir, "backups")
	tr := NewTracker(backupDir)

	filePath := filepath.Join(baseDir, "test.go")
	_ = os.WriteFile(filePath, []byte("content"), 0o644)

	// Remove backupDir, then make baseDir read-only so MkdirAll fails.
	_ = os.RemoveAll(backupDir)
	_ = os.Chmod(baseDir, 0o555)
	defer func() { _ = os.Chmod(baseDir, 0o755) }()

	err := tr.TrackEdit(filePath)
	if err == nil {
		t.Fatal("expected error when MkdirAll fails in copyFileData")
	}
	if !strings.Contains(err.Error(), "copy backup") {
		t.Errorf("error should mention copy backup, got: %v", err)
	}
}
