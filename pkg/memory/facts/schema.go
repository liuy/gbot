package facts

// initSchema creates the facts tables if they don't already exist. Idempotent.
// The layout mirrors messages_fts in pkg/memory/short:
//   - facts: the source of truth (content, timestamps)
//   - facts_fts: contentless FTS5 index (content=”) so deletes don't need the
//     'delete' command with old values
//   - facts_fts_map: bridges fact_id <-> fts_rowid; deleting a row here
//     orphans the FTS entry and the JOIN stops returning it.
func (s *Store) initSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS facts (
    fact_id    INTEGER PRIMARY KEY AUTOINCREMENT,
    content    TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE VIRTUAL TABLE IF NOT EXISTS facts_fts USING fts5(
    segmented_content, content='', tokenize='unicode61'
);
CREATE TABLE IF NOT EXISTS facts_fts_map (
    fact_id   INTEGER NOT NULL,
    fts_rowid INTEGER NOT NULL,
    PRIMARY KEY (fact_id, fts_rowid)
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	return nil
}
