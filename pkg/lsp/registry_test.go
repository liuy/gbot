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
	// Discover probes real binaries; with a fake spec that doesn't exist,
	// we expect zero results (the only safe portable assertion).
	specs := []ServerSpec{
		{Name: "this-lsp-does-not-exist-anywhere-xyz", Command: "this-lsp-does-not-exist-anywhere-xyz", FileExts: []string{".x"}, Language: "Fake"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.Start(ctx, specs)

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
	// Should start with file:// scheme.
	if uri[:7] != "file://" {
		t.Errorf("pathToURI = %q, missing file:// prefix", uri)
	}
}
