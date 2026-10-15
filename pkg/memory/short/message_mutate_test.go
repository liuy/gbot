package short

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- assertion helpers for message.go ---------------------------------------

// capturedLog is one slog record flattened to strings. Attribute values are
// compared as printed so int/int64/error differences stay exact.
type capturedLog struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

type logCapture struct {
	mu   sync.Mutex
	recs []capturedLog
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]string)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = fmt.Sprint(a.Value.Any())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, capturedLog{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// captureLogs redirects the default logger for the test's lifetime. message.go
// reports its non-fatal failures (FTS index, chain UUID lookup, settings parse)
// only through slog.Warn, so a removed warn call is invisible unless the record
// itself is asserted.
func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return c
}

func (c *logCapture) byMessage(msg string) []capturedLog {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedLog
	for _, r := range c.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func (c *logCapture) warnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.recs {
		if r.level == slog.LevelWarn {
			n++
		}
	}
	return n
}

func wantLogAttr(t *testing.T, rec capturedLog, key, want string) {
	t.Helper()
	v, ok := rec.attrs[key]
	if !ok {
		t.Fatalf("log %q has no %q attribute, got %v", rec.msg, key, rec.attrs)
	}
	if v != want {
		t.Errorf("log %q attr %q = %q, want %q", rec.msg, key, v, want)
	}
}

// wantWrappedError pins both the wrapper text and the %w chain: the
// errorf-wrap mutant (%w -> %v) leaves the message byte-identical, so only
// errors.Unwrap tells them apart.
func wantWrappedError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap its cause", err)
	}
}

// wantWrappedCause is the variant for driver-generated causes (sqlite parse and
// trigger messages) whose full text is not ours to pin.
func wantWrappedCause(t *testing.T, err error, prefix, causeContains string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want prefix %q", prefix)
	}
	if !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("error = %q, want prefix %q", err.Error(), prefix)
	}
	cause := errors.Unwrap(err)
	if cause == nil {
		t.Fatalf("error %q does not wrap its cause", err)
	}
	if !strings.Contains(cause.Error(), causeContains) {
		t.Errorf("cause = %q, want it to contain %q", cause.Error(), causeContains)
	}
}

func dropMessagesTable(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.DB().Exec("DROP TABLE messages"); err != nil {
		t.Fatalf("DROP TABLE messages: %v", err)
	}
}

func dropFTSMapTable(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.DB().Exec("DROP TABLE messages_fts_map"); err != nil {
		t.Fatalf("DROP TABLE messages_fts_map: %v", err)
	}
}

// blockDeletesOnMessages makes every DELETE against messages fail with the
// given RAISE message, so the delete-error branch is reachable while the
// preceding statements in the same transaction still succeed.
func blockDeletesOnMessages(t *testing.T, store *Store, message string) {
	t.Helper()
	_, err := store.DB().Exec(fmt.Sprintf(
		"CREATE TRIGGER block_msg_delete BEFORE DELETE ON messages "+
			"BEGIN SELECT RAISE(ABORT, '%s'); END;", message))
	if err != nil {
		t.Fatalf("create delete trigger: %v", err)
	}
}

// blockUpdatesOnSessions makes every UPDATE against sessions fail, which is how
// updateSessionFTS's failure branch is reached without breaking the insert.
func blockUpdatesOnSessions(t *testing.T, store *Store, message string) {
	t.Helper()
	_, err := store.DB().Exec(fmt.Sprintf(
		"CREATE TRIGGER block_session_update BEFORE UPDATE ON sessions "+
			"BEGIN SELECT RAISE(ABORT, '%s'); END;", message))
	if err != nil {
		t.Fatalf("create update trigger: %v", err)
	}
}

// --- AppendMessages ---------------------------------------------------------

// Line 28: the begin-transaction error must stay unwrappable.
func TestAppendMessages_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "test-session")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.AppendMessages("test-session", []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"first"}]`),
	})
	wantWrappedError(t, err, "begin transaction: sql: database is closed")
}

// Lines 34-35 + 666-667: dedupBatch's query error must reach the caller
// wrapped, not swallowed by the guard or replaced with a nil return.
func TestAppendMessages_DedupQueryError(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "test-session")
	dropMessagesTable(t, store)

	err := store.AppendMessages("test-session", []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"first"}]`),
	})
	wantWrappedError(t, err, "dedup check: SQL logic error: no such table: messages (1)")
}

