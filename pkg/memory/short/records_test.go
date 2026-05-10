package short

import (
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/tool/toolresult"
)

func newRecordsTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// Create session row so FK constraint is satisfied.
	_, _ = store.DB().Exec("INSERT OR IGNORE INTO sessions (session_id, project_dir) VALUES (?, ?)",
		"test-session", "/tmp")
	return store
}

func TestSaveAndLoadContentReplacementRecords(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	records := []toolresult.ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "preview-1"},
		{Kind: "tool-result", ToolUseID: "tr-2", Replacement: "preview-2"},
	}

	err := store.SaveContentReplacementRecords(sessionID, records)
	if err != nil {
		t.Fatalf("SaveContentReplacementRecords: %v", err)
	}

	loaded, err := store.LoadContentReplacementRecords(sessionID)
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 records, got %d", len(loaded))
	}
	if loaded[0].ToolUseID != "tr-1" || loaded[0].Replacement != "preview-1" {
		t.Errorf("record 0 = %+v, want {tr-1, preview-1}", loaded[0])
	}
	if loaded[1].ToolUseID != "tr-2" || loaded[1].Replacement != "preview-2" {
		t.Errorf("record 1 = %+v, want {tr-2, preview-2}", loaded[1])
	}
}

func TestSaveContentReplacementRecords_Empty(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	err := store.SaveContentReplacementRecords(sessionID, nil)
	if err != nil {
		t.Fatalf("SaveContentReplacementRecords(nil) should not error, got: %v", err)
	}

	loaded, err := store.LoadContentReplacementRecords(sessionID)
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 records, got %d", len(loaded))
	}
}

func TestLoadContentReplacementRecords_NoRecords(t *testing.T) {
	store := newRecordsTestStore(t)

	loaded, err := store.LoadContentReplacementRecords("nonexistent-session")
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected 0 records for nonexistent session, got %d", len(loaded))
	}
}

func TestLoadContentReplacementRecords_SkipsMalformed(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	// Insert valid records first.
	valid := []toolresult.ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "p1"},
	}
	if err := store.SaveContentReplacementRecords(sessionID, valid); err != nil {
		t.Fatalf("save valid: %v", err)
	}

	// Insert malformed record directly into DB.
	_, _ = store.DB().Exec(
		"INSERT INTO messages (session_id, uuid, type, subtype, content) VALUES (?, ?, ?, ?, ?)",
		sessionID, "bad-uuid", "metadata", "content_replacement", "not-valid-json",
	)

	loaded, err := store.LoadContentReplacementRecords(sessionID)
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 valid record (skipping malformed), got %d", len(loaded))
	}
	if loaded[0].ToolUseID != "tr-1" {
		t.Errorf("record = %q, want tr-1", loaded[0].ToolUseID)
	}
}

func TestSaveContentReplacementRecords_Accumulates(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	batch1 := []toolresult.ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "p1"},
	}
	batch2 := []toolresult.ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tr-2", Replacement: "p2"},
		{Kind: "tool-result", ToolUseID: "tr-3", Replacement: "p3"},
	}

	if err := store.SaveContentReplacementRecords(sessionID, batch1); err != nil {
		t.Fatalf("save batch1: %v", err)
	}
	if err := store.SaveContentReplacementRecords(sessionID, batch2); err != nil {
		t.Fatalf("save batch2: %v", err)
	}

	loaded, err := store.LoadContentReplacementRecords(sessionID)
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 accumulated records, got %d", len(loaded))
	}
	// Verify order: batch1 first, then batch2
	if loaded[0].ToolUseID != "tr-1" {
		t.Errorf("record 0 = %q, want tr-1", loaded[0].ToolUseID)
	}
	if loaded[1].ToolUseID != "tr-2" {
		t.Errorf("record 1 = %q, want tr-2", loaded[1].ToolUseID)
	}
	if loaded[2].ToolUseID != "tr-3" {
		t.Errorf("record 2 = %q, want tr-3", loaded[2].ToolUseID)
	}
}

// --- File Backup Records ---

func TestSaveLoadFileHistoryState(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	state := filehistory.FileHistoryState{
		Snapshots: []filehistory.FileHistorySnapshot{
			{MessageID: "", TrackedFileBackups: map[string]filehistory.FileHistoryBackup{}},
			{
				MessageID: "msg-1",
				TrackedFileBackups: map[string]filehistory.FileHistoryBackup{
					"/tmp/a.go": {BackupFileName: "abc123@v1", Version: 1},
				},
			},
		},
		TrackedFiles:     map[string]bool{"/tmp/a.go": true},
		SnapshotSequence: 1,
	}

	err := store.SaveFileHistoryState(sessionID, state)
	if err != nil {
		t.Fatalf("SaveFileHistoryState: %v", err)
	}

	loaded, err := store.LoadFileHistoryState(sessionID)
	if err != nil {
		t.Fatalf("LoadFileHistoryState: %v", err)
	}
	if loaded.SnapshotSequence != 1 {
		t.Errorf("SnapshotSequence = %d, want 1", loaded.SnapshotSequence)
	}
	if len(loaded.Snapshots) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(loaded.Snapshots))
	}
	if loaded.Snapshots[1].MessageID != "msg-1" {
		t.Errorf("snapshot 1 MessageID = %q, want msg-1", loaded.Snapshots[1].MessageID)
	}
	backup, ok := loaded.Snapshots[1].TrackedFileBackups["/tmp/a.go"]
	if !ok {
		t.Fatal("expected /tmp/a.go in snapshot 1 trackedFileBackups")
	}
	if backup.BackupFileName != "abc123@v1" {
		t.Errorf("BackupFileName = %q, want abc123@v1", backup.BackupFileName)
	}
	if !loaded.TrackedFiles["/tmp/a.go"] {
		t.Error("expected /tmp/a.go in tracked files")
	}
}

func TestLoadFileHistoryState_Empty(t *testing.T) {
	store := newRecordsTestStore(t)

	loaded, err := store.LoadFileHistoryState("nonexistent-session")
	if err != nil {
		t.Fatalf("LoadFileHistoryState: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for nonexistent session, got %+v", loaded)
	}
}

func TestSaveFileHistoryState_Empty(t *testing.T) {
	store := newRecordsTestStore(t)
	sessionID := "test-session"

	err := store.SaveFileHistoryState(sessionID, filehistory.FileHistoryState{})
	if err != nil {
		t.Fatalf("SaveFileHistoryState(empty) should not error, got: %v", err)
	}
}
