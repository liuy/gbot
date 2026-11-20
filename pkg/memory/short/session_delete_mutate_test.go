package short

import (
	"database/sql"
	"errors"
	"testing"
)

// createEngineSessionRaw inserts a session bound to engineID directly,
// bypassing CreateSessionWithEngine (fixed ids so error-message id[:8]
// prefixes are assertable).
func createEngineSessionRaw(t *testing.T, store *Store, sessionID, engineID string) {
	t.Helper()
	_, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, settings, engine_id, created_at, updated_at)
		 VALUES (?, '', '{}', ?, datetime('now'), datetime('now'))`,
		sessionID, engineID,
	)
	if err != nil {
		t.Fatalf("createEngineSessionRaw %s: %v", sessionID, err)
	}
}

// engineDeleteErr mirrors cleanupErr: exact context, zero count, %w chain.
func engineDeleteErr(t *testing.T, removed int, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("DeleteSessionsByEngine returned nil error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("DeleteSessionsByEngine error = %q, want %q", err.Error(), want)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 alongside the error", removed)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap its cause (%%w lost)", err.Error())
	}
}

func TestDeleteSessionsByEngine_QueryFailure(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	removed, err := store.DeleteSessionsByEngine("/proj", "e2")
	engineDeleteErr(t, removed, err, "query engine sessions: sql: database is closed")
}

func TestDeleteSessionsByEngine_ScanFailure(t *testing.T) {
	store := openTestStore(t)
	// A NULL session_id cannot scan into string; the guard must abort
	// before the delete loop sees an empty id.
	if _, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, settings, engine_id, created_at, updated_at)
		 VALUES (NULL, '/proj', '{}', 'e2', datetime('now'), datetime('now'))`,
	); err != nil {
		t.Fatalf("insert null-id session: %v", err)
	}
	removed, err := store.DeleteSessionsByEngine("/proj", "e2")
	engineDeleteErr(t, removed, err,
		`scan session id: sql: Scan error on column index 0, name "session_id": converting NULL to string is unsupported`)
}

func TestDeleteSessionsByEngine_BeginFailure(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "begin-blocked", "e2")
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
	removed, err := store.DeleteSessionsByEngine("", "e2")
	engineDeleteErr(t, removed, err, "begin: database is locked (5) (SQLITE_BUSY)")
}

func TestDeleteSessionsByEngine_FTSDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "fts-drop-x", "e2")
	if _, err := store.db.Exec("DROP TABLE messages_fts_map"); err != nil {
		t.Fatalf("drop messages_fts_map: %v", err)
	}
	removed, err := store.DeleteSessionsByEngine("", "e2")
	engineDeleteErr(t, removed, err,
		"delete FTS for session fts-drop: SQL logic error: no such table: messages_fts_map (1)")
}

func TestDeleteSessionsByEngine_MessageDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "msg-trig-x", "e2")
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
	removed, err := store.DeleteSessionsByEngine("", "e2")
	engineDeleteErr(t, removed, err, "delete messages for session msg-trig: constraint failed: boom (1811)")
}

func TestDeleteSessionsByEngine_SessionDeleteFailure(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "sess-trig-x", "e2")
	if _, err := store.db.Exec(
		`CREATE TRIGGER block_sess_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'boom'); END;`,
	); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	removed, err := store.DeleteSessionsByEngine("", "e2")
	engineDeleteErr(t, removed, err, "delete session sess-tri: constraint failed: boom (1811)")
}

func TestDeleteSessionsByEngine_CommitFailure(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "commit-trig", "e2")
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
	removed, err := store.DeleteSessionsByEngine("", "e2")
	engineDeleteErr(t, removed, err, "commit: constraint failed: FOREIGN KEY constraint failed (787)")
	// The failed commit must leave the session in place, not half-deleted.
	var count int
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM sessions WHERE session_id = ?", "commit-trig",
	).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("session survived failed commit = %d rows, want 1", count)
	}
}

func TestDeleteSessionsByEngine_RemovedCountIsExact(t *testing.T) {
	store := openTestStore(t)
	createEngineSessionRaw(t, store, "e2-first", "e2")
	createEngineSessionRaw(t, store, "e2-second", "e2")
	createEngineSessionRaw(t, store, "main-first", "main")

	removed, err := store.DeleteSessionsByEngine("", "e2")
	if err != nil {
		t.Fatalf("DeleteSessionsByEngine: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	var left int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE engine_id = 'e2'").Scan(&left); err != nil {
		t.Fatalf("count e2: %v", err)
	}
	if left != 0 {
		t.Errorf("e2 rows left = %d, want 0", left)
	}
}

func TestDeleteSessionsByEngine_EmptySkipsTransaction(t *testing.T) {
	store := openTestStore(t)
	// A held write lock makes any BEGIN fail; an engine with no sessions
	// must never open a transaction, so the call still succeeds.
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
	removed, err := store.DeleteSessionsByEngine("", "no-such-engine")
	if err != nil {
		t.Fatalf("empty engine delete under lock: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
}
