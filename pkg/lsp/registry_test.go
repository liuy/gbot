package lsp

import (
	"context"
	"slices"
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
	want := `no lsp server for file extension ".unknownext" (path: /tmp/foo.unknownext)`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestRegistry_ForFile_NoExtension(t *testing.T) {
	r := NewRegistry("/tmp")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.ForFile(ctx, "/tmp/README")
	if err == nil {
		t.Fatal("expected error for missing extension")
	}
	want := "lsp needs a file path with extension (e.g. .go), got: /tmp/README"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
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

func TestSortSpecsByName(t *testing.T) {
	// Command and Language are ordered opposite to Name so a sort keyed on the
	// wrong field cannot pass.
	specs := []ServerSpec{
		{Name: "rust-analyzer", Command: "alpha", Language: "Alpha", FileExts: []string{".rs"}},
		{Name: "clangd", Command: "zinc", Language: "Zulu", FileExts: []string{".c"}},
		{Name: "gopls", Command: "mid", Language: "Mid", FileExts: []string{".go"}},
	}
	sortSpecsByName(specs)

	got := make([]string, len(specs))
	for i, s := range specs {
		got[i] = s.Name + "/" + s.Language
	}
	want := []string{"clangd/Zulu", "gopls/Mid", "rust-analyzer/Alpha"}
	if !slices.Equal(got, want) {
		t.Errorf("sortSpecsByName = %v, want %v", got, want)
	}
}

func TestServerNamesLocked(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	defer r.mu.Unlock()

	if got := r.serverNamesLocked(); len(got) != 0 {
		t.Errorf("serverNamesLocked on empty registry = %v, want empty", got)
	}

	r.specs = []ServerSpec{
		{Name: "gopls", Language: "Go"},
		{Name: "pyright-langserver", Language: "Python"},
		{Name: "rust-analyzer", Language: "Rust"},
	}
	want := []string{"gopls", "pyright-langserver", "rust-analyzer"}
	if got := r.serverNamesLocked(); !slices.Equal(got, want) {
		t.Errorf("serverNamesLocked = %v, want %v", got, want)
	}
}
