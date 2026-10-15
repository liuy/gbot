package short

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// store.go:27 — the database directory is created with mode 0755.
func TestNewStore_CreatedDirMode(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "sub1", "sub2")
	dbPath := filepath.Join(dbDir, "test.db")

	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("stat db dir: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("db dir perm = %v, want -rwxr-xr-x", info.Mode().Perm())
	}
}

// store.go:28 — MkdirAll failure is reported with its cause.
func TestNewStore_MkdirErrorWrapsCause(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	store, err := NewStore(filepath.Join(blocker, "sub", "test.db"))
	wantErrWrappingCause(t, err, "create db directory: mkdir "+blocker+": not a directory")
	if store != nil {
		t.Errorf("store = %+v, want nil on mkdir error", store)
	}
}

// store.go:39 — a file SQLite rejects fails in initSchema, wrapped once per layer.
func TestNewStore_InitSchemaErrorWrapsCause(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "garbage.db")
	if err := writeGarbageFile(dbPath); err != nil {
		t.Fatalf("writeGarbageFile: %v", err)
	}

	store, err := NewStore(dbPath)
	wantErrWrappingCause(t, err, "init schema: init schema: file is not a database (26)")
	if store != nil {
		t.Errorf("store = %+v, want nil on schema error", store)
	}
}

// store.go:217 — initSchema's own wrap keeps the SQLite cause reachable.
func TestInitSchema_WrapsSchemaConflict(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conflict.db")
	seed, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	// sessions without project_dir: the CREATE TABLE is IF NOT EXISTS, so the
	// index build is what collides.
	if _, err := seed.Exec(`CREATE TABLE sessions (session_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	store := &Store{db: db}
	err = store.initSchema()
	wantErrWrappingCause(t, err, "init schema: SQL logic error: no such column: project_dir (1)")
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close db: %v", cerr)
	}
}

// store.go:120 — the write pool keeps one idle connection alive between
// queries; SetMaxIdleConns(0) would close and reopen it on every use.
func TestOpenSQLite_KeepsOneIdleConn(t *testing.T) {
	db, err := openSQLite(filepath.Join(t.TempDir(), "pool.db"))
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow("SELECT 1").Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("query result = %d, want 1", n)
	}
	stats := db.Stats()
	if stats.OpenConnections != 1 {
		t.Errorf("OpenConnections = %d, want 1 (the pinned conn stays idle, not closed)", stats.OpenConnections)
	}
	if stats.Idle != 1 {
		t.Errorf("Idle = %d, want 1", stats.Idle)
	}
}

// errReadPoolCloseFailed stands in for a read pool whose Close fails.
var errReadPoolCloseFailed = errors.New("read pool close failed")

// failingCloseConnector is a read-pool stand-in that implements io.Closer so
// (*sql.DB).Close returns an error, which is what Store.Close must handle.
type failingCloseConnector struct{}

func (failingCloseConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("failingCloseConnector: Connect must not be reached")
}
func (failingCloseConnector) Driver() driver.Driver { return failingCloseDriver{} }
func (failingCloseConnector) Close() error          { return errReadPoolCloseFailed }

type failingCloseDriver struct{}

func (failingCloseDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("failingCloseDriver: Open must not be reached")
}

// store.go:61-63 — when the read pool fails to close, Store.Close reports that
// error and still closes the write pool.
func TestClose_ReadPoolCloseError(t *testing.T) {
	store := openTestStore(t)

	if err := store.readDB.Close(); err != nil {
		t.Fatalf("close real read pool: %v", err)
	}
	store.readDB = sql.OpenDB(failingCloseConnector{})

	err := store.Close()
	if err == nil {
		t.Fatal("Close returned nil, want the read pool close error")
	}
	if !errors.Is(err, errReadPoolCloseFailed) {
		t.Errorf("Close error = %v, want %v", err, errReadPoolCloseFailed)
	}
	if _, err := store.DB().Exec("SELECT 1"); err == nil {
		t.Error("write pool still open: Close must close it even when the read pool close fails")
	}
}

// store.go:61 — on the normal path both pools end up closed.
func TestClose_ClosesBothPools(t *testing.T) {
	store := openTestStore(t)

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.DB().Exec("SELECT 1"); err == nil {
		t.Error("write pool still open after Close")
	}
	if err := store.readDB.QueryRow("SELECT 1").Scan(new(int)); err == nil {
		t.Error("read pool still open after Close")
	}
}
