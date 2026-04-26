package short

import (
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/toolresult"
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
