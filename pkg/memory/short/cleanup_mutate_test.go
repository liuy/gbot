package short

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// createOldSession inserts a session whose updated_at is ageDays in the past,
// bypassing AppendMessage (which would refresh updated_at through the FTS update).
func createOldSession(t *testing.T, store *Store, sessionID string, ageDays int) {
	t.Helper()

	modifier := fmt.Sprintf("-%d days", ageDays)
	_, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, settings, created_at, updated_at)
		 VALUES (?, '', '{}', datetime('now', ?), datetime('now', ?))`,
		sessionID, modifier, modifier,
	)
	if err != nil {
		t.Fatalf("createOldSession %s: %v", sessionID, err)
	}
}

// captureSlog swaps the default logger for a buffer and returns what was written.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return buf
}

// cleanupErr asserts the three properties every CleanupOldSessions error must
// have: the exact context prefix, a zero count, and a real %w chain to the cause.
func cleanupErr(t *testing.T, removed int, err error, want string) {
	t.Helper()

	if err == nil {
		t.Fatalf("CleanupOldSessions returned nil error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("CleanupOldSessions error = %q, want %q", err.Error(), want)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 alongside the error", removed)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap its cause (%%w lost)", err.Error())
	}
}

func TestCleanupOldSessions_QueryFailure(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err, "query old sessions: sql: database is closed")
}

func TestCleanupOldSessions_ScanFailure(t *testing.T) {
	store := openTestStore(t)

	// A NULL session_id cannot scan into a string; the guard must abort rather
	// than carry an empty id into the delete loop.
	if _, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, created_at, updated_at)
		 VALUES (NULL, '', datetime('now', '-40 days'), datetime('now', '-40 days'))`,
	); err != nil {
		t.Fatalf("insert null-id session: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err,
		`scan session id: sql: Scan error on column index 0, name "session_id": converting NULL to string is unsupported`)
}

func TestCleanupOldSessions_BeginFailure(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "begin-blocked", 40)

	// The DSN pins busy_timeout(5000); shortening it on the single write conn
	// keeps the contended BEGIN failing in milliseconds instead of five seconds.
	if _, err := store.db.Exec("PRAGMA busy_timeout(200)"); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}

	lock, err := sql.Open("sqlite", store.DBPath()+"?_pragma=busy_timeout(50)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	defer func() { _ = lock.Close() }()
	lock.SetMaxOpenConns(1)

	lockTx, err := lock.Begin()
	if err != nil {
		t.Fatalf("lock begin: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()
	if _, err := lockTx.Exec("INSERT INTO sessions (session_id, project_dir) VALUES ('lock-holder', '')"); err != nil {
		t.Fatalf("lock write: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err, "begin: database is locked (5) (SQLITE_BUSY)")
}

func TestCleanupOldSessions_FTSDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "fts-drop-x", 40)

	if _, err := store.db.Exec("DROP TABLE messages_fts_map"); err != nil {
		t.Fatalf("drop messages_fts_map: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err,
		"delete FTS for session fts-drop: SQL logic error: no such table: messages_fts_map (1)")
}

func TestCleanupOldSessions_MessageDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "msg-trig-x", 40)

	if _, err := store.db.Exec(
		`INSERT INTO messages (session_id, uuid, type, content) VALUES ('msg-trig-x', 'msg-trig-uuid', 'user', 'x')`,
	); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if _, err := store.db.Exec(
		`CREATE TRIGGER block_msg_delete BEFORE DELETE ON messages BEGIN SELECT RAISE(ABORT, 'boom'); END;`,
	); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err, "delete messages for session msg-trig: constraint failed: boom (1811)")
}

func TestCleanupOldSessions_SessionDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "sess-trig-x", 40)

	if _, err := store.db.Exec(
		`CREATE TRIGGER block_sess_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'boom'); END;`,
	); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err, "delete session sess-tri: constraint failed: boom (1811)")
}

func TestCleanupOldSessions_CommitFailure(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "commit-trig", 40)

	// The trigger writes a row under a DEFERRABLE INITIALLY DEFERRED foreign key,
	// so the violation is only detected when the transaction commits.
	if _, err := store.db.Exec(
		`CREATE TABLE deferred_child (
			id     INTEGER PRIMARY KEY,
			parent TEXT REFERENCES sessions(session_id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER orphan_on_session_delete AFTER DELETE ON sessions BEGIN
			INSERT INTO deferred_child (id, parent) VALUES (1, 'no-such-session');
		END;`,
	); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	cleanupErr(t, removed, err, "commit: constraint failed: FOREIGN KEY constraint failed (787)")

	// The failed commit must leave the session in place, not half-deleted.
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE session_id = ?", "commit-trig",
	).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("sessions after failed commit = %d, want 1 (rollback must undo the delete)", count)
	}
}

func TestCleanupOldSessions_LogsRemovedCount(t *testing.T) {
	store := openTestStore(t)
	createOldSession(t, store, "logged-old-a", 40)
	createOldSession(t, store, "logged-old-b", 45)
	createTestSession(t, store, "logged-recent")

	logBuf := captureSlog(t)
	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	if !strings.Contains(logBuf.String(), `msg="cleanup: removed old sessions" count=2`) {
		t.Errorf("cleanup log = %q, want a record with count=2", logBuf.String())
	}
}

func TestCleanupOldSessions_NothingToDeleteLogsNothing(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "recent-only")

	logBuf := captureSlog(t)
	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}

	if logBuf.String() != "" {
		t.Errorf("cleanup logged %q, want no record when no session is old", logBuf.String())
	}
}
