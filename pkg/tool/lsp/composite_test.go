package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/liuy/gbot/pkg/lsp"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPositionInRange(t *testing.T) {
	r := lsp.Range{
		Start: lsp.Position{Line: 5, Character: 3},
		End:   lsp.Position{Line: 8, Character: 10},
	}
	tests := []struct {
		name string
		pos  lsp.Position
		want bool
	}{
		{"inside", lsp.Position{Line: 6, Character: 0}, true},
		{"before start line", lsp.Position{Line: 4, Character: 0}, false},
		{"after end line", lsp.Position{Line: 9, Character: 0}, false},
		{"start line before char", lsp.Position{Line: 5, Character: 2}, false},
		{"start line at char", lsp.Position{Line: 5, Character: 3}, true},
		{"end line after char", lsp.Position{Line: 8, Character: 11}, false},
		{"end line at char", lsp.Position{Line: 8, Character: 10}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := positionInRange(tt.pos, r); got != tt.want {
				t.Errorf("positionInRange() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindInnermostSymbol(t *testing.T) {
	syms := []lsp.DocumentSymbol{
		{
			Name: "outer",
			Range: lsp.Range{
				Start: lsp.Position{Line: 0},
				End:   lsp.Position{Line: 20},
			},
			SelectionRange: lsp.Range{
				Start: lsp.Position{Line: 0, Character: 5},
				End:   lsp.Position{Line: 0, Character: 10},
			},
			Children: []lsp.DocumentSymbol{
				{
					Name: "inner",
					Range: lsp.Range{
						Start: lsp.Position{Line: 5},
						End:   lsp.Position{Line: 10},
					},
					SelectionRange: lsp.Range{
						Start: lsp.Position{Line: 5, Character: 5},
						End:   lsp.Position{Line: 5, Character: 10},
					},
				},
			},
		},
	}

	// Position inside inner child
	inner := findInnermostSymbol(syms, lsp.Position{Line: 7})
	if inner == nil || inner.Name != "inner" {
		t.Errorf("findInnermostSymbol(line 7) = %v, want inner", inner)
	}

	// Position inside outer but not inner
	outer := findInnermostSymbol(syms, lsp.Position{Line: 15})
	if outer == nil || outer.Name != "outer" {
		t.Errorf("findInnermostSymbol(line 15) = %v, want outer", outer)
	}

	// Position outside everything
	none := findInnermostSymbol(syms, lsp.Position{Line: 30})
	if none != nil {
		t.Errorf("findInnermostSymbol(line 30) = %v, want nil", none)
	}
}

func TestFindInnermostSymbol_Empty(t *testing.T) {
	got := findInnermostSymbol(nil, lsp.Position{Line: 0})
	if got != nil {
		t.Errorf("findInnermostSymbol(nil) = %v, want nil", got)
	}
}

func TestParseSymbolOccurrence(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantOcc  int
	}{
		{"foo", "foo", 1},
		{"foo#2", "foo", 2},
		{"foo#10", "foo", 10},
		{"bar.baz#3", "bar.baz", 3},
		{"foo#0", "foo#0", 1}, // #0 invalid, stays as name
		{"foo#", "foo#", 1},   // trailing # no number
		{"#foo", "#foo", 1},   // leading #
	}
	for _, tt := range tests {
		name, occ := parseSymbolOccurrence(tt.input)
		if name != tt.wantName || occ != tt.wantOcc {
			t.Errorf("parseSymbolOccurrence(%q) = (%q, %d), want (%q, %d)",
				tt.input, name, occ, tt.wantName, tt.wantOcc)
		}
	}
}

// ---------------------------------------------------------------------------
// Mutation-kill tests: source/inspect/impact through the real dispatch path.
// ---------------------------------------------------------------------------

// srcFile writes the shared fixture: outer func foo with inner method bar.
func srcFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "a.go")
	content := "" +
		"package main\n" + // line 0
		"func foo() {\n" + // line 1
		"	bar() {\n" + // line 2 (inner)
		"	}\n" + // line 3
		"}\n" + // line 4
		"func other() {}\n" // line 5
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func docSymsFor(d string, inner bool) []map[string]any {
	name, line := "foo", 1
	if inner {
		name, line = "bar", 2
	}
	return []map[string]any{{
		"name": name, "kind": 12,
		"range":          map[string]any{"start": map[string]any{"line": line, "character": 0}, "end": map[string]any{"line": 4, "character": 1}},
		"selectionRange": map[string]any{"start": map[string]any{"line": line, "character": 5}, "end": map[string]any{"line": line, "character": 8}},
		"children": []map[string]any{{
			"name": "bar", "kind": 6,
			"range":          map[string]any{"start": map[string]any{"line": 2, "character": 1}, "end": map[string]any{"line": 3, "character": 2}},
			"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 1}, "end": map[string]any{"line": 2, "character": 4}},
		}},
	}}
}

// TestSourceAction_ExactOutput resolves foo (position inside outer), finds the
// innermost symbol, and prints its source slice.
func TestSourceAction_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				return docSymsFor(d, false), true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	want := "func foo — a.go:2-5\n\nfunc foo() {\n\tbar() {\n\t}\n}"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("source output =\n%q\nwant\n%q", got, want)
	}
}

