package lsp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// helper to get an initialized in-process client with a custom handler.
func initClient(t *testing.T, handleCustom func(req rpcRequest) (any, bool)) (*Client, *inProcessServer, func()) {
	t.Helper()
	c, srv, cleanup := newInProcessServer(t)
	srv.handleCustom = handleCustom
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		cleanup()
		t.Fatalf("Initialize: %v", err)
	}
	return c, srv, cleanup
}

func TestDefinition_DecodeArray(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/definition" {
			return []map[string]any{
				{"uri": "file:///repo/foo.go", "range": map[string]any{
					"start": map[string]any{"line": 1, "character": 2},
					"end":   map[string]any{"line": 1, "character": 5},
				}},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	locs, err := Definition(ctx, c, "file:///repo/foo.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 || locs[0].URI != "file:///repo/foo.go" {
		t.Errorf("locs = %+v", locs)
	}
}

func TestDefinition_DecodeSingle(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/definition" {
			return map[string]any{
				"uri": "file:///repo/single.go", "range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 1},
				},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	locs, err := Definition(ctx, c, "file:///repo/single.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 || locs[0].URI != "file:///repo/single.go" {
		t.Errorf("locs = %+v", locs)
	}
}

func TestDefinition_DecodeNull(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/definition" {
			return nil, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	locs, err := Definition(ctx, c, "file:///repo/foo.go", Position{})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if locs != nil {
		t.Errorf("expected nil, got %+v", locs)
	}
}

func TestImplementation_DecodeArray(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/implementation" {
			return []map[string]any{
				{"uri": "file:///repo/impl1.go", "range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 1},
				}},
				{"uri": "file:///repo/impl2.go", "range": map[string]any{
					"start": map[string]any{"line": 2, "character": 0},
					"end":   map[string]any{"line": 2, "character": 1},
				}},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	locs, err := Implementation(ctx, c, "file:///repo/iface.go", Position{})
	if err != nil {
		t.Fatalf("Implementation: %v", err)
	}
	if len(locs) != 2 {
		t.Errorf("got %d impls, want 2", len(locs))
	}
}

func TestDocumentSymbols_DecodeArray(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/documentSymbol" {
			return []map[string]any{
				{
					"name": "Foo", "kind": 23, "detail": "struct",
					"range":          map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
				},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	syms, err := DocumentSymbols(ctx, c, "file:///repo/foo.go")
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "Foo" || syms[0].Kind != SymbolStruct {
		t.Errorf("syms = %+v", syms)
	}
}

func TestHoverAt_ReturnsHover(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/hover" {
			return map[string]any{
				"contents": map[string]any{"kind": "markdown", "value": "func foo()"},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := HoverAt(ctx, c, "file:///repo/foo.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("HoverAt: %v", err)
	}
	if h == nil {
		t.Fatal("expected non-nil hover")
	}
}

func TestHoverAt_ReturnsNull(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/hover" {
			return nil, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := HoverAt(ctx, c, "file:///repo/foo.go", Position{})
	if err != nil {
		t.Fatalf("HoverAt: %v", err)
	}
	if h != nil {
		t.Errorf("expected nil hover, got %+v", h)
	}
}

func TestPrepareRename_WrappedRange(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/prepareRename" {
			return map[string]any{
				"range": map[string]any{
					"start": map[string]any{"line": 1, "character": 0},
					"end":   map[string]any{"line": 1, "character": 5},
				},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := PrepareRename(ctx, c, "file:///repo/foo.go", Position{Line: 1, Character: 2})
	if err != nil {
		t.Fatalf("PrepareRename: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil range")
	}
	if r.Start.Line != 1 || r.End.Character != 5 {
		t.Errorf("range = %+v", r)
	}
}

func TestPrepareRename_Null(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/prepareRename" {
			return nil, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, err := PrepareRename(ctx, c, "file:///repo/foo.go", Position{})
	if err != nil {
		t.Fatalf("PrepareRename: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil range, got %+v", r)
	}
}

func TestRename_ReturnsEdit(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/rename" {
			return map[string]any{
				"changes": map[string]any{
					"file:///repo/foo.go": []map[string]any{
						{
							"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}},
							"newText": "newName",
						},
					},
				},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	edit, err := Rename(ctx, c, "file:///repo/foo.go", Position{Line: 0, Character: 0}, "newName")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if edit == nil || len(edit.Changes) == 0 {
		t.Fatalf("expected non-empty edit, got %+v", edit)
	}
}

func TestRename_ReturnsNull(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/rename" {
			return nil, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	edit, err := Rename(ctx, c, "file:///repo/foo.go", Position{}, "x")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if edit != nil {
		t.Errorf("expected nil edit, got %+v", edit)
	}
}

func TestCodeActions_DecodeArray(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/codeAction" {
			return []map[string]any{
				{"title": "Add import", "kind": "quickfix"},
				{"title": "Organize imports", "kind": "source.organizeImports"},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	actions, err := CodeActions(ctx, c, "file:///repo/foo.go", Range{
		Start: Position{Line: 0, Character: 0},
		End:   Position{Line: 0, Character: 10},
	}, CodeActionContext{TriggerKind: 1})
	if err != nil {
		t.Fatalf("CodeActions: %v", err)
	}
	if len(actions) != 2 || actions[0].Title != "Add import" {
		t.Errorf("actions = %+v", actions)
	}
}

func TestWorkspaceSymbol_SingleFallback(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "workspace/symbol" {
			// Return single object instead of array — decoder should fall back.
			return map[string]any{
				"name": "Foo", "kind": 12,
				"location": map[string]any{
					"uri":   "file:///x.go",
					"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
				},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	syms, err := WorkspaceSymbol(ctx, c, "Foo")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "Foo" {
		t.Errorf("syms = %+v", syms)
	}
}

func TestApplyCodeAction_NilEdit_NilCommand(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	action := CodeAction{Title: "no-op", Kind: "quickfix"}
	applied, err := ApplyCodeAction(ctx, c, action, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, nil
	})
	if err != nil {
		t.Errorf("ApplyCodeAction empty: %v", err)
	}
	if applied != nil {
		t.Errorf("expected nil applied for empty action, got %+v", applied)
	}
}

func TestDecodeLocations_BadInput(t *testing.T) {
	_, err := decodeLocations(json.RawMessage(`"not a location"`))
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
	_ = err.Error()
}

func TestSliceStr_Bounds(t *testing.T) {
	if got := sliceStr("ab", -1, 5); got != "ab" {
		t.Errorf("sliceStr clamped = %q", got)
	}
	if got := sliceStr("ab", 5, 10); got != "" {
		t.Errorf("sliceStr invalid range = %q, want empty", got)
	}
}

func TestComparePos(t *testing.T) {
	a := Position{Line: 1, Character: 5}
	b := Position{Line: 1, Character: 10}
	if comparePos(a, b) >= 0 {
		t.Errorf("a should be < b")
	}
	if comparePos(b, a) <= 0 {
		t.Errorf("b should be > a")
	}
	c := Position{Line: 2, Character: 0}
	if comparePos(a, c) >= 0 {
		t.Errorf("a should be < c (line check)")
	}
}

func TestSplitKeepNL_Trailing(t *testing.T) {
	// No trailing newline on last segment.
	got := splitKeepNL("ab\ncd")
	if len(got) != 2 || got[0] != "ab\n" || got[1] != "cd" {
		t.Errorf("splitKeepNL = %#v", got)
	}
}
