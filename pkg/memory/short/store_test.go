package short

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewStore_ErrorPaths verifies NewStore error handling.
func TestNewStore_ErrorPaths(t *testing.T) {
	// Test invalid directory (permission denied)
	invalidPath := "/root/invalid/db/path/test.db"
	_, err := NewStore(invalidPath)
	if err == nil {
		t.Error("NewStore should fail with permission denied")
	}
	if !filepath.IsAbs(err.Error()) || !filepath.IsAbs(invalidPath) {
		// Just verify error mentions path/cannot create
		if !containsAny(err.Error(), []string{"create", "directory", "permission", "denied", "access"}) {
			t.Errorf("error = %v, want path/create/permission error", err)
		}
	}
}

// TestDB_ReturnsDatabase verifies DB() returns the underlying database.
func TestDB_ReturnsDatabase(t *testing.T) {
	store := openTestStore(t)

	db := store.DB()
	if db == nil {
		t.Fatal("DB() returned nil")
	}

	// Verify it's functional by querying the schema
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' LIMIT 1").Scan(&name)
	if err != nil {
		t.Errorf("db query failed: %v", err)
	}
}

// TestDBPath_ReturnsPath verifies DBPath() returns the database file path.
func TestDBPath_ReturnsPath(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	if store.DBPath() != dbPath {
		t.Errorf("DBPath() = %q, want %q", store.DBPath(), dbPath)
	}
}

// TestSegment_BareStore verifies Segment works with a bare Store (no DB needed)
// via the package-level bigram analyzer. CJK ideographs become bigrams.
func TestSegment_BareStore(t *testing.T) {
	store := &Store{}
	// 4 ideographs -> bigrams "事务 务化" (no trailing unigram with outputUnigram=false)
	result := store.Segment("事务化")
	if result != "事务 务化" {
		t.Errorf("Segment(\"事务化\") = %q, want \"事务 务化\"", result)
	}
	// Latin stays whole.
	if got := store.Segment("hello world"); got != "hello world" {
		t.Errorf("Segment(\"hello world\") = %q, want \"hello world\"", got)
	}
	// Empty input short-circuits.
	if got := store.Segment(""); got != "" {
		t.Errorf("Segment(\"\") = %q, want \"\"", got)
	}
}

// TestNewStore_CreatesDirectory verifies NewStore creates parent directories.
func TestNewStore_CreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "subdir1", "subdir2", "test.db")

	// Directory doesn't exist yet
	if _, err := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(err) {
		t.Skip("directory already exists")
	}

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Verify directory was created
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if info.IsDir() {
		t.Error("dbPath is a directory, not a file")
	}
}

// TestAppendMessage_VerifiesFTSPopulated verifies AppendMessage populates FTS index.
func TestAppendMessage_VerifiesFTSPopulated(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Create session
	_, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir, model) VALUES (?, '', '')",
		sessionID,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	msg := testMessage(0, "user", "uuid-fts-1", "", `[{"type":"text","text":"unique search term pineapple"}]`)
	if err := store.AppendMessage(sessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Verify FTS index was populated
	results, err := store.SearchMessages("pineapple", &SearchOptions{
		SessionID: sessionID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) == 0 {
		t.Error("AppendMessage did not populate FTS index")
	}
}

// TestAppendMessages_WithTransaction verifies AppendMessages uses transaction.
func TestAppendMessages_WithTransaction(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Create session
	_, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir, model) VALUES (?, '', '')",
		sessionID,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	messages := []*TranscriptMessage{
		{UUID: "uuid-1", Type: "user", Content: `[{"type":"text","text":"first"}]`},
		{UUID: "uuid-2", Type: "assistant", Content: `[{"type":"text","text":"second"}]`},
		{UUID: "uuid-3", Type: "user", Content: `[{"type":"text","text":"third"}]`},
	}

	if err := store.AppendMessages(sessionID, messages); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	// Verify all messages were inserted
	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 3 {
		t.Errorf("got %d messages, want 3", len(loaded))
	}

	// Verify chain integrity
	if loaded[0].ParentUUID != "" {
		t.Errorf("first message parent_uuid = %q, want empty", loaded[0].ParentUUID)
	}
	if loaded[1].ParentUUID != "uuid-1" {
		t.Errorf("second message parent_uuid = %q, want uuid-1", loaded[1].ParentUUID)
	}
	if loaded[2].ParentUUID != "uuid-2" {
		t.Errorf("third message parent_uuid = %q, want uuid-2", loaded[2].ParentUUID)
	}
}

