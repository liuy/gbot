package short

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CreateSession creates a new session with a generated UUID.
// TS aligned: bootstrap/state.ts getInitialState() + regenerateSessionId()
func (s *Store) CreateSession(projectDir, model string) (*Session, error) {

	sessionID := uuid.New().String()
	now := time.Now()

	settingsJSON := "{}"

	query := `
		INSERT INTO sessions (
			session_id, project_dir, model, title,
			parent_session_id, fork_point_seq, agent_type, mode, settings,
			created_at, updated_at
		) VALUES (?, ?, ?, '', '', 0, '', '', ?, ?, ?)
	`
	_, err := s.db.Exec(query, sessionID, projectDir, model, settingsJSON, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	return &Session{
		SessionID:  sessionID,
		ProjectDir: projectDir,
		Model:      model,
		Title:      "",
		CreatedAt:  now,
		UpdatedAt:  now,
		Settings:   map[string]string{},
	}, nil
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(sessionID string) (*Session, error) {

	var ses Session
	var settingsJSON string
	var parentSessionID, agentType, mode sql.NullString
	var forkPointSeq sql.NullInt64

	query := `
		SELECT session_id, project_dir, model, title,
		       parent_session_id, fork_point_seq, agent_type, mode, settings,
		       created_at, updated_at
		FROM sessions WHERE session_id = ?
	`
	err := s.db.QueryRow(query, sessionID).Scan(
		&ses.SessionID, &ses.ProjectDir, &ses.Model, &ses.Title,
		&parentSessionID, &forkPointSeq, &agentType, &mode, &settingsJSON,
		&ses.CreatedAt, &ses.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}

	if parentSessionID.Valid {
		ses.ParentSessionID = parentSessionID.String
	}
	if forkPointSeq.Valid {
		ses.ForkPointSeq = int(forkPointSeq.Int64)
	}
	if agentType.Valid {
		ses.AgentType = agentType.String
	}
	if mode.Valid {
		ses.Mode = mode.String
	}
	if settingsJSON != "" {
		if err := json.Unmarshal([]byte(settingsJSON), &ses.Settings); err != nil {
			return nil, fmt.Errorf("unmarshal settings: %w", err)
		}
	} else {
		ses.Settings = map[string]string{}
	}

	return &ses, nil
}

// ListSessions returns sessions for a project directory, sorted by updated_at DESC.
// TS aligned: fetchLogs() → getSessionFilesLite() → enrichLogs()
func (s *Store) ListSessions(projectDir string, limit int) ([]*Session, error) {

	query := `
		SELECT session_id, project_dir, model, title,
		       parent_session_id, fork_point_seq, agent_type, mode, settings,
		       created_at, updated_at
		FROM sessions
		WHERE project_dir = ?
		ORDER BY updated_at DESC
	`

	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = s.db.Query(query+" LIMIT ?", projectDir, limit)
	} else {
		rows, err = s.db.Query(query, projectDir)
	}
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		var ses Session
		var settingsJSON string
		var parentSessionID, agentType, mode sql.NullString
		var forkPointSeq sql.NullInt64

		err := rows.Scan(
			&ses.SessionID, &ses.ProjectDir, &ses.Model, &ses.Title,
			&parentSessionID, &forkPointSeq, &agentType, &mode, &settingsJSON,
			&ses.CreatedAt, &ses.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}

		if parentSessionID.Valid {
			ses.ParentSessionID = parentSessionID.String
		}
		if forkPointSeq.Valid {
			ses.ForkPointSeq = int(forkPointSeq.Int64)
		}
		if agentType.Valid {
			ses.AgentType = agentType.String
		}
		if mode.Valid {
			ses.Mode = mode.String
		}
		if settingsJSON != "" {
			if err := json.Unmarshal([]byte(settingsJSON), &ses.Settings); err != nil {
				return nil, fmt.Errorf("unmarshal settings: %w", err)
			}
		} else {
			ses.Settings = map[string]string{}
		}

		sessions = append(sessions, &ses)
	}


	return sessions, nil
}

// UpdateSessionTitle updates the title of a session.
// TS aligned: saveCustomTitle() — first user prompt auto-extracted
func (s *Store) UpdateSessionTitle(sessionID, title string) error {

	query := `UPDATE sessions SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE session_id = ?`
	result, err := s.db.Exec(query, title, sessionID)
	if err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return nil
}

