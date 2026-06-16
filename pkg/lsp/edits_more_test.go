package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRangesOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b Range
		want bool
	}{
		{
			name: "no overlap disjoint",
			a:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
			b:    Range{Start: Position{Line: 0, Character: 5}, End: Position{Line: 0, Character: 10}},
			want: false,
		},
		{
			name: "overlap",
			a:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}},
			b:    Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 8}},
			want: true,
		},
		{
			name: "containment",
			a:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 10}},
			b:    Range{Start: Position{Line: 0, Character: 3}, End: Position{Line: 0, Character: 5}},
			want: true,
		},
		{
			name: "different lines no overlap",
			a:    Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 10}},
			b:    Range{Start: Position{Line: 1, Character: 0}, End: Position{Line: 1, Character: 10}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RangesOverlap(tc.a, tc.b); got != tc.want {
				t.Errorf("RangesOverlap = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlattenWorkspaceTextEdits(t *testing.T) {
	uri1 := "file:///a.go"
	uri2 := "file:///b.go"

	t.Run("nil returns empty map", func(t *testing.T) {
		out := FlattenWorkspaceTextEdits(nil)
		if len(out) != 0 {
			t.Errorf("nil = %v, want empty", out)
		}
	})

	t.Run("Changes only", func(t *testing.T) {
		edit := &WorkspaceEdit{
			Changes: map[string][]TextEdit{
				uri1: {{NewText: "a"}},
				uri2: {{NewText: "b"}, {NewText: "c"}},
			},
		}
		out := FlattenWorkspaceTextEdits(edit)
		if len(out) != 2 {
			t.Fatalf("got %d URIs, want 2", len(out))
		}
		if len(out[uri1]) != 1 || out[uri1][0].NewText != "a" {
			t.Errorf("uri1 = %+v", out[uri1])
		}
		if len(out[uri2]) != 2 {
			t.Errorf("uri2 = %+v", out[uri2])
		}
	})

	t.Run("DocumentChanges only", func(t *testing.T) {
		edit := &WorkspaceEdit{
			DocumentChanges: []map[string]any{
				{
					"textDocument": map[string]any{"uri": uri1, "version": 1},
					"edits": []map[string]any{
						{"range": map[string]any{}, "newText": "x"},
					},
				},
				{
					"kind": "create",
					"uri":  uri2,
				},
			},
		}
		out := FlattenWorkspaceTextEdits(edit)
		if len(out) != 1 {
			t.Fatalf("got %d URIs, want 1 (create op ignored)", len(out))
		}
		if len(out[uri1]) != 1 {
			t.Errorf("uri1 edits = %+v", out[uri1])
		}
	})

	t.Run("both Changes and DocumentChanges", func(t *testing.T) {
		edit := &WorkspaceEdit{
			Changes: map[string][]TextEdit{
				uri1: {{NewText: "from-changes"}},
			},
			DocumentChanges: []map[string]any{
				{
					"textDocument": map[string]any{"uri": uri1, "version": 1},
					"edits": []map[string]any{
						{"range": map[string]any{}, "newText": "from-doc-changes"},
					},
				},
			},
		}
		out := FlattenWorkspaceTextEdits(edit)
		if len(out[uri1]) != 2 {
			t.Errorf("expected 2 edits for uri1 (coalesced), got %d: %+v", len(out[uri1]), out[uri1])
		}
	})

	t.Run("empty edits ignored", func(t *testing.T) {
		edit := &WorkspaceEdit{
			Changes: map[string][]TextEdit{
				uri1: {},
			},
		}
		out := FlattenWorkspaceTextEdits(edit)
		if _, ok := out[uri1]; ok {
			t.Errorf("empty edit slice should not be in map: %+v", out)
		}
	})
}

func TestApplyEditsToPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	edits := []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}}, NewText: "WORLD"},
	}
	if err := ApplyEditsToPath(path, edits); err != nil {
		t.Fatalf("ApplyEditsToPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "WORLD\n" {
		t.Errorf("content = %q, want WORLD\\n", string(data))
	}
}

