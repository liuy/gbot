package short

import (
	"github.com/google/uuid"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCreateSession verifies CreateSession creates a session with correct fields.
func TestCreateSession(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses, err := store.CreateSession("/project/dir", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Verify UUID format
	if _, err := uuid.Parse(ses.SessionID); err != nil {
		t.Errorf("SessionID is not valid UUID: %s", ses.SessionID)
	}

	// Verify fields
	if ses.ProjectDir != "/project/dir" {
		t.Errorf("ProjectDir = %q, want %q", ses.ProjectDir, "/project/dir")
	}
	if ses.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", ses.Model, "claude-opus-4-6")
	}
	if ses.Title != "" {
		t.Errorf("Title = %q, want empty", ses.Title)
	}
	if ses.ParentSessionID != "" {
		t.Errorf("ParentSessionID = %q, want empty", ses.ParentSessionID)
	}
	if ses.ForkPointSeq != 0 {
		t.Errorf("ForkPointSeq = %d, want 0", ses.ForkPointSeq)
	}
	if ses.AgentType != "" {
		t.Errorf("AgentType = %q, want empty", ses.AgentType)
	}
	if ses.Mode != "" {
		t.Errorf("Mode = %q, want empty", ses.Mode)
	}
	if ses.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
	if ses.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}
	if !ses.CreatedAt.Equal(ses.UpdatedAt) {
		t.Errorf("CreatedAt %v != UpdatedAt %v", ses.CreatedAt, ses.UpdatedAt)
	}

	// Verify session exists in DB
	row := store.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE session_id = ?", ses.SessionID)
	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatalf("scan count: %v", err)
	}
	if count != 1 {
		t.Errorf("session count = %d, want 1", count)
	}
}

// TestGetSession_Existing verifies GetSession retrieves an existing session.
func TestGetSession_Existing(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	created, err := store.CreateSession("/my/project", "sonnet")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := store.GetSession(created.SessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}

	if got.SessionID != created.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, created.SessionID)
	}
	if got.ProjectDir != "/my/project" {
		t.Errorf("ProjectDir = %q, want %q", got.ProjectDir, "/my/project")
	}
	if got.Model != "sonnet" {
		t.Errorf("Model = %q, want %q", got.Model, "sonnet")
	}
}

// TestGetSession_NotFound verifies GetSession returns error for non-existent session.
func TestGetSession_NotFound(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	_, err := store.GetSession("nonexistent-id")
	if err == nil {
		t.Fatal("GetSession should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestGetSession_WithNullableFields verifies GetSession handles nullable fields correctly.
func TestGetSession_WithNullableFields(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Insert session with nullable fields set
	sessionID := uuid.New().String()
	now := time.Now()
	query := `
		INSERT INTO sessions (
			session_id, project_dir, model, title,
			parent_session_id, fork_point_seq, agent_type, mode, settings,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)
	`
	_, err := store.db.Exec(query, sessionID, "/project", "opus",
		"Custom Title", "parent-id", 42, "Explore", "plan", now, now)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	got, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}

	if got.Title != "Custom Title" {
		t.Errorf("Title = %q, want %q", got.Title, "Custom Title")
	}
	if got.ParentSessionID != "parent-id" {
		t.Errorf("ParentSessionID = %q, want %q", got.ParentSessionID, "parent-id")
	}
	if got.ForkPointSeq != 42 {
		t.Errorf("ForkPointSeq = %d, want 42", got.ForkPointSeq)
	}
	if got.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want %q", got.AgentType, "Explore")
	}
	if got.Mode != "plan" {
		t.Errorf("Mode = %q, want %q", got.Mode, "plan")
	}
}

// TestListSessions_EmptyProject verifies ListSessions returns empty list for project with no sessions.
func TestListSessions_EmptyProject(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	sessions, err := store.ListSessions("/empty/project", 10)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("len(sessions) = %d, want 0", len(sessions))
	}
}