// UpdateSessionTimestamp updates the updated_at timestamp to now.
func (s *Store) UpdateSessionTimestamp(sessionID string) error {

	query := `UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE session_id = ?`
	result, err := s.db.Exec(query, sessionID)
	if err != nil {
		return fmt.Errorf("update timestamp: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return nil
}

// DeleteSession deletes a session and all its messages (cascade).
func (s *Store) DeleteSession(sessionID string) error {

	query := `DELETE FROM sessions WHERE session_id = ?`
	result, err := s.db.Exec(query, sessionID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return nil
}

// ExtractFirstPrompt extracts the first meaningful user prompt from message content JSON.
// TS aligned: extractFirstPromptFromHead() (sessionStoragePortable.ts:135-201)
//
// The contentJSON parameter should be the full message JSON, e.g.:
//   {"type":"user","message":{"content":[...]}}
//
// Skips tool_result, isMeta, isCompactSummary, slash commands.
// Truncates to 200 chars. Bash input gets "!" prefix.
func ExtractFirstPrompt(contentJSON string) string {
	// Parse the full message JSON
	var msg map[string]any
	if err := json.Unmarshal([]byte(contentJSON), &msg); err != nil {
		return ""
	}

	// Extract message.content
	msgObj, ok := msg["message"].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := msgObj["content"]
	if !ok {
		return ""
	}

	// Extract text blocks from content
	texts := extractTextBlocks(content)

	// Patterns to skip
	var commandFallback string
	skipPattern := regexp.MustCompile(`^(?:\s*<[a-z][\w-]*[\s>]|\[Request interrupted by user[\]]*\])`)
	commandNameRe := regexp.MustCompile(`<command-name>(.*?)</command-name>`)
	bashInputRe := regexp.MustCompile(`<bash-input>([\s\S]*?)</bash-input>`)

	for _, raw := range texts {
		result := strings.ReplaceAll(raw, "\n", " ")
		result = strings.TrimSpace(result)
		if result == "" {
			continue
		}

		// Check for slash command but remember as fallback
		if cmdMatch := commandNameRe.FindStringSubmatch(result); cmdMatch != nil {
			if commandFallback == "" {
				commandFallback = cmdMatch[1]
			}
			continue
		}

		// Format bash input with "!" prefix
		if bashMatch := bashInputRe.FindStringSubmatch(result); bashMatch != nil {
			return "! " + strings.TrimSpace(bashMatch[1])
		}

		// Skip auto-generated patterns
		if skipPattern.MatchString(result) {
			continue
		}

		// Truncate to 200 chars
		if len(result) > 200 {
			result = strings.TrimSpace(result[:200]) + "…"
		}
		return result
	}

	if commandFallback != "" {
		return commandFallback
	}
	return ""
}

// extractTextBlocks extracts text strings from parsed message content.
// Content can be a string (legacy) or array of ContentBlock.
func extractTextBlocks(content any) []string {
	var texts []string

	switch v := content.(type) {
	case string:
		texts = append(texts, v)
	case []any:
		for _, item := range v {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			if blockType != "text" {
				continue
			}
			text, _ := block["text"].(string)
			if text != "" {
				texts = append(texts, text)
			}
		}
	}

	return texts
}

// getSession loads a session without acquiring the lock (caller must hold lock).
func (s *Store) getSession(sessionID string) (*Session, error) {
	var ses Session
	var settingsJSON string
	var parentSessionID, agentType, mode sql.NullString
	var forkPointSeq sql.NullInt64

	query := `
		SELECT session_id, project_dir, model, title,
		       parent_session_id, fork_point_seq, agent_type, mode, settings,
		       created_at, updated_at
		FROM sessions WHERE session_id = ?
	`
	err := s.db.QueryRow(query, sessionID).Scan(
		&ses.SessionID, &ses.ProjectDir, &ses.Model, &ses.Title,
		&parentSessionID, &forkPointSeq, &agentType, &mode, &settingsJSON,
		&ses.CreatedAt, &ses.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query session: %w", err)
	}

	if parentSessionID.Valid {
		ses.ParentSessionID = parentSessionID.String
	}
	if forkPointSeq.Valid {
		ses.ForkPointSeq = int(forkPointSeq.Int64)
	}
	if agentType.Valid {
		ses.AgentType = agentType.String
	}
	if mode.Valid {
		ses.Mode = mode.String
	}
	if settingsJSON != "" {
		if err := json.Unmarshal([]byte(settingsJSON), &ses.Settings); err != nil {
		slog.Warn("session: failed to parse settings", "error", err)
	}
	}
	if ses.Settings == nil {
		ses.Settings = map[string]string{}
	}

	return &ses, nil
}

// insertSession inserts a session without acquiring the lock (caller must hold lock).
func (s *Store) insertSession(sess *Session) error {
	settingsJSON, _ := json.Marshal(sess.Settings)

	query := `
		INSERT INTO sessions (
			session_id, project_dir, model, title,
			parent_session_id, fork_point_seq, agent_type, mode, settings,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.Exec(query,
		sess.SessionID, sess.ProjectDir, sess.Model, sess.Title,
		sess.ParentSessionID, sess.ForkPointSeq, sess.AgentType, sess.Mode, string(settingsJSON),
		sess.CreatedAt, sess.UpdatedAt,
	)
	return err
}
