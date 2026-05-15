package short

import (
	"fmt"
	"log/slog"
	"time"
)

// CleanupOldSessions deletes sessions (and their messages/FTS entries) older than maxAge.
// Equivalent to TS cleanupOldSessionFiles (cleanup.ts:155) which deletes entire old
// session JSONL files by timestamp cutoff.
func (s *Store) CleanupOldSessions(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).Format("2006-01-02 15:04:05")

	// Find sessions to delete
	rows, err := s.db.Query(
		"SELECT session_id FROM sessions WHERE updated_at < ?",
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("query old sessions: %w", err)
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan session id: %w", err)
		}
		sessionIDs = append(sessionIDs, id)
	}

	if len(sessionIDs) == 0 {
		return 0, nil
	}

	// Delete in transaction
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range sessionIDs {
		// Delete FTS entries
		if _, err := tx.Exec(
			"DELETE FROM messages_fts_map WHERE seq IN "+
				"(SELECT seq FROM messages WHERE session_id = ?)",
			id,
		); err != nil {
			return 0, fmt.Errorf("delete FTS for session %s: %w", id[:8], err)
		}

		// Delete messages
		if _, err := tx.Exec("DELETE FROM messages WHERE session_id = ?", id); err != nil {
			return 0, fmt.Errorf("delete messages for session %s: %w", id[:8], err)
		}

		// Delete session
		if _, err := tx.Exec("DELETE FROM sessions WHERE session_id = ?", id); err != nil {
			return 0, fmt.Errorf("delete session %s: %w", id[:8], err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	slog.Info("cleanup: removed old sessions", "count", len(sessionIDs))
	return len(sessionIDs), nil
}