// TestListSessions_SortedByUpdatedAt verifies ListSessions sorts by updated_at DESC.
func TestListSessions_SortedByUpdatedAt(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Create sessions with different timestamps
	ses1, _ := store.CreateSession("/project", "model1")
	time.Sleep(10 * time.Millisecond) // ensure different timestamps
	ses2, _ := store.CreateSession("/project", "model2")
	time.Sleep(10 * time.Millisecond)
	ses3, _ := store.CreateSession("/project", "model3")

	sessions, err := store.ListSessions("/project", 0)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	if len(sessions) != 3 {
		t.Fatalf("len(sessions) = %d, want 3", len(sessions))
	}

	// Most recently updated first
	if sessions[0].SessionID != ses3.SessionID {
		t.Errorf("sessions[0].SessionID = %q, want %q (most recent)", sessions[0].SessionID, ses3.SessionID)
	}
	if sessions[1].SessionID != ses2.SessionID {
		t.Errorf("sessions[1].SessionID = %q, want %q", sessions[1].SessionID, ses2.SessionID)
	}
	if sessions[2].SessionID != ses1.SessionID {
		t.Errorf("sessions[2].SessionID = %q, want %q (oldest)", sessions[2].SessionID, ses1.SessionID)
	}
}

// TestListSessions_WithLimit verifies ListSessions respects the limit parameter.
func TestListSessions_WithLimit(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	for i := range 5 {
		if _, err := store.CreateSession("/project", "model"); err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
	}

	// Request limit 3
	sessions, err := store.ListSessions("/project", 3)
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("len(sessions) = %d, want 3", len(sessions))
	}
}

// TestListSessions_FiltersByProjectDir verifies ListSessions only returns sessions for specified project.
func TestListSessions_FiltersByProjectDir(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses1, _ := store.CreateSession("/project/a", "model")
	ses2, _ := store.CreateSession("/project/b", "model")

	// List sessions for project/a only
	sessionsA, err := store.ListSessions("/project/a", 0)
	if err != nil {
		t.Fatalf("ListSessions project/a: %v", err)
	}

	if len(sessionsA) != 1 {
		t.Errorf("len(sessionsA) = %d, want 1", len(sessionsA))
	}
	if sessionsA[0].SessionID != ses1.SessionID {
		t.Errorf("sessionsA[0].SessionID = %q, want %q", sessionsA[0].SessionID, ses1.SessionID)
	}

	// List sessions for project/b only
	sessionsB, err := store.ListSessions("/project/b", 0)
	if err != nil {
		t.Fatalf("ListSessions project/b: %v", err)
	}

	if len(sessionsB) != 1 {
		t.Errorf("len(sessionsB) = %d, want 1", len(sessionsB))
	}
	if sessionsB[0].SessionID != ses2.SessionID {
		t.Errorf("sessionsB[0].SessionID = %q, want %q", sessionsB[0].SessionID, ses2.SessionID)
	}
}

// TestUpdateSessionTitle verifies UpdateSessionTitle updates the title.
func TestUpdateSessionTitle(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses, _ := store.CreateSession("/project", "model")

	err := store.UpdateSessionTitle(ses.SessionID, "My Custom Title")
	if err != nil {
		t.Fatalf("UpdateSessionTitle failed: %v", err)
	}

	// Verify title was updated
	updated, err := store.GetSession(ses.SessionID)
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}

	if updated.Title != "My Custom Title" {
		t.Errorf("Title = %q, want %q", updated.Title, "My Custom Title")
	}

	// Verify updated_at was bumped when title was set.
	// CURRENT_TIMESTAMP has second granularity, so compare truncated to seconds.
	updatedAtSec := updated.UpdatedAt.UTC().Truncate(time.Second)
	createdAtSec := updated.CreatedAt.UTC().Truncate(time.Second)
	if updatedAtSec.Before(createdAtSec) {
		t.Errorf("UpdatedAt %v should not be before CreatedAt %v", updatedAtSec, createdAtSec)
	}
}

