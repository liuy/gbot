package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func TestDefinitionExact(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/definition" {
				return []any{
					refLoc(lsp.FileToURI(filepath.Join(d, "a.go")), 2, 5),
					refLoc(lsp.FileToURI(filepath.Join(d, "vendor", "v.go")), 0, 0),
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := definition(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4}, dir)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	want := "Found 1 definition(s):\n\n" +
		"  a.go:3:6\n" +
		"       2: func a() {}\n" +
		"       3: func b() {}\n" +
		"       4: func c() {}\n\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("definition output =\n%q\nwant\n%q", got, want)
	}
}

func TestDefinition_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/definition" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := definition(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err == nil || !strings.HasPrefix(err.Error(), "definition:") {
		t.Fatalf("error = %v, want prefix definition:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not unwrap to context.Canceled: %v", err)
	}
}

func TestDefinition_Empty(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/definition" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := definition(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No definition found" {
		t.Errorf("output = %q", got)
	}
}

func TestTypeDefinition_ExactAndParams(t *testing.T) {
	var paramsCh []json.RawMessage
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/typeDefinition" {
				paramsCh = append(paramsCh, params)
				return []any{
					refLoc(lsp.FileToURI(filepath.Join(d, "a.go")), 2, 5),
					refLoc(lsp.FileToURI(filepath.Join(d, "vendor", "v.go")), 0, 0),
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	pos := lsp.Position{Line: 1, Character: 6}
	res, err := typeDefinition(context.Background(), c, uri, pos, dir)
	if err != nil {
		t.Fatalf("typeDefinition: %v", err)
	}
	want := "Found 1 type definition(s):\n\n" +
		"  a.go:3:6\n" +
		"       2: func a() {}\n" +
		"       3: func b() {}\n" +
		"       4: func c() {}\n\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("typeDefinition output =\n%q\nwant\n%q", got, want)
	}
	if len(paramsCh) != 1 {
		t.Fatalf("typeDefinition requests = %d, want 1", len(paramsCh))
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position lsp.Position `json:"position"`
	}
	if err := json.Unmarshal(paramsCh[0], &p); err != nil {
		t.Fatal(err)
	}
	if p.TextDocument.URI != uri || p.Position != pos {
		t.Errorf("params = %+v, want uri=%s pos=%+v", p, uri, pos)
	}
}

func TestTypeDefinition_DecodeError(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/typeDefinition" {
				return map[string]any{"nope": 1}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	_, err := typeDefinition(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err == nil || !strings.HasPrefix(err.Error(), "decodeLocations: unrecognized object:") {
		t.Fatalf("error = %v, want decodeLocations unrecognized", err)
	}
}

func TestTypeDefinition_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/typeDefinition" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := typeDefinition(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err == nil || !strings.HasPrefix(err.Error(), "type definition:") {
		t.Fatalf("error = %v, want prefix type definition:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}

func TestTypeDefinition_Empty(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/typeDefinition" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := typeDefinition(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No type definition found" {
		t.Errorf("output = %q", got)
	}
}

func TestImplementation_ExactAndEmpty(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/implementation" {
				return []any{
					refLoc(lsp.FileToURI(filepath.Join(d, "a.go")), 2, 5),
					refLoc(lsp.FileToURI(filepath.Join(d, "vendor", "v.go")), 0, 0),
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := implementation(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err != nil {
		t.Fatalf("implementation: %v", err)
	}
	want := "Found 1 implementation(s):\n\n" +
		"  a.go:3:6\n" +
		"       2: func a() {}\n" +
		"       3: func b() {}\n" +
		"       4: func c() {}\n\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("implementation output =\n%q\nwant\n%q", got, want)
	}
}

func TestImplementation_EmptyAndCancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/implementation" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := implementation(context.Background(), c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No implementation found" {
		t.Errorf("output = %q", got)
	}
}

func TestImplementation_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/implementation" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := implementation(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir)
	if err == nil || !strings.HasPrefix(err.Error(), "implementation:") {
		t.Fatalf("error = %v, want prefix implementation:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}
