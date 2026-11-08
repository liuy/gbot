package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
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
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	uri, pos, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, reg.DefaultRoot(), "")
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

	uri1, _, err := resolveSymbolPosition(context.Background(), reg, "foo#1", dir, reg.DefaultRoot(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri1, "a.go") {
		t.Errorf("occurrence 1 uri = %q, want a.go", uri1)
	}

	uri2, _, err := resolveSymbolPosition(context.Background(), reg, "foo#2", dir, reg.DefaultRoot(), "")
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
	_, _, err := resolveSymbolPosition(context.Background(), reg, "", "/test", reg.DefaultRoot(), "")
	if err == nil {
		t.Fatal("should fail for empty symbol")
	}
	if !strings.Contains(err.Error(), "symbol parameter required") {
		t.Errorf("wrong error: %v", err)
	}
}

// --- resolve.go error paths ---

func TestIntegration_ResolveSymbol_OccurrenceOutOfRange(t *testing.T) {
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

	_ = os.WriteFile(filepath.Join(dir, "test.go"),
		[]byte("package main\nfunc foo() {}\n"), 0644)

	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo#5", dir, reg.DefaultRoot(), "")
	if err == nil {
		t.Fatal("expected error for occurrence out of range")
	}
	if !strings.Contains(err.Error(), "occurrence") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIntegration_ResolveSymbolInWorkspace_NoServers(t *testing.T) {
	reg := lsp.NewRegistry("/empty")
	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo", "/empty", reg.DefaultRoot(), "")
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

	_, _, err := resolveSymbolPosition(context.Background(), reg, "nonexistent", dir, reg.DefaultRoot(), "")
	if err == nil {
		t.Fatal("expected error for symbol not found in workspace")
	}
	want := fmt.Sprintf(`symbol "nonexistent" not found in %s (pass file=<a path inside the project you mean> to query a different project)`, reg.DefaultRoot())
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestIntegration_ResolveQualifiedSymbolName(t *testing.T) {
	// gopls returns qualified names like "Store.LoadMessagesAfterSeq" but
	// users search for the unqualified name "LoadMessagesAfterSeq". The
	// exact match (s.Name == symbol) must handle this.
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []map[string]any{
					{
						"name": "Store.LoadMessagesAfterSeq",
						"kind": 6,
						"location": map[string]any{
							"uri": "file://" + filepath.Join(d, "store.go"),
							"range": map[string]any{
								"start": map[string]any{"line": 5, "character": 0},
								"end":   map[string]any{"line": 5, "character": 20},
							},
						},
					},
					{
						"name": "(*mockEngine).Messages",
						"kind": 6,
						"location": map[string]any{
							"uri": "file://" + filepath.Join(d, "mock.go"),
							"range": map[string]any{
								"start": map[string]any{"line": 10, "character": 0},
								"end":   map[string]any{"line": 10, "character": 20},
							},
						},
					},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	// Unqualified name should match qualified return
	uri, _, err := resolveSymbolPosition(context.Background(), reg, "LoadMessagesAfterSeq", dir, reg.DefaultRoot(), "")
	if err != nil {
		t.Fatalf("expected LoadMessagesAfterSeq to resolve, got: %v", err)
	}
	if !strings.Contains(uri, "store.go") {
		t.Errorf("uri = %q, want store.go", uri)
	}

	// (*Type).Method pattern: "Messages" should match "(*mockEngine).Messages"
	uri2, _, err := resolveSymbolPosition(context.Background(), reg, "Messages", dir, reg.DefaultRoot(), "")
	if err != nil {
		t.Fatalf("expected Messages to resolve, got: %v", err)
	}
	if !strings.Contains(uri2, "mock.go") {
		t.Errorf("uri = %q, want mock.go", uri2)
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

	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo#5", dir, reg.DefaultRoot(), "")
	if err == nil {
		t.Fatal("expected error for occurrence out of range in workspace")
	}
	if !strings.Contains(err.Error(), "occurrence") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveInWorkspace_ExtFilterScopesToMatchingSpec: with a .go symbol the
// only spec queried is the .go one (the .ts server's answer would otherwise
// win as the first match).
func TestResolveInWorkspace_ExtFilterScopesToMatchingSpec(t *testing.T) {
	goURI := "file:///w/go/a.go"
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: goURI, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 4}}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	uri, pos, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, dir, ".go")
	if err != nil {
		t.Fatalf("resolveSymbolPosition: %v", err)
	}
	if uri != goURI || pos.Line != 2 || pos.Character != 4 {
		t.Errorf("resolved = %s %+v", uri, pos)
	}
}

// TestResolveInWorkspace_ServerErrorSkipped: a server erroring on the query
// is skipped; a later server's match resolves (loop/break would give up).
func TestResolveInWorkspace_ServerErrorSkipped(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/symbol" {
			return []lsp.SymbolInformation{{
				Name: "foo", Kind: lsp.SymbolFunction,
				Location: lsp.Location{URI: "file:///w/a.go", Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}}},
			}}, true
		}
		return nil, false
	})
	uri, _, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, dir, "")
	if err != nil {
		t.Fatalf("resolveSymbolPosition: %v", err)
	}
	if uri != "file:///w/a.go" {
		t.Errorf("uri = %q", uri)
	}
}

