package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureFileOpen_Deduped(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.EnsureFileOpen(ctx, "file:///x/foo.go", "go", "package foo\n"); err != nil {
		t.Fatalf("EnsureFileOpen: %v", err)
	}
	// Second call should be deduped — openURIs entry still exists.
	if err := c.EnsureFileOpen(ctx, "file:///x/foo.go", "go", "package foo\n"); err != nil {
		t.Fatalf("EnsureFileOpen 2: %v", err)
	}
	c.mu.Lock()
	_, exists := c.openURIs["file:///x/foo.go"]
	c.mu.Unlock()
	if !exists {
		t.Error("openURIs entry missing after EnsureFileOpen")
	}
}

func TestDidChange_IncrementsVersion(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.EnsureFileOpen(ctx, "file:///x.go", "go", "v1"); err != nil {
		t.Fatalf("EnsureFileOpen: %v", err)
	}
	if err := c.DidChange(ctx, "file:///x.go", "v2"); err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	c.mu.Lock()
	v := c.openURIs["file:///x.go"]
	c.mu.Unlock()
	if v != 2 {
		t.Errorf("version = %d, want 2", v)
	}
}

func TestDidChange_NotOpened(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.DidChange(ctx, "file:///never-opened.go", "x")
	if err == nil {
		t.Fatal("expected error on DidChange before didOpen")
	}
	_ = err.Error()
}

func TestHandleServerRequest_WorkspaceConfiguration(t *testing.T) {
	_, srv, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	// Simulate server sending workspace/configuration request.
	// The client must respond to unblock the connection. We can't read the
	// response from srv.conn (the serve goroutine consumes it), so we verify
	// indirectly: the server send+client response completes without hang.
	srv.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      999,
		"method":  "workspace/configuration",
		"params":  map[string]any{"items": []any{}},
	})
	// If the client didn't respond, the pipe would deadlock.
	// The test passes by reaching cleanup without timeout.
}

func TestApplyWorkspaceEdit_CreateResourceOp(t *testing.T) {
	dir := t.TempDir()
	uri := pathToURI(filepath.Join(dir, "new.go"))

	dc := map[string]any{
		"kind": "create",
		"uri":  uri,
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %v, want 1 entry", changed)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err != nil {
		t.Errorf("created file missing: %v", err)
	}
}

func TestApplyWorkspaceEdit_DeleteResourceOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doomed.go")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(path)

	dc := map[string]any{"kind": "delete", "uri": uri}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %v, want 1 entry", changed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after delete: %v", err)
	}
}

func TestApplyWorkspaceEdit_RenameResourceOp(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.go")
	newPath := filepath.Join(dir, "new.go")
	if err := os.WriteFile(oldPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	dc := map[string]any{
		"kind":   "rename",
		"oldUri": pathToURI(oldPath),
		"newUri": pathToURI(newPath),
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %v", changed)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file still exists: %v", err)
	}
}

func TestPrepareRename_WrappedRange_Simplified(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/prepareRename" {
			return map[string]any{
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 5},
				},
				"placeholder": "oldName",
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := PrepareRename(ctx, c, "file:///x.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("PrepareRename: %v", err)
	}
	if r == nil || r.End.Character != 5 {
		t.Errorf("range = %+v", r)
	}
}
