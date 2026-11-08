package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
)

// refLoc builds a Location JSON shape for fake references responses.
func refLoc(uri string, line, ch int) map[string]any {
	return map[string]any{
		"uri": uri,
		"range": map[string]any{
			"start": map[string]any{"line": line, "character": ch},
			"end":   map[string]any{"line": line, "character": ch + 3},
		},
	}
}

// TestReferences_ExactOutput pins header, count, context formatting, and the
// plain tail beyond the context limit, with gitignored locations dropped.
func TestReferences_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				v := lsp.FileToURI(filepath.Join(d, "vendor", "v.go"))
				return []any{refLoc(a, 2, 5), refLoc(v, 0, 0)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\nfunc c() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	want := "Found 1 reference(s):\n\n" +
		"  a.go:3:6\n" +
		"       2: func a() {}\n" +
		"       3: func b() {}\n" +
		"       4: func c() {}\n\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("references output =\n%q\nwant\n%q", got, want)
	}
}

func TestReferences_PlainTailBeyondLimit(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				out := make([]any, 52)
				for i := range out {
					out[i] = refLoc(a, i%3, 0)
				}
				return out, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels", IsLinter: true})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	if !strings.HasPrefix(got, "Found 52 reference(s):\n\n") {
		t.Errorf("header = %q", got[:40])
	}
	if !strings.Contains(got, "... 2 additional reference(s) shown without context\n") {
		t.Errorf("missing plain-tail notice:\n%q", got[len(got)-200:])
	}
	tail := got[strings.LastIndex(got, "... 2 additional"):]
	if tail != "... 2 additional reference(s) shown without context\n  a.go:3:1\n  a.go:1:1\n" {
		t.Errorf("tail = %q", tail)
	}
}

// TestReferences_ProjectAwareRetry: the first response is only the queried
// declaration, so a project-aware server retries and sees the real refs.
func TestReferences_ProjectAwareRetryRealRefs(t *testing.T) {
	calls := 0
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				calls++
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				if calls == 1 {
					return []any{refLoc(a, 0, 4)}, true
				}
				return []any{refLoc(a, 1, 0), refLoc(a, 2, 0)}, true
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
	res, err := references(context.Background(), c, uri, lsp.Position{Line: 0, Character: 4}, dir, lsp.ServerSpec{Name: "fakels"})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	if !strings.HasPrefix(got, "Found 2 reference(s):") {
		t.Errorf("retry must surface the 2 real refs, got:\n%s", got)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (declaration then retry)", calls)
	}
}

