package short

// compact_store_mutate_test.go pins compact.go's store-side behaviour: the
// durable effects of RecordCompact (FTS indexing of every part, error wrapping
// per insert stage), the seq/created_at contract of the two insert helpers, and
// the boundary-lookup branches of the post-compact loaders.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// mcFTSRowsFor counts the FTS map rows indexed for a stored message uuid.
func mcFTSRowsFor(t *testing.T, store *Store, uuid string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(
		`SELECT COUNT(*) FROM messages_fts_map fm JOIN messages m ON m.seq = fm.seq WHERE m.uuid = ?`,
		uuid).Scan(&n); err != nil {
		t.Fatalf("count fts rows for %s: %v", uuid, err)
	}
	return n
}

// --- RecordCompact: FTS indexing of every persisted part (163, 172, 187, 198) ---

func TestMCRecordCompactIndexesEveryPart(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*TranscriptMessage{testMessage(0, "user", "sum-1", "", `[{"type":"text","text":"summary words"}]`)},
		MessagesToKeep:  []*TranscriptMessage{testMessage(0, "user", "keep-1", "", `[{"type":"text","text":"kept words"}]`)},
		Attachments:     []*TranscriptMessage{testMessage(0, "attachment", "att-1", "", `[{"type":"text","text":"attachment words"}]`)},
	}
	if err := store.RecordCompact("s", result); err != nil {
		t.Fatalf("RecordCompact: %v", err)
	}

	for _, uuid := range []string{result.BoundaryMarker.UUID, "sum-1", "keep-1", "att-1"} {
		if rows := mcFTSRowsFor(t, store, uuid); rows != 1 {
			t.Errorf("fts rows for %s = %d, want 1 (its content must be searchable after compact)", uuid, rows)
		}
	}
}

// --- RecordCompact: per-stage error context and wrapping (138, 161, 170, 196, 205) ---

