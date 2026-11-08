package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestRename_RequiresNewName(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	c := fakeClient(t, reg)
	_, err := rename(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, "", dir, true, lsp.ServerSpec{})
	if err == nil || err.Error() != "new_name parameter required for rename" {
		t.Fatalf("error = %v", err)
	}
}

func TestRename_NoEdits(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := rename(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, "new", dir, true, lsp.ServerSpec{})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "Rename returned no edits" {
		t.Errorf("output = %q", got)
	}
}

func TestRename_Apply_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				src := filepath.Join(d, "a.go")
				return map[string]any{"changes": map[string]any{
					lsp.FileToURI(src): []map[string]any{{
						"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 7}},
						"newText": "package2",
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "a.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := rename(context.Background(), c, lsp.FileToURI(src), lsp.Position{}, "new", dir, true, lsp.ServerSpec{})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	want := "Applied rename:\n  " + src + "\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
	data, _ := os.ReadFile(src)
	if string(data) != "package2 main\n" {
		t.Errorf("a.go = %q", data)
	}
}

func TestRename_ApplyFail(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				missing := filepath.Join(d, "gone.go")
				return map[string]any{"changes": map[string]any{
					lsp.FileToURI(missing): []map[string]any{{
						"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}},
						"newText": "X",
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	_, err := rename(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, "new", dir, true, lsp.ServerSpec{})
	if err == nil || !strings.HasPrefix(err.Error(), "apply rename:") {
		t.Fatalf("error = %v, want prefix apply rename:", err)
	}
	// %w not %v: callers may unwrap to the underlying edit failure.
	if errors.Unwrap(err) == nil {
		t.Fatal("apply-rename error must wrap its cause")
	}
}

func TestRename_Preview(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				src := lsp.FileToURI(filepath.Join(d, "a.go"))
				return map[string]any{"changes": map[string]any{
					src: []map[string]any{{
						"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 7}},
						"newText": "package2",
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "a.go")
	if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := rename(context.Background(), c, lsp.FileToURI(src), lsp.Position{}, "new", dir, false, lsp.ServerSpec{})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	want := "Rename preview:\n  a.go: 1 edit\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("preview =\n%q\nwant\n%q", got, want)
	}
	data, _ := os.ReadFile(src)
	if string(data) != "package main\n" {
		t.Errorf("preview must not write: %q", data)
	}
}

func TestRename_PreviewEmptyEdits(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				return map[string]any{"changes": map[string]any{}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := rename(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, "new", dir, false, lsp.ServerSpec{})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	want := "Rename preview:\n  (no edits)\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}
}

func TestRename_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/rename" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rename(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, "new", dir, true, lsp.ServerSpec{})
	if err == nil || !strings.HasPrefix(err.Error(), "rename:") {
		t.Fatalf("error = %v, want prefix rename:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}
