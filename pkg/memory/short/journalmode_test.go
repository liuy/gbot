package short

import (
	"os"
	"testing"
)

// TestStoreJournalModeDelete pins the store's journal mode to DELETE. WAL was
// removed because its -wal/-shm sidecars broke cross-process reads (Read tool
// mode=ro intermittently failed with "disk I/O error (522)" when the shm
// lifecycle raced). A regression back to WAL must fail here, not in the field.
func TestStoreJournalModeDelete(t *testing.T) {
	path := t.TempDir() + "/test.db"
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "delete" {
		t.Errorf("journal_mode = %q, want delete", mode)
	}
}

// TestStoreNoSidecarFilesAfterClose asserts the on-disk invariant that kills
// the 522 bug class: after the last connection closes, no -wal/-shm files
// remain. WAL mode leaves -wal/-shm behind after unclean stops.
func TestStoreNoSidecarFilesAfterClose(t *testing.T) {
	path := t.TempDir() + "/test.db"
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.CreateSession("/project", "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Errorf("sidecar %s exists after clean close; journal_mode must be DELETE", path+suffix)
		}
	}
}
