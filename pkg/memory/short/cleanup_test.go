package short

import (
	"testing"
	"time"
)

func TestCleanupOldSessions_DeletesExpired(t *testing.T) {
	store := openTestStore(t)

	// Create old session (updated 31 days ago)
	createTestSession(t, store, "old-session")

	// Add a message first (AppendMessage resets updated_at via updateSessionFTS)
	msg := testMessage(0, "user", "old-msg-1", "", `[{"type":"text","text":"old"}]`)
	if err := store.AppendMessage("old-session", msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Now set the session to be old (AFTER AppendMessage so FTS update doesn't reset it)
	_, err := store.db.Exec(
		"UPDATE sessions SET updated_at = datetime('now', '-31 days') WHERE session_id = ?",
		"old-session",
	)
	if err != nil {
		t.Fatalf("update old session: %v", err)
	}

	// Create recent session
	createTestSession(t, store, "recent-session")

	// Verify both sessions exist
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatalf("count sessions setup: %v", err)
	}
	if count != 2 {
		t.Fatalf("setup: got %d sessions, want 2", count)
	}

	// Cleanup sessions older than 30 days
	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}

	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	// Verify only recent session remains
	if err := store.db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("sessions after cleanup = %d, want 1", count)
	}

	// Verify old session's messages are deleted
	if err := store.db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE session_id = ?", "old-session",
	).Scan(&count); err != nil {
		t.Fatalf("count old messages: %v", err)
	}
	if count != 0 {
		t.Errorf("old session messages = %d, want 0", count)
	}

	// Verify recent session still exists
	exists, _ := store.MessageExists("recent-session", "old-msg-1")
	if exists {
		t.Error("old-msg-1 should not exist in recent-session")
	}
}

func TestCleanupOldSessions_NothingToDelete(t *testing.T) {
	store := openTestStore(t)
	createTestSession(t, store, "recent-session")

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}

	if removed != 0 {
		t.Errorf("removed = %d, want 0 (no old sessions)", removed)
	}
}

func TestCleanupOldSessions_EmptyDB(t *testing.T) {
	store := openTestStore(t)

	removed, err := store.CleanupOldSessions(30 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}

	if removed != 0 {
		t.Errorf("removed = %d, want 0 (empty db)", removed)
	}
}
