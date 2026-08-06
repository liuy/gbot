package forget

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

func openFactStore(t *testing.T) *short.Store {
	t.Helper()
	store, err := short.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("short.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestForget_DeletesExisting(t *testing.T) {
	fs := openFactStore(t)
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
	fs := openFactStore(t)
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
	fs := openFactStore(t)
	r := New(fs)

	_, err := r.Call(context.Background(), json.RawMessage(`not json`), nil)
	if err == nil {
		t.Fatal("invalid JSON should error")
	}
}

func TestForget_NotReadOnly(t *testing.T) {
	fs := openFactStore(t)
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