// TestReferences_LinterNoRetry: a linter spec is not project-aware — the
// declaration-only first answer stands.
func TestReferences_LinterNoRetry(t *testing.T) {
	calls := 0
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				calls++
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				return []any{refLoc(a, 0, 4)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels", IsLinter: true})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	if !strings.HasPrefix(got, "Found 1 reference(s):") {
		t.Errorf("linter must not retry, got:\n%s", got)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestReferences_AllGitignored(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				return []any{refLoc(lsp.FileToURI(filepath.Join(d, "vendor", "v.go")), 0, 0)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels", IsLinter: true})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No references found (all gitignored)" {
		t.Errorf("output = %q", got)
	}
}

func TestReferences_RequestError(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	_, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err == nil || !strings.HasPrefix(err.Error(), "references: ") {
		t.Fatalf("error = %v, want prefix references: ", err)
	}
}

// TestReferences_CancelledDuringWait: the retry wait aborts with a wrapped
// ctx error when the caller's deadline hits mid-wait.
func TestReferences_CancelledDuringWait(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				return []any{refLoc(lsp.FileToURI(filepath.Join(d, "a.go")), 0, 4)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := references(ctx, c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err == nil || !strings.HasPrefix(err.Error(), "references: ") {
		t.Fatalf("error = %v, want prefix references: ", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error does not unwrap to context.DeadlineExceeded: %v", err)
	}
}

// TestFileOp_ReferencesUsesLinterSpec pins that fileOp passes the
// ext-mapped spec into references: with a linter spec claiming .go, the
// declaration-only first answer must stand (no project-aware retry).
func TestFileOp_ReferencesUsesLinterSpec(t *testing.T) {
	reg2, dir2, cleanup2 := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "workspace/symbol" {
				return []lsp.SymbolInformation{{
					Name: "foo", Kind: lsp.SymbolFunction,
					Location: lsp.Location{URI: lsp.FileToURI(filepath.Join(d, "a.go")),
						Range: lsp.Range{Start: lsp.Position{Line: 1, Character: 5}}},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup2()
	if err := os.WriteFile(filepath.Join(dir2, "a.go"), []byte("package main\nfunc foo() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls2 := 0
	injectLinterFake(t, reg2, dir2, func(method string, _ json.RawMessage) (any, bool) {
		if method == "textDocument/references" {
			calls2++
			a := lsp.FileToURI(filepath.Join(dir2, "a.go"))
			// The declaration loc must CONTAIN the queried position (foo at
			// 1:5) so a project-aware spec would retry; the linter must not.
			return []any{refLoc(a, 1, 5)}, true
		}
		return nil, false
	})
	res, err := New(reg2).Call(context.Background(), mustInput(t, Input{
		Action: "references", File: filepath.Join(dir2, "a.go"), Symbol: "foo",
	}), basicCtxWithDir(t, dir2))
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got, _ := res.Data.(string)
	if !strings.HasPrefix(got, "Found 1 reference(s):") {
		t.Errorf("linter spec must not retry, got:\n%s", got)
	}
	if calls2 != 1 {
		t.Errorf("calls = %d, want 1", calls2)
	}
}

// injectLinterFake adds a second fake server flagged IsLinter that wins the
// ext→spec mapping, so reg.SpecForFile reports it for .go files.
func injectLinterFake(t *testing.T, reg *lsp.Registry, dir string, handler fakeHandler) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { serveFake(t, serverConn, handler, dir) })
	c := lsp.NewTestClient("linterls", clientConn)
	reg.InjectClientInRoot("linterls", dir, lsp.ServerSpec{
		Name: "linterls", Language: "Fake", FileExts: []string{".go"}, Command: "fake-lsp", IsLinter: true,
	}, c)
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close(); wg.Wait() })
}

// TestReferences_PreCancelled: a pre-cancelled ctx surfaces the raw
// context.Canceled, unwrapped.
func TestReferences_PreCancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := references(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{}, dir, lsp.ServerSpec{Name: "fakels"})
	if err == nil || err.Error() != "context canceled" {
		t.Fatalf("error = %v, want raw 'context canceled'", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}

// TestReferences_ErrorThenSuccess: an erroring first attempt must abort, not
// retry into the second attempt's answer.
func TestReferences_ErrorThenSuccess(t *testing.T) {
	calls := 0
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				calls++
				if calls == 1 {
					return rpcError{"boom"}, true
				}
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				return []any{refLoc(a, 1, 0), refLoc(a, 2, 0)}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	_, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels"})
	if err == nil || !strings.HasPrefix(err.Error(), "references: ") {
		t.Fatalf("error = %v, want the first attempt's error to abort", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after error)", calls)
	}
}

// TestReferences_OnePlainTailLine: exactly 51 references → one plain line.
func TestReferences_OnePlainTailLine(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/references" {
				a := lsp.FileToURI(filepath.Join(d, "a.go"))
				out := make([]any, 51)
				for i := range out {
					out[i] = refLoc(a, i%3, 0)
				}
				return out, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc a() {}\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := fakeClient(t, reg)
	res, err := references(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4},
		dir, lsp.ServerSpec{Name: "fakels", IsLinter: true})
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	got := fmt.Sprintf("%v", res.Data)
	tail := got[strings.LastIndex(got, "... "):]
	if tail != "... 1 additional reference(s) shown without context\n  a.go:3:1\n" {
		t.Errorf("tail = %q", tail)
	}
}
