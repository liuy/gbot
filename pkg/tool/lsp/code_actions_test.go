package lsptool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

func caEnv(t *testing.T, actions any, resolve any) (*lsp.Registry, string) {
	t.Helper()
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return actions, true
			case "codeAction/resolve":
				return resolve, true
			}
			return nil, false
		}
	})
	t.Cleanup(cleanup)
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return reg, dir
}

// TestCodeActions_RequestParams pins the range and context shape.
func TestCodeActions_RequestParams(t *testing.T) {
	paramsCh := make(chan string, 2)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				paramsCh <- string(params)
				return []any{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("code_actions: %v", err)
	}
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Range   lsp.Range `json:"range"`
		Context struct {
			TriggerKind int `json:"triggerKind"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, paramsCh, "codeAction params")), &p); err != nil {
		t.Fatal(err)
	}
	// foo is at line 1, char 5..8; the query range collapses onto the position.
	if p.Range.Start.Line != 1 || p.Range.End.Line != 1 || p.Range.Start.Character != 5 || p.Range.End.Character != 5 {
		t.Errorf("range = %+v, want line 1 chars 5..5", p.Range)
	}
	if p.Context.TriggerKind != 1 {
		t.Errorf("triggerKind = %d, want 1", p.Context.TriggerKind)
	}
}

func TestCodeActions_ApplyOutOfRangeQueries(t *testing.T) {
	for _, q := range []string{"-1", "99"} {
		t.Run("query "+q, func(t *testing.T) {
			reg, dir := caEnv(t, []map[string]any{
				{"title": "X", "kind": "quickfix"},
				{"title": "Y", "kind": ""},
			}, nil)
			res, err := New(reg).Call(context.Background(), mustInput(t, Input{
				Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
				Apply: new(true), Query: q,
			}), &tool.ToolUseContext{WorkingDir: dir})
			if err != nil {
				t.Fatalf("code_actions: %v", err)
			}
			want := "No code action matches \"" + q + "\". Available actions:\n" +
				"  0: [quickfix] X\n" +
				"  1: [action] Y\n"
			if got, _ := res.Data.(string); got != want {
				t.Errorf("output =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

func TestCodeActions_TitleMatchSelects(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				// Two titles match the "add" query: the first must win.
				return []map[string]any{
					{"title": "Add import", "kind": "quickfix"},
					{"title": "Add const", "kind": "quickfix"},
				}, true
			case "codeAction/resolve":
				// Echo the action being resolved so the test can see WHICH
				// action the selection loop picked.
				var req struct {
					Title string `json:"title"`
				}
				_ = json.Unmarshal(params, &req)
				return map[string]any{"title": req.Title, "kind": "quickfix", "edit": map[string]any{
					"changes": map[string]any{
						"file://" + filepath.Join(d, "foo.go"): []map[string]any{{
							"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 12}},
							"newText": "import \"fmt\"\n",
						}},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "add",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("code_actions: %v", err)
	}
	// The applied edit describes the per-file change; the closure inside
	// ApplyCodeAction must return it.
	want := "Applied \"Add import\":\n  Workspace edit:\n    " + filepath.Join(dir, "foo.go") + "\n"
	if got, _ := res.Data.(string); got != want {
		t.Errorf("output =\n%q\nwant\n%q", got, want)
	}
}

func TestCodeActions_AppliedFilesNotified(t *testing.T) {
	didChangeCh := make(chan string, 4)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return []map[string]any{{"title": "Add import", "kind": "quickfix"}}, true
			case "codeAction/resolve":
				return map[string]any{"title": "Add import", "kind": "quickfix", "edit": map[string]any{
					"changes": map[string]any{
						"file://" + filepath.Join(d, "foo.go"): []map[string]any{{
							"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 12}},
							"newText": "import \"fmt\"\n",
						}},
					},
				}}, true
			case "textDocument/didChange":
				didChangeCh <- method
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "0",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("code_actions: %v", err)
	}
	// After a successful apply the changed file is synced to the server via
	// didChange (the file was already opened during symbol resolution, so
	// only a didChange proves the post-apply sync happened).
	recvNot(t, didChangeCh, "didChange after apply")
}

func TestCodeActions_ApplyCommandError(t *testing.T) {
	_, err := applyWithFailingCommand(t)
	if err == nil || !strings.HasPrefix(err.Error(), "apply code action:") {
		t.Fatalf("error = %v, want prefix apply code action:", err)
	}
}

// applyWithFailingCommand runs code_actions apply whose command execution
// fails on the server.
func applyWithFailingCommand(t *testing.T) (*tool.ToolResult, error) {
	t.Helper()
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				// The command rides on the listed action: a command-only
				// resolve result is never adopted (ApplyCodeAction requires
				// r.Edit != nil to take the resolve response).
				return []map[string]any{{"title": "Run me", "kind": "quickfix",
					"command": map[string]any{"title": "run", "command": "workspace.runme"}}}, true
			case "workspace/executeCommand":
				return rpcError{"boom"}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "run",
	}), &tool.ToolUseContext{WorkingDir: dir})
}

func TestCodeActions_Cancelled(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/codeAction" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	c := fakeClient(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := codeActions(ctx, c, lsp.FileToURI(filepath.Join(dir, "foo.go")), lsp.Position{}, Input{})
	if err == nil || !strings.HasPrefix(err.Error(), "code actions:") {
		t.Fatalf("error = %v, want prefix code actions:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("no unwrap to context.Canceled: %v", err)
	}
}

// TestCodeActions_ApplyEditFail: an edit aimed at a missing file must fail
// the apply with the wrapped error.
func TestCodeActions_ApplyEditFail(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			switch method {
			case "textDocument/codeAction":
				return []map[string]any{{"title": "Fix it", "kind": "quickfix"}}, true
			case "codeAction/resolve":
				return map[string]any{"title": "Fix it", "kind": "quickfix", "edit": map[string]any{
					"changes": map[string]any{
						lsp.FileToURI(filepath.Join(d, "missing.go")): []map[string]any{{
							"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}},
							"newText": "X",
						}},
					},
				}}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: filepath.Join(dir, "foo.go"), Symbol: "foo",
		Apply: new(true), Query: "fix",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err == nil || !strings.HasPrefix(err.Error(), "apply code action:") {
		t.Fatalf("error = %v, want prefix apply code action:", err)
	}
}

// TestCodeActions_DiagnosticsParams pins the context.diagnostics round-trip:
// a diagnostic the server pushed BEFORE the request must ride along (servers
// filter quickfixes by it). serveFake cannot push server-initiated
// notifications, so this test hand-rolls the framing.
func TestCodeActions_DiagnosticsParams(t *testing.T) {
	paramsCh := make(chan string, 2)
	dir := t.TempDir()
	foo := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(foo, []byte("package main\nfunc foo(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fooURI := lsp.FileToURI(foo)

	clientConn, serverConn := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { serveDiagFake(t, serverConn, fooURI, paramsCh) })
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close(); wg.Wait() })
	c := lsp.NewTestClient("fakels", clientConn)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Initialize(ctx, "file:///repo"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	reg := lsp.NewRegistry(dir)
	reg.InjectClient("fakels", lsp.ServerSpec{
		Name: "fakels", Language: "Fake", FileExts: []string{".go"}, Command: "fake-lsp",
	}, c)

	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "code_actions", File: foo, Symbol: "foo",
	}), &tool.ToolUseContext{WorkingDir: dir})
	if err != nil {
		t.Fatalf("code_actions: %v", err)
	}
	if got, _ := res.Data.(string); got != "No code actions available" {
		t.Errorf("output = %q", got)
	}
	var p struct {
		Context struct {
			TriggerKind int `json:"triggerKind"`
			Diagnostics []struct {
				Message string `json:"message"`
				Range   struct {
					Start struct {
						Line      int `json:"line"`
						Character int `json:"character"`
					} `json:"start"`
				} `json:"range"`
			} `json:"diagnostics"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, paramsCh, "codeAction params")), &p); err != nil {
		t.Fatal(err)
	}
	assertNoNot(t, paramsCh, "second codeAction")
	if len(p.Context.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want the pushed one", p.Context.Diagnostics)
	}
	d := p.Context.Diagnostics[0]
	if d.Message != "declared and not used" || d.Range.Start.Line != 1 || d.Range.Start.Character != 5 {
		t.Errorf("diagnostic = %+v, want message+position of the pushed one", d)
	}
	if p.Context.TriggerKind != 1 {
		t.Errorf("triggerKind = %d, want 1", p.Context.TriggerKind)
	}
}

