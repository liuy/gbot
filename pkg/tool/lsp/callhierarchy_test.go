package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

func testCtx() context.Context { return context.Background() }

func TestSymbolKindStr(t *testing.T) {
	tests := []struct {
		kind int
		want string
	}{
		{12, "Function"},
		{6, "Method"},
		{5, "Class"},
		{23, "Struct"},
		{11, "Interface"},
		{999, "Kind(999)"},
	}
	for _, tt := range tests {
		if got := symbolKindStr(tt.kind); got != tt.want {
			t.Errorf("symbolKindStr(%d) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestFormatRanges(t *testing.T) {
	tests := []struct {
		name string
		in   []lsp.Range
		want string
	}{
		{"empty", nil, ""},
		{"single", []lsp.Range{{Start: lsp.Position{Line: 4, Character: 9}}}, "5:10"},
		{"multiple", []lsp.Range{
			{Start: lsp.Position{Line: 4, Character: 9}},
			{Start: lsp.Position{Line: 9, Character: 0}},
		}, "5:10, 10:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatRanges(tt.in); got != tt.want {
				t.Errorf("formatRanges() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]int{"zebra": 3, "apple": 1, "mango": 2}
	got := sortedKeys(m)
	want := []string{"apple", "mango", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys: got %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sortedKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGroupCallersByFile(t *testing.T) {
	calls := []callerEntry{
		{From: callHierarchyItem{URI: "file:///a.go", Name: "funcA"}},
		{From: callHierarchyItem{URI: "file:///b.go", Name: "funcB"}},
		{From: callHierarchyItem{URI: "file:///a.go", Name: "funcC"}},
	}
	got := groupCallersByFile(calls, "/wd")
	if len(got) != 2 {
		t.Fatalf("groupCallersByFile: got %d files, want 2", len(got))
	}
}

func TestGroupCalleesByFile(t *testing.T) {
	calls := []calleeEntry{
		{To: callHierarchyItem{URI: "file:///x.go", Name: "funcX"}},
		{To: callHierarchyItem{URI: "file:///x.go", Name: "funcY"}},
	}
	got := groupCalleesByFile(calls, "/wd")
	if len(got) != 1 {
		t.Fatalf("groupCalleesByFile: got %d files, want 1", len(got))
	}
	total := 0
	for _, calls := range got {
		total += len(calls)
	}
	if total != 2 {
		t.Errorf("groupCalleesByFile: got %d total calls, want 2", total)
	}
}

func TestFilterGitIgnoredCallers_AllKept(t *testing.T) {
	calls := []callerEntry{
		{From: callHierarchyItem{URI: "file:///tmp/nonrepo/a.go"}},
		{From: callHierarchyItem{URI: "file:///tmp/nonrepo/b.go"}},
	}
	got := filterGitIgnoredCallers(testCtx(), calls, "/tmp/nonrepo")
	if len(got) != 2 {
		t.Errorf("filterGitIgnoredCallers in non-git dir: got %d, want 2", len(got))
	}
}

func TestFilterGitIgnoredCallees_AllKept(t *testing.T) {
	calls := []calleeEntry{
		{To: callHierarchyItem{URI: "file:///tmp/nonrepo/x.go"}},
	}
	got := filterGitIgnoredCallees(testCtx(), calls, "/tmp/nonrepo")
	if len(got) != 1 {
		t.Errorf("filterGitIgnoredCallees in non-git dir: got %d, want 1", len(got))
	}
}

// --- "no results" integration error paths ---

func TestIntegration_CallHierarchy_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callers", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No call hierarchy") {
		t.Errorf("expected 'No call hierarchy': %q", got)
	}
}

func TestIntegration_Callees_NoResults(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return emptyHandler()
	})
	defer cleanup()

	_ = os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\nfunc foo() {}\n"), 0644)

	tt := New(reg)
	result, err := tt.Call(context.Background(), mustInput(t, Input{
		Action: "callees", Symbol: "foo", File: filepath.Join(dir, "test.go"),
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%v", result.Data)
	if !strings.Contains(got, "No call hierarchy") {
		t.Errorf("expected 'No call hierarchy': %q", got)
	}
}

// ---------------------------------------------------------------------------
// Mutation-kill tests: exact-output and error-path coverage for callHierarchy.
// Every assertion compares full formatted output so any Fprintf/format/range
// mutation shifts the string and fails.
// ---------------------------------------------------------------------------

// newGitEnv is newFakeEnv plus a git repo whose .gitignore excludes vendor/,
// so the gitignore-filtering paths run against real `git check-ignore`.
func newGitEnv(t *testing.T, handlerFactory func(dir string) fakeHandler) (*lsp.Registry, string, func()) {
	t.Helper()
	reg, dir, cleanup := newFakeEnv(t, handlerFactory)
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		cleanup()
		t.Skipf("git not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0o644); err != nil {
		cleanup()
		t.Fatalf("write .gitignore: %v", err)
	}
	return reg, dir, cleanup
}

func fakeClient(t *testing.T, reg *lsp.Registry) *lsp.Client {
	t.Helper()
	specs := reg.Snapshot()
	c, err := reg.ForSpec(context.Background(), specs[0])
	if err != nil {
		t.Fatalf("ForSpec: %v", err)
	}
	return c
}

func r0(line int) lsp.Range {
	return lsp.Range{Start: lsp.Position{Line: line, Character: 0}, End: lsp.Position{Line: line, Character: 5}}
}

func TestCallHierarchy_Callers_ExactOutput(t *testing.T) {
	var incomingParams []json.RawMessage
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []callHierarchyItem{{
					Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(0),
				}}, true
			case "callHierarchy/incomingCalls":
				incomingParams = append(incomingParams, params)
				return []callerEntry{
					{From: callHierarchyItem{Name: "callerTwo", Kind: 6, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(9)}},
					{From: callHierarchyItem{Name: "ignoredCaller", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "vendor", "lib.go")), Range: r0(0)},
						FromRanges: []lsp.Range{{Start: lsp.Position{Line: 0, Character: 0}}}},
					{From: callHierarchyItem{Name: "callerOne", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(1)},
						FromRanges: []lsp.Range{{Start: lsp.Position{Line: 2, Character: 3}}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)

	res, err := callHierarchy(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "a.go")), lsp.Position{Line: 0, Character: 4}, dir, "callers")
	if err != nil {
		t.Fatalf("callHierarchy: %v", err)
	}
	// Grouping is by file, order within a file preserved, files sorted;
	// vendor/lib.go is gitignored so only the two a.go callers survive.
	want := "Found 2 caller(s):\n\na.go:\n  callerTwo (Method) - Line 10\n  callerOne (Function) - Line 2 [calls at: 3:4]\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("callers output =\n%q\nwant\n%q", got, want)
	}

	// The incoming request must echo the prepared item verbatim.
	if len(incomingParams) != 1 {
		t.Fatalf("incomingCalls requests = %d, want 1", len(incomingParams))
	}
	var p struct {
		Item callHierarchyItem `json:"item"`
	}
	if err := json.Unmarshal(incomingParams[0], &p); err != nil {
		t.Fatalf("decode incoming params: %v", err)
	}
	if p.Item.Name != "foo" || p.Item.Kind != 12 || p.Item.Range.Start.Line != 0 {
		t.Errorf("item roundtrip = %+v, want name=foo kind=12 line=0", p.Item)
	}
	if p.Item.URI != lsp.FileToURI(filepath.Join(dir, "a.go")) {
		t.Errorf("item uri = %q", p.Item.URI)
	}
}

func TestCallHierarchy_Callees_ExactOutput(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []callHierarchyItem{{Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "x.go")), Range: r0(0)}}, true
			case "callHierarchy/outgoingCalls":
				return []calleeEntry{
					{To: callHierarchyItem{Name: "calleeIgnored", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "vendor", "lib.go")), Range: r0(0)}},
					{To: callHierarchyItem{Name: "calleeTwo", Kind: 23, URI: lsp.FileToURI(filepath.Join(d, "x.go")), Range: r0(6)}},
					{To: callHierarchyItem{Name: "calleeOne", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "x.go")), Range: r0(4)},
						FromRanges: []lsp.Range{{Start: lsp.Position{Line: 1, Character: 0}}}},
				}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)

	res, err := callHierarchy(context.Background(), c,
		lsp.FileToURI(filepath.Join(dir, "x.go")), lsp.Position{Line: 0, Character: 4}, dir, "callees")
	if err != nil {
		t.Fatalf("callHierarchy: %v", err)
	}
	want := "Found 2 callee(s):\n\nx.go:\n  calleeTwo (Struct) - Line 7\n  calleeOne (Function) - Line 5 [called from: 2:1]\n"
	if got := fmt.Sprintf("%v", res.Data); got != want {
		t.Errorf("callees output =\n%q\nwant\n%q", got, want)
	}
}

