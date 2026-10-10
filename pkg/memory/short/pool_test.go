package short

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestStore_WritePoolPinnedSingleConnection guards the single-write-connection
// architecture: the write pool must never grow beyond one connection. Two
// write connections on a WAL file can pair a commit with a checkpoint at the
// same instant and delete the -wal/-shm sidecars under live readers — the
// data-loss class this topology exists to prevent. DBStats mirrors
// SetMaxOpenConns, and a sequential write followed by Idle==1 proves the
// single conn is retained (MaxIdleConns=1), never discarded between writes.
func TestStore_WritePoolPinnedSingleConnection(t *testing.T) {
	store := openTestStore(t)

	st := store.db.Stats()
	if st.MaxOpenConnections != 1 {
		t.Errorf("write pool MaxOpenConnections = %d, want 1 (a second write conn reintroduces the WAL race)", st.MaxOpenConnections)
	}

	if _, err := store.CreateSession("/project", "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	st = store.db.Stats()
	if st.OpenConnections != 1 {
		t.Errorf("write pool OpenConnections after write = %d, want exactly 1", st.OpenConnections)
	}
	if st.Idle != 1 {
		t.Errorf("write pool Idle after write = %d, want 1 (the single conn must be retained, not closed)", st.Idle)
	}
}

// TestStore_ReadPoolRejectsWrites proves the read pool DSN is mode=ro: a
// write attempt must fail with SQLite's readonly error, and the pool must
// stay queryable afterwards (the rejected statement poisons nothing).
func TestStore_ReadPoolRejectsWrites(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, err = store.readDB.Exec(
		`INSERT INTO messages (session_id, uuid, type, content) VALUES (?, ?, 'user', '{}')`,
		ses.SessionID, "uuid-ro-write",
	)
	if err == nil {
		t.Fatal("write on read pool succeeded; DSN must be mode=ro")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("write on read pool error = %v, want sqlite readonly error", err)
	}

	var n int
	if err := store.readDB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&n); err != nil {
		t.Fatalf("read pool query after rejected write: %v", err)
	}
	if n != 0 {
		t.Errorf("messages count on read pool = %d, want 0", n)
	}
}

// TestStore_ReadPoolUsableImmediatelyOnFreshDB pins the startup-order
// outcome: right after NewStore returns on a database that did not exist,
// the read pool answers queries. A mode=ro connection cannot create the
// WAL sidecars itself, so this only works because NewStore opened the write
// connection and ran initSchema (whose DDL writes materialize -wal/-shm)
// before the read pool ever materializes a connection.
func TestStore_ReadPoolUsableImmediatelyOnFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Skip("fresh-db precondition not met")
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var tables int
	if err := store.readDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'messages'`,
	).Scan(&tables); err != nil {
		t.Fatalf("read pool query right after NewStore: %v", err)
	}
	if tables != 1 {
		t.Errorf("messages table visible on read pool = %d, want 1", tables)
	}
}

// TestStore_ConcurrentWritersAndReadPoolReaders is the concurrency smoke for
// the split topology: concurrent AppendMessages transactions serialize on the
// single write connection while readers hammer the read-only pool. Must pass
// under -race; the final count proves no append was lost or duplicated.
func TestStore_ConcurrentWritersAndReadPoolReaders(t *testing.T) {
	store := openTestStore(t)
	ses, err := store.CreateSession("/project", "model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	const writers = 4
	const perWriter = 25

	var writeWG sync.WaitGroup
	for w := range writers {
		writeWG.Go(func() {
			for i := range perWriter {
				msg := &TranscriptMessage{
					UUID:    fmt.Sprintf("w%d-%d", w, i),
					Type:    "user",
					Content: `[{"type":"text","text":"concurrent write"}]`,
				}
				if err := store.AppendMessages(ses.SessionID, []*TranscriptMessage{msg}); err != nil {
					t.Errorf("AppendMessages w%d-%d: %v", w, i, err)
					return
				}
			}
		})
	}

	readDone := make(chan struct{})
	var readWG sync.WaitGroup
	for range writers {
		readWG.Go(func() {
			for {
				select {
				case <-readDone:
					return
				default:
				}
				if _, err := store.LoadMessages(ses.SessionID); err != nil {
					t.Errorf("LoadMessages during writes: %v", err)
					return
				}
			}
		})
	}

	writeWG.Wait()
	close(readDone)
	readWG.Wait()

	msgs, err := store.LoadMessages(ses.SessionID)
	if err != nil {
		t.Fatalf("final LoadMessages: %v", err)
	}
	if len(msgs) != writers*perWriter {
		t.Errorf("messages after concurrent appends = %d, want %d", len(msgs), writers*perWriter)
	}
}
