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

func TestRemember_Update(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	// Seed an existing fact.
	id1, _, err := fs.AddFact("alice likes blue")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	// Update it with a new content.
	oldID := id1
	input, _ := json.Marshal(Input{FactID: &oldID, Content: "alice likes red"})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out, ok := result.Data.(*Output)
	if !ok {
		t.Fatalf("Data type = %T, want *Output", result.Data)
	}
	if !out.Stored {
		t.Error("Stored should be true for update")
	}
	if out.Duplicate {
		t.Error("Duplicate should be false for update")
	}
	if out.FactID == id1 {
		t.Error("update should produce a new fact_id, not reuse the old one")
	}
	if out.Message != "Updated fact." {
		t.Errorf("Message = %q, want %q", out.Message, "Updated fact.")
	}

	// Old fact must be gone, new fact must be searchable under the new id.
	hitsBlue, err := fs.SearchFacts("blue", 10)
	if err != nil {
		t.Fatalf("SearchFacts(blue): %v", err)
	}
	if len(hitsBlue) != 0 {
		t.Errorf("old fact should be deleted, got %d hits", len(hitsBlue))
	}
	hitsRed, err := fs.SearchFacts("red", 10)
	if err != nil {
		t.Fatalf("SearchFacts(red): %v", err)
	}
	if len(hitsRed) != 1 {
		t.Fatalf("expected 1 hit for new content, got %d", len(hitsRed))
	}
	if hitsRed[0].ID != out.FactID {
		t.Errorf("new fact id = %d, want %d", hitsRed[0].ID, out.FactID)
	}
}

func TestRemember_UpdateNonExistentID(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	// DeleteFact on an unknown id is a no-op, so the update degenerates to a create.
	missing := int64(99999)
	input, _ := json.Marshal(Input{FactID: &missing, Content: "brand new fact"})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*Output)
	if !out.Stored {
		t.Error("Stored should be true (fact created)")
	}
	if out.Duplicate {
		t.Error("Duplicate should be false")
	}
	if out.FactID <= 0 {
		t.Errorf("FactID should be > 0, got %d", out.FactID)
	}
	if out.Message != "Updated fact." {
		t.Errorf("Message = %q, want %q", out.Message, "Updated fact.")
	}
	hits, err := fs.SearchFacts("brand", 10)
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

func TestRemember_Description_Normal(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	input, _ := json.Marshal(Input{Content: "alice likes blue"})
	got, err := r.Description(input)
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if got != "alice likes blue" {
		t.Errorf("Description = %q, want %q", got, "alice likes blue")
	}
}

func TestRemember_Description_Truncate(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	long := strings.Repeat("a", 100)
	input, _ := json.Marshal(Input{Content: long})
	got, err := r.Description(input)
	if err != nil {
		t.Fatalf("Description: %v", err)
	}
	if len(got) != 80 {
		t.Errorf("Description length = %d, want 80", len(got))
	}
	if got != long[:80] {
		t.Errorf("Description = %q, want first 80 chars", got)
	}
}

func TestRemember_Description_InvalidJSON(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	got, err := r.Description(json.RawMessage(`{not json`))
	if err != nil {
		t.Fatalf("Description should not error on invalid JSON: %v", err)
	}
	if got != "Store a new fact" {
		t.Errorf("Description fallback = %q, want %q", got, "Store a new fact")
	}
}

func TestRemember_Call_MalformedJSON(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	_, err := r.Call(context.Background(), json.RawMessage(`{not json`), nil)
	if err == nil {
		t.Fatal("malformed JSON should error")
	}
	if !strings.Contains(err.Error(), "parse input") {
		t.Errorf("error = %q, want 'parse input'", err.Error())
	}
}

func TestRemember_Update_Duplicate(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	existingID, _, err := fs.AddFact("already here")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}
	toUpdate, _, err := fs.AddFact("to be replaced")
	if err != nil {
		t.Fatalf("AddFact: %v", err)
	}

	input, _ := json.Marshal(Input{FactID: &toUpdate, Content: "already here"})
	result, err := r.Call(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	out := result.Data.(*Output)
	if out.Stored {
		t.Error("Stored should be false when update target content is a duplicate")
	}
	if !out.Duplicate {
		t.Error("Duplicate should be true")
	}
	if out.FactID != existingID {
		t.Errorf("FactID = %d, want existing id %d", out.FactID, existingID)
	}
	if out.Message != "Fact already exists (duplicate)." {
		t.Errorf("Message = %q, want 'Fact already exists (duplicate).'", out.Message)
	}
}

func TestRemember_RenderResult_Fallback(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	got := r.RenderResult("not an Output")
	if got != "\"not an Output\"" {
		t.Errorf("RenderResult fallback = %q, want JSON-encoded string", got)
	}
}

func TestRemember_RenderResult_Output(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)

	out := &Output{Stored: true, FactID: 5, Message: "Stored new fact."}
	got := r.RenderResult(out)
	if got != "Stored new fact." {
		t.Errorf("RenderResult = %q, want %q", got, "Stored new fact.")
	}
}

func TestRemember_DecodeResult_Normal(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)
	dr := r.(tool.ToolWithDecodeResult)

	wire := tool.WrapSingleBlock(`{"stored":true,"fact_id":42,"duplicate":false,"message":"Stored new fact."}`)
	decoded, err := dr.DecodeResult(wire)
	if err != nil {
		t.Fatalf("DecodeResult: %v", err)
	}
	out, ok := decoded.(*Output)
	if !ok {
		t.Fatalf("decoded type = %T, want *Output", decoded)
	}
	if !out.Stored {
		t.Error("Stored should be true")
	}
	if out.FactID != 42 {
		t.Errorf("FactID = %d, want 42", out.FactID)
	}
	if out.Duplicate {
		t.Error("Duplicate should be false")
	}
	if out.Message != "Stored new fact." {
		t.Errorf("Message = %q, want %q", out.Message, "Stored new fact.")
	}
}

func TestRemember_DecodeResult_InvalidJSON(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)
	dr := r.(tool.ToolWithDecodeResult)

	wire := tool.WrapSingleBlock(`{not json`)
	_, err := dr.DecodeResult(wire)
	if err == nil {
		t.Fatal("DecodeResult should error on invalid JSON")
	}
}

func TestRemember_DecodeResult_NonArray(t *testing.T) {
	fs := openFactStore(t)
	r := New(fs)
	dr := r.(tool.ToolWithDecodeResult)

	_, err := dr.DecodeResult(json.RawMessage(`"bare string"`))
	if err == nil {
		t.Fatal("DecodeResult should error on non-array input")
	}
}