func TestMCRecordCompactBeginError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err := store.RecordCompact("s", &CompactResult{BoundaryMarker: CreateCompactBoundaryMessage("auto", 100, "")})
	if err == nil {
		t.Fatal("RecordCompact on a closed store must fail")
	}
	if !strings.HasPrefix(err.Error(), "begin transaction: ") {
		t.Errorf("error = %q, want the begin-transaction context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCRecordCompactBoundaryInsertError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	dup := testMessage(0, "user", boundary.UUID, "", `[{"type":"text","text":"already stored"}]`)
	if err := store.AppendMessage("s", dup); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	err := store.RecordCompact("s", &CompactResult{BoundaryMarker: boundary})
	if err == nil {
		t.Fatal("RecordCompact must fail when the boundary uuid is already stored")
	}
	if !strings.HasPrefix(err.Error(), "insert boundary: ") {
		t.Errorf("error = %q, want the insert-boundary context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCRecordCompactSummaryInsertError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	summary := testMessage(0, "user", "sum-dup", "", `[{"type":"text","text":"summary"}]`)
	if err := store.AppendMessage("s", summary); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker:  CreateCompactBoundaryMessage("auto", 100, ""),
		SummaryMessages: []*TranscriptMessage{summary},
	}
	err := store.RecordCompact("s", result)
	if err == nil {
		t.Fatal("RecordCompact must fail when the summary uuid is already stored")
	}
	if !strings.HasPrefix(err.Error(), "insert summary: ") {
		t.Errorf("error = %q, want the insert-summary context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCRecordCompactAttachmentInsertError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	att := testMessage(0, "attachment", "att-dup", "", `[{"type":"text","text":"attachment"}]`)
	if err := store.AppendMessage("s", att); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker: CreateCompactBoundaryMessage("auto", 100, ""),
		Attachments:    []*TranscriptMessage{att},
	}
	err := store.RecordCompact("s", result)
	if err == nil {
		t.Fatal("RecordCompact must fail when the attachment uuid is already stored")
	}
	if !strings.HasPrefix(err.Error(), "insert attachment: ") {
		t.Errorf("error = %q, want the insert-attachment context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

// TestMCRecordCompactKeptInsertError drives the kept-message stage through the
// FTS-map cleanup: the kept uuid is already indexed, and dropping the map table
// makes the cleanup DELETE fail inside the compact transaction.
func TestMCRecordCompactKeptInsertError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	kept := testMessage(0, "user", "kept-1", "", `[{"type":"text","text":"kept"}]`)
	if err := store.AppendMessage("s", kept); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE messages_fts_map`); err != nil {
		t.Fatalf("drop fts map: %v", err)
	}

	result := &CompactResult{
		BoundaryMarker: CreateCompactBoundaryMessage("auto", 100, ""),
		MessagesToKeep: []*TranscriptMessage{kept},
	}
	err := store.RecordCompact("s", result)
	if err == nil {
		t.Fatal("RecordCompact must fail when the kept-message FTS cleanup fails")
	}
	if !strings.HasPrefix(err.Error(), "insert kept message: ") {
		t.Errorf("error = %q, want the insert-kept-message context", err)
	}
	if !strings.Contains(err.Error(), "delete fts map for old seq") {
		t.Errorf("error = %q, want the fts-cleanup cause inside the context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCRecordCompactUpdateTimestampError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	if _, err := store.db.Exec(
		`CREATE TRIGGER mc_fail_update BEFORE UPDATE ON sessions BEGIN SELECT RAISE(ABORT, 'mc trigger'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := store.RecordCompact("s", &CompactResult{BoundaryMarker: CreateCompactBoundaryMessage("auto", 100, "")})
	if err == nil {
		t.Fatal("RecordCompact must fail when the session timestamp update fails")
	}
	if !strings.HasPrefix(err.Error(), "update session timestamp: ") {
		t.Errorf("error = %q, want the update-timestamp context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

// --- Post-compact loaders (218-271) ---

func TestMCLoadPostCompactMessagesNoBoundary(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	for _, m := range []*TranscriptMessage{
		testMessage(0, "user", "m1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "m2", "m1", `[{"type":"text","text":"two"}]`),
		testMessage(0, "user", "m3", "m2", `[{"type":"text","text":"three"}]`),
	} {
		if err := store.AppendMessage("s", m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	got, err := store.LoadPostCompactMessages("s")
	if err != nil {
		t.Fatalf("LoadPostCompactMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want all 3 (no boundary means load everything)", len(got))
	}
}

func TestMCLoadPostCompactChainNoBoundary(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	for _, m := range []*TranscriptMessage{
		testMessage(0, "user", "m1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "m2", "m1", `[{"type":"text","text":"two"}]`),
		testMessage(0, "user", "m3", "m2", `[{"type":"text","text":"three"}]`),
	} {
		if err := store.AppendMessage("s", m); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	got, err := store.LoadPostCompactChainMessages("s")
	if err != nil {
		t.Fatalf("LoadPostCompactChainMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d messages, want the full 3-message chain", len(got))
	}
}

func TestMCLoadPostCompactMessagesBoundaryError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	got, err := store.LoadPostCompactMessages("s")
	if err == nil {
		t.Fatalf("got %v, want an error when the boundary lookup fails", got)
	}
	if got != nil {
		t.Errorf("messages = %v, want nil alongside the error", got)
	}
	if !strings.HasPrefix(err.Error(), "find last boundary: ") {
		t.Errorf("error = %q, want the find-last-boundary context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCLoadPostCompactChainBoundaryError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	got, err := store.LoadPostCompactChainMessages("s")
	if err == nil {
		t.Fatalf("got %v, want an error when the boundary lookup fails", got)
	}
	if got != nil {
		t.Errorf("messages = %v, want nil alongside the error", got)
	}
	if !strings.HasPrefix(err.Error(), "get last boundary: ") {
		t.Errorf("error = %q, want the get-last-boundary context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

// TestMCLoadPostCompactChainScanError stores a row the scanner cannot decode
// (a text value in the integer is_sidechain column) after the boundary, so the
// post-boundary scan path has to report the failure.
func TestMCLoadPostCompactChainScanError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	boundary := CreateCompactBoundaryMessage("auto", 100, "")
	if err := store.AppendMessage("s", boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO messages (session_id, uuid, parent_uuid, logical_parent_uuid, is_sidechain,
		                           type, subtype, content, metadata, created_at)
		 VALUES (?, ?, '', '', 'not-an-int', 'user', '', ?, NULL, datetime('now'))`,
		"s", "broken-row", `[{"type":"text","text":"x"}]`); err != nil {
		t.Fatalf("insert unscannable row: %v", err)
	}

	got, err := store.LoadPostCompactChainMessages("s")
	if err == nil {
		t.Fatalf("got %d messages %v, want a scan error", len(got), chainUUIDs(got))
	}
	if got != nil {
		t.Errorf("messages = %v, want nil alongside the error", got)
	}
	if !strings.HasPrefix(err.Error(), "scan message: ") {
		t.Errorf("error = %q, want the scan-message context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the scan cause", err)
	}
}

// TestMCLoadPostCompactFirstRowBoundaryPinsSeqPath pins which loader each
// branch takes: with the boundary at seq 1 the post-compact reads go through
// the seq-window query, which — unlike LoadMessages/BuildConversationChain —
// does not run the snip-removal pass.
func TestMCLoadPostCompactFirstRowBoundaryPinsSeqPath(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	boundary := mcSnipBoundary("first-boundary", []any{"del-1"})
	if err := store.AppendMessage("s", boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}
	doomed := testMessage(0, "user", "del-1", boundary.UUID, `[{"type":"text","text":"marked for removal"}]`)
	if err := store.AppendMessage("s", doomed); err != nil {
		t.Fatalf("AppendMessage doomed: %v", err)
	}
	survivor := testMessage(0, "assistant", "surv-1", "del-1", `[{"type":"text","text":"survives"}]`)
	if err := store.AppendMessage("s", survivor); err != nil {
		t.Fatalf("AppendMessage survivor: %v", err)
	}

	var boundarySeq int64
	if err := store.db.QueryRow(`SELECT seq FROM messages WHERE uuid = ?`, "first-boundary").Scan(&boundarySeq); err != nil {
		t.Fatalf("read boundary seq: %v", err)
	}
	if boundarySeq != 1 {
		t.Fatalf("boundary seq = %d, want 1 (the fixture pins the seq==1 branch)", boundarySeq)
	}

	got, err := store.LoadPostCompactMessages("s")
	if err != nil {
		t.Fatalf("LoadPostCompactMessages: %v", err)
	}
	if !slices.Equal(chainUUIDs(got), []string{"first-boundary", "del-1", "surv-1"}) {
		t.Errorf("LoadPostCompactMessages = %v, want the raw seq window (no snip pass)", chainUUIDs(got))
	}

	chain, err := store.LoadPostCompactChainMessages("s")
	if err != nil {
		t.Fatalf("LoadPostCompactChainMessages: %v", err)
	}
	if !slices.Equal(chainUUIDs(chain), []string{"first-boundary", "del-1", "surv-1"}) {
		t.Errorf("LoadPostCompactChainMessages = %v, want the raw seq window chain (no snip pass)", chainUUIDs(chain))
	}
}

// --- insertOrReplaceMessageTx (1006, 1009, 1010, 1016, 1028, 1029) ---

func TestMCInsertOrReplaceClosedTx(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	seq, err := store.insertOrReplaceMessageTx(tx, "s", testMessage(0, "user", "closed-tx", "", `[{"type":"text","text":"x"}]`))
	if err == nil {
		t.Fatalf("got seq %d, want an error on a finished transaction", seq)
	}
	if seq != 0 {
		t.Errorf("seq = %d, want 0 alongside the error", seq)
	}
	if !strings.Contains(err.Error(), "lookup old seq for uuid closed-tx") {
		t.Errorf("error = %q, want the old-seq lookup context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCInsertOrReplaceFTSCleanupError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	msg := testMessage(0, "user", "replaced-1", "", `[{"type":"text","text":"original"}]`)
	if err := store.AppendMessage("s", msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE messages_fts_map`); err != nil {
		t.Fatalf("drop fts map: %v", err)
	}

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	seq, err := store.insertOrReplaceMessageTx(tx, "s", msg)
	if err == nil {
		t.Fatalf("got seq %d, want an error when the stale FTS row cannot be deleted", seq)
	}
	if seq != 0 {
		t.Errorf("seq = %d, want 0 alongside the error", seq)
	}
	if !strings.Contains(err.Error(), "delete fts map for old seq") {
		t.Errorf("error = %q, want the fts-cleanup context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the driver cause", err)
	}
}

func TestMCInsertOrReplaceReturnsRowSeqAndStampsCreatedAt(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	stamped := testMessage(0, "user", "stamped-1", "", `[{"type":"text","text":"kept time"}]`)
	seq, err := store.insertOrReplaceMessageTx(tx, "s", stamped)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("insertOrReplaceMessageTx: %v", err)
	}

	zero := &TranscriptMessage{UUID: "zero-time-1", Type: "user", Content: `[{"type":"text","text":"no time"}]`}
	if _, err := store.insertOrReplaceMessageTx(tx, "s", zero); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insertOrReplaceMessageTx zero time: %v", err)
	}
	// The store allows one connection, so the read-back has to wait for the tx.
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var storedSeq int64
	if err := store.db.QueryRow(`SELECT seq FROM messages WHERE uuid = ?`, "stamped-1").Scan(&storedSeq); err != nil {
		t.Fatalf("read stored seq: %v", err)
	}
	if seq != storedSeq {
		t.Errorf("returned seq %d, want the row's seq %d (FTS indexing depends on it)", seq, storedSeq)
	}

	var createdAt time.Time
	if err := store.db.QueryRow(`SELECT created_at FROM messages WHERE uuid = ?`, "zero-time-1").Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if createdAt.Before(testTimeBase) {
		t.Errorf("created_at = %v, want a wall-clock stamp for a zero CreatedAt", createdAt)
	}
}

func TestMCInsertOrReplaceUnknownSession(t *testing.T) {
	store := openTestStore(t)

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The old-seq lookup succeeds (no such uuid) but the INSERT OR REPLACE
	// breaks the messages.session_id foreign key.
	seq, err := store.insertOrReplaceMessageTx(tx, "no-such-session",
		testMessage(0, "user", "orphan-1", "", `[{"type":"text","text":"x"}]`))
	if err == nil {
		t.Fatalf("got seq %d, want a foreign-key error for an unknown session", seq)
	}
	if seq != 0 {
		t.Errorf("seq = %d, want 0 alongside the error", seq)
	}
}

// --- insertMessageTx (1037, 1063) ---

func TestMCInsertMessageTxDuplicateUUID(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	msg := testMessage(0, "user", "once-1", "", `[{"type":"text","text":"first"}]`)
	first, err := store.insertMessageTx(tx, "s", msg)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first == 0 {
		t.Errorf("first insert returned seq 0, want the row's seq")
	}

	second, err := store.insertMessageTx(tx, "s", msg)
	if err == nil {
		t.Fatalf("got seq %d, want a UNIQUE violation on the repeated uuid", second)
	}
	if second != 0 {
		t.Errorf("seq = %d, want 0 alongside the error", second)
	}
}

func TestMCInsertMessageTxStampsCreatedAt(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	zero := &TranscriptMessage{UUID: "zero-time-2", Type: "user", Content: `[{"type":"text","text":"no time"}]`}
	if _, err := store.insertMessageTx(tx, "s", zero); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insertMessageTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var createdAt time.Time
	if err := store.db.QueryRow(`SELECT created_at FROM messages WHERE uuid = ?`, "zero-time-2").Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if createdAt.Before(testTimeBase) {
		t.Errorf("created_at = %v, want a wall-clock stamp for a zero CreatedAt", createdAt)
	}
}

// --- indexMessageFTS (1072, 1073) ---

func TestMCIndexMessageFTSSkipsUnsearchableContent(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	noText := &TranscriptMessage{UUID: "no-text-1", Type: "assistant", Content: `[{"type":"tool_use","id":"t1","name":"Read","input":{}}]`}
	seqNoText, err := store.insertMessageTx(tx, "s", noText)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert no-text message: %v", err)
	}
	store.indexMessageFTS(tx, seqNoText, noText.Content)

	withText := &TranscriptMessage{UUID: "with-text-1", Type: "user", Content: `[{"type":"text","text":"searchable"}]`}
	seqWithText, err := store.insertMessageTx(tx, "s", withText)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert text message: %v", err)
	}
	store.indexMessageFTS(tx, seqWithText, withText.Content)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if rows := mcFTSRowsFor(t, store, "no-text-1"); rows != 0 {
		t.Errorf("fts rows for a textless message = %d, want 0", rows)
	}
	if rows := mcFTSRowsFor(t, store, "with-text-1"); rows != 1 {
		t.Errorf("fts rows for a text message = %d, want 1", rows)
	}
}

// TestMCIndexMessageFTSFailureLeavesWarning pins the best-effort contract: a
// failed index is logged (and the success path stays silent).
func TestMCIndexMessageFTSFailureLeavesWarning(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "s")

	stored := &TranscriptMessage{UUID: "fts-ok-1", Type: "user", Content: `[{"type":"text","text":"searchable"}]`}
	seq, err := store.insertMessageTx(nil, "s", stored)
	if err != nil {
		t.Fatalf("insertMessageTx: %v", err)
	}

	logs := mcCaptureLogs(t)

	// A seq with no messages row breaks the messages_fts_map.seq foreign key.
	store.indexMessageFTS(store.db, 4242, `[{"type":"text","text":"orphan"}]`)
	if got := logs.countWith("FTS index failed"); got != 1 {
		t.Errorf("FTS-failure warnings = %d, want 1: %v", got, logs.msgs)
	}

	store.indexMessageFTS(store.db, seq, stored.Content)
	if got := logs.countWith("FTS index failed"); got != 1 {
		t.Errorf("warnings after a successful index = %d, want no new warning: %v", got, logs.msgs)
	}
	if rows := mcFTSRowsFor(t, store, "fts-ok-1"); rows != 1 {
		t.Errorf("fts rows for the indexed message = %d, want 1", rows)
	}
}
