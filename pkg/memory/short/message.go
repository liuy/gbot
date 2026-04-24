package short

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// AppendMessage adds a single message to the session.
// TS align: insertMessageChain (sessionStorage.ts:993-1083)
// Maintains parent_uuid chain; progress messages don't advance the chain.
func (s *Store) AppendMessage(sessionID string, msg *TranscriptMessage) error {

	return s.appendMessage(sessionID, msg)
}

// AppendMessages adds multiple messages to the session in a single transaction.
// TS align: recordTranscript → insertMessageChain for batch writes.
func (s *Store) AppendMessages(sessionID string, msgs []*TranscriptMessage) error {

	// Begin transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Track the last chain participant's UUID
	lastChainUUID := s.getLastChainUUID(tx, sessionID)

	for _, msg := range msgs {
		if err := s.appendMessageTx(tx, sessionID, msg, lastChainUUID); err != nil {
			return err
		}

		// Update lastChainUUID only for chain participants
		if isChainParticipant(msg) {
			lastChainUUID = msg.UUID
		}
	}

	return tx.Commit()
}

// LoadMessages loads all messages for a session ordered by seq.
// TS align: loadTranscriptFile (sessionStorage.ts:370-440)
func (s *Store) LoadMessages(sessionID string) ([]*TranscriptMessage, error) {

	query := `
		SELECT seq, session_id, uuid, parent_uuid, logical_parent_uuid,
		       is_sidechain, type, subtype, content, created_at
		FROM messages
		WHERE session_id = ?
		ORDER BY seq ASC
	`

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var messages []*TranscriptMessage
	for rows.Next() {
		msg, err := s.scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	// Apply preserved segment relinks and snip removals after loading.
	// TS align: loadTranscriptFile (sessionStorage.ts:3704-3705)
	messages = applyPreservedSegmentRelinksOnLoad(messages)
	messages = ApplySnipRemovals(messages)

	return messages, nil
}

// LoadMessagesAfterSeq loads messages with seq > afterSeq.
// Used for loading messages after a compact boundary.
// TS align: getMessagesAfterCompactBoundary (sessionStorage.ts:2581-2603)
func (s *Store) LoadMessagesAfterSeq(sessionID string, afterSeq int) ([]*TranscriptMessage, error) {

	query := `
		SELECT seq, session_id, uuid, parent_uuid, logical_parent_uuid,
		       is_sidechain, type, subtype, content, created_at
		FROM messages
		WHERE session_id = ? AND seq > ?
		ORDER BY seq ASC
	`

	rows, err := s.db.Query(query, sessionID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("query messages after seq: %w", err)
	}
	defer rows.Close()

	var messages []*TranscriptMessage
	for rows.Next() {
		msg, err := s.scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

// GetLastBoundary finds the last compact boundary message.
// Returns the message, its seq, and error. Returns nil, 0, nil if none found.
// TS align: findLastCompactBoundaryIndex (messages.ts:4618-4629)
func (s *Store) GetLastBoundary(sessionID string) (*TranscriptMessage, int, error) {

	query := `
		SELECT seq, session_id, uuid, parent_uuid, logical_parent_uuid,
		       is_sidechain, type, subtype, content, created_at
		FROM messages
		WHERE session_id = ? AND type = 'system' AND subtype = 'compact_boundary'
		ORDER BY seq DESC
		LIMIT 1
	`

	msg, err := s.queryOneMessage(query, sessionID)
	if err == sql.ErrNoRows {
		return nil, 0, nil // No boundary found
	}
	if err != nil {
		return nil, 0, fmt.Errorf("query last boundary: %w", err)
	}

	return msg, int(msg.Seq), nil
}

// RemoveMessageByUUID deletes a message by its UUID (tombstone operation).
// TS align: removeMessageByUuid (sessionStorage.ts:2760-2790)
func (s *Store) RemoveMessageByUUID(sessionID, uuid string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Get seq before deletion (needed for FTS cleanup)
	var seq int64
	err = tx.QueryRow(
		"SELECT seq FROM messages WHERE session_id = ? AND uuid = ?",
		sessionID, uuid,
	).Scan(&seq)
	if err != nil {
		return fmt.Errorf("get message seq: %w", err)
	}

	// Delete FTS map first (FK references messages.seq)
	_, err = tx.Exec("DELETE FROM messages_fts_map WHERE seq = ?", seq)
	if err != nil {
		slog.Warn("failed to delete FTS map", "seq", seq, "error", err)
	}

	// Now safe to delete the message
	_, err = tx.Exec(
		"DELETE FROM messages WHERE session_id = ? AND uuid = ?",
		sessionID, uuid,
	)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}

	return tx.Commit()
}

// MessageExists checks if a message with the given UUID exists in the session.
// TS align: doesMessageExistInSession (sessionStorage.ts:1590-1596)
func (s *Store) MessageExists(sessionID, uuid string) (bool, error) {

	var exists bool
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM messages WHERE session_id = ? AND uuid = ?)",
		sessionID, uuid,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check message exists: %w", err)
	}

	return exists, nil
}

