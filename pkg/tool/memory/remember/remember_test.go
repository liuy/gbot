package remember

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestRemember_New(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	input, _ := json.Marshal(Input{Content: "alice likes blue"})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if !out.Stored {
		t.Error("Stored should be true for new fact")
	}
	if out.Duplicate {
		t.Error("Duplicate should be false for new fact")
	}
	if out.FactID <= 0 {
		t.Errorf("FactID should be > 0, got %d", out.FactID)
	}

	// Verify the fact was persisted.
	hits, err := fs.SearchFacts("blue", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].ID != out.FactID {
		t.Errorf("persisted id = %d, want %d", hits[0].ID, out.FactID)
	}
}

func TestRemember_Duplicate(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	// First call — stores.
	input1, _ := json.Marshal(Input{Content: "same fact"})
	result1, err := r.Call(context.Background(), input1, nil)
	if err != nil {
		t.Fatalf("first Call: %v", err)
	}
	out1 := result1.Data.(*Output)
	if !out1.Stored {
		t.Error("first Stored should be true")
	}

	// Second call: should be a duplicate.
	input2, _ := json.Marshal(Input{Content: "same fact"})
	result2, err := r.Call(context.Background(), input2, nil)
	if err != nil {
		t.Fatalf("second Call: %v", err)
	}
	out2 := result2.Data.(*Output)
	if out2.Stored {
		t.Error("second Stored should be false for duplicate")
	}
	if !out2.Duplicate {
		t.Error("Duplicate should be true")
	}
	if out1.FactID != out2.FactID {
		t.Errorf("duplicate should return same id: %d vs %d", out1.FactID, out2.FactID)
	}
}

func TestRemember_EmptyContent(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	input, _ := json.Marshal(Input{Content: ""})
	_, err := r.Call(context.Background(), input, nil)
	if err == nil {
		t.Fatal("empty content should error")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Errorf("error = %q, want 'content is required'", err.Error())
	}
}

func TestRemember_MissingContent(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	_, err := r.Call(context.Background(), json.RawMessage(`{}`), nil)
	if err == nil {
		t.Fatal("missing content should error")
	}
}

func TestRemember_NotReadOnly(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)
	if r.IsReadOnly(nil) {
		t.Error("remember should NOT be read-only")
	}
	if r.IsConcurrencySafe(nil) {
		t.Error("remember should NOT be concurrency-safe")
	}
	if r.InterruptBehavior() != tool.InterruptBlock {
		t.Error("remember should use InterruptBlock")
	}
}
