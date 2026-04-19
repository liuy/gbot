package short

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ForkSession creates a child session that branches from a parent session.
// The fork inherits context from forkPointSeq onward.
// Used for agent sub-conversations that run in parallel.
//
// TS: sessionStorage.ts:2900-2960 (forkSession)
func (s *Store) ForkSession(parentSessionID string, forkPointSeq int, agentType string) (*Session, error) {

	// Load parent session
	parent, err := s.getSessionLocked(parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("load parent session: %w", err)
	}
	if parent == nil {
		return nil, fmt.Errorf("parent session %q not found", parentSessionID)
	}

	// Create child session
	childID := uuid.New().String()
	child := &Session{
		SessionID:       childID,
		ProjectDir:      parent.ProjectDir,
		Model:           parent.Model,
		Title:           parent.Title,
		ParentSessionID: parentSessionID,
		ForkPointSeq:    forkPointSeq,
		AgentType:       agentType,
		Mode:            parent.Mode,
		Settings:        parent.Settings,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	if err := s.insertSessionLocked(child); err != nil {
		return nil, fmt.Errorf("insert fork session: %w", err)
	}

	// Copy messages from fork point onward
	if err := s.copyMessagesToFork(parentSessionID, childID, forkPointSeq); err != nil {
		return nil, fmt.Errorf("copy messages to fork: %w", err)
	}

	slog.Info("forked session", "child", childID, "parent", parentSessionID, "fork_seq", forkPointSeq, "dir", child.ProjectDir)

	return child, nil
}

// copyMessagesToFork copies messages from parent to child session starting at forkPointSeq.
// Rebuilds the parent_uuid chain so the child has its own independent chain.
func (s *Store) copyMessagesToFork(parentSessionID, childSessionID string, forkPointSeq int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Load parent messages from fork point
	query := `
		SELECT seq, uuid, parent_uuid, logical_parent_uuid,
		       is_sidechain, type, subtype, content, created_at
		FROM messages
		WHERE session_id = ? AND seq >= ?
		ORDER BY seq ASC
	`

	rows, err := tx.Query(query, parentSessionID, forkPointSeq)
	if err != nil {
		return fmt.Errorf("query parent messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Track UUID mapping for chain rebuild
	uuidMap := make(map[string]string) // old UUID → new UUID
	var lastChainUUID string

	for rows.Next() {
		var seq int64
		var msgUUID, parentUUID, logicalParentUUID string
		var isSidechain int
		var msgType, subtype, content string
		var createdAt time.Time

		if err := rows.Scan(&seq, &msgUUID, &parentUUID, &logicalParentUUID,
			&isSidechain, &msgType, &subtype, &content, &createdAt); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}

		// Skip progress messages in fork
		if msgType == "progress" {
			continue
		}

		// Generate new UUID for the forked message
		newUUID := uuid.New().String()
		uuidMap[msgUUID] = newUUID

		// Rebuild parent chain
		var newParentUUID string
		if lastChainUUID == "" {
			// First message in fork — empty parent (chain root)
			newParentUUID = ""
		} else {
			newParentUUID = lastChainUUID
		}

		// Insert into child session
		_, err := tx.Exec(`
			INSERT INTO messages (session_id, uuid, parent_uuid, logical_parent_uuid,
			                     is_sidechain, type, subtype, content, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, childSessionID, newUUID, newParentUUID, logicalParentUUID,
			isSidechain, msgType, subtype, content, createdAt)
		if err != nil {
			return fmt.Errorf("insert forked message: %w", err)
		}

		lastChainUUID = newUUID
	}


	return tx.Commit()
}

// GetForkChildren returns all child sessions forked from a parent session.
func (s *Store) GetForkChildren(parentSessionID string) ([]*Session, error) {

	query := `
		SELECT session_id, project_dir, model, title,
		       parent_session_id, fork_point_seq, agent_type, mode, settings,
		       created_at, updated_at
		FROM sessions
		WHERE parent_session_id = ?
		ORDER BY created_at ASC
	`

	rows, err := s.db.Query(query, parentSessionID)
	if err != nil {
		return nil, fmt.Errorf("query fork children: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var children []*Session
	for rows.Next() {
		var sess Session
		var settingsJSON string
		if err := rows.Scan(
			&sess.SessionID, &sess.ProjectDir, &sess.Model, &sess.Title,
			&sess.ParentSessionID, &sess.ForkPointSeq, &sess.AgentType, &sess.Mode, &settingsJSON,
			&sess.CreatedAt, &sess.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		children = append(children, &sess)
	}

	return children, nil
}

// MergeForkBack merges a forked session's new messages back into the parent.
// Only messages created after the fork point in the child are copied.
// TS: sessionStorage.ts:2970-3020 (mergeForkBack)
func (s *Store) MergeForkBack(childSessionID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Load child session to get parent and fork point
	child, err := s.getSessionLocked(childSessionID)
	if err != nil {
		return fmt.Errorf("load child session: %w", err)
	}
	if child == nil {
		return fmt.Errorf("child session %q not found", childSessionID)
	}
	if child.ParentSessionID == "" {
		return fmt.Errorf("session %q is not a fork", childSessionID)
	}

	// Get last seq in parent for appending
	var lastParentSeq int64
	err = tx.QueryRow(
		"SELECT COALESCE(MAX(seq), 0) FROM messages WHERE session_id = ?",
		child.ParentSessionID,
	).Scan(&lastParentSeq)
	if err != nil {
		return fmt.Errorf("get parent last seq: %w", err)
	}

	// Get parent's last chain UUID for linking
	var lastChainUUID string
	err = tx.QueryRow(`
		SELECT uuid FROM messages
		WHERE session_id = ? AND type != 'progress'
		ORDER BY seq DESC LIMIT 1
	`, child.ParentSessionID).Scan(&lastChainUUID)
	if err != nil {
		lastChainUUID = ""
	}

	// Count inherited messages (copied from parent during fork, excluding progress)
	// so we can skip them and only merge new child messages.
	var inheritedCount int
	_ = tx.QueryRow(`
		SELECT COUNT(*) FROM messages
		WHERE session_id = ? AND seq >= ? AND type != 'progress'
	`, child.ParentSessionID, child.ForkPointSeq).Scan(&inheritedCount)

	// Copy only NEW child messages (skip the inherited ones via OFFSET)
	rows, _ := tx.Query(`
		SELECT uuid, type, subtype, content, created_at, is_sidechain
		FROM messages
		WHERE session_id = ? AND is_sidechain = 0
		ORDER BY seq ASC
		LIMIT -1 OFFSET ?
	`, childSessionID, inheritedCount)
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var msgUUID, msgType, subtype, content string
		var createdAt time.Time
		var isSidechain int
		_ = rows.Scan(&msgUUID, &msgType, &subtype, &content, &createdAt, &isSidechain)

		// Skip progress
		if msgType == "progress" {
			continue
		}

		newUUID := uuid.New().String()

		_, err := tx.Exec(`
			INSERT INTO messages (session_id, uuid, parent_uuid, logical_parent_uuid,
			                     is_sidechain, type, subtype, content, created_at)
			VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)
		`, child.ParentSessionID, newUUID, lastChainUUID,
			isSidechain, msgType, subtype, content, createdAt)
		if err != nil {
			return fmt.Errorf("insert merged message: %w", err)
		}

		lastChainUUID = newUUID
	}


	return tx.Commit()
}
