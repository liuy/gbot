package short

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// indexRawMessage writes a message row plus its FTS entry directly, so columns
// AppendMessage never leaves NULL can be corrupted on purpose.
func indexRawMessage(t *testing.T, store *Store, sessionID, uuid, content string, cols string, args ...any) int64 {
	t.Helper()

	query := `INSERT INTO messages (session_id, uuid, type, content` + cols + `) VALUES (?, ?, 'user', ?` +
		strings.Repeat(", ?", len(args)) + `) RETURNING seq`
	var seq int64
	all := append([]any{sessionID, uuid, content}, args...)
	if err := store.db.QueryRow(query, all...).Scan(&seq); err != nil {
		t.Fatalf("insert message %s: %v", uuid, err)
	}
	if err := store.insertFTS(store.db, seq, content); err != nil {
		t.Fatalf("insertFTS %s: %v", uuid, err)
	}
	return seq
}

// countSlogRecords counts how many times a message was logged, which is how
// per-row warn loops are pinned (a loop that stops early logs less).
func countSlogRecords(buf *bytes.Buffer, msg string) int {
	return strings.Count(buf.String(), `msg="`+msg+`"`)
}

// insertSearchSession creates a session row with extra columns set and returns
// its id.
func insertSearchSession(t *testing.T, store *Store, sessionID, cols string, args ...any) {
	t.Helper()

	query := `INSERT INTO sessions (session_id, project_dir, created_at, updated_at, ` + cols + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", 4+len(args)), ", ") + `)`
	all := append([]any{sessionID, "/search/proj", testTimeBase, testTimeBase}, args...)
	if _, err := store.db.Exec(query, all...); err != nil {
		t.Fatalf("insert session %s: %v", sessionID, err)
	}
}

// --- validMessageTypes allowlist ---

// Every allowlisted type must survive validation; a dropped map entry turns a
// legal filter into "invalid message type".
func TestSearchMessages_AllMessageTypesAccepted(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	indexRawMessage(t, store, sess.SessionID, "am-1", `[{"type":"text","text":"allowlist probe"}]`, "")

	for _, msgType := range []string{"user", "assistant", "system", "progress", "attachment", "result"} {
		_, err := store.SearchMessages("allowlist", &SearchOptions{
			SessionID: sess.SessionID,
			Types:     []string{msgType},
			Limit:     10,
		})
		if err != nil {
			t.Errorf("Types=[%s]: got error %v, want the type accepted", msgType, err)
		}
	}
}

// --- isMalformedFTSError ---