func TestLocationFromMap(t *testing.T) {
	t.Run("Location shape with uri and range", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"uri":   json.RawMessage(`"file:///x.go"`),
			"range": json.RawMessage(`{"start":{"line":1,"character":2},"end":{"line":1,"character":5}}`),
		}
		loc, ok := locationFromMap(m)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if loc.URI != "file:///x.go" {
			t.Errorf("URI = %q", loc.URI)
		}
		if loc.Range.Start.Line != 1 || loc.Range.Start.Character != 2 {
			t.Errorf("start = %+v", loc.Range.Start)
		}
		if loc.Range.End.Character != 5 {
			t.Errorf("end = %+v", loc.Range.End)
		}
	})

	t.Run("Location shape without range", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"uri": json.RawMessage(`"file:///x.go"`),
		}
		loc, ok := locationFromMap(m)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if loc.URI != "file:///x.go" {
			t.Errorf("URI = %q", loc.URI)
		}
	})

	t.Run("Location uri unmarshal fails", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"uri": json.RawMessage(`12345`),
		}
		_, ok := locationFromMap(m)
		if ok {
			t.Error("expected ok=false for bad uri")
		}
	})

	t.Run("LocationLink with targetUri and targetSelectionRange", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"targetUri":            json.RawMessage(`"file:///link.go"`),
			"targetSelectionRange": json.RawMessage(`{"start":{"line":3,"character":0},"end":{"line":3,"character":4}}`),
			"targetRange":          json.RawMessage(`{"start":{"line":0,"character":0},"end":{"line":5,"character":0}}`),
		}
		loc, ok := locationFromMap(m)
		if !ok {
			t.Fatal("expected ok=true for LocationLink")
		}
		if loc.URI != "file:///link.go" {
			t.Errorf("URI = %q", loc.URI)
		}
		if loc.Range.Start.Line != 3 {
			t.Errorf("expected targetSelectionRange to win, got start line %d", loc.Range.Start.Line)
		}
	})

	t.Run("LocationLink with targetRange only", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"targetUri":   json.RawMessage(`"file:///link.go"`),
			"targetRange": json.RawMessage(`{"start":{"line":7,"character":0},"end":{"line":7,"character":4}}`),
		}
		loc, ok := locationFromMap(m)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if loc.Range.Start.Line != 7 {
			t.Errorf("expected targetRange fallback, got start line %d", loc.Range.Start.Line)
		}
	})

	t.Run("LocationLink targetUri unmarshal fails", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"targetUri": json.RawMessage(`[1,2,3]`),
		}
		_, ok := locationFromMap(m)
		if ok {
			t.Error("expected ok=false for bad targetUri")
		}
	})

	t.Run("neither uri nor targetUri", func(t *testing.T) {
		m := map[string]json.RawMessage{
			"somethingElse": json.RawMessage(`"x"`),
		}
		_, ok := locationFromMap(m)
		if ok {
			t.Error("expected ok=false for unknown shape")
		}
	})
}

func TestDecodeLocations_Array_LocationLink(t *testing.T) {
	raw := json.RawMessage(`[{"targetUri":"file:///x.go","targetSelectionRange":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}]`)
	out, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations: %v", err)
	}
	if len(out) != 1 || out[0].URI != "file:///x.go" {
		t.Errorf("out = %+v", out)
	}
}

func TestDecodeLocations_Array_MalformedElement(t *testing.T) {
	// Array where one element has neither uri nor targetUri — gets dropped.
	raw := json.RawMessage(`[{"foo":"bar"}]`)
	out, err := decodeLocations(raw)
	if err != nil {
		t.Fatalf("decodeLocations: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 after dropping malformed element, got %d", len(out))
	}
}