// TestResolveInWorkspace_NoMatchNamesRoot: the error must name the root that
// was actually queried.
func TestResolveInWorkspace_NoMatchNamesRoot(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, dir, "")
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("error = %v, want mention of queried root %s", err, dir)
	}
	if strings.Contains(err.Error(), "occurrence") {
		t.Errorf("error should be the not-found form: %v", err)
	}
}

func TestResolveInWorkspace_OccurrenceBounds(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: "file:///w/a.go", Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	_, _, err := resolveSymbolPosition(context.Background(), reg, "foo#2", dir, dir, "")
	if err == nil || err.Error() != "symbol \"foo\" occurrence 2 not found (found 1)" {
		t.Fatalf("error = %v", err)
	}
}

// TestResolveInWorkspace_ForFileErrorSkipped: a spec whose client cannot be
// obtained (evicted → spawn fails) is skipped; later specs resolve.
func TestResolveInWorkspace_ForFileErrorSkipped(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler { return nil })
	defer cleanup()
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/symbol" {
			return []lsp.SymbolInformation{{
				Name: "foo", Kind: lsp.SymbolFunction,
				Location: lsp.Location{URI: "file:///w/a.go", Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 0}}},
			}}, true
		}
		return nil, false
	})
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict: no live client")
	}
	uri, _, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, dir, "")
	if err != nil {
		t.Fatalf("resolveSymbolPosition: %v", err)
	}
	if uri != "file:///w/a.go" {
		t.Errorf("uri = %q", uri)
	}
}

// TestResolveInWorkspace_ExtFilterSkipsForeignSpecs: with a .go ext the ts
// server (first in the spec list) must not be consulted at all — its answer
// would otherwise win as the first match.
func TestResolveInWorkspace_ExtFilterSkipsForeignSpecs(t *testing.T) {
	dir := t.TempDir()
	tsHit := make(chan string, 2)
	goURI := "file:///w/go/a.go"
	tsURI := "file:///w/ts/a.ts"
	specs := []lsp.ServerSpec{
		{Name: "tsrv", Language: "Ts", FileExts: []string{".ts"}, Command: "fake"},
		{Name: "gols", Language: "Go", FileExts: []string{".go"}, Command: "fake"},
	}
	reg, cleanup := manualFakeEnv(t, dir, specs, map[string]fakeHandler{
		"tsrv": func(method string, params json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				tsHit <- string(params)
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: tsURI, Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}}},
				}}, true
			}
			return nil, false
		},
		"gols": func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: goURI, Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 4}}},
				}}, true
			}
			return nil, false
		},
	})
	defer cleanup()
	uri, pos, err := resolveSymbolPosition(context.Background(), reg, "foo", dir, dir, ".go")
	if err != nil {
		t.Fatalf("resolveSymbolPosition: %v", err)
	}
	if uri != goURI || pos.Line != 2 || pos.Character != 4 {
		t.Errorf("resolved = %s %+v, want the gols match", uri, pos)
	}
	assertNoNot(t, tsHit, "tsrv workspace/symbol")
}

// TestResolveInWorkspace_NotFoundNamesLaunchRoot: without a claimed ext the
// error names the LAUNCH root, not the caller's root parameter.
func TestResolveInWorkspace_NotFoundNamesLaunchRoot(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg, cleanup := newTwoRootFakeEnv(t, mainDir, wtDir, "")
	defer cleanup()
	_, _, err := resolveSymbolPosition(context.Background(), reg, "missing", mainDir, wtDir, ".md")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), mainDir) {
		t.Errorf("error must name the launch root %s: %v", mainDir, err)
	}
	if strings.Contains(err.Error(), wtDir) {
		t.Errorf("error must not name the worktree root %s: %v", wtDir, err)
	}
}