func TestSourceAction_InnerSymbol(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				// Resolve INSIDE the child range so the innermost symbol wins.
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 2, Character: 1}}},
				}}, true
			case "textDocument/documentSymbol":
				return docSymsFor(d, false), true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	want := "method bar — a.go:3-4\n\n\tbar() {\n\t}"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("inner source =\n%q\nwant\n%q", got, want)
	}
}

func TestSourceAction_NoSymbolAtPosition(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 5, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				// Symbol range does not cover the resolved position.
				return []map[string]any{{
					"name": "foo", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 11}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 11}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.HasPrefix(err.Error(), "no symbol found at resolved position in ") {
		t.Fatalf("error = %v", err)
	}
}

func TestSourceAction_DocumentSymbolError(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.HasPrefix(err.Error(), "documentSymbol: ") {
		t.Fatalf("error = %v, want prefix documentSymbol: ", err)
	}
}

// TestSourceAction_FileVanishes: the file disappears between open and read.
func TestSourceAction_FileVanishes(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				_ = os.Remove(filepath.Join(d, "a.go"))
				return docSymsFor(d, false), true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.HasPrefix(err.Error(), "read file: ") {
		t.Fatalf("error = %v, want prefix read file: ", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error does not unwrap to *fs.PathError: %v", err)
	}
}

// TestSourceAction_RangeBeyondEnd: server ranges past the file clamp.
func TestSourceAction_RangeBeyondEnd(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 99, Character: 0}}},
				}}, true
			case "textDocument/documentSymbol":
				return []map[string]any{{
					"name": "foo", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 99, "character": 0}, "end": map[string]any{"line": 120, "character": 0}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 99, "character": 0}, "end": map[string]any{"line": 99, "character": 3}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if got, _ := res.Data.(string); got != "Symbol range beyond file end" {
		t.Errorf("output = %q", got)
	}
}

func TestSourceAction_EndClamped(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				return []map[string]any{{
					"name": "foo", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 99, "character": 0}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	// End line clamps to the last file line (5).
	want := "func foo — a.go:2-7\n\nfunc foo() {\n\tbar() {\n\t}\n}\nfunc other() {}\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("clamped output =\n%q\nwant\n%q", got, want)
	}
}

// compositeHandler answers hover/definition/references/incoming/outgoing for
// the inspect/impact actions, with per-method error toggles.
func compositeHandler(d string, fail map[string]bool) fakeHandler {
	return func(method string, _ json.RawMessage) (any, bool) {
		a := lsp.FileToURI(filepath.Join(d, "a.go"))
		switch method {
		case "workspace/symbol":
			return []lsp.SymbolInformation{{
				Name: "foo", Kind: lsp.SymbolFunction,
				Location: lsp.Location{URI: a, Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
			}}, true
		case "textDocument/hover":
			if fail["hover"] {
				return rpcError{"boom"}, true
			}
			return map[string]any{"contents": map[string]any{"value": "func foo()"}}, true
		case "textDocument/definition":
			if fail["definition"] {
				return rpcError{"boom"}, true
			}
			return []any{refLoc(a, 1, 5)}, true
		case "textDocument/references":
			if fail["references"] {
				return rpcError{"boom"}, true
			}
			return []any{refLoc(a, 1, 5)}, true
		case "textDocument/prepareCallHierarchy":
			return []callHierarchyItem{{Name: "foo", Kind: 12, URI: a, Range: r0(1)}}, true
		case "callHierarchy/incomingCalls":
			if fail["incoming"] {
				return rpcError{"boom"}, true
			}
			return []callerEntry{{From: callHierarchyItem{Name: "callerA", Kind: 12, URI: a, Range: r0(3)}}}, true
		case "callHierarchy/outgoingCalls":
			if fail["outgoing"] {
				return rpcError{"boom"}, true
			}
			return []calleeEntry{{To: callHierarchyItem{Name: "calleeA", Kind: 12, URI: a, Range: r0(4)}}}, true
		}
		return nil, false
	}
}

func compositeCall(t *testing.T, action string, fail map[string]bool) (string, error) {
	t.Helper()
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler { return compositeHandler(d, fail) })
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: action, File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		return "", err
	}
	got, _ := res.Data.(string)
	return got, nil
}

func TestInspectAction_ExactOutput(t *testing.T) {
	got, err := compositeCall(t, "inspect", nil)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	head := "Inspect: foo\n\n" +
		"## Type Info\nfunc foo()\n\n" +
		"## Definition\nFound 1 definition(s):\n\n" +
		"  a.go:2:6\n" +
		"       1: package main\n" +
		"       2: func foo() {\n" +
		"       3: \tbar() {\n\n"
	// Definition section ends with a blank line, Callers without one.
	if !strings.HasPrefix(got, head) {
		t.Errorf("inspect head =\n%q\nwant prefix\n%q", got[:200], head)
	}
	if !strings.HasSuffix(got, "## Callers\nFound 1 caller(s):\n\na.go:\n  callerA (Function) - Line 4\n\n") {
		t.Errorf("inspect tail = %q", got[len(got)-90:])
	}
}

