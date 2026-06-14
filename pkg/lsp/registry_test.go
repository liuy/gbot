package lsp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRegistry_SnapshotEmpty(t *testing.T) {
	r := NewRegistry("/tmp")
	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("new registry Snapshot = %v, want empty", got)
	}
}

func TestRegistry_StartWithFakeSpecs(t *testing.T) {
	r := NewRegistry("/tmp")
	specs := []ServerSpec{
		{Name: "this-lsp-does-not-exist-anywhere-xyz", Command: "this-lsp-does-not-exist-anywhere-xyz", FileExts: []string{".x"}, Language: "Fake"},
	}
	r.Scan(specs)

	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot = %v, want empty after failed discovery", got)
	}
	if r.HasExtension(".x") {
		t.Errorf("HasExtension(.x) = true, want false")
	}
}

func TestRegistry_HasExtension_ManualInsert(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.extToSpec[".go"] = ServerSpec{Name: "gopls", Language: "Go"}
	r.mu.Unlock()

	if !r.HasExtension(".go") {
		t.Errorf("HasExtension(.go) = false, want true")
	}
	if r.HasExtension(".py") {
		t.Errorf("HasExtension(.py) = true, want false")
	}
}

func TestRegistry_ForFile_NoSpec(t *testing.T) {
	r := NewRegistry("/tmp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.ForFile(ctx, "/tmp/foo.unknownext")
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
	_ = err.Error()
}

func TestRegistry_ForFile_NoExtension(t *testing.T) {
	r := NewRegistry("/tmp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.ForFile(ctx, "/tmp/README")
	if err == nil {
		t.Fatal("expected error for missing extension")
	}
	_ = err.Error()
}

func TestRegistry_Shutdown_Idempotent(t *testing.T) {
	r := NewRegistry("/tmp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Empty registry; Shutdown should be a no-op.
	r.Shutdown(ctx)
	// Call again — still no-op.
	r.Shutdown(ctx)
}

func TestDiscover_EmptySpecs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	alive := Discover(ctx, nil, "/tmp")
	if len(alive) != 0 {
		t.Errorf("Discover(nil) = %v, want empty", alive)
	}
}

func TestPathToURI_Absolute(t *testing.T) {
	uri := pathToURI("/tmp/x")
	if uri[:7] != "file://" {
		t.Errorf("pathToURI = %q, missing file:// prefix", uri)
	}
}

func TestLSPString_Empty(t *testing.T) {
	r := NewRegistry("/tmp")
	if s := r.LSPString(); s != "" {
		t.Errorf("LSPString() = %q, want empty", s)
	}
}

func TestLSPString_WithServers(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.specs = []ServerSpec{
		{Name: "gopls", Language: "Go"},
		{Name: "tsserver", Language: "TypeScript"},
	}
	r.mu.Unlock()
	s := r.LSPString()
	if s != "gopls (Go) | tsserver (TypeScript)" {
		t.Errorf("LSPString() = %q", s)
	}
}

func TestInjectLSPField_NilReg(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)
	result := InjectLSPField(schema, nil)
	if string(result) != `{"type":"object","properties":{"pattern":{"type":"string"}}}` {
		t.Errorf("InjectLSPField with nil reg modified schema: %s", result)
	}
}

func TestInjectLSPField_EmptyReg(t *testing.T) {
	r := NewRegistry("/tmp")
	schema := json.RawMessage(`{"type":"object"}`)
	result := InjectLSPField(schema, r)
	if string(result) != `{"type":"object"}` {
		t.Errorf("InjectLSPField with empty reg modified schema: %s", result)
	}
}

func TestInjectLSPField_WithSpecs(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.specs = []ServerSpec{{Name: "gopls", Language: "Go"}}
	r.mu.Unlock()
	schema := json.RawMessage(`{"type":"object","properties":{}}`)
	result := InjectLSPField(schema, r)
	var m map[string]any
	_ = json.Unmarshal(result, &m)
	props, _ := m["properties"].(map[string]any)
	if _, ok := props["lsp"]; !ok {
		t.Errorf("InjectLSPField did not add lsp property: %s", result)
	}
}