func TestIsMalformedFTSError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not malformed", nil, false},
		{"fts5 keyword only", errors.New("fts5: out of memory"), true},
		{"uppercase fts5 is lowercased first", errors.New("FTS5: bad query"), true},
		{"syntax error only", errors.New(`near "(": syntax error`), true},
		{"unrelated error", errors.New("no such column: x"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMalformedFTSError(tt.err); got != tt.want {
				t.Errorf("isMalformedFTSError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// --- SearchMessages option defaults ---

// limit 1 must still take the LIMIT branch.
func TestSearchMessages_LimitOne(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 3 {
		indexRawMessage(t, store, sess.SessionID, fmt.Sprintf("lim-%d", i),
			fmt.Sprintf(`[{"type":"text","text":"limitprobe alpha %d"}]`, i), "")
	}

	results, err := store.SearchMessages("limitprobe", &SearchOptions{SessionID: sess.SessionID, Limit: 1})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1 (limit 1 must apply)", len(results))
	}
}

// The default limit is exactly 100: a 99 or 101 default changes the page size.
func TestSearchMessages_DefaultLimitIs100(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 101 {
		indexRawMessage(t, store, sess.SessionID, fmt.Sprintf("dl-%d", i),
			fmt.Sprintf(`[{"type":"text","text":"defaultlimit %d"}]`, i), "")
	}

	results, err := store.SearchMessages("defaultlimit", &SearchOptions{SessionID: sess.SessionID})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 100 {
		t.Errorf("len(results) = %d, want 100 (default limit)", len(results))
	}
}

// An explicit offset must skip rows rather than be clamped back to zero.
func TestSearchMessages_OffsetSkipsRows(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for i := range 3 {
		indexRawMessage(t, store, sess.SessionID, fmt.Sprintf("off-%d", i),
			fmt.Sprintf(`[{"type":"text","text":"offsetprobe %s"}]`, strings.Repeat("w", i+1)), "")
	}

	full, err := store.SearchMessages("offsetprobe", &SearchOptions{SessionID: sess.SessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages offset 0: %v", err)
	}
	if len(full) != 3 {
		t.Fatalf("offset 0: len(results) = %d, want 3", len(full))
	}

	paged, err := store.SearchMessages("offsetprobe", &SearchOptions{SessionID: sess.SessionID, Limit: 10, Offset: 1})
	if err != nil {
		t.Fatalf("SearchMessages offset 1: %v", err)
	}
	if len(paged) != 2 {
		t.Fatalf("offset 1: len(results) = %d, want 2", len(paged))
	}
	if paged[0].TranscriptMessage.UUID != full[1].TranscriptMessage.UUID {
		t.Errorf("offset 1 first row = %s, want %s (the row after the skipped one)",
			paged[0].TranscriptMessage.UUID, full[1].TranscriptMessage.UUID)
	}
}

// The session filter must narrow the result set to the requested session.
func TestSearchMessages_SessionFilterNarrows(t *testing.T) {
	store := openTestStore(t)
	sessA, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession A: %v", err)
	}
	sessB, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession B: %v", err)
	}
	indexRawMessage(t, store, sessA.SessionID, "sf-a", `[{"type":"text","text":"sharedterm one"}]`, "")
	indexRawMessage(t, store, sessB.SessionID, "sf-b", `[{"type":"text","text":"sharedterm two"}]`, "")

	results, err := store.SearchMessages("sharedterm", &SearchOptions{SessionID: sessA.SessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (session filter must apply)", len(results))
	}
	if results[0].TranscriptMessage.SessionID != sessA.SessionID {
		t.Errorf("result session = %q, want %q", results[0].TranscriptMessage.SessionID, sessA.SessionID)
	}
}

// --- SearchMessages row scan loop ---

// A row that fails to scan is warned about and skipped: both the warning and
// the skip must happen once per bad row, and the loop must keep going.
func TestSearchMessages_ScanFailureWarnsAndSkipsEachRow(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	indexRawMessage(t, store, sess.SessionID, "ok-1", `[{"type":"text","text":"scanloop good"}]`, "")
	indexRawMessage(t, store, sess.SessionID, "bad-1", `[{"type":"text","text":"scanloop bad1"}]`, ", is_sidechain", nil)
	indexRawMessage(t, store, sess.SessionID, "bad-2", `[{"type":"text","text":"scanloop bad2"}]`, ", is_sidechain", nil)

	logBuf := captureSlog(t)
	results, err := store.SearchMessages("scanloop", &SearchOptions{SessionID: sess.SessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1 (both corrupt rows must be dropped)", len(results))
	}
	if results[0].TranscriptMessage.UUID != "ok-1" {
		t.Errorf("result uuid = %q, want ok-1", results[0].TranscriptMessage.UUID)
	}
	if n := countSlogRecords(logBuf, "search: scan message row"); n != 2 {
		t.Errorf("scan warnings logged = %d, want 2 (one per corrupt row)", n)
	}
}

// NULL-able message columns must be materialised onto the returned message.
func TestSearchMessages_NullableColumnsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	sess, err := store.CreateSession("/search/proj", "m")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	indexRawMessage(t, store, sess.SessionID, "nc-1",
		`[{"type":"text","text":"nullcols probe"}]`,
		", parent_uuid, logical_parent_uuid, subtype, metadata",
		"pu-1", "lpu-1", "st-1", `{"m":1}`)

	results, err := store.SearchMessages("nullcols", &SearchOptions{SessionID: sess.SessionID, Limit: 10})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}

	got := results[0].TranscriptMessage
	if got.ParentUUID != "pu-1" {
		t.Errorf("ParentUUID = %q, want pu-1", got.ParentUUID)
	}
	if got.LogicalParentUUID != "lpu-1" {
		t.Errorf("LogicalParentUUID = %q, want lpu-1", got.LogicalParentUUID)
	}
	if got.Subtype != "st-1" {
		t.Errorf("Subtype = %q, want st-1", got.Subtype)
	}
	if got.Metadata != `{"m":1}` {
		t.Errorf("Metadata = %q, want %q", got.Metadata, `{"m":1}`)
	}
}

// --- normalizeScores ---

// A single-hit batch must still normalise to 1.0; an early return for
// len(results) == 1 leaks the raw negative bm25 rank.
func TestNormalizeScores_SingleHitNormalizesTo1(t *testing.T) {
	rs := []*SearchResult{{Score: -5.9}}
	normalizeScores(rs)
	if rs[0].Score != 1.0 {
		t.Errorf("single-hit score = %v, want 1.0", rs[0].Score)
	}
}

// The all-zero batch pins exact +0.0 so -0.0 never reaches the JSON layer.
func TestNormalizeScores_NegativeZeroPinnedToPositive(t *testing.T) {
	rs := []*SearchResult{{Score: math.Copysign(0, -1)}, {Score: math.Copysign(0, -1)}}
	normalizeScores(rs)
	for i, r := range rs {
		if r.Score != 0 {
			t.Errorf("rs[%d].Score = %v, want 0", i, r.Score)
		}
		if math.Signbit(r.Score) {
			t.Errorf("rs[%d].Score is negative zero, want +0.0", i)
		}
	}
}

// --- SearchSessions ---

func TestSearchSessions_LimitOne(t *testing.T) {
	store := openTestStore(t)
	for i := range 2 {
		id := fmt.Sprintf("sl-%d", i)
		insertSearchSession(t, store, id, "settings", "{}")
		indexRawMessage(t, store, id, "slm-"+id, `[{"type":"text","text":"sesslimit probe"}]`, "")
	}

	results, err := store.SearchSessions("sesslimit", "/search/proj", 1)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1 (limit 1 must apply)", len(results))
	}
}

// The default session limit is exactly 50.
func TestSearchSessions_DefaultLimitIs50(t *testing.T) {
	store := openTestStore(t)
	for i := range 51 {
		id := fmt.Sprintf("dl-%d", i)
		insertSearchSession(t, store, id, "settings", "{}")
		indexRawMessage(t, store, id, "dlm-"+id, `[{"type":"text","text":"sessdefault probe"}]`, "")
	}

	results, err := store.SearchSessions("sessdefault", "/search/proj", 0)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(results) != 50 {
		t.Errorf("len(results) = %d, want 50 (default limit)", len(results))
	}
}

func TestSearchMessages_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.SearchMessages("anything", &SearchOptions{Limit: 10})
	assertWrappedErr(t, err, "search messages: ")
}