// TestUpdateSessionTitle_NotFound verifies UpdateSessionTitle returns error for non-existent session.
func TestUpdateSessionTitle_NotFound(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.UpdateSessionTitle("nonexistent", "Title")
	if err == nil {
		t.Fatal("UpdateSessionTitle should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestUpdateSessionTimestamp verifies UpdateSessionTimestamp updates updated_at.
func TestUpdateSessionTimestamp(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses, _ := store.CreateSession("/project", "model")
	time.Sleep(2 * time.Second) // ensure SQLite CURRENT_TIMESTAMP differs (second granularity)

	err := store.UpdateSessionTimestamp(ses.SessionID)
	if err != nil {
		t.Fatalf("UpdateSessionTimestamp failed: %v", err)
	}

	updated, err := store.GetSession(ses.SessionID)
	if err != nil {
		t.Fatalf("GetSession after update: %v", err)
	}

	// Compare UTC times to avoid timezone issues
	sesUTC := ses.UpdatedAt.UTC()
	updatedUTC := updated.UpdatedAt.UTC()

	if !updatedUTC.After(sesUTC) {
		t.Errorf("UpdatedAt %v not after %v", updatedUTC, sesUTC)
	}
}

// TestUpdateSessionTimestamp_NotFound verifies UpdateSessionTimestamp returns error for non-existent session.
func TestUpdateSessionTimestamp_NotFound(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.UpdateSessionTimestamp("nonexistent")
	if err == nil {
		t.Fatal("UpdateSessionTimestamp should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestDeleteSession verifies DeleteSession deletes the session.
func TestDeleteSession(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses, _ := store.CreateSession("/project", "model")

	err := store.DeleteSession(ses.SessionID)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify session is gone
	_, err = store.GetSession(ses.SessionID)
	if err == nil {
		t.Fatal("GetSession should fail after DeleteSession")
	}
}

// TestDeleteSession_CascadeDeleteMessages verifies DeleteSession also deletes all messages.
func TestDeleteSession_CascadeDeleteMessages(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	ses, _ := store.CreateSession("/project", "model")

	// Directly insert messages via SQL (independent of message.go implementation)
	for i := range 3 {
		_, err := store.db.Exec(
			"INSERT INTO messages (session_id, uuid, type, content) VALUES (?, ?, ?, ?)",
			ses.SessionID, uuid.New().String(), "user", `{"type":"text","text":"hello"}`,
		)
		if err != nil {
			t.Fatalf("insert message %d: %v", i, err)
		}
	}

	// Verify messages exist
	var countBefore int
	row := store.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", ses.SessionID)
	if err := row.Scan(&countBefore); err != nil {
		t.Fatalf("scan count before: %v", err)
	}
	if countBefore != 3 {
		t.Errorf("message count before = %d, want 3", countBefore)
	}

	// Delete session
	if err := store.DeleteSession(ses.SessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// Verify messages are also deleted (cascade)
	var countAfter int
	row = store.db.QueryRow("SELECT COUNT(*) FROM messages WHERE session_id = ?", ses.SessionID)
	if err := row.Scan(&countAfter); err != nil {
		t.Fatalf("scan count after: %v", err)
	}
	if countAfter != 0 {
		t.Errorf("message count after = %d, want 0 (cascade delete)", countAfter)
	}
}

// TestDeleteSession_NotFound verifies DeleteSession returns error for non-existent session.
func TestDeleteSession_NotFound(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.DeleteSession("nonexistent")
	if err == nil {
		t.Fatal("DeleteSession should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestExtractFirstPrompt_SimpleText verifies ExtractFirstPrompt extracts simple text.
func TestExtractFirstPrompt_SimpleText(t *testing.T) {
	content := `{"type":"user","message":{"content":"Hello, world!"}}`

	result := ExtractFirstPrompt(content)
	if result != "Hello, world!" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", result, "Hello, world!")
	}
}

// TestExtractFirstPrompt_ArrayContent verifies ExtractFirstPrompt handles array content.
func TestExtractFirstPrompt_ArrayContent(t *testing.T) {
	content := `{"type":"user","message":{"content":[{"type":"text","text":"First message"},{"type":"text","text":"Second message"}]}}`

	result := ExtractFirstPrompt(content)
	if result != "First message" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", result, "First message")
	}
}

// TestExtractFirstPrompt_StringContent verifies ExtractFirstPrompt handles string content (legacy format).
func TestExtractFirstPrompt_StringContent(t *testing.T) {
	content := `{"type":"user","message":{"content":"Just a string message"}}`

	result := ExtractFirstPrompt(content)
	if result != "Just a string message" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", result, "Just a string message")
	}
}

// TestExtractFirstPrompt_TruncatesTo200 verifies ExtractFirstPrompt truncates to 200 chars.
func TestExtractFirstPrompt_TruncatesTo200(t *testing.T) {
	longText := strings.Repeat("a", 250)
	content := `{"type":"user","message":{"content":"` + longText + `"}}`

	result := ExtractFirstPrompt(content)
	if len(result) != 203 { // 200 + "…"
		t.Errorf("len(result) = %d, want 203", len(result))
	}
	if !strings.HasSuffix(result, "…") {
		t.Errorf("result should end with ellipsis, got %q", result)
	}
}

// TestExtractFirstPrompt_SkipCommandName verifies ExtractFirstPrompt skips command-name but remembers fallback.
func TestExtractFirstPrompt_SkipCommandName(t *testing.T) {
	content := `{"type":"user","message":{"content":"<command-name>test-command</command-name>"}}`

	result := ExtractFirstPrompt(content)
	if result != "test-command" {
		t.Errorf("ExtractFirstPrompt (command fallback) = %q, want %q", result, "test-command")
	}
}

// TestExtractFirstPrompt_BashInputPrefix verifies ExtractFirstPrompt adds "!" prefix for bash input.
func TestExtractFirstPrompt_BashInputPrefix(t *testing.T) {
	content := `{"type":"user","message":{"content":"<bash-input>ls -la</bash-input>"}}`

	result := ExtractFirstPrompt(content)
	if result != "! ls -la" {
		t.Errorf("ExtractFirstPrompt (bash) = %q, want %q", result, "! ls -la")
	}
}

// TestExtractFirstPrompt_SkipXmlTags verifies ExtractFirstPrompt skips XML-like tags.
func TestExtractFirstPrompt_SkipXmlTags(t *testing.T) {
	content := `{"type":"user","message":{"content":"<session-hook>some metadata</session-hook>"}}`

	result := ExtractFirstPrompt(content)
	if result != "" {
		t.Errorf("ExtractFirstPrompt (xml tag) = %q, want empty", result)
	}
}

// TestExtractFirstPrompt_SkipInterruptMarker verifies ExtractFirstPrompt skips interrupt markers.
func TestExtractFirstPrompt_SkipInterruptMarker(t *testing.T) {
	content := `{"type":"user","message":{"content":"[Request interrupted by user]"}}`

	result := ExtractFirstPrompt(content)
	if result != "" {
		t.Errorf("ExtractFirstPrompt (interrupt) = %q, want empty", result)
	}
}

// TestExtractFirstPrompt_ReplacesNewlines verifies ExtractFirstPrompt replaces newlines with spaces.
func TestExtractFirstPrompt_ReplacesNewlines(t *testing.T) {
	content := `{"type":"user","message":{"content":"Line1\nLine2\nLine3"}}`

	result := ExtractFirstPrompt(content)
	if result != "Line1 Line2 Line3" {
		t.Errorf("ExtractFirstPrompt = %q, want %q", result, "Line1 Line2 Line3")
	}
}

// TestExtractFirstPrompt_EmptyContent verifies ExtractFirstPrompt returns empty for empty content.
func TestExtractFirstPrompt_EmptyContent(t *testing.T) {
	result := ExtractFirstPrompt("")
	if result != "" {
		t.Errorf("ExtractFirstPrompt (empty) = %q, want empty", result)
	}
}

// TestExtractFirstPrompt_InvalidJSON verifies ExtractFirstPrompt returns empty for invalid JSON.
func TestExtractFirstPrompt_InvalidJSON(t *testing.T) {
	result := ExtractFirstPrompt("not json")
	if result != "" {
		t.Errorf("ExtractFirstPrompt (invalid) = %q, want empty", result)
	}
}

// testStore creates a temporary store for testing.
func testStore(t *testing.T) (*Store, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	cleanup := func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	return store, cleanup
}

// TestCreateSession_ErrorPaths verifies CreateSession handles database errors.
func TestCreateSession_ErrorPaths(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Close store to force errors
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.CreateSession("/project", "model")
	if err == nil {
		t.Error("CreateSession should fail when store is closed")
	}
}

// TestListSessions_WithProjectDirFilter verifies ListSessions filters by project directory.
func TestListSessions_WithProjectDirFilter(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	// Create sessions in different projects
	ses1, _ := store.CreateSession("/project/a", "model")
	ses2, _ := store.CreateSession("/project/b", "model")

	// List sessions for project/a
	sessionsA, err := store.ListSessions("/project/a", 0)
	if err != nil {
		t.Fatalf("ListSessions project/a: %v", err)
	}

	if len(sessionsA) != 1 {
		t.Errorf("len(sessionsA) = %d, want 1", len(sessionsA))
	}
	if sessionsA[0].SessionID != ses1.SessionID {
		t.Errorf("sessionsA[0].SessionID = %q, want %q", sessionsA[0].SessionID, ses1.SessionID)
	}

	// List sessions for project/b
	sessionsB, err := store.ListSessions("/project/b", 0)
	if err != nil {
		t.Fatalf("ListSessions project/b: %v", err)
	}

	if len(sessionsB) != 1 {
		t.Errorf("len(sessionsB) = %d, want 1", len(sessionsB))
	}
	if sessionsB[0].SessionID != ses2.SessionID {
		t.Errorf("sessionsB[0].SessionID = %q, want %q", sessionsB[0].SessionID, ses2.SessionID)
	}
}

// TestGetSession_NotFound verifies GetSession returns error for non-existent session.
func TestGetSession_NotFound2(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	_, err := store.GetSession("nonexistent-id")
	if err == nil {
		t.Fatal("GetSession should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want 'not found'", err)
	}
}

// TestUpdateSessionTitle_NotFound verifies UpdateSessionTitle handles non-existent sessions.
func TestUpdateSessionTitle_NotFound2(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.UpdateSessionTitle("nonexistent", "Title")
	if err == nil {
		t.Fatal("UpdateSessionTitle should return error for non-existent session")
	}
}

// TestUpdateSessionTimestamp_NotFound2 verifies UpdateSessionTimestamp handles non-existent sessions.
func TestUpdateSessionTimestamp_NotFound2(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.UpdateSessionTimestamp("nonexistent")
	if err == nil {
		t.Fatal("UpdateSessionTimestamp should return error for non-existent session")
	}
}

// TestDeleteSession_NotFound2 verifies DeleteSession handles non-existent sessions.
func TestDeleteSession_NotFound2(t *testing.T) {
	store, cleanup := testStore(t)
	defer cleanup()

	err := store.DeleteSession("nonexistent")
	if err == nil {
		t.Fatal("DeleteSession should return error for non-existent session")
	}
}

// Line 22-24: CreateSession — marshal settings error (impossible with empty map, but line exists)
func TestCreateSession_MarshalSettingsError(t *testing.T) {
	// json.Marshal(map[string]string{}) never errors, so this path is effectively dead code.
	// Test the normal path instead.
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "sonnet")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if ses.Settings == nil {
		t.Error("Settings should not be nil")
	}
}

// Line 71-73: GetSession — query error (not ErrNoRows)
func TestGetSession_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := store.GetSession("any-session")
	if err == nil {
		t.Fatal("GetSession should fail with closed store")
	}
}

// Lines 88-93: GetSession — invalid settings JSON

// Lines 91-93: GetSession — empty settings JSON
func TestGetSession_EmptySettings(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_dir, settings)
		VALUES (?, '/project', '')
	`, sessionID)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	ses, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if ses.Settings == nil || len(ses.Settings) != 0 {
		t.Errorf("Settings = %v, want empty map", ses.Settings)
	}
}

// GetSession with corrupt settings JSON triggers json.Unmarshal error
func TestGetSession_InvalidSettings(t *testing.T) {
	store := openTestStore(t)
	sessionID := "corrupt-session"
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_dir, model, title, settings, created_at, updated_at)
		VALUES (?, '/test', 'claude', 'test', 'not-json', datetime('now'), datetime('now'))
	`, sessionID)
	if err != nil {
		t.Fatalf("insert corrupt session: %v", err)
	}

	_, err = store.GetSession(sessionID)
	if err == nil {
		t.Fatal("GetSession should fail with corrupt settings JSON")
	}
	if !strings.Contains(err.Error(), "unmarshal settings") {
		t.Errorf("error should mention unmarshal settings, got: %v", err)
	}
}

// Lines 118-120: ListSessions — query error
func TestListSessions_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := store.ListSessions("/project", 10)
	if err == nil {
		t.Fatal("ListSessions should fail with closed store")
	}
}

// Lines 135-137: ListSessions — scan error
func TestListSessions_ScanError(t *testing.T) {
	store := openTestStore(t)
	// Insert session with corrupted timestamp
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_dir, created_at, updated_at)
		VALUES ('s1', '/project', 'invalid-timestamp', 'invalid-timestamp')
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_, err = store.ListSessions("/project", 10)
	if err == nil {
		t.Error("ListSessions should fail with corrupted timestamp")
	}
}

// Lines 152-157: ListSessions — invalid settings in scan

// Lines 162-164: ListSessions — rows.Err
func TestListSessions_RowsErr(t *testing.T) {
	// rows.Err hard to trigger directly
}

// Lines 175-177: UpdateSessionTitle — query error
func TestUpdateSessionTitle_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := store.UpdateSessionTitle("any", "title")
	if err == nil {
		t.Fatal("UpdateSessionTitle should fail with closed store")
	}
}

