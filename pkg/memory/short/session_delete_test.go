package short

import "testing"

func seedEngineSession(t *testing.T, store *Store, projectDir, engineID, uuid string) string {
	t.Helper()
	ses, err := store.CreateSessionWithEngine(projectDir, "m", engineID)
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}
	msg := testMessage(0, "user", uuid, "", `[{"type":"text","text":"hi"}]`)
	if err := store.AppendMessage(ses.SessionID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	return ses.SessionID
}

func countRows(t *testing.T, store *Store, table, where, arg string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+where, arg).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestDeleteSessionsByEngine_RemovesOnlyThatEngine(t *testing.T) {
	store := openTestStore(t)
	mainSID := seedEngineSession(t, store, "/proj", "main", "u-main-1")
	e2SID := seedEngineSession(t, store, "/proj", "e2", "u-e2-1")

	removed, err := store.DeleteSessionsByEngine("/proj", "e2")
	if err != nil {
		t.Fatalf("DeleteSessionsByEngine: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if got := countRows(t, store, "sessions", "session_id = ?", e2SID); got != 0 {
		t.Errorf("e2 session rows = %d, want 0", got)
	}
	if got := countRows(t, store, "messages", "session_id = ?", e2SID); got != 0 {
		t.Errorf("e2 message rows = %d, want 0", got)
	}
	if got := countRows(t, store, "messages_fts_map", "seq IN (SELECT seq FROM messages WHERE session_id = ?)", e2SID); got != 0 {
		t.Errorf("e2 fts rows = %d, want 0", got)
	}
	if got := countRows(t, store, "sessions", "session_id = ?", mainSID); got != 1 {
		t.Errorf("main session rows = %d, want 1 (other engine untouched)", got)
	}
	if got := countRows(t, store, "messages", "session_id = ?", mainSID); got != 1 {
		t.Errorf("main message rows = %d, want 1", got)
	}
}

func TestDeleteSessionsByEngine_ScopedToProjectDir(t *testing.T) {
	store := openTestStore(t)
	seedEngineSession(t, store, "/proj-a", "e2", "u-a-1")
	seedEngineSession(t, store, "/proj-b", "e2", "u-b-1")

	removed, err := store.DeleteSessionsByEngine("/proj-a", "e2")
	if err != nil {
		t.Fatalf("DeleteSessionsByEngine: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if got := countRows(t, store, "sessions", "engine_id = ?", "e2"); got != 1 {
		t.Errorf("remaining e2 sessions = %d, want 1 (other project)", got)
	}
}

func TestDeleteSessionsByEngine_NothingToDelete(t *testing.T) {
	store := openTestStore(t)
	removed, err := store.DeleteSessionsByEngine("/proj", "ghost")
	if err != nil {
		t.Fatalf("DeleteSessionsByEngine: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}