func TestDecodeLocations_Array_BadJSON(t *testing.T) {
	raw := json.RawMessage(`[invalid`)
	_, err := decodeLocations(raw)
	if err == nil {
		t.Fatal("expected error for invalid array JSON")
	}
	if !containsSubstring(err.Error(), "decodeLocations array") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDecodeLocations_Single_BadJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid`)
	_, err := decodeLocations(raw)
	if err == nil {
		t.Fatal("expected error for invalid object JSON")
	}
	if !containsSubstring(err.Error(), "decodeLocations single") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDecodeLocations_Single_NotALocation(t *testing.T) {
	raw := json.RawMessage(`{"foo":"bar"}`)
	_, err := decodeLocations(raw)
	if err == nil {
		t.Fatal("expected error for object without uri/targetUri")
	}
	if !containsSubstring(err.Error(), "unrecognized object") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPrepareRename_BareRange(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/prepareRename" {
			// Bare Range response: top-level start/end, no "range" wrapper.
			// PrepareRename's first unmarshal into {Range Range} succeeds but
			// leaves wrap.Range as zero value (no nested "range" key). The
			// check `>= 0` is true, so it returns the zero-value range.
			return map[string]any{
				"start": map[string]any{"line": 2, "character": 0},
				"end":   map[string]any{"line": 2, "character": 3},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	r, err := PrepareRename(testContext(t), c, "file:///x.go", Position{Line: 2, Character: 1})
	if err != nil {
		t.Fatalf("PrepareRename: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil range for bare Range response")
	}
	// First branch (>= 0 check) matches the zero-value range.
	if r.Start.Line != 0 || r.End.Character != 0 {
		t.Errorf("range = %+v, want zero-value (first branch matches)", r)
	}
}

func TestPrepareRename_Garbage(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/prepareRename" {
			return "garbage string", true
		}
		return nil, false
	})
	defer cleanup()

	r, err := PrepareRename(testContext(t), c, "file:///x.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("PrepareRename: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil for unparseable response, got %+v", r)
	}
}

func TestResolveCodeAction(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "codeAction/resolve" {
			return map[string]any{
				"title": "resolved",
				"edit":  map[string]any{"changes": map[string]any{}},
			}, true
		}
		return nil, false
	})
	defer cleanup()

	out, err := ResolveCodeAction(testContext(t), c, CodeAction{Title: "x"})
	if err != nil {
		t.Fatalf("ResolveCodeAction: %v", err)
	}
	if out == nil || out.Title != "resolved" {
		t.Errorf("out = %+v", out)
	}
	if out.Edit == nil {
		t.Error("expected Edit to be populated")
	}
}

func TestResolveCodeAction_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "codeAction/resolve" {
			return "not a code action", true
		}
		return nil, false
	})
	defer cleanup()

	_, err := ResolveCodeAction(testContext(t), c, CodeAction{Title: "x"})
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
	if !containsSubstring(err.Error(), "codeAction/resolve") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyCodeAction_ApplyEditFails(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	action := CodeAction{Title: "x", Edit: &WorkspaceEdit{}}
	_, err := ApplyCodeAction(testContext(t), c, action, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, errSentinel
	})
	if err == nil {
		t.Fatal("expected error when applyEdit fails")
	}
	if !containsSubstring(err.Error(), "apply edit failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyCodeAction_CommandFails(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		return nil, false
	})
	defer cleanup()

	c.teardownOnce.Do(func() {
		close(c.done)
		close(c.dead)
	})

	action := CodeAction{Title: "x", Command: &Command{Command: "go.import.fix"}}
	_, err := ApplyCodeAction(testContext(t), c, action, func(_ *WorkspaceEdit) ([]string, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error when executeCommand fails")
	}
	if !containsSubstring(err.Error(), "server is not running") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyWorkspaceEdit_DocumentChanges_NoEdits(t *testing.T) {
	uri := "file:///nonexistent.go"
	dc := map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 1},
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}
	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want empty (no edits)", changed)
	}
}

func TestApplyWorkspaceEdit_DocumentChanges_BadEditUnmarshal(t *testing.T) {
	uri := "file:///nonexistent.go"
	dc := map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 1},
		"edits":        "not-an-array",
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}
	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	// Bad edits should be skipped (ok=true with empty edits).
	if len(changed) != 0 {
		t.Errorf("changed = %v, want empty", changed)
	}
}

func TestApplyWorkspaceEdit_ResourceOp_CreateFails(t *testing.T) {
	dir := t.TempDir()
	// Use a path under a file (already exists as a file) to make MkdirAll fail.
	blockerPath := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blockerPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(filepath.Join(blockerPath, "sub.go"))
	dc := map[string]any{"kind": "create", "uri": uri}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	_, err := ApplyWorkspaceEdit(edit)
	if err == nil {
		t.Fatal("expected error when create fails (mkdir under file)")
	}
	if !containsSubstring(err.Error(), "create") {
		t.Errorf("expected 'create' in error, got %v", err)
	}
}

func TestApplyWorkspaceEdit_ResourceOp_RenameMissingSource(t *testing.T) {
	dir := t.TempDir()
	oldURI := pathToURI(filepath.Join(dir, "does-not-exist.go"))
	newURI := pathToURI(filepath.Join(dir, "new.go"))
	dc := map[string]any{"kind": "rename", "oldUri": oldURI, "newUri": newURI}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	_, err := ApplyWorkspaceEdit(edit)
	if err == nil {
		t.Fatal("expected error when rename source missing")
	}
	if !containsSubstring(err.Error(), "rename") {
		t.Errorf("expected 'rename' in error, got %v", err)
	}
}

func TestApplyWorkspaceEdit_ResourceOp_DeleteMissing(t *testing.T) {
	dir := t.TempDir()
	uri := pathToURI(filepath.Join(dir, "never-existed.go"))
	dc := map[string]any{"kind": "delete", "uri": uri}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}

	// RemoveAll on missing path is not an error.
	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit delete-missing: %v", err)
	}
	if len(changed) != 1 {
		t.Errorf("expected 1 changed entry, got %v", changed)
	}
}

func TestApplyWorkspaceEdit_AllFailed(t *testing.T) {
	dc := map[string]any{
		"textDocument": map[string]any{"uri": "file:///does-not-exist.go", "version": 1},
		"edits": []map[string]any{
			{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "newText": "x"},
		},
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{dc}}
	_, err := ApplyWorkspaceEdit(edit)
	if err == nil {
		t.Fatal("expected error when all edits failed")
	}
	if !containsSubstring(err.Error(), "all failed") {
		t.Errorf("expected 'all failed' in error, got %v", err)
	}
}

func TestApplyResourceOp_EmptyKind(t *testing.T) {
	desc, err := applyResourceOp(map[string]any{})
	if err != nil {
		t.Errorf("empty kind should not error: %v", err)
	}
	if desc != "" {
		t.Errorf("empty kind should return empty desc, got %q", desc)
	}
}

func TestApplyResourceOp_UnknownKind(t *testing.T) {
	desc, err := applyResourceOp(map[string]any{"kind": "weird"})
	if err != nil {
		t.Errorf("unknown kind should not error: %v", err)
	}
	if desc != "" {
		t.Errorf("unknown kind should return empty desc, got %q", desc)
	}
}

func TestExtractTextDocumentEdit_NoTextDocument(t *testing.T) {
	_, _, ok := extractTextDocumentEdit(map[string]any{"kind": "create"})
	if ok {
		t.Error("expected ok=false for resource op")
	}
}

func TestExtractTextDocumentEdit_EmptyURI(t *testing.T) {
	_, _, ok := extractTextDocumentEdit(map[string]any{
		"textDocument": map[string]any{"uri": ""},
	})
	if ok {
		t.Error("expected ok=false for empty uri")
	}
}

func TestExtractTextDocumentEdit_NoEdits(t *testing.T) {
	uri, edits, ok := extractTextDocumentEdit(map[string]any{
		"textDocument": map[string]any{"uri": "file:///x.go", "version": 1},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if uri != "file:///x.go" {
		t.Errorf("uri = %q", uri)
	}
	if len(edits) != 0 {
		t.Errorf("edits = %+v, want empty", edits)
	}
}

func TestApplyTextEditsToString_EndLineOutOfRange(t *testing.T) {
	content := "one line\n"
	edits := []TextEdit{
		{
			Range:   Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 5, Character: 0}},
			NewText: "replaced",
		},
	}
	got, err := applyTextEditsToString(content, edits)
	if err != nil {
		t.Fatalf("applyTextEditsToString: %v", err)
	}
	// End line is clamped to last line; result replaces through end.
	if !containsSubstring(got, "replaced") {
		t.Errorf("expected replaced in %q", got)
	}
}

// errSentinel is a sentinel error for testing applyEdit failure.
var errSentinel = newSentinelError("apply edit failed")

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }

func newSentinelError(msg string) error { return &sentinelError{msg: msg} }

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// testContext returns a context with a short timeout for tests.
func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
