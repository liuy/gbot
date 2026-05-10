package filehistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
