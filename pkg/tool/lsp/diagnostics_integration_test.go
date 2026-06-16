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

var _ = json.RawMessage(nil)

func TestIntegration_Check_WithDiagnostics(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	fooPath := filepath.Join(dir, "foo.go")
	_ = os.WriteFile(fooPath, []byte("package main\nfunc foo() {}\n"), 0644)

	// Open the file then inject diagnostics
	uri := lsp.FileToURI(fooPath)
	c, _ := reg.ForFile(context.Background(), fooPath)
	_ = c.EnsureFileOpen(context.Background(), uri, "go", "package main\nfunc foo() {}\n")
	c.InjectDiagnostics(uri, []lsp.Diagnostic{
		{Severity: 1, Message: "missing return", Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 4}, End: lsp.Position{Line: 1, Character: 7}}, Source: "gopls"},
		{Severity: 2, Message: "unused variable", Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 0, Character: 7}}, Source: "gopls"},
	})

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "check", File: fooPath,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "2 diagnostic") || !strings.Contains(got, "ERROR") || !strings.Contains(got, "missing return") || !strings.Contains(got, "WARN") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Check_SymbolLevel_WithDiagnostics(t *testing.T) {
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

	fooPath := filepath.Join(dir, "foo.go")
	_ = os.WriteFile(fooPath, []byte("package main\n\nfunc foo() int {\n\treturn 1\n}\n"), 0644)

	uri := lsp.FileToURI(fooPath)
	c, _ := reg.ForFile(context.Background(), fooPath)
	_ = c.EnsureFileOpen(context.Background(), uri, "go", "package main\n\nfunc foo() int {\n\treturn 1\n}\n")
	c.InjectDiagnostics(uri, []lsp.Diagnostic{
		{Severity: 1, Message: "type mismatch", Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 1}, End: lsp.Position{Line: 3, Character: 7}}, Source: "gopls"},
	})

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "check", Symbol: "foo", File: fooPath,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "1 diagnostic") || !strings.Contains(got, "type mismatch") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Check_ProjectLevel_WithDiagnostics(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	fooPath := filepath.Join(dir, "foo.go")
	barPath := filepath.Join(dir, "bar.go")
	_ = os.WriteFile(fooPath, []byte("package main\nfunc foo() {}\n"), 0644)
	_ = os.WriteFile(barPath, []byte("package main\nfunc bar() {}\n"), 0644)

	fooURI := lsp.FileToURI(fooPath)
	barURI := lsp.FileToURI(barPath)
	c, _ := reg.ForFile(context.Background(), fooPath)
	_ = c.EnsureFileOpen(context.Background(), fooURI, "go", "package main\nfunc foo() {}\n")
	c.InjectDiagnostics(fooURI, []lsp.Diagnostic{
		{Severity: 1, Message: "error in foo", Range: lsp.Range{Start: lsp.Position{Line: 0}}},
	})

	c2, _ := reg.ForFile(context.Background(), barPath)
	_ = c2.EnsureFileOpen(context.Background(), barURI, "go", "package main\nfunc bar() {}\n")
	c2.InjectDiagnostics(barURI, []lsp.Diagnostic{
		{Severity: 2, Message: "warning in bar", Range: lsp.Range{Start: lsp.Position{Line: 0}}},
	})

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "check",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "2 diagnostic") || !strings.Contains(got, "2 file") {
		t.Errorf("got %q", got)
	}
}

func TestIntegration_Check_SymbolLevel_NoDiags(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()

	fooPath := filepath.Join(dir, "foo.go")
	_ = os.WriteFile(fooPath, []byte("package main\nfunc foo() {}\n"), 0644)

	uri := lsp.FileToURI(fooPath)
	c, _ := reg.ForFile(context.Background(), fooPath)
	_ = c.EnsureFileOpen(context.Background(), uri, "go", "package main\nfunc foo() {}\n")

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "check", Symbol: "foo", File: fooPath,
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No diagnostics") {
		t.Errorf("got %q", got)
	}
}
