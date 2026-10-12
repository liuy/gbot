package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// ---- callhierarchy.go: symbolKindStr ----------------------------------

// The kind table is rendered directly to the model; a mutated key or value
// silently mislabels every symbol in a callers/callees listing.
func TestSymbolKindStr_Table(t *testing.T) {
	want := map[int]string{
		1: "File", 2: "Module", 3: "Namespace", 4: "Package", 5: "Class",
		6: "Method", 7: "Property", 8: "Field", 9: "Constructor", 10: "Enum",
		11: "Interface", 12: "Function", 13: "Variable", 14: "Constant",
		15: "String", 16: "Number", 17: "Boolean", 18: "Array", 19: "Object",
		20: "Key", 21: "Null", 22: "EnumMember", 23: "Struct", 24: "Event",
		25: "Operator", 26: "TypeParameter",
		0:  "Kind(0)",
		27: "Kind(27)",
		99: "Kind(99)",
	}
	for kind, wantName := range want {
		if got := symbolKindStr(kind); got != wantName {
			t.Errorf("symbolKindStr(%d) = %q, want %q", kind, got, wantName)
		}
	}
}

// ---- lsp.go: the 10MB didOpen guard ------------------------------------

func TestFileOp_DidOpenSizeGuard(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr string // "" means the guard must not fire
	}{
		{"exactly at the limit", 10 * 1024 * 1024, ""},
		{"just over", 10_490_000, "file too large for LSP analysis (10.0MB, max 10MB)"},
		{"over, rounds to 10.0", 10_537_000, "file too large for LSP analysis (10.0MB, max 10MB)"},
		{"over, rounds to 10.2", 10_643_252, "file too large for LSP analysis (10.2MB, max 10MB)"},
	}
	for _, tc := range tests {
		reg, dir, cleanup := newFakeEnv(t, func(_ string) fakeHandler {
			return func(method string, _ json.RawMessage) (any, bool) {
				if method == "textDocument/documentSymbol" {
					return nil, true
				}
				return nil, false
			}
		})
		defer cleanup()

		path := filepath.Join(dir, "big.go")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", tc.size)), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		result, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "symbols", File: path,
		}), &tool.ToolUseContext{WorkingDir: dir})

		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: Call: %v", tc.name, err)
				continue
			}
			if got := fmt.Sprintf("%v", result.Data); got != "No symbols found" {
				t.Errorf("%s: data = %q, want %q", tc.name, got, "No symbols found")
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected %q, got data %q", tc.name, tc.wantErr, result.Data)
			continue
		}
		if err.Error() != tc.wantErr {
			t.Errorf("%s: error = %q, want %q", tc.name, err.Error(), tc.wantErr)
		}
	}
}

// ---- symbols.go: empty documentSymbol responses ------------------------

func TestSymbols_EmptyResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
	}{
		{"null", nil},
		{"empty array", []any{}},
	} {
		reg, dir, cleanup := newFakeEnv(t, func(_ string) fakeHandler {
			return func(method string, _ json.RawMessage) (any, bool) {
				if method == "textDocument/documentSymbol" {
					return tc.result, true
				}
				return nil, false
			}
		})
		defer cleanup()
		path := filepath.Join(dir, "foo.go")
		if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		result, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "symbols", File: path,
		}), &tool.ToolUseContext{WorkingDir: dir})
		if err != nil {
			t.Errorf("%s: Call: %v", tc.name, err)
			continue
		}
		if got := fmt.Sprintf("%v", result.Data); got != "No symbols found" {
			t.Errorf("%s: data = %q, want %q", tc.name, got, "No symbols found")
		}
	}
}

// ---- references.go: project-aware retry gate ---------------------------

func TestReferences_ProjectAwareRetry(t *testing.T) {
	uri := func(dir string) string { return lsp.FileToURI(filepath.Join(dir, "foo.go")) }

	tests := []struct {
		name     string
		locs     func(dir string) []map[string]any
		wantCall int
		wantData string
	}{
		{
			name:     "no references retries to the limit",
			locs:     func(string) []map[string]any { return nil },
			wantCall: referencesRetryCount + 1,
			wantData: "No references found",
		},
		{
			name: "one real reference stops the retry",
			locs: func(d string) []map[string]any {
				return []map[string]any{{
					"uri": uri(d),
					"range": map[string]any{
						"start": map[string]any{"line": 40, "character": 7},
						"end":   map[string]any{"line": 40, "character": 10},
					},
				}}
			},
			wantCall: 1,
			wantData: "Found 1 reference(s):\n\n  foo.go:41:8\n\n",
		},
	}
	for _, tc := range tests {
		var mu sync.Mutex
		calls := 0
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, _ json.RawMessage) (any, bool) {
				if method == "textDocument/references" {
					mu.Lock()
					calls++
					mu.Unlock()
					return tc.locs(d), true
				}
				return nil, false
			}
		})
		defer cleanup()
		path := filepath.Join(dir, "foo.go")
		if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		result, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "references", File: path, Symbol: "foo",
		}), &tool.ToolUseContext{WorkingDir: dir})
		if err != nil {
			t.Errorf("%s: Call: %v", tc.name, err)
			continue
		}
		mu.Lock()
		gotCalls := calls
		mu.Unlock()
		if gotCalls != tc.wantCall {
			t.Errorf("%s: references requested %d times, want %d", tc.name, gotCalls, tc.wantCall)
		}
		got := fmt.Sprintf("%v", result.Data)
		if got != tc.wantData {
			t.Errorf("%s: data = %q, want %q", tc.name, got, tc.wantData)
		}
	}
}