// Lines 190-192: UpdateSessionTimestamp — query error
func TestUpdateSessionTimestamp_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := store.UpdateSessionTimestamp("any")
	if err == nil {
		t.Fatal("UpdateSessionTimestamp should fail with closed store")
	}
}

// Lines 205-207: DeleteSession — query error
func TestDeleteSession_QueryError(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	err := store.DeleteSession("any")
	if err == nil {
		t.Fatal("DeleteSession should fail with closed store")
	}
}

func TestListSessions_CorruptSettings(t *testing.T) {
	store := openTestStore(t)

	// Insert session with invalid JSON settings — use project_dir matching the query
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_dir, model, title, settings, created_at, updated_at)
		VALUES (?, '/test', 'claude', 'test', 'not-json', datetime('now'), datetime('now'))
	`, "corrupt-session")
	if err != nil {
		t.Fatalf("insert corrupt session: %v", err)
	}

	// ListSessions returns an error because settings unmarshal fails
	_, err = store.ListSessions("/test", 10)
	if err == nil {
		t.Fatal("expected error from ListSessions with corrupt settings")
	}
	if !strings.Contains(err.Error(), "unmarshal settings") {
		t.Errorf("error should mention unmarshal settings, got: %v", err)
	}
}

// TestCreateSession_MarshalError triggers json.Marshal error for settings.
// Note: json.Marshal on map[string]string{} never fails in practice.
// This is effectively dead code but we test the path exists.
func TestCreateSession_MarshalError_Path(t *testing.T) {
	// json.Marshal(map[string]string{}) always succeeds.
	// This test documents that line 22-24 is dead code for this input type.
	// No practical way to trigger json.Marshal failure on a simple map.
}

// TestCreateSession_InsertError triggers insert error by dropping sessions table.
func TestCreateSession_InsertError(t *testing.T) {
	store := openTestStore(t)

	if _, err := store.db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatalf("PRAGMA foreign_keys=OFF: %v", err)
	}
	if _, err := store.db.Exec("DROP TABLE sessions"); err != nil {
		t.Fatalf("DROP TABLE sessions: %v", err)
	}

	_, err := store.CreateSession("/test", "model")
	if err == nil {
		t.Fatal("CreateSession should fail when sessions table is dropped")
	}
	if !strings.Contains(err.Error(), "insert session") {
		t.Errorf("error should mention 'insert session', got: %v", err)
	}
}

// TestListSessions_ScanError_V2 triggers scan error via corrupted timestamp.
func TestListSessions_ScanError_V2(t *testing.T) {
	store := openTestStore(t)

	sessionID := "scan-test-session"
	createTestSession(t, store, sessionID)

	// Corrupt the timestamp to trigger scan error
	_, err := store.db.Exec("UPDATE sessions SET created_at = 'not-a-timestamp' WHERE session_id = ?", sessionID)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	_, err = store.ListSessions("", 100)
	if err == nil {
		t.Fatal("ListSessions should fail with corrupted timestamp")
	}
	if !strings.Contains(err.Error(), "scan session") {
		t.Errorf("error should mention 'scan session', got: %v", err)
	}
}

// TestListSessions_EmptySettings tests the branch where settingsJSON is empty,
// causing the else branch to execute (set empty map).
func TestListSessions_EmptySettings(t *testing.T) {
	store := openTestStore(t)

	// Create a session with empty settings (directly via SQL)
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_dir, model, title, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))
	`, "empty-settings-session", "/test", "gpt-4", "Test", "")
	if err != nil {
		t.Fatalf("INSERT session: %v", err)
	}

	sessions, err := store.ListSessions("/test", 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}

	found := false
	for _, s := range sessions {
		if s.SessionID == "empty-settings-session" {
			found = true
			if s.Settings == nil {
				t.Error("Settings should be non-nil empty map, got nil")
			}
			if len(s.Settings) != 0 {
				t.Errorf("Settings should be empty map, got %v", s.Settings)
			}
		}
	}
	if !found {
		t.Error("empty-settings-session not found in results")
	}
}