// serveDiagFake: like serveFake, but pushes one publishDiagnostics right after
// the initialize handshake; documentSymbol is answered positionally so the
// tool can resolve symbol foo without the on-disk scanner.
func serveDiagFake(t *testing.T, conn net.Conn, diagURI string, paramsCh chan<- string) {
	t.Helper()
	r := bufio.NewReader(conn)
	writeResp := func(v any) bool {
		body, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
			return false
		}
		_, err := conn.Write(body)
		return err == nil
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		var contentLen int
		if _, err := fmt.Sscanf(line, "Content-Length: %d", &contentLen); err != nil {
			continue
		}
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int64           `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC == "" {
			continue
		}
		switch req.Method {
		case "initialize":
			if !writeResp(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{
				"capabilities": map[string]any{"codeActionProvider": true},
			}}) {
				return
			}
			// Push the diagnostic the codeAction request must echo back.
			diag := map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": diagURI,
					"diagnostics": []map[string]any{{
						"range":   map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
						"message": "declared and not used",
					}},
				},
			}
			if !writeResp(diag) {
				return
			}
		case "workspace/symbol":
			if !writeResp(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []map[string]any{{
				"name": "foo", "kind": 12,
				"location": map[string]any{
					"uri":   diagURI,
					"range": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
				},
			}}}) {
				return
			}
		case "textDocument/documentSymbol":
			if !writeResp(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []map[string]any{{
				"name": "foo", "kind": 12,
				"range":          map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
				"selectionRange": map[string]any{"start": map[string]any{"line": 1, "character": 5}, "end": map[string]any{"line": 1, "character": 8}},
			}}}) {
				return
			}
		case "textDocument/codeAction":
			paramsCh <- string(req.Params)
			if !writeResp(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": []any{}}) {
				return
			}
		case "exit":
			return
		default:
			if req.ID != 0 {
				if !writeResp(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": nil}) {
					return
				}
			}
		}
	}
}
