package short

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fork.go:20-21 — a failed parent load is wrapped, and must not be mistaken
// for "parent not found".
func TestForkSession_LoadParentErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "fork-load-parent")
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close write pool: %v", err)
	}

	child, err := store.ForkSession("fork-load-parent", 0, "Explore")
	wantErrWrappingCause(t, err, "load parent session: query session: sql: database is closed")
	if child != nil {
		t.Errorf("child = %+v, want nil on load error", child)
	}
}

// fork.go:31-41 — the fork child copies every inherited parent field and
// stamps its own creation times.
func TestForkSession_InheritsParentFields(t *testing.T) {
	store := openTestStore(t)
	parentID := "fork-parent-fields"
	if _, err := store.DB().Exec(`
		INSERT INTO sessions (session_id, project_dir, model, title, parent_session_id,
		                      fork_point_seq, agent_type, mode, settings, context_tokens,
		                      created_at, updated_at)
		VALUES (?, ?, ?, ?, '', 0, 'main', 'plan', '{"theme":"dark"}', 12345,
		        datetime('now'), datetime('now'))`,
		parentID, "/proj/dir", "opus-4", "Parent title"); err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	start := time.Now().UTC().Truncate(time.Millisecond) // REAL-TIME: ForkSession stamps time.Now(); only a lower bound is asserted
	child, err := store.ForkSession(parentID, 7, "Explore")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	if child.SessionID == "" || child.SessionID == parentID {
		t.Errorf("child SessionID = %q, want a fresh id distinct from %q", child.SessionID, parentID)
	}
	if child.ProjectDir != "/proj/dir" {
		t.Errorf("ProjectDir = %q, want /proj/dir", child.ProjectDir)
	}
	if child.Model != "opus-4" {
		t.Errorf("Model = %q, want opus-4", child.Model)
	}
	if child.Title != "Parent title" {
		t.Errorf("Title = %q, want 'Parent title'", child.Title)
	}
	if child.ParentSessionID != parentID {
		t.Errorf("ParentSessionID = %q, want %q", child.ParentSessionID, parentID)
	}
	if child.ForkPointSeq != 7 {
		t.Errorf("ForkPointSeq = %d, want 7", child.ForkPointSeq)
	}
	if child.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", child.AgentType)
	}
	if child.Mode != "plan" {
		t.Errorf("Mode = %q, want plan", child.Mode)
	}
	if len(child.Settings) != 1 || child.Settings["theme"] != "dark" {
		t.Errorf("Settings = %v, want map[theme:dark]", child.Settings)
	}
	if child.ContextTokens != 12345 {
		t.Errorf("ContextTokens = %d, want 12345", child.ContextTokens)
	}
	for name, got := range map[string]time.Time{"CreatedAt": child.CreatedAt, "UpdatedAt": child.UpdatedAt} {
		if got.IsZero() {
			t.Errorf("%s is zero, want the fork time", name)
		}
		if got.Before(start) {
			t.Errorf("%s = %v, want no earlier than %v", name, got, start)
		}
	}
}

