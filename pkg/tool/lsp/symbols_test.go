package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestSymbols_DocumentSymbolTree(t *testing.T) {
	var paramsCh []json.RawMessage
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				paramsCh = append(paramsCh, params)
				return []map[string]any{{
					"name": "foo", "kind": 12, "detail": "main decl",
					"range":          map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 15}},
					"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
					"children": []map[string]any{{
						"name": "inner", "kind": 6,
						"range":          map[string]any{"start": map[string]any{"line": 2, "character": 1}, "end": map[string]any{"line": 2, "character": 9}},
						"selectionRange": map[string]any{"start": map[string]any{"line": 2, "character": 1}, "end": map[string]any{"line": 2, "character": 6}},
					}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	c := fakeClient(t, reg)
	res, err := symbols(context.Background(), c, uri, filepath.Join(dir, "a.go"), dir)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	want := "Symbols in a.go:\n\n- func `foo` — main decl @ line 2\n  - method `inner` @ line 3\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("symbols tree =\n%q\nwant\n%q", got, want)
	}
	if len(paramsCh) != 1 {
		t.Fatalf("documentSymbol requests = %d, want 1", len(paramsCh))
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(paramsCh[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.TextDocument.URI != uri {
		t.Errorf("params uri = %q, want %q", p.TextDocument.URI, uri)
	}
}

func TestSymbols_FlatSymbolInformation(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return []map[string]any{{
					"name": "foo", "kind": 12, "containerName": "Outer",
					"location": map[string]any{
						"uri":   lsp.FileToURI(filepath.Join(d, "a.go")),
						"range": map[string]any{"start": map[string]any{"line": 3, "character": 0}},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := symbols(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), filepath.Join(dir, "a.go"), dir)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	want := "Symbols in a.go:\n\n  func `foo` (Outer) @ line 4\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("symbols flat =\n%q\nwant\n%q", got, want)
	}
}

func TestSymbols_NullAndEmptyArray(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
		want   string
	}{
		{"null", nil, "No symbols found"},
		{"empty array", []any{}, "No symbols found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					if method == "textDocument/documentSymbol" {
						return tc.result, true
					}
					return nil, false
				}
			})
			defer cleanup()
			c := fakeClient(t, reg)
			res, err := symbols(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), filepath.Join(dir, "a.go"), dir)
			if err != nil {
				t.Fatalf("symbols: %v", err)
			}
			if got := fmt.Sprintf("%v", res.Data); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSymbols_DecodeErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result any
		want   string
	}{
		{"docsymbol decode", json.RawMessage(`[{"name":123,"selectionRange":{"start":{"line":0,"character":0}}}]`), "symbols DocumentSymbol decode:"},
		{"plainformation decode", json.RawMessage(`[{"name":123,"location":{"uri":"file:///a.go"}}]`), "symbols SymbolInformation decode:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					if method == "textDocument/documentSymbol" {
						return tc.result, true
					}
					return nil, false
				}
			})
			defer cleanup()
			c := fakeClient(t, reg)
			_, err := symbols(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), filepath.Join(dir, "a.go"), dir)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error = %v, want prefix %q", err, tc.want)
			}
			// The wrap must preserve the decoder error for errors.As — a %v
			// wrap erases it (mutation kill for the errorf verb).
			if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
				t.Errorf("error does not unwrap to *json.UnmarshalTypeError: %v", err)
			}
		})
	}
}

func TestSymbols_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/documentSymbol" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := symbols(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), filepath.Join(dir, "a.go"), dir)
	if err == nil || !strings.HasPrefix(err.Error(), "symbols:") {
		t.Fatalf("error = %v, want prefix symbols:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}