func TestSearchSessions_QueryErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := store.SearchSessions("anything", "/search/proj", 10)
	assertWrappedErr(t, err, "search sessions: ")
}

func TestSearchSessions_ScanErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec(
		`INSERT INTO sessions (session_id, project_dir, settings, created_at, updated_at)
		 VALUES ('st-1', '/search/proj', '{}', 'not-a-ts', 'not-a-ts')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	indexRawMessage(t, store, "st-1", "stm-1", `[{"type":"text","text":"scanerr probe"}]`, "")

	_, err := store.SearchSessions("scanerr", "/search/proj", 10)
	assertWrappedErr(t, err, "scan session: ")
}

// SearchSessions selects the nullable session columns; each must land on the
// returned Session.
func TestSearchSessions_NullableColumnsRoundTrip(t *testing.T) {
	store := openTestStore(t)
	insertSearchSession(t, store, "nc-1",
		"model, title, parent_session_id, fork_point_seq, agent_type, mode, settings",
		"opus-4", "My Title", "parent-x", 42, "Explore", "plan", "{}")
	indexRawMessage(t, store, "nc-1", "ncm-1", `[{"type":"text","text":"nullcolssess probe"}]`, "")

	results, err := store.SearchSessions("nullcolssess", "/search/proj", 10)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}

	got := results[0]
	if got.Model != "opus-4" {
		t.Errorf("Model = %q, want opus-4", got.Model)
	}
	if got.Title != "My Title" {
		t.Errorf("Title = %q, want My Title", got.Title)
	}
	if got.ParentSessionID != "parent-x" {
		t.Errorf("ParentSessionID = %q, want parent-x", got.ParentSessionID)
	}
	if got.ForkPointSeq != 42 {
		t.Errorf("ForkPointSeq = %d, want 42", got.ForkPointSeq)
	}
	if got.AgentType != "Explore" {
		t.Errorf("AgentType = %q, want Explore", got.AgentType)
	}
	if got.Mode != "plan" {
		t.Errorf("Mode = %q, want plan", got.Mode)
	}
}

// --- FTS write paths ---

func TestInsertFTS_FTSInsertErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE messages_fts"); err != nil {
		t.Fatalf("drop messages_fts: %v", err)
	}

	err := store.insertFTS(store.db, 1, `[{"type":"text","text":"hello"}]`)
	assertWrappedErr(t, err, "insert fts: ")
}

func TestInsertFTS_MapInsertErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE messages_fts_map"); err != nil {
		t.Fatalf("drop messages_fts_map: %v", err)
	}

	err := store.insertFTS(store.db, 1, `[{"type":"text","text":"hello"}]`)
	assertWrappedErr(t, err, "insert fts map: ")
}

func TestDeleteFTS_ErrorWraps(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE messages_fts_map"); err != nil {
		t.Fatalf("drop messages_fts_map: %v", err)
	}

	err := store.deleteFTS(1)
	assertWrappedErr(t, err, "delete fts map: ")
}

// updateSessionFTS returns the driver error unwrapped; zeroing it hides every
// freshness write failure.
func TestUpdateSessionFTS_ErrorReturned(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.db.Exec("DROP TABLE sessions"); err != nil {
		t.Fatalf("drop sessions: %v", err)
	}

	err := store.updateSessionFTS(store.db, "any")
	if err == nil {
		t.Fatal("updateSessionFTS should fail when the sessions table is gone")
	}
	if err.Error() != "SQL logic error: no such table: sessions (1)" {
		t.Errorf("error = %q, want %q", err.Error(), "SQL logic error: no such table: sessions (1)")
	}
}

// --- ExtractSearchableText block loop ---

// A non-object block is skipped, not terminal: blocks after it still index.
func TestExtractSearchableText_SkipsBadBlockAndContinues(t *testing.T) {
	got := ExtractSearchableText(`[{"type":"text","text":"first"},123,{"type":"text","text":"second"}]`)
	if got != "first\nsecond" {
		t.Errorf("ExtractSearchableText = %q, want %q", got, "first\nsecond")
	}
}
