package short

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	_ "modernc.org/sqlite"
	"github.com/go-ego/gse"
)

// DB wraps the underlying sql.DB so callers don't need to import database/sql.
func (s *Store) DB() *sql.DB { return s.db }

// Package-level singleton gse segmenter. gse.New() loads ~50MB of dictionary
// files (~2.75s). Sharing one instance across all Store objects avoids repeating
// this cost on every NewStore call. Cut() is safe for concurrent use.
var (
	globalGse      gse.Segmenter
	globalGseOnce  sync.Once
	globalGseReady atomic.Bool
)

func initGse() {
	globalGseOnce.Do(func() {
		var seg gse.Segmenter
		if err := seg.LoadDictEmbed("zh"); err != nil {
			slog.Warn("gse: failed to load dictionary, FTS segmentation disabled", "error", err)
			return
		}
		globalGse = seg
		globalGseReady.Store(true)
	})
}

// Store manages short-term memory persistence via SQLite.
// Concurrency is handled by SQLite WAL mode + transactions — no Go-level mutex needed.
type Store struct {
	db     *sql.DB
	dbPath string
}

// NewStore opens or creates a SQLite database at dbPath and initializes the schema.
func NewStore(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

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

	// Load gse dictionary in background — it takes ~2-3s and is not
	// needed for session resume or message persistence. Segment() degrades
	// gracefully to raw text while the dictionary loads.
	go initGse()

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

// Segment tokenizes text using gse for FTS5 indexing.
// Returns space-separated tokens. Falls back to raw text if the
// dictionary hasn't finished loading yet.
func (s *Store) Segment(text string) string {
	if !globalGseReady.Load() {
		return text
	}
	segments := globalGse.Cut(text, true)
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
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	// Migrate: add metadata column if missing (pre-metadata databases).
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN metadata TEXT DEFAULT NULL`)

	return nil
}