func TestCallHierarchy_EmptyMessages(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []callHierarchyItem{{Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(0)}}, true
			case "callHierarchy/incomingCalls", "callHierarchy/outgoingCalls":
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	pos := lsp.Position{Line: 0, Character: 4}

	res, err := callHierarchy(context.Background(), c, uri, pos, dir, "callers")
	if err != nil {
		t.Fatalf("callers: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No callers found (nothing calls this function)" {
		t.Errorf("callers empty = %q", got)
	}

	res, err = callHierarchy(context.Background(), c, uri, pos, dir, "callees")
	if err != nil {
		t.Fatalf("callees: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No callees found (this function calls nothing)" {
		t.Errorf("callees empty = %q", got)
	}
}

func TestCallHierarchy_AllGitignored(t *testing.T) {
	reg, dir, cleanup := newGitEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []callHierarchyItem{{Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(0)}}, true
			case "callHierarchy/incomingCalls":
				return []callerEntry{{From: callHierarchyItem{Name: "v1", URI: lsp.FileToURI(filepath.Join(d, "vendor", "a.go")), Range: r0(0)}}}, true
			case "callHierarchy/outgoingCalls":
				return []calleeEntry{{To: callHierarchyItem{Name: "v2", URI: lsp.FileToURI(filepath.Join(d, "vendor", "a.go")), Range: r0(0)}}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	pos := lsp.Position{Line: 0, Character: 4}

	res, err := callHierarchy(context.Background(), c, uri, pos, dir, "callers")
	if err != nil {
		t.Fatalf("callers: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No callers found (all gitignored)" {
		t.Errorf("callers all-ignored = %q", got)
	}

	res, err = callHierarchy(context.Background(), c, uri, pos, dir, "callees")
	if err != nil {
		t.Fatalf("callees: %v", err)
	}
	if got := fmt.Sprintf("%v", res.Data); got != "No callees found (all gitignored)" {
		t.Errorf("callees all-ignored = %q", got)
	}
}

func TestCallHierarchy_DecodeErrors(t *testing.T) {
	// An object where the code expects an array makes json.Unmarshal fail.
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return map[string]any{"name": "foo"}, true
			case "callHierarchy/incomingCalls":
				return map[string]any{"from": "x"}, true
			case "callHierarchy/outgoingCalls":
				return map[string]any{"to": "x"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	pos := lsp.Position{Line: 0, Character: 4}

	_, err := callHierarchy(context.Background(), c, uri, pos, dir, "callers")
	if err == nil || !strings.HasPrefix(err.Error(), "decode call hierarchy items:") {
		t.Fatalf("prepare decode error = %v", err)
	}
	var ute *json.UnmarshalTypeError
	if !errors.As(err, &ute) {
		t.Errorf("prepare decode error does not unwrap to *json.UnmarshalTypeError: %v", err)
	}

	// For the second-stage decodes the prepare response must be valid.
	reg2, dir2, cleanup2 := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/prepareCallHierarchy":
				return []callHierarchyItem{{Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(0)}}, true
			case "callHierarchy/incomingCalls":
				return map[string]any{"from": "x"}, true
			case "callHierarchy/outgoingCalls":
				return map[string]any{"to": "x"}, true
			}
			return nil, false
		}
	})
	defer cleanup2()
	c2 := fakeClient(t, reg2)
	uri2 := lsp.FileToURI(filepath.Join(dir2, "a.go"))

	_, err = callHierarchy(context.Background(), c2, uri2, pos, dir2, "callers")
	if err == nil || !strings.HasPrefix(err.Error(), "decode callers:") {
		t.Fatalf("callers decode error = %v", err)
	}
	if !errors.As(err, &ute) {
		t.Errorf("callers decode error does not unwrap to *json.UnmarshalTypeError: %v", err)
	}

	_, err = callHierarchy(context.Background(), c2, uri2, pos, dir2, "callees")
	if err == nil || !strings.HasPrefix(err.Error(), "decode callees:") {
		t.Fatalf("callees decode error = %v", err)
	}
	if !errors.As(err, &ute) {
		t.Errorf("callees decode error does not unwrap to *json.UnmarshalTypeError: %v", err)
	}
}

// TestCallHierarchy_RequestErrors kills the Request-error wraps. The incoming
// handler holds the response until the ctx is cancelled, so the client returns
// context.Canceled deterministically for the second request while the first
// (prepare) completed normally.
func TestCallHierarchy_RequestErrors(t *testing.T) {
	for _, dirn := range []string{"callers", "callees"} {
		t.Run(dirn, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			holdMethod := "callHierarchy/incomingCalls"
			if dirn == "callees" {
				holdMethod = "callHierarchy/outgoingCalls"
			}
			arrived := make(chan struct{})
			reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
				return func(method string, _ json.RawMessage) (any, bool) {
					switch method {
					case "textDocument/prepareCallHierarchy":
						return []callHierarchyItem{{Name: "foo", Kind: 12, URI: lsp.FileToURI(filepath.Join(d, "a.go")), Range: r0(0)}}, true
					case holdMethod:
						// Signal arrival, then wait for the cancel so the
						// serveFake loop is free to drain the client's
						// $/cancelRequest notification; holdResponse keeps the
						// real response from ever racing the cancellation.
						close(arrived)
						<-ctx.Done()
						return holdResponse{}, true
					}
					return nil, false
				}
			})
			defer cleanup()
			c := fakeClient(t, reg)
			go func() {
				<-arrived
				cancel()
			}()

			_, err := callHierarchy(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")),
				lsp.Position{Line: 0, Character: 4}, dir, dirn)
			cancel()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.HasPrefix(err.Error(), holdMethod+":") {
				t.Errorf("error = %q, want prefix %q", err.Error(), holdMethod+":")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error does not unwrap to context.Canceled: %v", err)
			}
		})
	}
}

// TestCallHierarchy_PrepareRequestCancelled kills the prepare error wrap.
// The handler holds the prepare response: with an instant null reply the
// client's select would race the already-closed ctx.Done() (~50% flake), but
// with holdResponse the only way Request can finish is the cancellation.
func TestCallHierarchy_PrepareRequestCancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/prepareCallHierarchy" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := callHierarchy(ctx, c, lsp.FileToURI(filepath.Join(dir, "a.go")),
		lsp.Position{Line: 0, Character: 4}, dir, "callers")
	if err == nil || !strings.HasPrefix(err.Error(), "prepareCallHierarchy:") {
		t.Fatalf("error = %v, want prefix prepareCallHierarchy:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not unwrap to context.Canceled: %v", err)
	}
}

// TestCallHierarchy_PrepareParams pins the request shape: textDocument.uri
// must carry the queried URI and position must round-trip.
func TestCallHierarchy_PrepareParams(t *testing.T) {
	var prepareParams json.RawMessage
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/prepareCallHierarchy" {
				prepareParams = params
				return []callHierarchyItem{{Name: "foo", Kind: 12}}, true
			}
			if method == "callHierarchy/incomingCalls" {
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	uri := lsp.FileToURI(filepath.Join(dir, "a.go"))
	pos := lsp.Position{Line: 7, Character: 3}

	_, err := callHierarchy(context.Background(), c, uri, pos, dir, "callers")
	if err != nil {
		t.Fatalf("callHierarchy: %v", err)
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position lsp.Position `json:"position"`
	}
	if err := json.Unmarshal(prepareParams, &p); err != nil {
		t.Fatalf("decode prepare params: %v", err)
	}
	if p.TextDocument.URI != uri {
		t.Errorf("prepare params uri = %q, want %q", p.TextDocument.URI, uri)
	}
	if p.Position != pos {
		t.Errorf("prepare params position = %+v, want %+v", p.Position, pos)
	}
}
