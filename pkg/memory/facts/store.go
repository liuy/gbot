// Package facts implements structured fact storage on top of the same SQLite
// database that backs pkg/memory/short. The facts table holds long-lived,
// user-centric facts extracted by the dream agent; recall/remember/forget are
// the tools the LLM uses to query and mutate it.
//
// Architecture mirrors messages_fts: a contentless FTS5 virtual table plus a
// map table. gse pre-segmentation is injected via Segmenter so the package has
// no dependency on pkg/memory/short.
package facts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Segmenter tokenizes text for FTS5 indexing (gse pre-segmentation). Injected
// from the caller (short.Store.Segment) so facts has no dependency on the
// short package or gse directly.
type Segmenter func(string) string

// Store owns the facts schema and queries. It shares a *sql.DB with the short
// store (same memory.db file); it never closes the handle — the caller owns it.
type Store struct {
	db      *sql.DB
	segment Segmenter
}

// Fact is one row of the facts table.
type Fact struct {
	ID        int64
	Content   string
	CreatedAt time.Time
}

// NewStore attaches a facts schema to db and runs initSchema. db is owned by
// the caller (typically short.Store.DB()). segment is invoked before every FTS
// write to pre-tokenize content.
func NewStore(db *sql.DB, segment Segmenter) (*Store, error) {
	if db == nil {
		return nil, errors.New("facts: nil db")
	}
	if segment == nil {
		return nil, errors.New("facts: nil segmenter")
	}
	s := &Store{db: db, segment: segment}
	if err := s.initSchema(); err != nil {
		return nil, fmt.Errorf("facts: init schema: %w", err)
	}
	return s, nil
}

// Close is a no-op: db is shared with short.Store, which owns the lifecycle.
func (s *Store) Close() error { return nil }

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

	segmented := s.segment(content)
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
// breaks, so it stops matching) — matches deleteFTS in pkg/memory/short.
func (s *Store) DeleteFact(factID int64) error {
	if _, err := s.db.Exec(`DELETE FROM facts_fts_map WHERE fact_id = ?`, factID); err != nil {
		return fmt.Errorf("facts: delete fts map: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM facts WHERE fact_id = ?`, factID); err != nil {
		return fmt.Errorf("facts: delete fact: %w", err)
	}
	return nil
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