// Line 48: a dedup-skipped chain participant still advances lastChainUUID, even
// when it is not the newest chain participant already in the store.
func TestAppendMessages_DedupSkipAdvancesChain(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"two"}]`),
	}); err != nil {
		t.Fatalf("seed AppendMessages: %v", err)
	}

	// uuid-1 already exists, so it is skipped; the new message must chain onto
	// uuid-1 (the skipped participant), not onto uuid-2 (the store's newest).
	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "uuid-3", "", `[{"type":"text","text":"three"}]`),
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("got %d messages, want 3", len(loaded))
	}
	if loaded[2].UUID != "uuid-3" {
		t.Fatalf("third message uuid = %q, want uuid-3", loaded[2].UUID)
	}
	if loaded[2].ParentUUID != "uuid-1" {
		t.Errorf("uuid-3 parent_uuid = %q, want uuid-1 (dedup-skipped participant advances chain)", loaded[2].ParentUUID)
	}
}

// Lines 53-54: an append failure mid-batch aborts the batch with the insert
// error; the guard must not be skipped and the error must not be zeroed.
func TestAppendMessages_InsertFailureMidBatch(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	err := store.AppendMessages(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "dup-uuid", "", `[{"type":"text","text":"first"}]`),
		testMessage(0, "assistant", "dup-uuid", "", `[{"type":"text","text":"second"}]`),
	})
	wantWrappedError(t, err, "insert message: constraint failed: UNIQUE constraint failed: messages.uuid (2067)")

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("got %d messages after a failed batch, want 0 (transaction must roll back)", len(loaded))
	}
}

// --- LoadMessages -----------------------------------------------------------

// Line 80: query error text and wrap.
func TestLoadMessages_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	msgs, err := store.LoadMessages("test-session")
	wantWrappedError(t, err, "query messages: SQL logic error: no such table: messages (1)")
	if msgs != nil {
		t.Errorf("got %d messages on query error, want nil", len(msgs))
	}
}

// Line 88: scan error text and wrap.
func TestLoadMessages_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if _, err := store.DB().Exec(`
		INSERT INTO messages (session_id, uuid, type, content, created_at)
		VALUES (?, 'bad-scan-1', 'user', 'hello', 'not-a-timestamp')
	`, sessionID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.LoadMessages(sessionID)
	wantWrappedCause(t, err, "scan message: ", "unsupported Scan")
}

// Line 95: LoadMessages must apply preserved-segment relinks, which prune
// everything before the boundary that is not in the preserved segment.
func TestLoadMessages_AppliesPreservedSegmentRelinks(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	boundary := CreateCompactBoundaryMessage("auto", 5000, "pre-1")
	if err := annotateBoundaryWithPreservedSegment(boundary, "head-1", boundary.UUID, "tail-1"); err != nil {
		t.Fatalf("annotate boundary: %v", err)
	}

	msgs := []*TranscriptMessage{
		{UUID: "pre-1", Type: "user", Content: `[{"type":"text","text":"before"}]`, CreatedAt: testTimeBase},
		{UUID: boundary.UUID, Type: "system", Subtype: "compact_boundary", Content: boundary.Content, CreatedAt: testTimeBase},
		{UUID: "head-1", Type: "assistant", Content: `[{"type":"text","text":"head"}]`, CreatedAt: testTimeBase},
		{UUID: "tail-1", Type: "assistant", Content: `[{"type":"text","text":"tail"}]`, CreatedAt: testTimeBase},
		{UUID: "post-1", Type: "user", Content: `[{"type":"text","text":"after"}]`, CreatedAt: testTimeBase},
	}
	if err := store.AppendMessages(sessionID, msgs); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	var uuids []string
	for _, m := range loaded {
		uuids = append(uuids, m.UUID)
	}
	want := []string{boundary.UUID, "head-1", "tail-1", "post-1"}
	if len(uuids) != len(want) {
		t.Fatalf("got %d messages %v, want %d %v", len(uuids), uuids, len(want), want)
	}
	for i := range want {
		if uuids[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, uuids[i], want[i])
		}
	}
}

// Line 96: LoadMessages must drop messages listed in a boundary's
// snipMetadata.removedUuids and relink survivors past them.
func TestLoadMessages_AppliesSnipRemovals(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	inner, err := json.Marshal(map[string]any{
		"type":            "system",
		"subtype":         "compact_boundary",
		"compactMetadata": map[string]any{},
		"snipMetadata":    map[string]any{"removedUuids": []any{"snipped-1"}},
	})
	if err != nil {
		t.Fatalf("marshal boundary: %v", err)
	}

	msgs := []*TranscriptMessage{
		{UUID: "snip-boundary", Type: "system", Subtype: "compact_boundary", Content: wrapAsTextBlock(string(inner)), CreatedAt: testTimeBase},
		{UUID: "snipped-1", Type: "user", Content: `[{"type":"text","text":"drop me"}]`, CreatedAt: testTimeBase},
		{UUID: "kept-1", Type: "assistant", Content: `[{"type":"text","text":"I stay"}]`, CreatedAt: testTimeBase},
	}
	if err := store.AppendMessages(sessionID, msgs); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d messages, want 2 (boundary + survivor)", len(loaded))
	}
	if loaded[0].UUID != "snip-boundary" || loaded[1].UUID != "kept-1" {
		t.Fatalf("loaded uuids = %q, %q, want snip-boundary, kept-1", loaded[0].UUID, loaded[1].UUID)
	}
	if loaded[1].ParentUUID != "snip-boundary" {
		t.Errorf("survivor parent_uuid = %q, want snip-boundary (relinked past the snipped message)", loaded[1].ParentUUID)
	}
}

// --- LoadMessagesAfterSeq / BeforeSeq ---------------------------------------

// Line 116: query error text and wrap.
func TestLoadMessagesAfterSeq_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	_, err := store.LoadMessagesAfterSeq("test-session", 0)
	wantWrappedError(t, err, "query messages after seq: SQL logic error: no such table: messages (1)")
}

// Line 124: scan error text and wrap.
func TestLoadMessagesAfterSeq_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if _, err := store.DB().Exec(`
		INSERT INTO messages (session_id, uuid, type, content, created_at)
		VALUES (?, 'bad-scan-2', 'user', 'hello', 'not-a-timestamp')
	`, sessionID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.LoadMessagesAfterSeq(sessionID, 0)
	wantWrappedCause(t, err, "scan message: ", "unsupported Scan")
}

// Lines 147-148: query error must not be ignored and must stay wrapped.
func TestLoadMessagesBeforeSeq_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	msgs, err := store.LoadMessagesBeforeSeq("test-session", 10)
	wantWrappedError(t, err, "query messages before seq: SQL logic error: no such table: messages (1)")
	if msgs != nil {
		t.Errorf("got %d messages on query error, want nil", len(msgs))
	}
}

// Lines 155-156: scan error must not be ignored and must stay wrapped.
func TestLoadMessagesBeforeSeq_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if _, err := store.DB().Exec(`
		INSERT INTO messages (session_id, uuid, type, content, created_at)
		VALUES (?, 'bad-scan-3', 'user', 'hello', 'not-a-timestamp')
	`, sessionID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.LoadMessagesBeforeSeq(sessionID, 100)
	wantWrappedCause(t, err, "scan message: ", "unsupported Scan")
}

// --- GetLastBoundary --------------------------------------------------------

// Lines 182-183: a query failure returns (nil, 0, wrapped error) — the seq
// literal is part of the contract, so 0 must stay 0.
func TestGetLastBoundary_QueryError(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	msg, seq, err := store.GetLastBoundary("test-session")
	wantWrappedError(t, err, "query last boundary: SQL logic error: no such table: messages (1)")
	if msg != nil {
		t.Errorf("got message %+v on query error, want nil", msg)
	}
	if seq != 0 {
		t.Errorf("got seq %d on query error, want 0", seq)
	}
}

// --- RemoveMessageByUUID ----------------------------------------------------

// Line 194: begin error text and wrap.
func TestRemoveMessageByUUID_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.RemoveMessageByUUID("test-session", "uuid-1")
	wantWrappedError(t, err, "begin transaction: sql: database is closed")
}

// Line 205: seq lookup failure text and wrap.
func TestRemoveMessageByUUID_GetSeqErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	err := store.RemoveMessageByUUID("test-session", "uuid-1")
	wantWrappedError(t, err, "get message seq: SQL logic error: no such table: messages (1)")
}

// Lines 210-211: an FTS map delete failure is non-fatal but must be warned
// about, and must not short-circuit the message delete.
func TestRemoveMessageByUUID_FTSMapDeleteWarns(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	dropFTSMapTable(t, store)

	if err := store.RemoveMessageByUUID(sessionID, "uuid-1"); err != nil {
		t.Fatalf("RemoveMessageByUUID should succeed despite FTS map failure: %v", err)
	}

	exists, err := store.MessageExists(sessionID, "uuid-1")
	if err != nil {
		t.Fatalf("MessageExists: %v", err)
	}
	if exists {
		t.Error("message still present: the FTS map warning must not abort the delete")
	}

	recs := logs.byMessage("failed to delete FTS map")
	if len(recs) != 1 {
		t.Fatalf("got %d 'failed to delete FTS map' records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "seq", "1")
	wantLogAttr(t, recs[0], "error", "SQL logic error: no such table: messages_fts_map (1)")
}

// Line 210 (negated comparison): the warning must fire only on failure.
func TestRemoveMessageByUUID_NoWarnOnSuccess(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := store.RemoveMessageByUUID(sessionID, "uuid-1"); err != nil {
		t.Fatalf("RemoveMessageByUUID: %v", err)
	}
	if n := logs.warnCount(); n != 0 {
		t.Errorf("got %d warnings on a clean delete, want 0", n)
	}
}

// Line 220: the message delete failure must be reported, wrapped.
func TestRemoveMessageByUUID_DeleteErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hello"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	blockDeletesOnMessages(t, store, "delete_blocked")

	err := store.RemoveMessageByUUID(sessionID, "uuid-1")
	wantWrappedCause(t, err, "delete message: ", "delete_blocked")
}

// --- TruncateMessagesFromIndex ----------------------------------------------

// Line 233: begin error text and wrap.
func TestTruncateMessagesFromIndex_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.TruncateMessagesFromIndex("test-session", 0)
	wantWrappedError(t, err, "begin: sql: database is closed")
}

// Line 259: FTS cleanup failure aborts the truncate, wrapped.
func TestTruncateMessagesFromIndex_FTSDeleteErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"two"}]`),
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	dropFTSMapTable(t, store)

	err := store.TruncateMessagesFromIndex(sessionID, 0)
	wantWrappedError(t, err, "delete FTS: SQL logic error: no such table: messages_fts_map (1)")

	count, err := store.CountVisibleMessages(sessionID)
	if err != nil {
		t.Fatalf("CountVisibleMessages: %v", err)
	}
	if count != 2 {
		t.Errorf("got %d messages after a failed truncate, want 2 (transaction must roll back)", count)
	}
}

