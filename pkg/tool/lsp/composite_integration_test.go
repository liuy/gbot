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
	"github.com/liuy/gbot/pkg/tool"
)

func TestIntegration_Callers(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"uri":  "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
				}}, true
			case "callHierarchy/incomingCalls":
				return []map[string]any{{
					"from": map[string]any{
						"name": "bar",
						"kind": 12,
						"uri":  "file://" + filepath.Join(d, "bar.go"),
						"range": map[string]any{
							"start": map[string]any{"line": 4, "character": 0},
							"end":   map[string]any{"line": 4, "character": 10},
						},
					},
					"fromRanges": []map[string]any{{
						"start": map[string]any{"line": 5, "character": 2},
						"end":   map[string]any{"line": 5, "character": 5},
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package main\nfunc bar() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callers", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 1 caller") || !strings.Contains(got, "bar") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Callees(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"uri":  "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
				}}, true
			case "callHierarchy/outgoingCalls":
				return []map[string]any{{
					"to": map[string]any{
						"name": "baz",
						"kind": 12,
						"uri":  "file://" + filepath.Join(d, "baz.go"),
						"range": map[string]any{
							"start": map[string]any{"line": 9, "character": 0},
							"end":   map[string]any{"line": 9, "character": 10},
						},
					},
					"fromRanges": []map[string]any{{
						"start": map[string]any{"line": 1, "character": 2},
						"end":   map[string]any{"line": 1, "character": 5},
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "baz.go"), []byte("package main\nfunc baz() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callees", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Found 1 callee") || !strings.Contains(got, "baz") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Callers_Empty(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"uri":  "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 5},
						"end":   map[string]any{"line": 0, "character": 8},
					},
				}}, true
			case "callHierarchy/incomingCalls":
				return []map[string]any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callers", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No callers found") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Source(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 0},
						"end":   map[string]any{"line": 4, "character": 1},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 8},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	content := "package main\n\nfunc foo() int {\n\treturn 42\n}\n"
	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte(content), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "source", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "foo") || !strings.Contains(got, "return 42") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Inspect(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/hover":
				return map[string]any{
					"contents": map[string]any{
						"kind":  "markdown",
						"value": "func foo() int",
					},
				}, true
			case "textDocument/definition":
				return []map[string]any{{
					"uri": "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 8},
					},
				}}, true
			case "textDocument/prepareCallHierarchy":
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"uri":  "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 8},
					},
				}}, true
			case "callHierarchy/incomingCalls":
				return []map[string]any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n\nfunc foo() int {\n\treturn 42\n}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "inspect", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "Type Info") || !strings.Contains(got, "Definition") || !strings.Contains(got, "Callers") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Impact(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/references":
				return []map[string]any{{
					"uri": "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 8},
					},
				}}, true
			case "textDocument/prepareCallHierarchy":
				return []map[string]any{{
					"name": "foo",
					"kind": 12,
					"uri":  "file://" + filepath.Join(d, "foo.go"),
					"range": map[string]any{
						"start": map[string]any{"line": 2, "character": 5},
						"end":   map[string]any{"line": 2, "character": 8},
					},
				}}, true
			case "callHierarchy/incomingCalls":
				return []map[string]any{}, true
			case "callHierarchy/outgoingCalls":
				return []map[string]any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\n\nfunc foo() int {\n\treturn 42\n}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "impact", Symbol: "foo", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "References") || !strings.Contains(got, "Callers") || !strings.Contains(got, "Callees") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Source_NotFound(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	_, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "source", Symbol: "nonexistent", File: filepath.Join(dir, "foo.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil {
		t.Fatal("expected error for nonexistent symbol")
	}
}

func TestResolveInWorkspace_NoServers(t *testing.T) {
	reg := lsp.NewRegistry("/test")
	_, _, err := resolveInWorkspace(context.Background(), reg, "foo", 1, "/test")
	if err == nil {
		t.Fatal("expected error with no servers")
	}
	if !strings.Contains(err.Error(), "no language server") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFilterGitIgnored_Empty(t *testing.T) {
	got := filterGitIgnored(context.Background(), nil, "/tmp")
	if got != nil {
		t.Errorf("filterGitIgnored(nil) = %v, want nil", got)
	}
}

// --- composite "all sub-errors" integration tests ---

func TestIntegration_Inspect_AllSubErrors(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "inspect", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "## Definition") || !strings.Contains(got, "Error") {
		t.Errorf("expected error sections in inspect output: %q", got)
	}
}

func TestIntegration_Impact_AllSubErrors(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "impact", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "## References") || !strings.Contains(got, "No references found") {
		t.Errorf("expected References section in impact output: %q", got)
	}
}