// ---- code_actions.go: Only filter, index bounds, result sections ------

func TestCodeActions_OnlyFilterPerMode(t *testing.T) {
	tests := []struct {
		name     string
		apply    *bool
		query    string
		wantOnly string // "" means the request must carry no "only"
	}{
		{"list with kind filter", nil, "quickfix", `"only":["quickfix"]`},
		{"list without filter", nil, "", ""},
		{"apply by index carries no filter", new(true), "0", ""},
		{"apply without query carries no filter", new(true), "", ""},
	}
	for _, tc := range tests {
		var mu sync.Mutex
		var saw string
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				switch method {
				case "textDocument/codeAction":
					mu.Lock()
					saw = string(params)
					mu.Unlock()
					return []map[string]any{
						{"title": "First", "kind": "quickfix"},
						{"title": "Second", "kind": "quickfix"},
					}, true
				case "codeAction/resolve":
					return map[string]any{
						"title": "First",
						"kind":  "quickfix",
						"edit": map[string]any{
							"changes": map[string]any{
								lsp.FileToURI(filepath.Join(d, "foo.go")): []map[string]any{{
									"range": map[string]any{
										"start": map[string]any{"line": 0, "character": 0},
										"end":   map[string]any{"line": 0, "character": 7},
									},
									"newText": "package x",
								}},
							},
						},
					}, true
				}
				return nil, false
			}
		})
		defer cleanup()
		path := filepath.Join(dir, "foo.go")
		if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		if _, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "code_actions", File: path, Symbol: "foo", Apply: tc.apply, Query: tc.query,
		}), &tool.ToolUseContext{WorkingDir: dir}); err != nil {
			t.Errorf("%s: Call: %v", tc.name, err)
			continue
		}
		mu.Lock()
		params := saw
		mu.Unlock()
		if tc.wantOnly == "" {
			if strings.Contains(params, `"only"`) {
				t.Errorf("%s: request carries an only filter: %s", tc.name, params)
			}
			continue
		}
		if !strings.Contains(params, tc.wantOnly) {
			t.Errorf("%s: request %s does not carry %s", tc.name, params, tc.wantOnly)
		}
	}
}

func TestCodeActions_ApplyIndexBounds(t *testing.T) {
	actions := []map[string]any{
		{"title": "First", "kind": "quickfix"},
		{"title": "Second", "kind": "quickfix"},
		{"title": "Third", "kind": "quickfix"},
	}
	tests := []struct {
		query string
		want  func(dir string) string
	}{
		{"0", func(d string) string {
			return "Applied \"First\":\n  Workspace edit:\n    " + filepath.Join(d, "foo.go") + "\n"
		}},
		{"2", func(d string) string {
			return "Applied \"Third\":\n  Workspace edit:\n    " + filepath.Join(d, "foo.go") + "\n"
		}},
		{"3", func(string) string {
			return "No code action matches \"3\". Available actions:\n  0: [quickfix] First\n  1: [quickfix] Second\n  2: [quickfix] Third\n"
		}},
	}
	for _, tc := range tests {
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				switch method {
				case "textDocument/codeAction":
					return actions, true
				case "codeAction/resolve":
					var action map[string]any
					_ = json.Unmarshal(params, &action)
					title, _ := action["title"].(string)
					return map[string]any{
						"title": title,
						"kind":  "quickfix",
						"edit": map[string]any{
							"changes": map[string]any{
								lsp.FileToURI(filepath.Join(d, "foo.go")): []map[string]any{{
									"range": map[string]any{
										"start": map[string]any{"line": 0, "character": 0},
										"end":   map[string]any{"line": 0, "character": 7},
									},
									"newText": "package x",
								}},
							},
						},
					}, true
				}
				return nil, false
			}
		})
		defer cleanup()
		path := filepath.Join(dir, "foo.go")
		if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		result, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "code_actions", File: path, Symbol: "foo", Apply: new(true), Query: tc.query,
		}), &tool.ToolUseContext{WorkingDir: dir})
		if err != nil {
			t.Errorf("query %q: Call: %v", tc.query, err)
			continue
		}
		got := fmt.Sprintf("%v", result.Data)
		if got != tc.want(dir) {
			t.Errorf("query %q: data = %q, want %q", tc.query, got, tc.want(dir))
		}
		if strings.Contains(got, "Executed command(s):") {
			t.Errorf("query %q: data has an executed-command section without commands: %q", tc.query, got)
		}
	}
}

