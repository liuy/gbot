package forget

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/memory/facts"
	"github.com/liuy/gbot/pkg/tool"
	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	return db, func() { _ = db.Close() }
}

func identitySegmenter(s string) string { return s }

func TestForget_DeletesExisting(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	id, _, err := fs.AddFact("to be forgotten")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	r := New(fs)
	input, _ := json.Marshal(Input{FactID: id})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if !out.Deleted {
		t.Error("Deleted should be true")
	}
	if out.FactID != id {
		t.Errorf("FactID = %d, want %d", out.FactID, id)
	}

	// Verify the fact is gone.
	hits, err := fs.SearchFacts("forgotten", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("fact should be gone, got %d hits", len(hits))
	}
}

func TestForget_NonExistentId(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	r := New(fs)

	input, _ := json.Marshal(Input{FactID: 99999})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("forget unknown id should not error: %v", err)
	}
	out := result.Data.(*Output)
	if !out.Deleted {
		t.Error("Deleted should still be true (DeleteFact is idempotent)")
	}
}

func TestForget_InvalidJSON(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	r := New(fs)

	_, err = r.Call(context.Background(), json.RawMessage(`not json`), nil)
	if err == nil {
		t.Fatal("invalid JSON should error")
	}
}

func TestForget_NotReadOnly(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	fs, err := facts.NewStore(db, identitySegmenter)
	if err != nil {
		t.Fatalf("facts.NewStore: %v", err)
	}
	r := New(fs)
	if r.IsReadOnly(nil) {
		t.Error("forget should NOT be read-only")
	}
	if r.IsConcurrencySafe(nil) {
		t.Error("forget should NOT be concurrency-safe")
	}
	if r.InterruptBehavior() != tool.InterruptBlock {
		t.Error("forget should use InterruptBlock")
	}
}