// Line 268: message delete failure reported, wrapped.
func TestTruncateMessagesFromIndex_DeleteErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"one"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	blockDeletesOnMessages(t, store, "truncate_blocked")

	err := store.TruncateMessagesFromIndex(sessionID, 0)
	wantWrappedCause(t, err, "delete messages: ", "truncate_blocked")
}

// Line 272: the truncate log line is the only record of what was removed; its
// session prefix is a slice literal, so the exact value must be pinned.
func TestTruncateMessagesFromIndex_LogsTruncation(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "truncate-target-01"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"one"}]`),
		testMessage(0, "assistant", "uuid-2", "", `[{"type":"text","text":"two"}]`),
		testMessage(0, "user", "uuid-3", "", `[{"type":"text","text":"three"}]`),
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	if err := store.TruncateMessagesFromIndex(sessionID, 1); err != nil {
		t.Fatalf("TruncateMessagesFromIndex: %v", err)
	}

	recs := logs.byMessage("store:truncate_from_index")
	if len(recs) != 1 {
		t.Fatalf("got %d 'store:truncate_from_index' records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "session", "truncate")
	wantLogAttr(t, recs[0], "index", "1")
	wantLogAttr(t, recs[0], "target_seq", "2")
	wantLogAttr(t, recs[0], "removed", "2")
}

// --- MessageExists / GetMessageByUUID ---------------------------------------

// Line 286: exists-query failure text and wrap.
func TestMessageExists_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	exists, err := store.MessageExists("test-session", "uuid-1")
	wantWrappedError(t, err, "check message exists: SQL logic error: no such table: messages (1)")
	if exists {
		t.Error("exists = true on a query error, want false")
	}
}

// Lines 308-309: a query failure must not be swallowed into (nil, nil).
func TestGetMessageByUUID_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	msg, err := store.GetMessageByUUID("uuid-1")
	wantWrappedError(t, err, "get message by uuid: SQL logic error: no such table: messages (1)")
	if msg != nil {
		t.Errorf("got message %+v on query error, want nil", msg)
	}
}

// Line 633: scanMessageFromRow must copy the nullable metadata column.
func TestGetMessageByUUID_ReturnsMetadata(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	msg := testMessage(0, "assistant", "meta-uuid", "", `[{"type":"text","text":"hi"}]`)
	msg.Metadata = `{"model":"sonnet","stop_reason":"end_turn"}`
	if err := store.AppendMessage(sessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, err := store.GetMessageByUUID("meta-uuid")
	if err != nil {
		t.Fatalf("GetMessageByUUID: %v", err)
	}
	if got == nil {
		t.Fatal("GetMessageByUUID returned nil for an existing uuid")
	}
	if got.Metadata != `{"model":"sonnet","stop_reason":"end_turn"}` {
		t.Errorf("Metadata = %q, want the stored metadata JSON", got.Metadata)
	}
	if got.SessionID != sessionID || got.Type != "assistant" || got.UUID != "meta-uuid" {
		t.Errorf("scanned message = %+v, want session %q type assistant uuid meta-uuid", got, sessionID)
	}
}

// --- MessagesSince / LastAssistantTime --------------------------------------

// Lines 328-329: query failure must not be swallowed and must stay wrapped.
func TestMessagesSince_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	msgs, err := store.MessagesSince(testTimeBase.Add(-time.Hour))
	wantWrappedError(t, err, "query messages since: SQL logic error: no such table: messages (1)")
	if msgs != nil {
		t.Errorf("got %d messages on query error, want nil", len(msgs))
	}
}

// Lines 336-337: scan failure must not be swallowed and must stay wrapped.
func TestMessagesSince_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if _, err := store.DB().Exec(`
		INSERT INTO messages (session_id, uuid, type, content, is_sidechain, created_at)
		VALUES (?, 'bad-scan-4', 'user', 'hello', 0, 'not-a-timestamp')
	`, sessionID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.MessagesSince(testTimeBase.Add(-24 * time.Hour))
	wantWrappedCause(t, err, "scan message: ", "unsupported Scan")
}

// Line 342: the accumulated slice, not just the rows.Err() tail, is the result.
func TestMessagesSince_ReturnsMatchesInOrder(t *testing.T) {
	store := openTestStore(t)
	sessionID := "since-session"
	createTestSession(t, store, sessionID)

	older := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 2, 1, 11, 0, 0, 0, time.UTC)
	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		{UUID: "since-1", Type: "user", Content: `[{"type":"text","text":"one"}]`, CreatedAt: older},
		{UUID: "since-2", Type: "assistant", Content: `[{"type":"text","text":"two"}]`, CreatedAt: newer},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	got, err := store.MessagesSince(older.Add(-time.Minute))
	if err != nil {
		t.Fatalf("MessagesSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].UUID != "since-1" || got[1].UUID != "since-2" {
		t.Errorf("uuids = %q, %q, want since-1, since-2", got[0].UUID, got[1].UUID)
	}
}

// Lines 361-362: a query failure must not fall through to msg.CreatedAt on a nil row.
func TestLastAssistantTime_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	got, err := store.LastAssistantTime()
	wantWrappedError(t, err, "query last assistant time: SQL logic error: no such table: messages (1)")
	if !got.IsZero() {
		t.Errorf("LastAssistantTime = %v on query error, want zero time", got)
	}
}

// --- RecordSidechainTranscript / LoadSidechainTranscript --------------------

// Line 375: begin error text and wrap.
func TestRecordSidechainTranscript_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.RecordSidechainTranscript("test-session", "agent-1", []*TranscriptMessage{
		testMessage(0, "assistant", "side-1", "", `[{"type":"text","text":"hi"}]`),
	})
	wantWrappedError(t, err, "begin transaction: sql: database is closed")
}

// Line 390: progress messages do not advance the sidechain's lastChainUUID, and
// the update must not abort the batch.
func TestRecordSidechainTranscript_ChainSkipsProgress(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	sidechain := []*TranscriptMessage{
		testMessage(0, "user", "side-user", "", `[{"type":"text","text":"task"}]`),
		testMessage(0, "progress", "side-progress", "", `thinking...`),
		testMessage(0, "assistant", "side-assistant", "", `[{"type":"text","text":"done"}]`),
	}
	if err := store.RecordSidechainTranscript(sessionID, "agent-1", sidechain); err != nil {
		t.Fatalf("RecordSidechainTranscript: %v", err)
	}

	loaded, err := store.LoadSidechainTranscript(sessionID, "agent-1")
	if err != nil {
		t.Fatalf("LoadSidechainTranscript: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("got %d sidechain messages, want 3", len(loaded))
	}
	for _, m := range loaded {
		if m.IsSidechain != 1 {
			t.Errorf("message %q is_sidechain = %d, want 1", m.UUID, m.IsSidechain)
		}
	}
	if loaded[0].ParentUUID != "" {
		t.Errorf("side-user parent_uuid = %q, want empty (first message of the sidechain)", loaded[0].ParentUUID)
	}
	if loaded[2].ParentUUID != "side-user" {
		t.Errorf("side-assistant parent_uuid = %q, want side-user (progress must not advance the chain)", loaded[2].ParentUUID)
	}
}

// Line 411: query error text and wrap.
func TestLoadSidechainTranscript_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	_, err := store.LoadSidechainTranscript("test-session", "agent-1")
	wantWrappedError(t, err, "query sidechain messages: SQL logic error: no such table: messages (1)")
}

// Line 419: scan error text and wrap.
func TestLoadSidechainTranscript_ScanErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if _, err := store.DB().Exec(`
		INSERT INTO messages (session_id, uuid, type, content, is_sidechain, created_at)
		VALUES (?, 'bad-scan-5', 'assistant', 'hi', 1, 'not-a-timestamp')
	`, sessionID); err != nil {
		t.Fatalf("insert: %v", err)
	}

	_, err := store.LoadSidechainTranscript(sessionID, "agent-1")
	wantWrappedCause(t, err, "scan message: ", "unsupported Scan")
}

// --- FindLatestMessage ------------------------------------------------------

// Line 447: maxTime must be updated, otherwise every later candidate wins and
// the newest-by-timestamp message is not the one returned.
func TestFindLatestMessage_PrefersLatestTimestamp(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	// Insertion order (seq) is deliberately the reverse of timestamp order.
	if err := store.AppendMessages(sessionID, []*TranscriptMessage{
		{UUID: "late", Type: "user", Content: `[{"type":"text","text":"newest"}]`, CreatedAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)},
		{UUID: "early", Type: "user", Content: `[{"type":"text","text":"older"}]`, CreatedAt: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	latest, err := store.FindLatestMessage(sessionID, func(m *TranscriptMessage) bool { return true })
	if err != nil {
		t.Fatalf("FindLatestMessage: %v", err)
	}
	if latest == nil {
		t.Fatal("FindLatestMessage returned nil")
	}
	if latest.UUID != "late" {
		t.Errorf("latest uuid = %q, want late (highest created_at, not last in seq order)", latest.UUID)
	}
}

// --- CountVisibleMessages ---------------------------------------------------

// Lines 465-466: the failure path returns exactly (0, wrapped error).
func TestCountVisibleMessages_QueryError(t *testing.T) {
	store := openTestStore(t)
	dropMessagesTable(t, store)

	count, err := store.CountVisibleMessages("test-session")
	wantWrappedError(t, err, "count visible messages: SQL logic error: no such table: messages (1)")
	if count != 0 {
		t.Errorf("got count %d on query error, want 0", count)
	}
}

// --- GetPreBoundaryMetadata -------------------------------------------------

// Line 484: session lookup failure text and wrap.
func TestGetPreBoundaryMetadata_QueryErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)

	metadata, err := store.GetPreBoundaryMetadata("missing-session")
	wantWrappedError(t, err, "query session metadata: sql: no rows in result set")
	if metadata != nil {
		t.Errorf("got metadata %+v on query error, want nil", metadata)
	}
}

// Lines 492-498: every settings branch must end with an initialized map, and
// the parse warning must fire only when JSON parsing actually failed.
func TestGetPreBoundaryMetadata_SettingsBranches(t *testing.T) {
	tests := []struct {
		name     string
		settings any
		want     map[string]string
		warns    int
	}{
		{name: "null", settings: nil, want: map[string]string{}, warns: 0},
		{name: "empty string", settings: "", want: map[string]string{}, warns: 0},
		{name: "empty object", settings: "{}", want: map[string]string{}, warns: 0},
		{name: "valid object", settings: `{"a":"b","c":"d"}`, want: map[string]string{"a": "b", "c": "d"}, warns: 0},
		{name: "invalid json", settings: `{"broken":`, want: map[string]string{}, warns: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			store := openTestStore(t)
			sessionID := "settings-session"
			_, err := store.DB().Exec(`
				INSERT INTO sessions (session_id, project_dir, agent_type, mode, settings)
				VALUES (?, '/project', 'Explore', 'plan', ?)
			`, sessionID, tc.settings)
			if err != nil {
				t.Fatalf("insert session: %v", err)
			}

			metadata, err := store.GetPreBoundaryMetadata(sessionID)
			if err != nil {
				t.Fatalf("GetPreBoundaryMetadata: %v", err)
			}
			if metadata == nil {
				t.Fatal("GetPreBoundaryMetadata returned nil metadata")
			}
			if metadata.Settings == nil {
				t.Fatalf("Settings is nil, want an initialized map with %d entries", len(tc.want))
			}
			if len(metadata.Settings) != len(tc.want) {
				t.Fatalf("Settings = %v, want %v", metadata.Settings, tc.want)
			}
			for k, v := range tc.want {
				if metadata.Settings[k] != v {
					t.Errorf("Settings[%q] = %q, want %q", k, metadata.Settings[k], v)
				}
			}
			if n := logs.warnCount(); n != tc.warns {
				t.Errorf("got %d warnings, want %d for settings %v", n, tc.warns, tc.settings)
			}
			if n := len(logs.byMessage("failed to parse settings JSON")); n != tc.warns {
				t.Errorf("got %d 'failed to parse settings JSON' records, want %d", n, tc.warns)
			}
		})
	}
}

// --- appendMessage / appendMessageTx ----------------------------------------

// Line 508: begin error text and wrap.
func TestAppendMessage_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.AppendMessage("test-session", testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hi"}]`))
	wantWrappedError(t, err, "begin transaction: sql: database is closed")
}