func TestCodeActions_ExecutedCommandSection(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(_ string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return []map[string]any{{
					"title":   "Run fixer",
					"kind":    "source.fixAll",
					"command": map[string]any{"command": "gbot.fix", "arguments": []any{}},
				}}, true
			case "codeAction/resolve":
				return nil, true
			}
			return nil, false
		}
	})
	defer cleanup()
	path := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: path, Symbol: "foo", Apply: new(true), Query: "0",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	want := "Applied \"Run fixer\":\n  Executed command(s):\n    gbot.fix\n"
	if got := fmt.Sprintf("%v", result.Data); got != want {
		t.Errorf("data = %q, want %q", got, want)
	}
}

// ---- composite.go: source body clamping and header --------------------

func TestSourceAction_ClampsRangeAndNumbersLines(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return []map[string]any{{
					"name": "foo", "kind": 12, "detail": "(string)",
					"range": map[string]any{
						"start": map[string]any{"line": 1, "character": 0},
						"end":   map[string]any{"line": 999, "character": 0},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 1, "character": 5},
						"end":   map[string]any{"line": 1, "character": 8},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	path := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(path, []byte("package main\nfunc foo(){}\n// tail\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "source", File: path, Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	want := "func foo — foo.go:2-4\n\nfunc foo(){}\n// tail\n"
	if got := fmt.Sprintf("%v", result.Data); got != want {
		t.Errorf("data = %q, want %q", got, want)
	}
}

// ---- reload.go: extension filter --------------------------------------

func TestReload_ScopesToTheFilesExtension(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	goPath := filepath.Join(dir, "x.go")
	if err := os.WriteFile(goPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := reg.ForFile(context.Background(), goPath)
	if err != nil {
		t.Fatalf("ForFile: %v", err)
	}
	reg.InjectClientInRoot("fakepy", dir, lsp.ServerSpec{
		Name:     "fakepy",
		Language: "Python",
		FileExts: []string{".py"},
		Command:  "fake-py",
	}, c)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "reload", File: goPath,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := fmt.Sprintf("%v", result.Data); got != "Reloaded fakels" {
		t.Errorf("reload output = %q, want only the .go server reloaded", got)
	}
}

// ---- rename_file.go: preview of server-provided edits -----------------

func TestRenameFile_PreviewEdits(t *testing.T) {
	tests := []struct {
		name   string
		result any
		want   string
	}{
		{
			name:   "null response applies nothing",
			result: nil,
			want:   "Rename preview: foo.go → bar.go\n  No LSP edits would be applied\n",
		},
		{
			name:   "empty edit applies nothing",
			result: map[string]any{"changes": map[string]any{}, "documentChanges": []any{}},
			want:   "Rename preview: foo.go → bar.go\n  No LSP edits would be applied\n",
		},
		{
			name: "documentChanges entry with no edits contributes nothing",
			result: map[string]any{
				"documentChanges": []any{map[string]any{
					"textDocument": map[string]any{"uri": ""},
					"edits":        []any{},
				}},
			},
			want: "Rename preview: foo.go → bar.go\n",
		},
	}
	for _, tc := range tests {
		reg, dir, cleanup := newFakeEnv(t, func(_ string) fakeHandler {
			return func(method string, _ json.RawMessage) (any, bool) {
				if method == "workspace/willRenameFiles" {
					return tc.result, true
				}
				return nil, false
			}
		})
		defer cleanup()
		src := filepath.Join(dir, "foo.go")
		if err := os.WriteFile(src, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		tt := New(reg)
		result, err := tt.Call(context.Background(), mustInput(t, Input{
			Action: "rename_file", File: src, NewName: filepath.Join(dir, "bar.go"), Apply: new(false),
		}), &tool.ToolUseContext{WorkingDir: dir})
		if err != nil {
			t.Errorf("%s: Call: %v", tc.name, err)
			continue
		}
		got := fmt.Sprintf("%v", result.Data)
		if got != tc.want {
			t.Errorf("%s: data = %q, want %q", tc.name, got, tc.want)
		}
	}
}
