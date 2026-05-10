package filehistory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Extended rewind tests for snapshot-based model
// ---------------------------------------------------------------------------

// TestRewind_SameFileMultipleEditsDifferentTurns edits the same file in 3 turns
// and rewinds to turn 1, verifying all intermediate changes are undone.
func TestRewind_SameFileMultipleEditsDifferentTurns(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "budget.go")
	if err := os.WriteFile(filePath, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: edit file to v1
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Turn 2: edit file to v2
	if err := os.WriteFile(filePath, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Turn 3: edit file to v3
	if err := os.WriteFile(filePath, []byte("v3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatal(err)
	}

	// Verify current state
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v3" {
		t.Fatalf("current should be v3, got %q", string(data))
	}

	// Rewind to msg-1 (post-turn-1 state = "v1")
	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	data, err = os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1" {
		t.Errorf("expected v1 after rewind to msg-1, got %q", string(data))
	}
}

// TestRewind_ProgressiveRewind rewinds step by step (3→2→1→0).
func TestRewind_ProgressiveRewind(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("turn1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("turn2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filePath, []byte("turn3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatal(err)
	}

	// Rewind 3→2
	if _, err := tr.Rewind("msg-2"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filePath); err != nil {
		t.Fatal(err)
	} else if string(data) != "turn2" {
		t.Errorf("after rewind to msg-2, expected turn2, got %q", string(data))
	}

	// Rewind 2→1
	if _, err := tr.Rewind("msg-1"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filePath); err != nil {
		t.Fatal(err)
	} else if string(data) != "turn1" {
		t.Errorf("after rewind to msg-1, expected turn1, got %q", string(data))
	}

	// Rewind 1→0 (initial snapshot, empty MessageID)
	if _, err := tr.Rewind(""); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filePath); err != nil {
		t.Fatal(err)
	} else if string(data) != "base" {
		t.Errorf("after rewind to initial, expected base, got %q", string(data))
	}
}

// TestRewind_FileDeletedBetweenTrackAndSnapshot tests the race where a file
// is tracked, then deleted externally before MakeSnapshot runs.
func TestRewind_FileDeletedBetweenTrackAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// TrackEdit captures v1 backup of "original"
	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}

	// File is deleted between TrackEdit and MakeSnapshot
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}

	// MakeSnapshot should handle the missing file gracefully
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// File should not exist on disk
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("file should not exist after deletion, got err=%v", err)
	}

	// Rewind to initial snapshot (before msg-1) should restore the file from v1 backup.
	// Rewinding to msg-1 would mean "restore to deleted state" — no files restored.
	restored, err := tr.Rewind("")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("restored content = %q, want %q", string(data), "original")
	}
}

// TestRewind_MultiFileComplexScenario tests multiple files, some created, some edited.
func TestRewind_MultiFileComplexScenario(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	fileC := filepath.Join(dir, "c.go")

	if err := os.WriteFile(fileA, []byte("a-original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b-original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: edit a.go
	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("a-edit1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	// Turn 2: edit b.go and create c.go
	if err := tr.TrackEdit(fileB); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(fileC); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("b-edit1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileC, []byte("c-created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	// Turn 3: edit a.go again and c.go
	if err := tr.TrackEdit(fileA); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(fileC); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileA, []byte("a-edit2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileC, []byte("c-edit1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, fileA, "a-edit2")
	assertFileContent(t, fileB, "b-edit1")
	assertFileContent(t, fileC, "c-edit1")

	// Rewind to msg-1
	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 3 {
		t.Fatalf("expected 3 restored files, got %d: %v", len(restored), restored)
	}

	assertFileContent(t, fileA, "a-edit1")
	assertFileContent(t, fileB, "b-original")
	assertFileNotExists(t, fileC)
}

// TestRewind_NewFileCreatedAndEdited tests file created in turn 1, edited in turn 2.
func TestRewind_NewFileCreatedAndEdited(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "new.go")

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	assertFileContent(t, filePath, "edited")

	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, filePath, "created")
	if len(restored) != 1 {
		t.Errorf("expected 1 restored, got %d", len(restored))
	}
}

// TestRewind_NewFileRewindToBeforeCreation rewinds to before file was created.
func TestRewind_NewFileRewindToBeforeCreation(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "new.go")

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("created"), 0o644); err != nil {
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
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	assertFileNotExists(t, filePath)
}

// TestRewind_NoChangesWhenFilesMatch returns empty restored list when files unchanged.
func TestRewind_NoChangesWhenFilesMatch(t *testing.T) {
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

	restored, err := tr.Rewind("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	// TS applySnapshot restores conditionally via checkOriginFileChanged (L576).
	// Same-content files are NOT restored — 0 changes is correct.
	if len(restored) != 0 {
		t.Errorf("expected 0 restored files (content unchanged), got %d", len(restored))
	}
}

// TestRewind_RewindFilesOnlyNoTruncate verifies snapshots are preserved.
func TestRewind_RewindFilesOnlyNoTruncate(t *testing.T) {
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
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}

	assertFileContent(t, filePath, "original")

	if len(tr.State().Snapshots) != snapCountBefore {
		t.Errorf("snapshots should be preserved: got %d, want %d",
			len(tr.State().Snapshots), snapCountBefore)
	}
}

// TestRewindFilesOnly_DeletedFile restores by deleting a created file.
func TestRewindFilesOnly_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := filepath.Join(dir, "new.go")

	if err := tr.TrackEdit(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("created"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.RewindFilesOnly("")
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	assertFileNotExists(t, filePath)
}

// TestRewindFilesOnly_NoTargetSnapshot returns error.
func TestRewindFilesOnly_NoTargetSnapshot(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	_, err := tr.RewindFilesOnly("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent snapshot")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention not found, got: %v", err)
	}
}

// TestHasChangesAtMessage checks various change scenarios.
func TestHasChangesAtMessage(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if tr.HasChangesAtMessage("anything") {
		t.Error("expected false with no tracked files")
	}

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

	if tr.HasChangesAtMessage("msg-1") {
		t.Error("expected false when file unchanged")
	}

	if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}

	if !tr.HasChangesAtMessage("msg-1") {
		t.Error("expected true when file differs from snapshot")
	}
}

// TestTruncateSnapshotsFrom tests snapshot truncation.
func TestTruncateSnapshotsFrom(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-2"); err != nil {
		t.Fatal(err)
	}
	if err := tr.MakeSnapshot("msg-3"); err != nil {
		t.Fatal(err)
	}

	if len(tr.State().Snapshots) != 4 { // initial + 3
		t.Fatalf("expected 4 snapshots, got %d", len(tr.State().Snapshots))
	}

	tr.TruncateSnapshotsFrom("msg-2")

	state := tr.State()
	if len(state.Snapshots) != 2 { // initial + msg-1
		t.Errorf("expected 2 snapshots, got %d", len(state.Snapshots))
	}
	if state.Snapshots[1].MessageID != "msg-1" {
		t.Errorf("last snapshot should be msg-1, got %q", state.Snapshots[1].MessageID)
	}
}

// TestTruncateSnapshotsFrom_NonexistentMessageID is a no-op.
func TestTruncateSnapshotsFrom_NonexistentMessageID(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatal(err)
	}

	countBefore := len(tr.State().Snapshots)
	tr.TruncateSnapshotsFrom("nonexistent")

	if len(tr.State().Snapshots) != countBefore {
		t.Error("expected no change for nonexistent messageID")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", filepath.Base(path), string(data), expected)
	}
}

func assertFileNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist", filepath.Base(path))
	}
}