// Lines 526-530: a compact boundary resets parent_uuid and carries the previous
// chain head in logical_parent_uuid.
func TestAppendMessage_CompactBoundaryResetsParent(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "prev-1", "", `[{"type":"text","text":"before"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	boundary := &TranscriptMessage{
		UUID:              "boundary-1",
		Type:              "system",
		Subtype:           "compact_boundary",
		ParentUUID:        "stale-parent",
		LogicalParentUUID: "stale-logical",
		Content:           `[{"type":"text","text":"Conversation compacted"}]`,
		CreatedAt:         testTimeBase,
	}
	if err := store.AppendMessage(sessionID, boundary); err != nil {
		t.Fatalf("AppendMessage boundary: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d messages, want 2", len(loaded))
	}
	if loaded[1].UUID != "boundary-1" {
		t.Fatalf("second message uuid = %q, want boundary-1", loaded[1].UUID)
	}
	if loaded[1].ParentUUID != "" {
		t.Errorf("boundary parent_uuid = %q, want empty (boundary starts a new chain)", loaded[1].ParentUUID)
	}
	if loaded[1].LogicalParentUUID != "prev-1" {
		t.Errorf("boundary logical_parent_uuid = %q, want prev-1", loaded[1].LogicalParentUUID)
	}
}

// Line 526: a system message that is not a compact_boundary stays on the chain.
func TestAppendMessage_SystemWithoutCompactSubtypeKeepsParent(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "prev-1", "", `[{"type":"text","text":"before"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	info := &TranscriptMessage{
		UUID:              "system-1",
		Type:              "system",
		Subtype:           "informational",
		ParentUUID:        "stale-parent",
		LogicalParentUUID: "stale-logical",
		Content:           `[{"type":"text","text":"fyi"}]`,
		CreatedAt:         testTimeBase,
	}
	if err := store.AppendMessage(sessionID, info); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d messages, want 2", len(loaded))
	}
	if loaded[1].ParentUUID != "prev-1" {
		t.Errorf("system message parent_uuid = %q, want prev-1 (only compact_boundary resets the chain)", loaded[1].ParentUUID)
	}
	if loaded[1].LogicalParentUUID != "stale-logical" {
		t.Errorf("system message logical_parent_uuid = %q, want the caller's value untouched", loaded[1].LogicalParentUUID)
	}
}

// Line 526: subtype compact_boundary on a non-system message is not a boundary.
func TestAppendMessage_NonSystemWithCompactSubtypeKeepsParent(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "prev-1", "", `[{"type":"text","text":"before"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	lookalike := &TranscriptMessage{
		UUID:              "user-boundary-1",
		Type:              "user",
		Subtype:           "compact_boundary",
		ParentUUID:        "stale-parent",
		LogicalParentUUID: "stale-logical",
		Content:           `[{"type":"text","text":"not a real boundary"}]`,
		CreatedAt:         testTimeBase,
	}
	if err := store.AppendMessage(sessionID, lookalike); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("got %d messages, want 2", len(loaded))
	}
	if loaded[1].ParentUUID != "prev-1" {
		t.Errorf("parent_uuid = %q, want prev-1 (type must be system for a boundary)", loaded[1].ParentUUID)
	}
	if loaded[1].LogicalParentUUID != "stale-logical" {
		t.Errorf("logical_parent_uuid = %q, want the caller's value untouched", loaded[1].LogicalParentUUID)
	}
}

// Line 553: insert failure text and wrap.
func TestAppendMessage_InsertErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "dup-1", "", `[{"type":"text","text":"first"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	err := store.AppendMessage(sessionID, testMessage(0, "assistant", "dup-1", "", `[{"type":"text","text":"second"}]`))
	wantWrappedError(t, err, "insert message: constraint failed: UNIQUE constraint failed: messages.uuid (2067)")
}

// Lines 560-561: an FTS index failure is non-fatal but must be warned about,
// and the message insert must still be committed.
func TestAppendMessage_FTSIndexFailureWarns(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"first"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	dropFTSMapTable(t, store)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-2", "", `[{"type":"text","text":"second"}]`)); err != nil {
		t.Fatalf("AppendMessage should survive an FTS failure: %v", err)
	}

	count, err := store.CountVisibleMessages(sessionID)
	if err != nil {
		t.Fatalf("CountVisibleMessages: %v", err)
	}
	if count != 2 {
		t.Errorf("got %d messages, want 2 (the FTS warning must not abort the append)", count)
	}

	recs := logs.byMessage("FTS index failed")
	if len(recs) != 1 {
		t.Fatalf("got %d 'FTS index failed' records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "seq", "2")
	wantLogAttr(t, recs[0], "error", "insert fts map: SQL logic error: no such table: messages_fts_map (1)")
}

// Lines 560 and 566: the two FTS warnings must fire only on failure.
func TestAppendMessage_NoWarningsOnCleanAppend(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"searchable"}]`)); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if n := logs.warnCount(); n != 0 {
		t.Errorf("got %d warnings on a clean append, want 0", n)
	}
}

// Lines 566-567: a session timestamp update failure is non-fatal but must be
// warned about.
func TestAppendMessage_SessionTimestampFailureWarns(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)
	blockUpdatesOnSessions(t, store, "timestamp_blocked")

	if err := store.AppendMessage(sessionID, testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hi"}]`)); err != nil {
		t.Fatalf("AppendMessage should survive a session timestamp failure: %v", err)
	}

	recs := logs.byMessage("update session FTS timestamp failed")
	if len(recs) != 1 {
		t.Fatalf("got %d 'update session FTS timestamp failed' records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "session", sessionID)
	errAttr, ok := recs[0].attrs["error"]
	if !ok {
		t.Fatalf("warning has no error attribute: %v", recs[0].attrs)
	}
	if !strings.Contains(errAttr, "timestamp_blocked") {
		t.Errorf("error attr = %q, want it to mention timestamp_blocked", errAttr)
	}
}

// --- getLastChainUUID -------------------------------------------------------

// Line 586: sql.ErrNoRows is the empty-session case, not a warning condition.
func TestGetLastChainUUID_EmptySessionNoWarn(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if got := store.getLastChainUUID(tx, sessionID); got != "" {
		t.Errorf("getLastChainUUID = %q on an empty session, want empty string", got)
	}
	if n := logs.warnCount(); n != 0 {
		t.Errorf("got %d warnings for sql.ErrNoRows, want 0", n)
	}
}

// Lines 585-589: a real query error must be warned about, not silently
// converted into the empty-session path.
func TestGetLastChainUUID_QueryErrorWarns(t *testing.T) {
	logs := captureLogs(t)
	store := openTestStore(t)
	dropMessagesTable(t, store)

	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if got := store.getLastChainUUID(tx, "test-session"); got != "" {
		t.Errorf("getLastChainUUID = %q on a query error, want empty string", got)
	}
	recs := logs.byMessage("failed to get last chain UUID")
	if len(recs) != 1 {
		t.Fatalf("got %d 'failed to get last chain UUID' records, want 1", len(recs))
	}
	wantLogAttr(t, recs[0], "error", "SQL logic error: no such table: messages (1)")
}

// --- AppendMessagesWithForkPoint --------------------------------------------

// Line 692: begin error text and wrap.
func TestAppendMessagesWithForkPoint_BeginErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := store.AppendMessagesWithForkPoint("test-session", []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hi"}]`),
	}, "")
	wantWrappedError(t, err, "begin: sql: database is closed")
}

