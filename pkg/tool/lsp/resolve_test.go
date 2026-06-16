package lsptool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

// --- happy-path resolveSymbolPosition ---

func TestIntegration_ResolveInWorkspace(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"location": map[string]any{
						"uri": "file://" + filepath.Join(d, "foo.go"),
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 5},
							"end":   map[string]any{"line": 0, "character": 8},
						},
					},
				}}, true
			}
			if method == "textDocument/documentSymbol" {
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	uri, pos, err := resolveSymbolPosition(context.Background(), reg, "foo", "", dir)
	if err != nil {
		t.Fatalf("resolveSymbolPosition: %v", err)
	}
	if !strings.Contains(uri, "foo.go") {
		t.Errorf("uri = %q", uri)
	}
	if pos.Line != 0 || pos.Character != 5 {
		t.Errorf("pos = %+v", pos)
	}
}

func TestIntegration_ResolveInWorkspace_Occurrence(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []map[string]any{
					{
						"name": "foo",
						"kind": 12,
						"location": map[string]any{
							"uri": "file://" + filepath.Join(d, "a.go"),
							"range": map[string]any{
								"start": map[string]any{"line": 0, "character": 5},
								"end":   map[string]any{"line": 0, "character": 8},
							},
						},
					},
					{
						"name": "foo",
						"kind": 12,
						"location": map[string]any{
							"uri": "file://" + filepath.Join(d, "b.go"),
							"range": map[string]any{
								"start": map[string]any{"line": 1, "character": 5},
								"end":   map[string]any{"line": 1, "character": 8},
							},
						},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc foo() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	uri1, _, err := resolveSymbolPosition(context.Background(), reg, "foo#1", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri1, "a.go") {
		t.Errorf("occurrence 1 uri = %q, want a.go", uri1)
	}

	uri2, _, err := resolveSymbolPosition(context.Background(), reg, "foo#2", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri2, "b.go") {
		t.Errorf("occurrence 2 uri = %q, want b.go", uri2)
	}
}

// --- unit-level resolveSymbolPosition ---

func TestResolveSymbolPosition_EmptySymbol(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	_, _, err := resolveSymbolPosition(context.Background(), reg, "", "", "/test")
	if err == nil {
		t.Fatal("should fail for empty symbol")
	}
	if !strings.Contains(err.Error(), "symbol parameter required") {
		t.Errorf("wrong error: %v", err)
	}
}

// --- resolve.go error paths ---

func TestIntegration_ResolveSymbolInFile_DocumentSymbolError(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\n"), 0644)

	_, _, err := resolveSymbolPosition(context.Background(), reg, "missing", filepath.Join(dir, "test.go"), dir)
	if err == nil {
		t.Fatal("expected error for symbol not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegration_ResolveSymbol_OccurrenceOutOfRange(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"),
		[]byte("package main\nfunc foo() {}\n"), 0644)

	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo#5", filepath.Join(dir, "test.go"), dir)
	if err == nil {
		t.Fatal("expected error for occurrence out of range")
	}
	if !strings.Contains(err.Error(), "occurrence") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegration_ResolveSymbolInWorkspace_NoServers(t *testing.T) {
	reg := lsp.NewRegistry("/empty")
	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo", "", "/empty")
	if err == nil {
		t.Fatal("expected error for no servers")
	}
	if !strings.Contains(err.Error(), "no language server") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegration_ResolveSymbolInWorkspace_NotFound(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_, _, err := resolveSymbolPosition(context.Background(), reg, "nonexistent", "", dir)
	if err == nil {
		t.Fatal("expected error for symbol not found in workspace")
	}
	if !strings.Contains(err.Error(), "not found in workspace") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegration_ResolveSymbolInWorkspace_OccurrenceOutOfRange(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"location": map[string]any{
						"uri": "file://" + filepath.Join(d, "test.go"),
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
						},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo#5", "", dir)
	if err == nil {
		t.Fatal("expected error for occurrence out of range in workspace")
	}
	if !strings.Contains(err.Error(), "occurrence") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- collectDocumentSymbolMatches unit ---

func TestCollectDocumentSymbolMatches_Nested(t *testing.T) {
	syms := []lsp.DocumentSymbol{
		{Name: "foo", Kind: lsp.SymbolFunction, SelectionRange: lsp.Range{Start: lsp.Position{Line: 0}}},
		{Name: "bar", Kind: lsp.SymbolMethod, SelectionRange: lsp.Range{Start: lsp.Position{Line: 5}},
			Children: []lsp.DocumentSymbol{
				{Name: "foo", Kind: lsp.SymbolVariable, SelectionRange: lsp.Range{Start: lsp.Position{Line: 7}}},
			}},
	}
	var matches []symbolMatch
	collectDocumentSymbolMatches(syms, "foo", &matches, "file:///test.go")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].pos.Line != 0 {
		t.Errorf("first match line = %d", matches[0].pos.Line)
	}
	if matches[1].pos.Line != 7 {
		t.Errorf("second match line = %d", matches[1].pos.Line)
	}
}
