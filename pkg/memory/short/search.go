package short

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ftsSpecialRe matches FTS5 query operators that could be exploited.
var ftsSpecialRe = regexp.MustCompile(`[*()"[\]{}^~]`)

// validMessageTypes is the allowlist for message type filtering.
var validMessageTypes = map[string]bool{
	"user": true, "assistant": true, "system": true,
	"progress": true, "attachment": true, "result": true,
}

// sanitizeFTSQuery strips FTS5 special operators from user input.
func sanitizeFTSQuery(query string) string {
	sanitized := ftsSpecialRe.ReplaceAllString(query, " ")
	// Remove FTS5 keywords that could appear as standalone tokens
	for _, kw := range []string{"AND", "OR", "NOT", "NEAR"} {
		sanitized = strings.ReplaceAll(sanitized, " "+kw+" ", " ")
		if after, ok := strings.CutPrefix(sanitized, kw+" "); ok {
			sanitized = after
		}
		if before, ok := strings.CutSuffix(sanitized, " "+kw); ok {
			sanitized = before
		}
	}
	if len(sanitized) > 500 {
		sanitized = sanitized[:500]
	}
	return strings.TrimSpace(sanitized)
}

// SearchMessages performs FTS5 full-text search on messages.
// Results are ranked by relevance (bm25) via fts5.rank.
func (s *Store) SearchMessages(query string, opts *SearchOptions) ([]*SearchResult, error) {
	if opts == nil {
		opts = &SearchOptions{}
	}
	if opts.Limit <= 0 {
		opts.Limit = 100
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	// Segment query for Chinese + English mixed search
	segmentedQuery := s.Segment(sanitizeFTSQuery(query))

	// Build WHERE clause
	var whereClauses []string
	var args []any

	// FTS5 MATCH must come first
	whereClauses = append(whereClauses, "f.segmented_content MATCH ?")
	args = append(args, segmentedQuery)

	// Optional filters
	if opts.SessionID != "" {
		whereClauses = append(whereClauses, "m.session_id = ?")
		args = append(args, opts.SessionID)
	}
	if opts.ProjectDir != "" {
		whereClauses = append(whereClauses, "s.project_dir = ?")
		args = append(args, opts.ProjectDir)
	}
	if len(opts.Types) > 0 {
		for _, t := range opts.Types {
			if !validMessageTypes[t] {
				return nil, fmt.Errorf("invalid message type: %q", t)
			}
		}
		placeholders := make([]string, len(opts.Types))
		for i, t := range opts.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("m.type IN (%s)", strings.Join(placeholders, ",")))
	}

	// Join with sessions table for project_dir filter
	joinClause := ""
	if opts.ProjectDir != "" {
		joinClause = "JOIN sessions s ON m.session_id = s.session_id"
	}

	querySQL := fmt.Sprintf(`
		SELECT m.*, f.rank as score
		FROM messages_fts f
		JOIN messages_fts_map fm ON f.rowid = fm.fts_rowid
		JOIN messages m ON fm.seq = m.seq
		%s
		WHERE %s
		ORDER BY f.rank
		LIMIT ? OFFSET ?`,
		joinClause,
		strings.Join(whereClauses, " AND "))

	args = append(args, opts.Limit, opts.Offset)

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*SearchResult
	for rows.Next() {
		var msg TranscriptMessage
		var score float64
		var subtype sql.NullString
		var parentUUID, logicalParentUUID sql.NullString

		if err := rows.Scan(
			&msg.Seq,
			&msg.SessionID,
			&msg.UUID,
			&parentUUID,
			&logicalParentUUID,
			&msg.IsSidechain,
			&msg.Type,
			&subtype,
			&msg.Content,
			&msg.CreatedAt,
			&score,
		); err != nil {
			slog.Warn("search: scan message row", "error", err)
			continue
		}
		if parentUUID.Valid {
			msg.ParentUUID = parentUUID.String
		}
		if logicalParentUUID.Valid {
			msg.LogicalParentUUID = logicalParentUUID.String
		}
		if subtype.Valid {
			msg.Subtype = subtype.String
		}

		results = append(results, &SearchResult{
			TranscriptMessage: &msg,
			Score:   score,
		})
	}

	return results, nil
}