// Lines 697-698 plus dedupBatch's query guard: the dedup failure must reach the
// caller wrapped, not as a nil error.
func TestAppendMessagesWithForkPoint_DedupErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "test-session")
	dropMessagesTable(t, store)

	err := store.AppendMessagesWithForkPoint("test-session", []*TranscriptMessage{
		testMessage(0, "user", "uuid-1", "", `[{"type":"text","text":"hi"}]`),
	}, "")
	wantWrappedError(t, err, "dedup check: SQL logic error: no such table: messages (1)")
}

// Lines 711-712: an append failure mid-batch aborts with the insert error.
func TestAppendMessagesWithForkPoint_InsertErrorWrapsCause(t *testing.T) {
	store := openTestStore(t)
	sessionID := "test-session"
	createTestSession(t, store, sessionID)

	err := store.AppendMessagesWithForkPoint(sessionID, []*TranscriptMessage{
		testMessage(0, "user", "dup-uuid", "", `[{"type":"text","text":"first"}]`),
		testMessage(0, "assistant", "dup-uuid", "", `[{"type":"text","text":"second"}]`),
	}, "")
	wantWrappedError(t, err, "insert message: constraint failed: UNIQUE constraint failed: messages.uuid (2067)")

	loaded, err := store.LoadMessages(sessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("got %d messages after a failed batch, want 0 (transaction must roll back)", len(loaded))
	}
}