// fork.go:45 — an insert failure on the sessions row is wrapped with context.
func TestForkSession_InsertErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "fork-ins-parent")
	if _, err := store.DB().Exec(
		`CREATE TRIGGER blk_fork_ins BEFORE INSERT ON sessions
		 BEGIN SELECT RAISE(ABORT, 'blocked by test trigger'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	child, err := store.ForkSession("fork-ins-parent", 0, "")
	wantErrWrappingCause(t, err, "insert fork session: constraint failed: blocked by test trigger (1811)")
	if child != nil {
		t.Errorf("child = %+v, want nil on insert error", child)
	}
}

// fork.go:50 — a message-copy failure aborts the fork with context.
func TestForkSession_CopyMessagesErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "fork-copy-parent")
	if _, err := store.DB().Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("drop messages: %v", err)
	}

	child, err := store.ForkSession("fork-copy-parent", 0, "")
	wantErrWrappingCause(t, err,
		"copy messages to fork: query parent messages: SQL logic error: no such table: messages (1)")
	if child != nil {
		t.Errorf("child = %+v, want nil on copy error", child)
	}
}

// fork.go:53 — the fork is logged with child, parent, fork point and dir.
func TestForkSession_LogsFork(t *testing.T) {
	logs := captureSlogDefault(t)

	store := openTestStore(t)
	createTestSession(t, store, "fork-log-parent")
	child, err := store.ForkSession("fork-log-parent", 3, "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	want := fmt.Sprintf(`msg="forked session" child=%s parent=fork-log-parent fork_seq=3 dir=`, child.SessionID)
	if !strings.Contains(logs.String(), want) {
		t.Errorf("log output = %q, want a record containing %q", logs.String(), want)
	}
}

// fork.go:63 — a failed BEGIN is reported with its cause.
func TestCopyMessagesToFork_BeginError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "cmf-begin-parent")
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close write pool: %v", err)
	}

	err := store.copyMessagesToFork("cmf-begin-parent", "cmf-begin-child", 0)
	wantErrWrappingCause(t, err, "begin transaction: sql: database is closed")
}

// fork.go:78 — a failed parent-message query is reported with its cause.
func TestCopyMessagesToFork_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "cmf-query-parent")
	if _, err := store.DB().Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("drop messages: %v", err)
	}

	err := store.copyMessagesToFork("cmf-query-parent", "cmf-query-child", 0)
	wantErrWrappingCause(t, err, "query parent messages: SQL logic error: no such table: messages (1)")
}

// fork.go:96 — an unscannable parent row is reported with its cause.
func TestCopyMessagesToFork_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "cmf-scan-parent")
	if _, err := store.DB().Exec(`INSERT INTO sessions (session_id, project_dir) VALUES ('cmf-scan-child','/p')`); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	// INTEGER-affinity column holding text: SQLite stores it as TEXT and the
	// driver refuses to scan it into an int.
	if _, err := store.DB().Exec(
		`INSERT INTO messages (session_id, uuid, type, subtype, content, is_sidechain)
		 VALUES ('cmf-scan-parent','cmf-bad','user','','[]','abc')`); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	err := store.copyMessagesToFork("cmf-scan-parent", "cmf-scan-child", 1)
	wantErrWrappingCause(t, err,
		`scan message: sql: Scan error on column index 4, name "is_sidechain": converting driver.Value type string ("abc") to a int: invalid syntax`)
}

// fork.go:125 — a failed child insert is reported with its cause.
func TestCopyMessagesToFork_InsertErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "cmf-ins-parent")
	if _, err := store.DB().Exec(
		`INSERT INTO messages (session_id, uuid, type, subtype, content) VALUES ('cmf-ins-parent','cmf-m1','user','','[]')`); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	err := store.copyMessagesToFork("cmf-ins-parent", "cmf-ins-ghost-child", 1)
	wantErrWrappingCause(t, err, "insert forked message: constraint failed: FOREIGN KEY constraint failed (787)")
}

// fork.go:148 — a failed children query is reported with its cause.
func TestGetForkChildren_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.readDB.Close(); err != nil {
		t.Fatalf("close read pool: %v", err)
	}

	children, err := store.GetForkChildren("any-parent")
	wantErrWrappingCause(t, err, "query fork children: sql: database is closed")
	if children != nil {
		t.Errorf("children = %v, want nil on query error", children)
	}
}

// fork.go:161 — an unscannable child row is reported with its cause.
func TestGetForkChildren_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.DB().Exec(`INSERT INTO sessions (session_id, project_dir) VALUES ('gfc-parent','/p')`); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, parent_session_id, fork_point_seq)
		 VALUES ('gfc-child','/p','gfc-parent','abc')`); err != nil {
		t.Fatalf("seed child: %v", err)
	}

	children, err := store.GetForkChildren("gfc-parent")
	wantErrWrappingCause(t, err,
		`scan session: sql: Scan error on column index 5, name "fork_point_seq": converting driver.Value type string ("abc") to a int: invalid syntax`)
	if children != nil {
		t.Errorf("children = %v, want nil on scan error", children)
	}
}