// RecordSidechainTranscript stores a sub-agent's transcript.
// Messages are marked with is_sidechain=1.
// TS align: recordSidechainTranscript (sessionStorage.ts:2800-2830)
func (s *Store) RecordSidechainTranscript(sessionID string, agentID string, messages []*TranscriptMessage) error {

	// Begin transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Track the last chain participant's UUID
	lastChainUUID := s.getLastChainUUID(tx, sessionID)

	for _, msg := range messages {
		msg.IsSidechain = 1 // Mark as sidechain
		if err := s.appendMessageTx(tx, sessionID, msg, lastChainUUID); err != nil {
			return err
		}

		// Update lastChainUUID only for chain participants
		if isChainParticipant(msg) {
			lastChainUUID = msg.UUID
		}
	}

	return tx.Commit()
}

// LoadSidechainTranscript loads messages for a specific agent from the sidechain.
// TS align: loadSubagentTranscripts (sessionStorage.ts:2840-2860)
func (s *Store) LoadSidechainTranscript(sessionID string, agentID string) ([]*TranscriptMessage, error) {

	query := `
		SELECT m.seq, m.session_id, m.uuid, m.parent_uuid, m.logical_parent_uuid,
		       m.is_sidechain, m.type, m.subtype, m.content, m.created_at
		FROM messages m
		WHERE m.session_id = ? AND m.is_sidechain = 1
		ORDER BY m.seq ASC
	`

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query sidechain messages: %w", err)
	}
	defer rows.Close()

	var messages []*TranscriptMessage
	for rows.Next() {
		msg, err := s.scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}

// FindLatestMessage finds the latest message matching the filter function.
// Returns nil, nil if no message matches.
// TS align: findLatestMessage (sessionStorage.ts:2046-2061)
func (s *Store) FindLatestMessage(sessionID string, filter func(*TranscriptMessage) bool) (*TranscriptMessage, error) {
	// Delegate locking to LoadMessages — holding our own RLock here would
	// recursively RLock when LoadMessages is called, risking deadlock under
	// writer contention (Go's sync.RWMutex is not reentrant).
	messages, err := s.LoadMessages(sessionID)
	if err != nil {
		return nil, err
	}

	var latest *TranscriptMessage
	var maxTime time.Time

	for _, msg := range messages {
		if !filter(msg) {
			continue
		}
		if msg.CreatedAt.After(maxTime) {
			maxTime = msg.CreatedAt
			latest = msg
		}
	}

	return latest, nil
}

// CountVisibleMessages counts messages that are not progress type.
// Used by TUI for display counts.
// TS align: countVisibleMessages (sessionStorage.ts:2453)
func (s *Store) CountVisibleMessages(sessionID string) (int, error) {

	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM messages WHERE session_id = ? AND type != 'progress'",
		sessionID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count visible messages: %w", err)
	}

	return count, nil
}

// GetPreBoundaryMetadata retrieves session metadata (agent_type, mode, settings).
// Go strategy: query sessions table directly (TS scans metadata messages).
// TS align: scanPreBoundaryMetadata (sessionStorage.ts:3157)
func (s *Store) GetPreBoundaryMetadata(sessionID string) (*PreBoundaryMetadata, error) {

	var agentType, mode, settingsJSON sql.NullString
	err := s.db.QueryRow(`
		SELECT agent_type, mode, settings
		FROM sessions
		WHERE session_id = ?
	`, sessionID).Scan(&agentType, &mode, &settingsJSON)
	if err != nil {
		return nil, fmt.Errorf("query session metadata: %w", err)
	}

	metadata := &PreBoundaryMetadata{
		AgentType: agentType.String,
		Mode:      mode.String,
	}

	if settingsJSON.Valid && settingsJSON.String != "" && settingsJSON.String != "{}" {
		if err := json.Unmarshal([]byte(settingsJSON.String), &metadata.Settings); err != nil {
			slog.Warn("failed to parse settings JSON", "error", err)
			metadata.Settings = make(map[string]string)
		}
	} else {
		metadata.Settings = make(map[string]string)
	}

	return metadata, nil
}

