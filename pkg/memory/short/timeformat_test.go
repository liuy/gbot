package short

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/tool/toolresult"
)

// canonicalTimeRe matches the single storage format all time columns must use
// (driver _time_format=sqlite&_timezone=UTC): YYYY-MM-DD HH:MM:SS[.ffffff]+00:00.
// Only when every row shares it does SQL string comparison (ORDER BY, >, <)
// equal chronological comparison.
var canonicalTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?\+00:00$`)

// pinLocalCST fixes the local zone for the cleanup boundary: both the naive
// cutoff string and time.Now() render through time.Local, so the red/green
// outcome must not depend on the host timezone.
func pinLocalCST(t *testing.T) {
	t.Helper()
	orig := time.Local
	time.Local = time.FixedZone("CST-TEST", 8*3600)
	t.Cleanup(func() { time.Local = orig })
}

func TestStoreWritesCanonicalUTCTimestamps(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	var created, updated string
	err = store.db.QueryRow(
		"SELECT CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM sessions WHERE session_id = ?",
		ses.SessionID,
	).Scan(&created, &updated)
	if err != nil {
		t.Fatalf("raw read sessions timestamps: %v", err)
	}
	if !canonicalTimeRe.MatchString(created) {
		t.Errorf("created_at = %q, want canonical UTC format", created)
	}
	if !canonicalTimeRe.MatchString(updated) {
		t.Errorf("updated_at = %q, want canonical UTC format", updated)
	}

	got, err := store.GetSession(ses.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Errorf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
	}
	if !got.CreatedAt.Equal(ses.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v (nanosecond precision must survive the round trip)", got.CreatedAt, ses.CreatedAt)
	}
}

func TestSessionUpdatesWriteCanonicalTimestamp(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	updates := []struct {
		name string
		call func() error
	}{
		{"UpdateSessionTimestamp", func() error { return store.UpdateSessionTimestamp(ses.SessionID) }},
		{"UpdateSessionTitle", func() error { return store.UpdateSessionTitle(ses.SessionID, "title") }},
		{"UpdateContextTokens", func() error { return store.UpdateContextTokens(ses.SessionID, 42) }},
	}
	for _, u := range updates {
		if err := u.call(); err != nil {
			t.Fatalf("%s: %v", u.name, err)
		}
		var updated string
		err := store.db.QueryRow(
			"SELECT CAST(updated_at AS TEXT) FROM sessions WHERE session_id = ?",
			ses.SessionID,
		).Scan(&updated)
		if err != nil {
			t.Fatalf("%s raw read updated_at: %v", u.name, err)
		}
		if !canonicalTimeRe.MatchString(updated) {
			t.Errorf("%s: updated_at = %q, want canonical UTC format", u.name, updated)
		}
	}
}

func TestMessageCreatedAtNonUTCZoneStoredAsUTC(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	createdAt := time.Date(2026, 3, 15, 17, 30, 0, 123456789, time.FixedZone("X", 8*3600))
	msg := &TranscriptMessage{
		UUID:      "msg-nonutc",
		Type:      "user",
		Content:   `[{"type":"text","text":"tz probe"}]`,
		CreatedAt: createdAt,
	}
	if err := store.AppendMessage(ses.SessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	var raw string
	if err := store.db.QueryRow(
		"SELECT CAST(created_at AS TEXT) FROM messages WHERE uuid = ?",
		msg.UUID,
	).Scan(&raw); err != nil {
		t.Fatalf("raw read messages.created_at: %v", err)
	}
	if !strings.HasPrefix(raw, "2026-03-15 09:30:00") {
		t.Errorf("created_at = %q, want UTC wall clock prefix 2026-03-15 09:30:00", raw)
	}
	if !strings.HasSuffix(raw, "+00:00") {
		t.Errorf("created_at = %q, want +00:00 suffix", raw)
	}

	msgs, err := store.LoadMessages(ses.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("LoadMessages count = %d, want 1", len(msgs))
	}
	if !msgs[0].CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v", msgs[0].CreatedAt, createdAt)
	}
}

func TestCleanupOldSessions_TimezoneBoundary(t *testing.T) {
	pinLocalCST(t)

	store := openTestStore(t)
	oldSes, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession old: %v", err)
	}
	recentSes, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession recent: %v", err)
	}

	if _, err := store.db.Exec(
		"UPDATE sessions SET updated_at = ? WHERE session_id = ?",
		time.Now().Add(-30*24*time.Hour-time.Hour), oldSes.SessionID, // REAL-TIME: offset relative to the real clock CleanupOldSessions reads
	); err != nil {
		t.Fatalf("set old updated_at: %v", err)
	}
	if _, err := store.db.Exec(
		"UPDATE sessions SET updated_at = ? WHERE session_id = ?",
		time.Now().Add(-30*24*time.Hour+time.Hour), recentSes.SessionID, // REAL-TIME: offset relative to the real clock CleanupOldSessions reads
	); err != nil {
		t.Fatalf("set recent updated_at: %v", err)
	}

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only the session 1h past the 30d boundary)", removed)
	}
}

func TestFileHistoryAndContentReplacementsWriteCanonicalTimestamp(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := store.SaveFileHistoryState(ses.SessionID, filehistory.FileHistoryState{}); err != nil {
		t.Fatalf("SaveFileHistoryState: %v", err)
	}
	if err := store.SaveFileHistoryState(ses.SessionID, filehistory.FileHistoryState{}); err != nil {
		t.Fatalf("SaveFileHistoryState second: %v", err)
	}
	var fhCreated string
	if err := store.db.QueryRow(
		"SELECT CAST(created_at AS TEXT) FROM file_history_snapshots WHERE session_id = ?",
		ses.SessionID,
	).Scan(&fhCreated); err != nil {
		t.Fatalf("raw read file_history_snapshots.created_at: %v", err)
	}
	if !canonicalTimeRe.MatchString(fhCreated) {
		t.Errorf("file_history_snapshots.created_at = %q, want canonical UTC format", fhCreated)
	}

	records := []toolresult.ContentReplacementRecord{
		{Kind: "tool-result", ToolUseID: "tu-1", Replacement: "preview"},
	}
	if err := store.SaveContentReplacementRecords(ses.SessionID, records); err != nil {
		t.Fatalf("SaveContentReplacementRecords: %v", err)
	}
	var crCreated string
	if err := store.db.QueryRow(
		"SELECT CAST(created_at AS TEXT) FROM content_replacements WHERE session_id = ?",
		ses.SessionID,
	).Scan(&crCreated); err != nil {
		t.Fatalf("raw read content_replacements.created_at: %v", err)
	}
	if !canonicalTimeRe.MatchString(crCreated) {
		t.Errorf("content_replacements.created_at = %q, want canonical UTC format", crCreated)
	}
}

func TestTimestampsSurviveReopen(t *testing.T) {
	path := t.TempDir() + "/test.db"
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	msgCreatedAt := time.Date(2026, 3, 15, 17, 30, 0, 123456789, time.FixedZone("X", 8*3600))
	msg := &TranscriptMessage{
		UUID:      "msg-reopen",
		Type:      "user",
		Content:   `[{"type":"text","text":"reopen probe"}]`,
		CreatedAt: msgCreatedAt,
	}
	if err := store.AppendMessage(ses.SessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	got, err := store2.GetSession(ses.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.CreatedAt.Equal(ses.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, ses.CreatedAt)
	}
	msgs, err := store2.LoadMessages(ses.SessionID)
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("LoadMessages count = %d, want 1", len(msgs))
	}
	if !msgs[0].CreatedAt.Equal(msgCreatedAt) {
		t.Errorf("message CreatedAt = %v, want %v", msgs[0].CreatedAt, msgCreatedAt)
	}

	var created, updated, msgCreated string
	err = store2.db.QueryRow(
		"SELECT CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM sessions WHERE session_id = ?",
		ses.SessionID,
	).Scan(&created, &updated)
	if err != nil {
		t.Fatalf("raw read sessions timestamps: %v", err)
	}
	if err := store2.db.QueryRow(
		"SELECT CAST(created_at AS TEXT) FROM messages WHERE uuid = ?",
		msg.UUID,
	).Scan(&msgCreated); err != nil {
		t.Fatalf("raw read messages.created_at: %v", err)
	}
	if !canonicalTimeRe.MatchString(created) {
		t.Errorf("sessions.created_at = %q, want canonical UTC format", created)
	}
	if !canonicalTimeRe.MatchString(updated) {
		t.Errorf("sessions.updated_at = %q, want canonical UTC format", updated)
	}
	if !canonicalTimeRe.MatchString(msgCreated) {
		t.Errorf("messages.created_at = %q, want canonical UTC format", msgCreated)
	}
}