// fork.go:178 — a failed child-session load is reported with its cause.
func TestMergeForkBack_LoadChildError(t *testing.T) {
	store := openTestStore(t)
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close write pool: %v", err)
	}

	err := store.MergeForkBack("mfb-any-child")
	wantErrWrappingCause(t, err, "load child session: query session: sql: database is closed")
}

// fork.go:189 — a BEGIN blocked by another writer is reported, not skipped.
func TestMergeForkBack_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "mfb-lock-parent")
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, parent_session_id, fork_point_seq)
		 VALUES ('mfb-lock-child','/p','mfb-lock-parent',0)`); err != nil {
		t.Fatalf("seed child: %v", err)
	}

	// A second handle holding the write lock makes BEGIN IMMEDIATE time out
	// against busy_timeout(5000).
	hog, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatalf("open hog handle: %v", err)
	}
	defer hog.Close()
	tx, err := hog.Begin()
	if err != nil {
		t.Fatalf("hog begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO sessions (session_id, project_dir) VALUES ('mfb-hog','/p')`); err != nil {
		t.Fatalf("hog insert: %v", err)
	}

	err = store.MergeForkBack("mfb-lock-child")
	wantErrWrappingCause(t, err, "begin transaction: database is locked (5) (SQLITE_BUSY)")
	if rerr := tx.Rollback(); rerr != nil {
		t.Fatalf("hog rollback: %v", rerr)
	}
}

