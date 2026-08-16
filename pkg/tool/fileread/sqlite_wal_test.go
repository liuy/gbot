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
// With journal_mode=DELETE there are no -wal/-shm sidecars whose lifecycle
// can race the reader; rollback journal (-journal) only exists mid-transaction.
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
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(DELETE)&_pragma=foreign_keys(ON)&_txlock=immediate")
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
// database whose writer process was killed without cleanup. This is the
// regression guard for the WAL-era bug where a live -wal with a missing -shm
// made every mode=ro open fail with "disk I/O error (522)": with
// journal_mode=DELETE there are no sidecar files to race, so the read path
// must be deterministic regardless of how the writer stopped.
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

// TestExecuteSqlite_NoSidecarFilesAfterCommit asserts the on-disk invariant
// that removes the 522 bug class: outside a transaction there must be no
// -wal, -shm, or -journal file next to the database. A WAL-mode regression
// (journal_mode leaking back into the store DSN) would leave -wal/-shm here.
func TestExecuteSqlite_NoSidecarFilesAfterCommit(t *testing.T) {
	dbPath := createDBWithClosedWriter(t)

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			t.Errorf("sidecar %s exists after committed transactions; journal_mode must be DELETE, not WAL", dbPath+suffix)
		}
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
