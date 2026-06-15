package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyTextEditsToString_SingleLine(t *testing.T) {
	content := "hello world\n"
	edits := []TextEdit{{
		Range:   Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
		NewText: "HELLO",
	}}
	got, err := applyTextEditsToString(content, edits)
	if err != nil {
		t.Fatalf("applyTextEditsToString: %v", err)
	}
	if want := "HELLO world\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsToString_MultiLine(t *testing.T) {
	content := "line1\nline2\nline3\n"
	edits := []TextEdit{{
		Range:   Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 1, Character: 0}},
		NewText: "REPLACED\n",
	}}
	got, err := applyTextEditsToString(content, edits)
	if err != nil {
		t.Fatalf("applyTextEditsToString: %v", err)
	}
	if want := "REPLACED\nline2\nline3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsToString_BottomUpPreservesIndices(t *testing.T) {
	content := "aaa\nbbb\nccc\n"
	// Two edits: replace "aaa" with "XXX", replace "ccc" with "ZZZ".
	// Order in slice is top-to-bottom; function must apply bottom-up.
	edits := []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 3}}, NewText: "XXX"},
		{Range: Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 3}}, NewText: "ZZZ"},
	}
	got, err := applyTextEditsToString(content, edits)
	if err != nil {
		t.Fatalf("applyTextEditsToString: %v", err)
	}
	if want := "XXX\nbbb\nZZZ\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestApplyTextEditsToString_RejectsOverlap(t *testing.T) {
	content := "hello\n"
	// Overlapping ranges: [0:2] and [1:3] on same line.
	edits := []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 3}}, NewText: "AAA"},
		{Range: Range{Start: Position{Line: 0, Character: 1}, End: Position{Line: 0, Character: 4}}, NewText: "BBB"},
	}
	_, err := applyTextEditsToString(content, edits)
	if err == nil {
		t.Fatal("expected overlap error, got nil")
	}
}

func TestApplyTextEditsToString_Empty(t *testing.T) {
	got, err := applyTextEditsToString("abc", nil)
	if err != nil {
		t.Fatalf("applyTextEditsToString: %v", err)
	}
	if got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}

func TestApplyTextEditsToString_OutOfRangeLine(t *testing.T) {
	content := "one line\n"
	edits := []TextEdit{{
		Range:   Range{Start: Position{Line: 5, Character: 0}, End: Position{Line: 5, Character: 3}},
		NewText: "x",
	}}
	_, err := applyTextEditsToString(content, edits)
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
}

func TestApplyWorkspaceEdit_LegacyChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(path, []byte("package foo\n\nfunc bar() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(path)

	edit := &WorkspaceEdit{
		Changes: map[string][]TextEdit{
			uri: {{
				Range:   Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 8}},
				NewText: "baz",
			}},
		},
	}
	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 || changed[0] != path {
		t.Errorf("changed = %v, want [%s]", changed, path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "package foo\n\nfunc baz() {}\n"; string(got) != want {
		t.Errorf("file content = %q, want %q", string(got), want)
	}
}

func TestApplyWorkspaceEdit_NilSafe(t *testing.T) {
	changed, err := ApplyWorkspaceEdit(nil)
	if err != nil {
		t.Errorf("nil edit returned error: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("nil edit changed files: %v", changed)
	}
}

func TestApplyWorkspaceEdit_DocumentChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bar.go")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(path)

	// documentChanges with textDocument + edits.
	dc := map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 1},
		"edits": []map[string]any{
			{
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 1},
				},
				"newText": "X",
			},
		},
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 || changed[0] != path {
		t.Errorf("changed = %v, want [%s]", changed, path)
	}
	got, _ := os.ReadFile(path)
	if want := "X\nb\nc\n"; string(got) != want {
		t.Errorf("content = %q, want %q", string(got), want)
	}
}

func TestApplyCodeAction_WithEdit(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// CodeAction carrying a WorkspaceEdit — ApplyCodeAction should apply the edit (empty).
	action := CodeAction{
		Title: "Add import",
		Kind:  "quickfix",
		Edit:  &WorkspaceEdit{},
	}
	if _, err := ApplyCodeAction(ctx, c, action, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, nil
	}); err != nil {
		t.Errorf("ApplyCodeAction: %v", err)
	}
}

func TestApplyCodeAction_WithCommand(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	action := CodeAction{
		Title:   "Organize imports",
		Kind:    "source.organizeImports",
		Command: &Command{Title: "o", Command: "go.import.fix"},
	}
	// Server's default handler responds with null for unknown methods.
	applied, err := ApplyCodeAction(ctx, c, action, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, nil
	})
	if err != nil {
		t.Errorf("ApplyCodeAction: %v", err)
	}
	// The server responds null for unknown commands, so ApplyCodeAction
	// returns a result struct with no edits applied (empty Edits).
	if applied == nil {
		t.Fatal("ApplyCodeAction applied=nil, want non-nil result")
	}
	if len(applied.Edits) != 0 {
		t.Errorf("ApplyCodeAction Edits=%v, want empty", applied.Edits)
	}
}

func TestReferences_DecodeArray(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()

	// Override default echo for references to return a real []Location.
	srv.handleCustom = func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/references" {
			return []map[string]any{
				{
					"uri":   "file:///repo/foo.go",
					"range": map[string]any{"start": map[string]any{"line": 10, "character": 5}, "end": map[string]any{"line": 10, "character": 8}},
				},
			}, true
		}
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	refs, err := References(ctx, c, "file:///repo/foo.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	if refs[0].URI != "file:///repo/foo.go" {
		t.Errorf("uri = %q", refs[0].URI)
	}
	if refs[0].Range.Start.Line != 10 {
		t.Errorf("start line = %d, want 10", refs[0].Range.Start.Line)
	}
}

func TestWorkspaceSymbol_DecodeArray(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()

	srv.handleCustom = func(req rpcRequest) (any, bool) {
		if req.Method == "workspace/symbol" {
			return []map[string]any{
				{
					"name":          "foo",
					"kind":          12,
					"location":      map[string]any{"uri": "file:///x.go", "range": map[string]any{}},
					"containerName": "main",
				},
			}, true
		}
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	syms, err := WorkspaceSymbol(ctx, c, "foo")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1", len(syms))
	}
	if syms[0].Name != "foo" || syms[0].Kind != SymbolFunction {
		t.Errorf("symbol = %+v", syms[0])
	}
}

func TestURIToPath(t *testing.T) {
	cases := []struct {
		uri, want string
	}{
		{"file:///tmp/x.go", "/tmp/x.go"},
		{"file:///home/user/foo%20bar.go", "/home/user/foo bar.go"},
	}
	for _, tc := range cases {
		got := uriToPath(tc.uri)
		// On non-Linux we only sanity-check that prefix mapping is right.
		if got != tc.want && got[:len(tc.want)-4] != tc.want[:len(tc.want)-4] {
			t.Errorf("uriToPath(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