// appendMessage adds a single message within a transaction.
func (s *Store) appendMessage(sessionID string, msg *TranscriptMessage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lastChainUUID := s.getLastChainUUID(tx, sessionID)
	if err := s.appendMessageTx(tx, sessionID, msg, lastChainUUID); err != nil {
		return err
	}

	return tx.Commit()
}

// appendMessageTx adds a message within a transaction.
func (s *Store) appendMessageTx(tx *sql.Tx, sessionID string, msg *TranscriptMessage, lastChainUUID string) error {
	// Set parent_uuid
	// - First message: empty string
	// - Compact boundary: empty string (new chain root), logical_parent_uuid = lastChainUUID
	// - Other messages: parent_uuid = lastChainUUID
	isCompactBoundary := msg.Type == "system" && msg.Subtype == "compact_boundary"

	if isCompactBoundary {
		msg.ParentUUID = ""
		msg.LogicalParentUUID = lastChainUUID
	} else {
		msg.ParentUUID = lastChainUUID
	}

	// Preserve the message's CreatedAt; default to now if zero
	createdAt := msg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	// Insert message
	query := `
		INSERT INTO messages (session_id, uuid, parent_uuid, logical_parent_uuid,
		                     is_sidechain, type, subtype, content, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	result, err := tx.Exec(query, sessionID, msg.UUID, msg.ParentUUID,
		msg.LogicalParentUUID, msg.IsSidechain, msg.Type, msg.Subtype, msg.Content,
		createdAt)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	// Update FTS index
	seq, _ := result.LastInsertId()
	text := ExtractTextFromJSON(msg.Content)
	if text != "" {
		if ftsErr := s.insertFTS(tx, seq, msg.Content); ftsErr != nil {
			slog.Warn("FTS index failed", "seq", seq, "error", ftsErr)
		}
	}

	// Update session timestamp for search result freshness
	if ftsErr := s.updateSessionFTS(tx, sessionID); ftsErr != nil {
		slog.Warn("update session FTS timestamp failed", "session", sessionID, "error", ftsErr)
	}

	return nil
}

// getLastChainUUID gets the last chain participant's UUID for a session.
func (s *Store) getLastChainUUID(tx *sql.Tx, sessionID string) string {
	var uuid string

	query := `
		SELECT uuid FROM messages
		WHERE session_id = ? AND type != 'progress'
		ORDER BY seq DESC LIMIT 1
	`

	err := tx.QueryRow(query, sessionID).Scan(&uuid)

	if err != nil {
		if err == sql.ErrNoRows {
			return "" // No messages yet
		}
		slog.Warn("failed to get last chain UUID", "error", err)
		return ""
	}

	return uuid
}

// queryOneMessage executes a query and returns a single message.
func (s *Store) queryOneMessage(query string, args ...any) (*TranscriptMessage, error) {
	row := s.db.QueryRow(query, args...)
	return s.scanMessageFromRow(row)
}

// scanMessage scans a message from a rows object.
func (s *Store) scanMessage(rows *sql.Rows) (*TranscriptMessage, error) {
	var msg TranscriptMessage
	err := rows.Scan(
		&msg.Seq, &msg.SessionID, &msg.UUID, &msg.ParentUUID, &msg.LogicalParentUUID,
		&msg.IsSidechain, &msg.Type, &msg.Subtype, &msg.Content, &msg.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// scanMessageFromRow scans a message from a single row.
func (s *Store) scanMessageFromRow(row *sql.Row) (*TranscriptMessage, error) {
	var msg TranscriptMessage
	err := row.Scan(
		&msg.Seq, &msg.SessionID, &msg.UUID, &msg.ParentUUID, &msg.LogicalParentUUID,
		&msg.IsSidechain, &msg.Type, &msg.Subtype, &msg.Content, &msg.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}
