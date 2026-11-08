package lsptool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

func wsSym(name, container, uri string, line int) lsp.SymbolInformation {
	return lsp.SymbolInformation{
		Name: name, ContainerName: container, Kind: lsp.SymbolFunction,
		Location: lsp.Location{URI: uri, Range: lsp.Range{Start: lsp.Position{Line: line}}},
	}
}

func TestWorkspaceSymbol_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				b := lsp.FileToURI(filepath.Join(d, "foo.go"))
				return []lsp.SymbolInformation{
					wsSym("fooHelper", "", a, 1),   // name match
					wsSym("bar", "fooMaker", a, 3), // container match
					wsSym("unrelated", "", a, 5),   // dropped
					wsSym("thing", "", b, 7),       // path match
					wsSym("fooHelper", "", a, 1),   // exact duplicate
					wsSym("Foo", "", a, 9),         // case-insensitive match
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	want := "Found 4 symbol(s) matching \"foo\":\n\n" +
		"  func `fooHelper` @ line 2\n" +
		"  func `bar` (fooMaker) @ line 4\n" +
		"  func `thing` @ line 8\n" +
		"  func `Foo` @ line 10\n"
	// The two fooHelper entries dedupe to one; order of the remaining follows
	// the server response order.
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}

func TestWorkspaceSymbol_WhitespaceQueryKeepsAll(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				return []lsp.SymbolInformation{
					wsSym("foo", "", a, 1),
					wsSym("bar", "", a, 3),
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	// A whitespace-only query trims to the empty needle: no filtering.
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "  ",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	want := "Found 2 symbol(s) matching \"  \":\n\n" +
		"  func `foo` @ line 2\n" +
		"  func `bar` @ line 4\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}

func TestWorkspaceSymbol_NoMatch(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{wsSym("foo", "", lsp.FileToURI(filepath.Join(d, "a.go")), 1)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "zzz",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	if got, _ := res.Data.(string); got != "No symbols matching \"zzz\"" {
		t.Errorf("output = %q", got)
	}
}

func TestWorkspaceSymbol_Truncation(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				out := make([]lsp.SymbolInformation, 201)
				for i := range out {
					out[i] = wsSym(fmt.Sprintf("sym%03d", i), "", a, i)
				}
				return out, true
			}
			return nil, false
		}
	})
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "sym",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, "Found 200 symbol(s) matching \"sym\":\n\n") {
		t.Errorf("header = %q", got[:60])
	}
	if !strings.HasSuffix(got, "... 1 more (truncated)\n") {
		t.Errorf("missing truncation footer, tail = %q", got[len(got)-60:])
	}
	if got[strings.LastIndex(got, "`sym199`"):] != "`sym199` @ line 200\n... 1 more (truncated)\n" {
		t.Errorf("tail = %q", got[strings.LastIndex(got, "`sym199`"):])
	}
}

// TestWorkspaceSymbol_FileExtFilter: file= restricts the query to specs
// serving that extension; an extension no spec claims leaves all specs.
func TestWorkspaceSymbol_FileExtFilter(t *testing.T) {
	t.Run("matching ext narrows", func(t *testing.T) {
		tsParams := make(chan string, 2)
		goParams := make(chan string, 2)
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				if method == "workspace/symbol" {
					goParams <- string(params)
					return []lsp.SymbolInformation{}, true
				}
				return nil, false
			}
		})
		defer cleanup()
		clientConn, serverConn := net.Pipe()
		var wg sync.WaitGroup
		wg.Go(func() {
			serveFake(t, serverConn, func(method string, params json.RawMessage) (any, bool) {
				if method == "workspace/symbol" {
					tsParams <- string(params)
					return []lsp.SymbolInformation{}, true
				}
				return nil, false
			}, dir)
		})
		tc := lsp.NewTestClient("tsls", clientConn)
		reg.InjectClientInRoot("tsls", dir, lsp.ServerSpec{
			Name: "tsls", Language: "Ts", FileExts: []string{".ts"}, Command: "fake",
		}, tc)
		t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close(); wg.Wait() })

		res, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "workspace_symbol", Query: "zzz", File: filepath.Join(dir, "x.ts"),
		}), basicCtxWithDir(t, dir))
		if err != nil {
			t.Fatalf("workspace_symbol: %v", err)
		}
		if got, _ := res.Data.(string); got != "No symbols matching \"zzz\"" {
			t.Errorf("output = %q", got)
		}
		recvNot(t, tsParams, "tsls workspace/symbol")
		// The .go spec must be filtered OUT of the query set, not merely
		// out-resolved by the .ts winner.
		assertNoNot(t, goParams, "fakels workspace/symbol")
	})

	t.Run("unclaimed ext queries all", func(t *testing.T) {
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, _ json.RawMessage) (any, bool) {
				if method == "workspace/symbol" {
					return []lsp.SymbolInformation{wsSym("foo", "", lsp.FileToURI(filepath.Join(d, "a.go")), 1)}, true
				}
				return nil, false
			}
		})
		defer cleanup()
		res, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "workspace_symbol", Query: "foo", File: filepath.Join(dir, "x.md"),
		}), basicCtxWithDir(t, dir))
		if err != nil {
			t.Fatalf("workspace_symbol: %v", err)
		}
		if got, _ := res.Data.(string); !strings.HasPrefix(got, "Found 1 symbol(s)") {
			t.Errorf("output = %q", got)
		}
	})
}

// TestWorkspaceSymbol_ServerErrorsSkipped: a spec whose query errors is
// skipped, later specs still contribute.
func TestWorkspaceSymbol_ServerErrorsSkipped(t *testing.T) {
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
			return []lsp.SymbolInformation{wsSym("foo", "", lsp.FileToURI(filepath.Join(dir, "b.go")), 2)}, true
		}
		return nil, false
	})
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	want := "Found 1 symbol(s) matching \"foo\":\n\n  func `foo` @ line 3\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}

func TestWorkspaceSymbol_NoServers(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "foo",
	}), basicCtx())
	if err == nil || err.Error() != "no language server configured" {
		t.Fatalf("error = %v, want 'no language server configured'", err)
	}
}

// TestWorkspaceSymbol_SpawnFailureSkipped: a spec whose client cannot be
// obtained (evicted, spawn fails) is skipped; later specs still contribute.
func TestWorkspaceSymbol_SpawnFailureSkipped(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler { return nil })
	defer cleanup()
	injectSecondFake(t, reg, dir, "fakels2", func(method string, _ json.RawMessage) (any, bool) {
		if method == "workspace/symbol" {
			return []lsp.SymbolInformation{wsSym("foo", "", lsp.FileToURI(filepath.Join(dir, "b.go")), 2)}, true
		}
		return nil, false
	})
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict: no live client")
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	want := "Found 1 symbol(s) matching \"foo\":\n\n  func `foo` @ line 3\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}

// TestWorkspaceSymbol_ExactlyAtLimit: 200 symbols is NOT truncated.
func TestWorkspaceSymbol_ExactlyAtLimit(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				out := make([]lsp.SymbolInformation, 200)
				for i := range out {
					out[i] = wsSym(fmt.Sprintf("sym%03d", i), "", a, i)
				}
				return out, true
			}
			return nil, false
		}
	})
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "workspace_symbol", Query: "sym",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("workspace_symbol: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, "Found 200 symbol(s) matching \"sym\":\n\n") {
		t.Errorf("header = %q", got[:50])
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("200 exactly at the limit must not truncate:\n%s", got[len(got)-80:])
	}
}
