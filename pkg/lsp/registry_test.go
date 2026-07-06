package lsp

import (
	"context"
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
	if s != "gopls (Go), tsserver (TypeScript)" {
		t.Errorf("LSPString() = %q", s)
	}
}
