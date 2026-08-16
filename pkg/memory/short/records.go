package short

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/tool/toolresult"
)

// SaveContentReplacementRecords persists budget replacement records.
// TS align: content-replacement is a separate entry type in JSONL, not mixed into messages.
func (s *Store) SaveContentReplacementRecords(sessionID string, records []toolresult.ContentReplacementRecord) error {
	if len(records) == 0 {
		return nil
	}
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshal content replacement records: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO content_replacements (session_id, replacements, created_at) VALUES (?, ?, ?)`,
		sessionID, string(data), time.Now(),
	)
	return err
}

// LoadContentReplacementRecords loads all budget replacement records.
// Merges records from all rows for the session.
func (s *Store) LoadContentReplacementRecords(sessionID string) ([]toolresult.ContentReplacementRecord, error) {
	rows, err := s.db.Query(
		`SELECT replacements FROM content_replacements WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query content replacement records: %w", err)
	}
	defer rows.Close()

	var all []toolresult.ContentReplacementRecord
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan content replacement: %w", err)
		}
		var batch []toolresult.ContentReplacementRecord
		if err := json.Unmarshal([]byte(data), &batch); err != nil {
			continue
		}
		all = append(all, batch...)
	}
	return all, nil
}

// SaveFileHistoryState persists the file history snapshot state to a dedicated table.
// TS align: file-history-snapshot is a separate entry type in JSONL, not mixed into messages.
func (s *Store) SaveFileHistoryState(sessionID string, state filehistory.FileHistoryState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal file history state: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO file_history_snapshots (session_id, snapshot_data, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET snapshot_data = excluded.snapshot_data, created_at = excluded.created_at`,
		sessionID, string(data), time.Now(),
	)
	return err
}

// LoadFileHistoryState loads the most recent file history state.
// Returns nil if no persisted state exists.
func (s *Store) LoadFileHistoryState(sessionID string) (*filehistory.FileHistoryState, error) {
	var data string
	err := s.db.QueryRow(
		`SELECT snapshot_data FROM file_history_snapshots WHERE session_id = ?`,
		sessionID,
	).Scan(&data)
	if err != nil {
		return nil, nil
	}
	var state filehistory.FileHistoryState
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return nil, fmt.Errorf("unmarshal file history state: %w", err)
	}
	return &state, nil
}
