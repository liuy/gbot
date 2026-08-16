package short

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the underlying sql.DB so callers don't need to import database/sql.
func (s *Store) DB() *sql.DB { return s.db }

// Store manages short-term memory persistence via SQLite.
type Store struct {
	db     *sql.DB
	dbPath string
}

// NewStore opens or creates a SQLite database at dbPath and initializes the schema.
func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := openSQLite(dbPath)
	if err != nil {
		return nil, err
	}

	s := &Store{db: db, dbPath: dbPath}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return s, nil
}

// Close shuts down the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DBPath returns the database file path.
func (s *Store) DBPath() string {
	return s.dbPath
}

// openSQLite opens a SQLite database at path with pragmas applied via the DSN
// query string. This is critical: db.Exec("PRAGMA ...") only sets the pragma
// on whichever connection the pool happens to use for that call — other
// connections in the pool won't inherit it. Using _pragma=N in the DSN ensures
// every connection the pool creates runs the pragma on open.
//
// _txlock=immediate makes every tx.Begin() use BEGIN IMMEDIATE instead of
// BEGIN DEFERRED: the transaction acquires the reserved (write) lock up front
// rather than lazily upgrading from a read lock on first write. This is
// mandatory for multi-connection correctness — without it, two connections
// can each hold a shared (read) lock and then deadlock trying to upgrade to a
// write lock, and busy_timeout does NOT cover that deadlock.
//
// _time_format=sqlite&_timezone=UTC makes the driver serialize every bound
// time.Time as a canonical UTC string (YYYY-MM-DD HH:MM:SS[.fff]+00:00) and
// scan TIMESTAMP columns back as UTC, so SQL string comparison (ORDER BY,
// >, <) equals chronological comparison. Mixing local-zone strings or UTC
// naive strings into the same columns breaks that ordering. The DDL's
// DEFAULT CURRENT_TIMESTAMP stays as a forgotten-bind fallback; its naive
// UTC output compares correctly against canonical strings.
func openSQLite(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate&_time_format=sqlite&_timezone=UTC"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	return db, nil
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		session_id        TEXT PRIMARY KEY,
		project_dir       TEXT NOT NULL,
		model             TEXT DEFAULT '',
		title             TEXT DEFAULT '',
		parent_session_id TEXT DEFAULT '',
		fork_point_seq    INTEGER DEFAULT 0,
		agent_type        TEXT DEFAULT '',
		mode              TEXT DEFAULT '',
		settings          TEXT DEFAULT '{}',
		context_tokens    INTEGER DEFAULT 0,
		created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_dir);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_sessions_project_updated ON sessions(project_dir, updated_at);

	CREATE TABLE IF NOT EXISTS messages (
		seq               INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id        TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		uuid              TEXT NOT NULL,
		parent_uuid       TEXT DEFAULT '',
		logical_parent_uuid TEXT DEFAULT '',
		is_sidechain      INTEGER DEFAULT 0,
		type              TEXT NOT NULL,
		subtype           TEXT DEFAULT '',
		content           TEXT NOT NULL,
		metadata          TEXT DEFAULT NULL,
		created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_uuid ON messages(uuid);
	CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at);

	CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts
		USING fts5(
			segmented_content,
			content='',
			tokenize='unicode61'
		);

	CREATE TABLE IF NOT EXISTS messages_fts_map (
		seq               INTEGER PRIMARY KEY REFERENCES messages(seq),
		fts_rowid         INTEGER NOT NULL,
		segmented_content TEXT NOT NULL
	);

	-- File history snapshots stored separately from messages.
	-- TS align: file-history-snapshot is a separate entry type in JSONL.
	CREATE TABLE IF NOT EXISTS file_history_snapshots (
		session_id    TEXT NOT NULL PRIMARY KEY REFERENCES sessions(session_id) ON DELETE CASCADE,
		snapshot_data TEXT NOT NULL,
		created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	-- Content replacement records stored separately from messages.
	-- TS align: content-replacement is a separate entry type in JSONL.
	CREATE TABLE IF NOT EXISTS content_replacements (
		session_id    TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
		agent_id      TEXT DEFAULT '',
		replacements  TEXT NOT NULL,
		created_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	// Migrate: add metadata column if missing (pre-metadata databases).
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN metadata TEXT DEFAULT NULL`)

	// Migrate: add engine_id column to sessions (multi-engine support).
	// Existing rows backfill to 'main' via DEFAULT.
	_, _ = s.db.Exec(`ALTER TABLE sessions ADD COLUMN engine_id TEXT NOT NULL DEFAULT 'main'`)

	return nil
}
