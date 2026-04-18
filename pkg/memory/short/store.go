package short

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
	"github.com/go-ego/gse"
)

// DB wraps the underlying sql.DB so callers don't need to import database/sql.
func (s *Store) DB() *sql.DB { return s.db }

// Package-level singleton gse segmenter. gse.New() loads ~50MB of dictionary
// files (~2.75s). Sharing one instance across all Store objects avoids repeating
// this cost on every NewStore call. Cut() is safe for concurrent use.
var (
	globalGse     gse.Segmenter
	globalGseOnce sync.Once
	globalGseErr  error
)

func initGse() error {
	globalGseOnce.Do(func() {
		var seg gse.Segmenter
		globalGseErr = seg.LoadDict()
		if globalGseErr == nil {
			globalGse = seg
		}
	})
	return globalGseErr
}

// Store manages short-term memory persistence via SQLite.
// Concurrency is handled by SQLite WAL mode + transactions — no Go-level mutex needed.
type Store struct {
	db     *sql.DB
	gse    *gse.Segmenter
	dbPath string
}

// NewStore opens or creates a SQLite database at dbPath and initializes the schema.
func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, _ := sql.Open("sqlite", dbPath)

	// Performance pragmas
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}

	_ = initGse()

	s := &Store{db: db, gse: &globalGse, dbPath: dbPath}
	_ = s.initSchema()

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

// Segment tokenizes text using gse for FTS5 indexing.
// Returns space-separated tokens.
func (s *Store) Segment(text string) string {
	if s.gse == nil {
		return text
	}
	segments := s.gse.Cut(text, true)
	return strings.Join(segments, " ")
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
		created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_dir);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated ON sessions(updated_at DESC);

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
		created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_uuid ON messages(uuid);

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
	`
	_, err := s.db.Exec(schema)
	return err
}
