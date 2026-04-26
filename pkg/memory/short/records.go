package short

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/liuy/gbot/pkg/toolresult"
)

// SaveContentReplacementRecords persists budget replacement records to the transcript.
// Stored as a metadata message with subtype "content_replacement".
// Each call appends a new metadata message; records accumulate across turns.
// TS align: writeToTranscript callback in applyToolResultBudget (toolResultStorage.ts:924-936).
func (s *Store) SaveContentReplacementRecords(sessionID string, records []toolresult.ContentReplacementRecord) error {
	if len(records) == 0 {
		return nil
	}
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("marshal content replacement records: %w", err)
	}
	msg := &TranscriptMessage{
		UUID:    fmt.Sprintf("budget-%d", time.Now().UnixNano()),
		Type:    "metadata",
		Subtype: "content_replacement",
		Content: string(data),
	}
	return s.AppendMessage(sessionID, msg)
}

// LoadContentReplacementRecords loads all budget replacement records from the transcript.
// Merges records from all metadata messages with subtype "content_replacement".
// TS align: loadContentReplacementRecords (toolResultStorage.ts:960-992).
func (s *Store) LoadContentReplacementRecords(sessionID string) ([]toolresult.ContentReplacementRecord, error) {
	query := `
		SELECT content FROM messages
		WHERE session_id = ? AND type = 'metadata' AND subtype = 'content_replacement'
		ORDER BY seq ASC
	`
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query content replacement records: %w", err)
	}
	defer rows.Close()

	var all []toolresult.ContentReplacementRecord
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, fmt.Errorf("scan content: %w", err)
		}
		var batch []toolresult.ContentReplacementRecord
		if err := json.Unmarshal([]byte(content), &batch); err != nil {
			continue // skip malformed records
		}
		all = append(all, batch...)
	}
	return all, nil
}