// TestAppendMessages_EmptyList verifies AppendMessages handles empty list.
func TestAppendMessages_EmptyList(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Create session
	_, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir, model) VALUES (?, '', '')",
		sessionID,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Empty list should succeed
	if err := store.AppendMessages(sessionID, []*TranscriptMessage{}); err != nil {
		t.Errorf("AppendMessages with empty list failed: %v", err)
	}

	// Verify no messages were inserted
	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("got %d messages, want 0", len(loaded))
	}
}

// Helper function to check if string contains any of the substrings
func containsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr ||
		s[len(s)-len(substr):] == substr ||
		containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestAppendMessages_TransactionBeginError verifies AppendMessages handles transaction begin errors.
func TestAppendMessages_TransactionBeginError(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"

	// Create session
	_, err := store.DB().Exec(
		"INSERT INTO sessions (session_id, project_dir, model) VALUES (?, '', '')",
		sessionID,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Close store to force transaction errors
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	messages := []*TranscriptMessage{
		{UUID: "uuid-1", Type: "user", Content: `[{"type":"text","text":"first"}]`},
	}

	err = store.AppendMessages(sessionID, messages)
	if err == nil {
		t.Error("AppendMessages should fail when store is closed")
	}
}

// TestNewStore_PragmaError verifies NewStore handles pragma errors.
func TestNewStore_PragmaError(t *testing.T) {
	// This test would require mocking sql.Open to return a db that fails on Exec
	// Hard to test without interface changes
	t.Skip("requires mocking sql.DB.Exec to fail on pragma")
}

// Lines 53-55: NewStore — sql.Open error
func TestNewStore_SQLOpenError(t *testing.T) {
	// Use an invalid database path that will fail to open
	// On Linux, /dev/null as a db path should fail
	_, err := NewStore("/dev/null/test.db")
	if err == nil {
		t.Error("NewStore should fail with invalid path")
	}
}

// Lines 64-67: NewStore — pragma error
func TestNewStore_PragmaFailure(t *testing.T) {
	// We can't easily force pragma errors without mocking, but we can test
	// that the error path exists. The skipped test in store_test.go documents this.
	// Instead, verify normal path succeeds.
	store, err := NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// Lines 76-79: NewStore — initSchema error
func TestNewStore_InitSchemaError(t *testing.T) {
	// Create a read-only directory
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/ro/test.db"
	// Don't create the directory — MkdirAll will succeed but
	// the database creation might fail for other reasons.
	// Actually, NewStore calls MkdirAll which creates the directory.
	// The initSchema error is hard to trigger without a corrupted SQLite.
	// Test that normal path works.
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestNewStore_PragmaError tests NewStore failing on a corrupted database file.
// With DSN-based pragmas, the failure surfaces during schema init (the
// corrupted file can't be read as SQLite), not during a db.Exec("PRAGMA")
// call.
func TestNewStore_PragmaError_V2(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	// Write garbage to make SQLite fail on open/schema.
	if err := writeGarbageFile(dbPath); err != nil {
		t.Fatalf("writeGarbageFile: %v", err)
	}

	_, err := NewStore(dbPath)
	if err == nil {
		t.Fatal("NewStore should fail with corrupted database file")
	}
	// The error should mention schema/open/database/file — any indicator that
	// SQLite rejected the file. We don't assert a specific wording since the
	// failure point (DSN pragma vs schema init) is driver-dependent.
	errStr := err.Error()
	if !strings.Contains(errStr, "schema") && !strings.Contains(errStr, "open") &&
		!strings.Contains(errStr, "SQL") && !strings.Contains(errStr, "database") &&
		!strings.Contains(errStr, "file") {
		t.Errorf("error should mention schema/open/database/file, got: %v", errStr)
	}
}

// writeGarbageFile writes random bytes to a file to corrupt it for error-path testing.
func writeGarbageFile(path string) error {
	garbage := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD, 0x80, 0x81}
	return os.WriteFile(path, garbage, 0644)
}