// fork.go:200 — a failed parent-last-seq read is reported with its cause.
func TestMergeForkBack_ParentLastSeqError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "mfb-seq-parent")
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, parent_session_id, fork_point_seq)
		 VALUES ('mfb-seq-child','/p','mfb-seq-parent',0)`); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if _, err := store.DB().Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("drop messages: %v", err)
	}

	err := store.MergeForkBack("mfb-seq-child")
	wantErrWrappingCause(t, err, "get parent last seq: SQL logic error: no such table: messages (1)")
}

// forkMergeFixture builds a parent with two messages and a fork whose child has
// two inherited copies plus two new messages, then merges it back.
func forkMergeFixture(t *testing.T, store *Store, tag string) (parentID, childID string) {
	t.Helper()
	parentID = tag + "-parent"
	childID = tag + "-child"
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, created_at, updated_at) VALUES (?, '/p', datetime('now'), datetime('now'))`,
		parentID); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	for _, u := range []string{tag + "-p1", tag + "-p2"} {
		if _, err := store.DB().Exec(
			`INSERT INTO messages (session_id, uuid, type, subtype, content) VALUES (?, ?, 'user', '', '[]')`,
			parentID, u); err != nil {
			t.Fatalf("seed parent message %s: %v", u, err)
		}
	}
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, parent_session_id, fork_point_seq, created_at, updated_at)
		 VALUES (?, '/p', ?, 0, datetime('now'), datetime('now'))`, childID, parentID); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	for _, u := range []string{tag + "-in1", tag + "-in2", tag + "-new1", tag + "-new2"} {
		if _, err := store.DB().Exec(
			`INSERT INTO messages (session_id, uuid, type, subtype, content) VALUES (?, ?, 'user', '', '[]')`,
			childID, u); err != nil {
			t.Fatalf("seed child message %s: %v", u, err)
		}
	}
	return parentID, childID
}

func parentRowsAfterMerge(t *testing.T, store *Store, parentID string) []struct{ uuid, parentUUID string } {
	t.Helper()
	rows, err := store.DB().Query(
		`SELECT uuid, parent_uuid FROM messages WHERE session_id = ? ORDER BY seq ASC`, parentID)
	if err != nil {
		t.Fatalf("query parent rows: %v", err)
	}
	defer rows.Close()
	var out []struct{ uuid, parentUUID string }
	for rows.Next() {
		var r struct{ uuid, parentUUID string }
		if err := rows.Scan(&r.uuid, &r.parentUUID); err != nil {
			t.Fatalf("scan parent row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate parent rows: %v", err)
	}
	return out
}

// fork.go:205,210,258 — merged messages chain onto the parent's last message
// and then onto each other.
func TestMergeForkBack_ChainsMergedMessages(t *testing.T) {
	store := openTestStore(t)
	parentID, childID := forkMergeFixture(t, store, "mfb-chain")

	if err := store.MergeForkBack(childID); err != nil {
		t.Fatalf("MergeForkBack: %v", err)
	}

	rows := parentRowsAfterMerge(t, store, parentID)
	if len(rows) != 4 {
		t.Fatalf("parent has %d rows, want 4 (2 original + 2 merged)", len(rows))
	}
	if rows[0].uuid != "mfb-chain-p1" || rows[1].uuid != "mfb-chain-p2" {
		t.Fatalf("original rows changed: %q, %q", rows[0].uuid, rows[1].uuid)
	}
	if rows[2].parentUUID != "mfb-chain-p2" {
		t.Errorf("first merged row parent_uuid = %q, want mfb-chain-p2 (the parent's last chain uuid)", rows[2].parentUUID)
	}
	if rows[3].parentUUID != rows[2].uuid {
		t.Errorf("second merged row parent_uuid = %q, want %q (the previous merged row)", rows[3].parentUUID, rows[2].uuid)
	}
	if rows[3].uuid == rows[2].uuid {
		t.Errorf("merged rows share uuid %q, want distinct ids", rows[3].uuid)
	}
}

// fork.go:236-238 — an unscannable child row is warned about and skipped; the
// rows after it still merge.
func TestMergeForkBack_SkipsUnscannableRow(t *testing.T) {
	logs := captureSlogDefault(t)

	store := openTestStore(t)
	parentID, childID := forkMergeFixture(t, store, "mfb-scan")
	// Replace the third child row (first one past the inherited window) with
	// an unparseable created_at.
	if _, err := store.DB().Exec(
		`UPDATE messages SET created_at = 'not-a-time' WHERE session_id = ? AND uuid = 'mfb-scan-new1'`,
		childID); err != nil {
		t.Fatalf("poison row: %v", err)
	}

	if err := store.MergeForkBack(childID); err != nil {
		t.Fatalf("MergeForkBack: %v", err)
	}

	rows := parentRowsAfterMerge(t, store, parentID)
	if len(rows) != 3 {
		t.Fatalf("parent has %d rows, want 3 (poisoned row skipped, later row merged)", len(rows))
	}
	if rows[2].parentUUID != "mfb-scan-p2" {
		t.Errorf("merged row parent_uuid = %q, want mfb-scan-p2", rows[2].parentUUID)
	}
	if got := logs.String(); !strings.Contains(got, `msg="fork: scan message row"`) {
		t.Errorf("log output = %q, want the scan-warning record", got)
	}
}

// fork.go:255 — a failed merged-message insert is reported with its cause.
func TestMergeForkBack_InsertError(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.DB().Exec(
		`INSERT INTO sessions (session_id, project_dir, parent_session_id, fork_point_seq)
		 VALUES ('mfb-ins-child','/p','mfb-ins-ghost-parent',0)`); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if _, err := store.DB().Exec(
		`INSERT INTO messages (session_id, uuid, type, subtype, content) VALUES ('mfb-ins-child','mfb-ins-c1','user','','[]')`); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	err := store.MergeForkBack("mfb-ins-child")
	wantErrWrappingCause(t, err, "insert merged message: constraint failed: FOREIGN KEY constraint failed (787)")
}
