package short

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fact represents one structured fact row.
type Fact struct {
	ID        int64
	Content   string
	CreatedAt time.Time
}

// AddFact stores content if it is not already present. Returns the fact_id and
// whether a new row was inserted (false = duplicate). On duplicate the
// existing fact_id is still resolved and returned so callers (e.g. dream) can
// reference it.
func (s *Store) AddFact(content string) (factID int64, inserted bool, err error) {
	if strings.TrimSpace(content) == "" {
		return 0, false, errors.New("facts: empty content")
	}

	res, err := s.db.Exec(`INSERT OR IGNORE INTO facts(content) VALUES(?)`, content)
	if err != nil {
		return 0, false, fmt.Errorf("facts: insert: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("facts: rows affected: %w", err)
	}

	if rows == 0 {
		// Duplicate — resolve the existing id so callers can still operate on it.
		var id int64
		if err := s.db.QueryRow(`SELECT fact_id FROM facts WHERE content = ?`, content).Scan(&id); err != nil {
			return 0, false, fmt.Errorf("facts: lookup duplicate id: %w", err)
		}
		return id, false, nil
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("facts: last insert id: %w", err)
	}

	segmented := s.Segment(content)
	if _, err := s.db.Exec(`INSERT INTO facts_fts(segmented_content) VALUES(?)`, segmented); err != nil {
		return 0, false, fmt.Errorf("facts: insert fts: %w", err)
	}
	var ftsRowid int64
	if err := s.db.QueryRow(`SELECT last_insert_rowid()`).Scan(&ftsRowid); err != nil {
		return 0, false, fmt.Errorf("facts: read fts rowid: %w", err)
	}
	if _, err := s.db.Exec(`INSERT INTO facts_fts_map(fact_id, fts_rowid) VALUES(?, ?)`, id, ftsRowid); err != nil {
		return 0, false, fmt.Errorf("facts: insert fts map: %w", err)
	}
	return id, true, nil
}

// DeleteFact removes a fact and its FTS map entry. Deleting an unknown id is
// not an error. The contentless FTS row is orphaned (JOIN via the map table
// breaks, so it stops matching) — matches deleteFTS for messages.
func (s *Store) DeleteFact(factID int64) error {
	if _, err := s.db.Exec(`DELETE FROM facts_fts_map WHERE fact_id = ?`, factID); err != nil {
		return fmt.Errorf("facts: delete fts map: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM facts WHERE fact_id = ?`, factID); err != nil {
		return fmt.Errorf("facts: delete fact: %w", err)
	}
	return nil
}

// UpdateFact replaces an existing fact's content inside one transaction so a
// failure cannot leave orphaned rows. Deleting an unknown id is not an error
// (it degrades to a plain insert). If the new content already exists as a
// different fact, returns that existing id with inserted=false.
func (s *Store) UpdateFact(factID int64, content string) (newID int64, inserted bool, err error) {
	if strings.TrimSpace(content) == "" {
		return 0, false, errors.New("facts: empty content")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, fmt.Errorf("facts: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	if _, err := tx.Exec(`DELETE FROM facts_fts_map WHERE fact_id = ?`, factID); err != nil {
		return 0, false, fmt.Errorf("facts: delete fts map: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM facts WHERE fact_id = ?`, factID); err != nil {
		return 0, false, fmt.Errorf("facts: delete fact: %w", err)
	}

	res, err := tx.Exec(`INSERT OR IGNORE INTO facts(content) VALUES(?)`, content)
	if err != nil {
		return 0, false, fmt.Errorf("facts: insert: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("facts: rows affected: %w", err)
	}

	if rows == 0 {
		var id int64
		if err := tx.QueryRow(`SELECT fact_id FROM facts WHERE content = ?`, content).Scan(&id); err != nil {
			return 0, false, fmt.Errorf("facts: lookup duplicate id: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, false, fmt.Errorf("facts: commit: %w", err)
		}
		return id, false, nil
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("facts: last insert id: %w", err)
	}

	segmented := s.Segment(content)
	if _, err := tx.Exec(`INSERT INTO facts_fts(segmented_content) VALUES(?)`, segmented); err != nil {
		return 0, false, fmt.Errorf("facts: insert fts: %w", err)
	}
	var ftsRowid int64
	if err := tx.QueryRow(`SELECT last_insert_rowid()`).Scan(&ftsRowid); err != nil {
		return 0, false, fmt.Errorf("facts: read fts rowid: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO facts_fts_map(fact_id, fts_rowid) VALUES(?, ?)`, id, ftsRowid); err != nil {
		return 0, false, fmt.Errorf("facts: insert fts map: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("facts: commit: %w", err)
	}
	return id, true, nil
}

// SearchFacts runs an FTS5 query against the facts index and returns matches
// ranked by relevance. The query is passed through verbatim so FTS5 operators
// (AND/OR/NOT, parentheses, prefix) are honored — recall relies on this.
// Malformed FTS5 syntax yields (nil, nil) rather than failing the whole call.
func (s *Store) SearchFacts(query string, limit int) ([]Fact, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := s.db.Query(`
		SELECT f.fact_id, f.content, f.created_at
		FROM facts_fts ft
		JOIN facts_fts_map fm ON ft.rowid = fm.fts_rowid
		JOIN facts f ON fm.fact_id = f.fact_id
		WHERE ft.segmented_content MATCH ?
		ORDER BY ft.rank
		LIMIT ?`, query, limit)
	if err != nil {
		if isMalformedFTSError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("facts: search: %w", err)
	}
	defer rows.Close()

	var facts []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.Content, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("facts: scan: %w", err)
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		if isMalformedFTSError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("facts: rows err: %w", err)
	}
	return facts, nil
}