func TestInspectAction_ErrorSections(t *testing.T) {
	got, err := compositeCall(t, "inspect", map[string]bool{"hover": true, "definition": true, "incoming": true})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !strings.Contains(got, "## Type Info\nError: hover: lsp error -32000: boom\n\n") {
		t.Errorf("hover error section missing:\n%s", got[:120])
	}
	if !strings.Contains(got, "## Definition\nError: definition: lsp error -32000: boom\n\n") {
		t.Errorf("definition error section missing:\n%s", got)
	}
	if !strings.Contains(got, "## Callers\nError: callHierarchy/incomingCalls: lsp error -32000: boom\n") {
		t.Errorf("callers error section missing:\n%s", got)
	}
}

func TestImpactAction_ExactOutput(t *testing.T) {
	got, err := compositeCall(t, "impact", nil)
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if !strings.HasPrefix(got, "Impact: foo\n\n## References\nFound 1 reference(s):\n\n") {
		t.Errorf("impact head =\n%q", got[:100])
	}
	if !strings.Contains(got, "## Callers\nFound 1 caller(s):") {
		t.Errorf("impact callers missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "## Callees\nFound 1 callee(s):\n\na.go:\n  calleeA (Function) - Line 5\n\n") {
		t.Errorf("impact tail = %q", got[len(got)-90:])
	}
}

func TestImpactAction_ErrorSections(t *testing.T) {
	got, err := compositeCall(t, "impact", map[string]bool{"references": true, "incoming": true, "outgoing": true})
	if err != nil {
		t.Fatalf("impact: %v", err)
	}
	if !strings.Contains(got, "## References\nError: references: lsp error -32000: boom\n\n") {
		t.Errorf("references error section missing:\n%s", got)
	}
	if !strings.Contains(got, "## Callers\nError: callHierarchy/incomingCalls: lsp error -32000: boom\n\n") {
		t.Errorf("callers error section missing:\n%s", got)
	}
	if !strings.Contains(got, "## Callees\nError: callHierarchy/outgoingCalls: lsp error -32000: boom\n") {
		t.Errorf("callees error section missing:\n%s", got)
	}
}

// TestInspectImpact_ResolveError: an unresolvable symbol fails the whole call.
func TestInspectImpact_ResolveError(t *testing.T) {
	for _, action := range []string{"inspect", "impact", "source"} {
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return nil
		})
		defer cleanup()
		srcFile(t, dir)
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: action, File: filepath.Join(dir, "a.go"), Symbol: "missing",
		}), basicCtxWithDir(t, dir))
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%s: error = %v, want symbol-not-found", action, err)
		}
	}
}

// TestSourceAction_SkipNonCoveringSymbol: a leading symbol whose range does
// not cover the position is skipped (continue), not fatal (break).
func TestSourceAction_SkipNonCoveringSymbol(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				return []map[string]any{
					{"name": "unrelated", "kind": 13,
						"range":          map[string]any{"start": map[string]any{"line": 5, "character": 0}, "end": map[string]any{"line": 5, "character": 12}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 5, "character": 0}, "end": map[string]any{"line": 5, "character": 9}}},
					{"name": "foo", "kind": 12,
						"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 4, "character": 1}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if got, _ := res.Data.(string); !strings.HasPrefix(got, "func foo — a.go:2-5") {
		t.Errorf("output = %q, want the covering symbol", got[:min(len(got), 60)])
	}
}

// TestSourceAction_StartExactlyAtFileEnd pins the >= boundary: a range
// starting at len(lines) is beyond the file.
func TestSourceAction_StartExactlyAtFileEnd(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 7, Character: 0}}},
				}}, true
			case "textDocument/documentSymbol":
				return []map[string]any{{
					"name": "foo", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 7, "character": 0}, "end": map[string]any{"line": 7, "character": 3}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 7, "character": 0}, "end": map[string]any{"line": 7, "character": 3}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if got, _ := res.Data.(string); got != "Symbol range beyond file end" {
		t.Errorf("output = %q", got)
	}
}

// TestSourceAction_EndExactlyAtFileLength: end == len(lines) still clamps.
func TestSourceAction_EndExactlyAtFileLength(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "workspace/symbol":
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			case "textDocument/documentSymbol":
				return []map[string]any{{
					"name": "foo", "kind": 12,
					"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 7, "character": 0}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	srcFile(t, dir)
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "source", File: filepath.Join(dir, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	want := "func foo — a.go:2-7\n\nfunc foo() {\n\tbar() {\n\t}\n}\nfunc other() {}\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}