// SearchSessions searches sessions by message content.
// Returns sessions whose messages match the query, ranked by best match score.
// Note: best_score is used for ordering but not included in returned Session structs.
func (s *Store) SearchSessions(query string, projectDir string, limit int) ([]*Session, error) {
	if limit <= 0 {
		limit = 50
	}

	segmentedQuery := s.Segment(sanitizeFTSQuery(query))

	var whereClause string
	var args []any

	// projectDir placeholder comes first in WHERE clause
	if projectDir != "" {
		whereClause = "s.project_dir = ? AND "
		args = append(args, projectDir)
	}
	// MATCH placeholder comes last
	args = append(args, segmentedQuery)

	querySQL := `
		SELECT DISTINCT s.session_id, s.project_dir, s.model, s.title,
		               s.parent_session_id, s.fork_point_seq, s.agent_type, s.mode,
		               s.settings, s.created_at, s.updated_at
		FROM sessions s
		JOIN messages m ON m.session_id = s.session_id
		JOIN messages_fts_map fm ON fm.seq = m.seq
		JOIN messages_fts f ON f.rowid = fm.fts_rowid
		WHERE ` + whereClause + `f.segmented_content MATCH ?
		GROUP BY s.session_id
		ORDER BY MAX(f.rank) DESC
		LIMIT ?`

	args = append(args, limit)

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("search sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []*Session
	for rows.Next() {
		var sess Session
		var settings sql.NullString
		var parentSessionID, agentType, mode, title, model sql.NullString
		var forkPointSeq sql.NullInt64

		err := rows.Scan(
			&sess.SessionID,
			&sess.ProjectDir,
			&model,
			&title,
			&parentSessionID,
			&forkPointSeq,
			&agentType,
			&mode,
			&settings,
			&sess.CreatedAt,
			&sess.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if model.Valid {
			sess.Model = model.String
		}
		if title.Valid {
			sess.Title = title.String
		}
		if parentSessionID.Valid {
			sess.ParentSessionID = parentSessionID.String
		}
		if forkPointSeq.Valid {
			sess.ForkPointSeq = int(forkPointSeq.Int64)
		}
		if agentType.Valid {
			sess.AgentType = agentType.String
		}
		if mode.Valid {
			sess.Mode = mode.String
		}
		if settings.Valid {
			if err := json.Unmarshal([]byte(settings.String), &sess.Settings); err != nil {
				sess.Settings = make(map[string]string)
			}
		}

		sessions = append(sessions, &sess)
	}

	return sessions, nil
}

// dbExec is the common interface between *sql.DB and *sql.Tx.
type dbExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertFTS adds a message to the FTS5 index.
// db should be the same *sql.Tx or *sql.DB used for the preceding INSERT
// to avoid SQLITE_BUSY from a second connection.
func (s *Store) insertFTS(db dbExec, seq int64, content string) error {
	// Extract searchable text from JSON content
	text := extractTextFromJSON(content)
	segmented := s.Segment(text)

	// Insert into FTS table
	result, err := db.Exec(`
		INSERT INTO messages_fts(segmented_content)
		VALUES(?)`,
		segmented)
	if err != nil {
		return fmt.Errorf("insert fts: %w", err)
	}

	ftsRowid, _ := result.LastInsertId()

	// Map seq to fts_rowid
	_, err = db.Exec(`
		INSERT INTO messages_fts_map(seq, fts_rowid, segmented_content)
		VALUES(?, ?, ?)`,
		seq, ftsRowid, segmented)
	if err != nil {
		return fmt.Errorf("insert fts map: %w", err)
	}

	return nil
}

// deleteFTS removes a message from the FTS5 index.
// Called from message delete when a message is removed.
// Note: For contentless FTS5 tables (content=”), we only delete from the map table.
// The FTS table entry is implicitly orphaned but consumes minimal space.
func (s *Store) deleteFTS(seq int64) error {
	// Delete from map table
	_, err := s.db.Exec(`
		DELETE FROM messages_fts_map WHERE seq = ?`,
		seq)
	if err != nil {
		return fmt.Errorf("delete fts map: %w", err)
	}

	return nil
}

// extractTextFromJSON extracts plain searchable text from message content JSON.
// Mirrors TS transcriptSearch.ts renderableSearchText() logic:
// - text blocks: include text
// - tool_use blocks: include input fields (command, pattern, file_path, etc.)
// - tool_result blocks: include output content (stdout, content, etc.)
// - thinking blocks: skip (hidden in UI)
// - system-reminder tags: strip (Claude context, not user-visible)
func extractTextFromJSON(contentJSON string) string {
	// Try to parse as JSON array of content blocks
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		// Not an array, try as single block or plain text
		var singleBlock map[string]any
		if err2 := json.Unmarshal([]byte(contentJSON), &singleBlock); err2 == nil {
			blocks = []json.RawMessage{[]byte(contentJSON)}
		} else {
			// Plain text string
			return stripSystemReminders(contentJSON)
		}
	}

	var parts []string
	for _, block := range blocks {
		var blockMap map[string]any
		if err := json.Unmarshal(block, &blockMap); err != nil {
			continue
		}

		blockType, _ := blockMap["type"].(string)

		switch blockType {
		case "text":
			if text, ok := blockMap["text"].(string); ok {
				parts = append(parts, text)
			}
		case "tool_use":
			// Extract input fields for searchability
			if input, ok := blockMap["input"].(map[string]any); ok {
				parts = append(parts, extractToolUseInput(input)...)
			}
		case "tool_result":
			// Skip "content" field (metadata placeholder), extract actual output
			// Duck-type common result shapes
			for _, key := range []string{"stdout", "stderr", "output", "result", "text"} {
				if val, ok := blockMap[key].(string); ok {
					parts = append(parts, val)
				}
			}
			// Nested file content (Read tool)
			if file, ok := blockMap["file"].(map[string]any); ok {
				if content, ok := file["content"].(string); ok {
					parts = append(parts, content)
				}
			}
		case "thinking":
			// Skip - hidden in UI
		default:
			// For unknown block types, try to extract any text field
			if text, ok := blockMap["text"].(string); ok {
				parts = append(parts, text)
			}
		}
	}

	result := strings.Join(parts, "\n")
	return stripSystemReminders(result)
}

// extractToolUseInput extracts searchable fields from tool_use input.
// Mirrors TS transcriptSearch.ts toolUseSearchText().
func extractToolUseInput(input map[string]any) []string {
	var parts []string

	// Primary argument fields shown in UI
	for _, key := range []string{
		"command", "pattern", "file_path", "path", "prompt",
		"description", "query", "url", "skill",
	} {
		if val, ok := input[key].(string); ok {
			parts = append(parts, val)
		}
	}

	// Array fields like args[], files[]
	for _, key := range []string{"args", "files"} {
		if val, ok := input[key].([]any); ok {
			var strs []string
			for _, v := range val {
				if s, ok := v.(string); ok {
					strs = append(strs, s)
				}
			}
			if len(strs) > 0 {
				parts = append(parts, strings.Join(strs, " "))
			}
		}
	}

	return parts
}

// stripSystemReminders removes <system-reminder> tags and their content.
// Mirrors TS transcriptSearch.ts system reminder stripping logic.
func stripSystemReminders(text string) string {
	const openTag = "<system-reminder>"
	const closeTag = "</system-reminder>"

	result := text
	for {
		open := strings.Index(result, openTag)
		if open < 0 {
			break
		}
		close := strings.Index(result[open:], closeTag)
		if close < 0 {
			break
		}
		close += open + len(closeTag)
		result = result[:open] + result[close:]
	}
	return result
}

// updateSessionFTS updates the updated_at timestamp for search result freshness.
// Called when messages are added to a session to keep search results ordered by recency.
func (s *Store) updateSessionFTS(db dbExec, sessionID string) error {
	now := time.Now().UTC()
	_, err := db.Exec(`
		UPDATE sessions SET updated_at = ? WHERE session_id = ?`,
		now, sessionID)
	return err
}
