package fileread

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createDBWithClosedWriter builds a database via a separate writer process
// that is then SIGKILLed — the worst-case on-disk state a reader can face.
// In WAL mode the kill leaves -wal/-shm behind with committed frames, which
// is exactly the historical "disk I/O error (522)" trigger: a stale sidecar
// pair with no live writer.
func createDBWithClosedWriter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ready := filepath.Join(dir, "ready")

	// Helper subprocess: open the store's DSN, commit rows, signal ready,
	// block until killed. Killed (not closed) so no cleanup runs at all.
	cmd := exec.Command(os.Args[0], "-test.run=TestHelper_WriterProcess")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"WRITER_DB_PATH="+dbPath,
		"WRITER_READY_FILE="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second) // REAL-TIME: bounded wait for helper subprocess readiness
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) { // REAL-TIME: readiness poll against wall clock
			_ = cmd.Process.Kill()
			t.Fatal("helper never became ready")
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: poll interval for helper readiness file
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_, _ = cmd.Process.Wait()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file must exist after writer commit: %v", err)
	}
	return dbPath
}

// TestHelper_WriterProcess is the child side of createDBWithClosedWriter.
// Never runs as a real test: exits via the env guard unless spawned by parent.
func TestHelper_WriterProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	dbPath := os.Getenv("WRITER_DB_PATH")
	// Mirror pkg/memory/short's DSN pragmas; journal_mode matches store.go.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate")
	if err != nil {
		os.Exit(2)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		os.Exit(3)
	}
	for i := 1; i <= 3; i++ {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, 'row')`, i); err != nil {
			os.Exit(4)
		}
	}
	if f, err := os.Create(os.Getenv("WRITER_READY_FILE")); err == nil {
		_ = f.Close()
	}
	select {} // block forever; parent SIGKILLs us
}

// TestExecuteSqlite_ReadAfterWriterKill verifies the Read tool can read a
// WAL-mode database whose writer process was SIGKILLed — committed frames
// sit in an orphaned -wal with a stale -shm. This was the historical
// "disk I/O error (522)" shape; the mode=ro open must recover it readably.
func TestExecuteSqlite_ReadAfterWriterKill(t *testing.T) {
	dbPath := createDBWithClosedWriter(t)

	in := Input{FilePath: dbPath + ":t"}
	result, err := Execute(context.Background(), mustMarshalInput(t, in), nil)
	if err != nil {
		t.Fatalf("Execute failed after writer kill: %v", err)
	}
	out, ok := result.Data.(TextOutput)
	if !ok {
		t.Fatalf("Data type = %T, want TextOutput", result.Data)
	}
	if !strings.Contains(out.Content, "row") {
		t.Errorf("expected rows in output, got: %q", out.Content)
	}
}

// TestExecuteSqlite_NoJournalFileAfterCommit guards against a different
// regression class than the WAL-era sidecar check: a rollback -journal must
// never survive committed transactions (it would mean a torn transaction
// state), regardless of journal mode. The -wal/-shm pair is EXPECTED in WAL
// mode after a kill and is covered by the recovery test above.
func TestExecuteSqlite_NoJournalFileAfterCommit(t *testing.T) {
	dbPath := createDBWithClosedWriter(t)

	if _, err := os.Stat(dbPath + "-journal"); err == nil {
		t.Errorf("rollback journal %s exists after committed transactions", dbPath+"-journal")
	}
}

func mustMarshalInput(t *testing.T, in Input) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return json.RawMessage(raw)
}
