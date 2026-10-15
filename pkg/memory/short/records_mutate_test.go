package short

import (
	"errors"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/tool/toolresult"
)

// wantErrWrappingCause pins an error's exact text and its %w cause chain:
// an errorf-wrap mutation (%w → %v) keeps the text but drops Unwrap.
func wantErrWrappingCause(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap its cause", want)
	}
}

// records.go:15-16 — an empty batch must not reach the INSERT at all.
func TestSaveContentReplacementRecords_EmptyWritesNoRow(t *testing.T) {
	store := newRecordsTestStore(t)

	for _, batch := range [][]toolresult.ContentReplacementRecord{nil, {}} {
		if err := store.SaveContentReplacementRecords("test-session", batch); err != nil {
			t.Fatalf("SaveContentReplacementRecords(%v): %v", batch, err)
		}
	}

	var rows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM content_replacements`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("content_replacements rows = %d, want 0 (empty batch short-circuits)", rows)
	}
}

// wantErrExact pins the error text for paths that return the driver error
// unwrapped (Save* ends in a bare `return err`).
func wantErrExact(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %q", want)
	}
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// records.go:26 — the Exec error must be returned, not swallowed.
func TestSaveContentReplacementRecords_ForeignKeyViolation(t *testing.T) {
	store := newRecordsTestStore(t)

	err := store.SaveContentReplacementRecords("no-such-session",
		[]toolresult.ContentReplacementRecord{{Kind: "tool-result", ToolUseID: "tr-1", Replacement: "p"}})
	wantErrExact(t, err, "constraint failed: FOREIGN KEY constraint failed (787)")

	var rows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM content_replacements`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("content_replacements rows = %d, want 0", rows)
	}
}

// records.go:36-37 — a failed read query is reported, and the nil *sql.Rows
// it leaves behind must never be dereferenced.
func TestLoadContentReplacementRecords_QueryError(t *testing.T) {
	store := newRecordsTestStore(t)
	if err := store.readDB.Close(); err != nil {
		t.Fatalf("close read pool: %v", err)
	}

	loaded, err := store.LoadContentReplacementRecords("test-session")
	wantErrWrappingCause(t, err, "query content replacement records: sql: database is closed")
	if loaded != nil {
		t.Errorf("loaded = %v, want nil on query error", loaded)
	}
}

// records.go:49 — a malformed batch is skipped, not a stop signal: later rows
// still contribute their records.
func TestLoadContentReplacementRecords_MalformedRowDoesNotStopScan(t *testing.T) {
	store := newRecordsTestStore(t)

	// created_at decides row order (ORDER BY created_at ASC), so the
	// malformed row is deliberately the oldest.
	if _, err := store.DB().Exec(
		`INSERT INTO content_replacements (session_id, replacements, created_at) VALUES (?, ?, ?)`,
		"test-session", "not-json", "2020-01-01 00:00:00"); err != nil {
		t.Fatalf("insert malformed: %v", err)
	}
	if err := store.SaveContentReplacementRecords("test-session",
		[]toolresult.ContentReplacementRecord{{Kind: "tool-result", ToolUseID: "tr-after", Replacement: "p"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := store.LoadContentReplacementRecords("test-session")
	if err != nil {
		t.Fatalf("LoadContentReplacementRecords: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d records, want 1 (malformed row skipped, later row kept)", len(loaded))
	}
	if loaded[0].ToolUseID != "tr-after" {
		t.Errorf("record = %+v, want tr-after", loaded[0])
	}
}

// records.go:60-61 — an unmarshalable state fails the save instead of
// writing an empty blob.
func TestSaveFileHistoryState_MarshalError(t *testing.T) {
	store := newRecordsTestStore(t)

	state := filehistory.FileHistoryState{
		Snapshots: []filehistory.FileHistorySnapshot{{
			MessageID: "msg-1",
			Timestamp: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
	}
	err := store.SaveFileHistoryState("test-session", state)
	wantErrWrappingCause(t, err,
		"marshal file history state: json: error calling MarshalJSON for type *time.Time: year outside of range [0,9999]")

	var rows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM file_history_snapshots`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Errorf("file_history_snapshots rows = %d, want 0", rows)
	}
}

// records.go:68 — the upsert error must be returned.
func TestSaveFileHistoryState_ForeignKeyViolation(t *testing.T) {
	store := newRecordsTestStore(t)

	err := store.SaveFileHistoryState("no-such-session", filehistory.FileHistoryState{SnapshotSequence: 3})
	wantErrExact(t, err, "constraint failed: FOREIGN KEY constraint failed (787)")
}

// records.go:83-84 — a stored blob that is not valid JSON is an error, not a
// zero-valued state.
func TestLoadFileHistoryState_MalformedBlob(t *testing.T) {
	store := newRecordsTestStore(t)

	if _, err := store.DB().Exec(
		`INSERT INTO file_history_snapshots (session_id, snapshot_data) VALUES (?, ?)`,
		"test-session", "xx{not json"); err != nil {
		t.Fatalf("insert blob: %v", err)
	}

	state, err := store.LoadFileHistoryState("test-session")
	wantErrWrappingCause(t, err,
		"unmarshal file history state: invalid character 'x' looking for beginning of value")
	if state != nil {
		t.Errorf("state = %+v, want nil on unmarshal error", state)
	}
}
