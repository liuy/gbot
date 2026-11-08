package lsptool

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/lsp"
)

// manualFakeEnv builds a registry with caller-ordered injected specs (the
// newFakeEnv harness always injects fakels first, which hides selection-order
// mutations). Handlers are keyed by spec name.
func manualFakeEnv(t *testing.T, dir string, specs []lsp.ServerSpec, handlers map[string]fakeHandler) (*lsp.Registry, func()) {
	t.Helper()
	reg := lsp.NewRegistry(dir)
	var conns []interface{ Close() error }
	var wgs []*sync.WaitGroup
	for _, spec := range specs {
		clientConn, serverConn := net.Pipe()
		conns = append(conns, clientConn, serverConn)
		wg := &sync.WaitGroup{}
		h := handlers[spec.Name]
		wg.Go(func() { serveFake(t, serverConn, h, dir) })
		wgs = append(wgs, wg)
		c := lsp.NewTestClient(spec.Name, clientConn)
		reg.InjectClientInRoot(spec.Name, dir, spec, c)
	}
	cleanup := func() {
		for _, c := range conns {
			_ = c.Close()
		}
		for _, wg := range wgs {
			wg.Wait()
		}
	}
	return reg, cleanup
}

func TestRequest_NoServerConfigured(t *testing.T) {
	reg := lsp.NewRegistry(t.TempDir())
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "textDocument/hover",
	}), basicCtx())
	if err == nil || err.Error() != "no language server configured" {
		t.Fatalf("error = %v, want 'no language server configured'", err)
	}
}

func TestRequest_ParamsMatrix(t *testing.T) {
	t.Run("payload override", func(t *testing.T) {
		paramsCh := make(chan string, 2)
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				if method == "custom/method" {
					paramsCh <- string(params)
					return map[string]any{"ok": true}, true
				}
				return nil, false
			}
		})
		defer cleanup()
		res, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method", Payload: `{"x":1}`,
		}), basicCtxWithDir(t, dir))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if got, _ := res.Data.(string); got != "fakels ← custom/method:\n{\n  \"ok\": true\n}" {
			t.Errorf("output = %q", got)
		}
		if got := recvNot(t, paramsCh, "params"); got != "{\"x\":1}" {
			t.Errorf("params = %q, want {\"x\":1}", got)
		}
	})
	t.Run("invalid payload", func(t *testing.T) {
		reg, _, cleanup := newFakeEnv(t, nil)
		defer cleanup()
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method", Payload: `{bad`,
		}), basicCtx())
		if err == nil || !strings.HasPrefix(err.Error(), "invalid payload JSON:") {
			t.Fatalf("error = %v, want prefix invalid payload JSON:", err)
		}
		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			t.Errorf("error does not unwrap to *json.SyntaxError: %v", err)
		}
	})
	t.Run("file only", func(t *testing.T) {
		paramsCh := make(chan string, 2)
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				if method == "custom/method" {
					paramsCh <- string(params)
					return nil, true
				}
				return nil, false
			}
		})
		defer cleanup()
		src := filepath.Join(dir, "a.go")
		if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method", File: src,
		}), basicCtxWithDir(t, dir))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		want := "{\"textDocument\":{\"uri\":\"" + lsp.FileToURI(src) + "\"}}"
		if got := recvNot(t, paramsCh, "params"); got != want {
			t.Errorf("params = %q, want %q", got, want)
		}
	})
	t.Run("file and symbol", func(t *testing.T) {
		paramsCh := make(chan string, 2)
		reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				if method == "custom/method" {
					paramsCh <- string(params)
					return nil, true
				}
				return nil, false
			}
		})
		defer cleanup()
		src := filepath.Join(dir, "a.go")
		if err := os.WriteFile(src, []byte("package main\nfunc foo() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method", File: src, Symbol: "foo",
		}), basicCtxWithDir(t, dir))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		// defaultWorkspaceSymbols resolves foo to line 1, char 5.
		want := "{\"position\":{\"line\":1,\"character\":5},\"textDocument\":{\"uri\":\"" + lsp.FileToURI(src) + "\"}}"
		if got := recvNot(t, paramsCh, "params"); got != want {
			t.Errorf("params = %q, want %q", got, want)
		}
	})
	t.Run("symbol not found", func(t *testing.T) {
		reg, dir, cleanup := newFakeEnv(t, nil)
		defer cleanup()
		src := filepath.Join(dir, "a.go")
		if err := os.WriteFile(src, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method", File: src, Symbol: "nope",
		}), basicCtxWithDir(t, dir))
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want symbol-not-found", err)
		}
	})
	t.Run("neither file nor payload", func(t *testing.T) {
		paramsCh := make(chan string, 2)
		reg, _, cleanup := newFakeEnv(t, func(d string) fakeHandler {
			return func(method string, params json.RawMessage) (any, bool) {
				if method == "custom/method" {
					paramsCh <- string(params)
					return nil, true
				}
				return nil, false
			}
		})
		defer cleanup()
		_, err := New(reg).Call(context.Background(), mustInput(t, Input{
			Action: "request", Query: "custom/method",
		}), basicCtx())
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if got := recvNot(t, paramsCh, "params"); got != "{}" {
			t.Errorf("params = %q, want {}", got)
		}
	})
}

func TestRequest_ReadFileError(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/method", File: filepath.Join(dir, "missing.go"),
	}), basicCtxWithDir(t, dir))
	if err == nil || !strings.HasPrefix(err.Error(), "read file:") {
		t.Fatalf("error = %v, want prefix read file:", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); !ok {
		t.Errorf("error does not unwrap to *fs.PathError: %v", err)
	}
}

func TestRequest_CancelledContext(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, _ json.RawMessage) (any, bool) {
			if method == "textDocument/hover" {
				return holdResponse{}, true
			}
			return nil, false
		}
	})
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(reg).Call(ctx, mustInput(t, Input{
		Action: "request", Query: "textDocument/hover",
	}), basicCtx())
	if err == nil || !strings.HasPrefix(err.Error(), "lsp textDocument/hover:") {
		t.Fatalf("error = %v, want prefix lsp textDocument/hover:", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not unwrap to context.Canceled: %v", err)
	}
}

func TestRequest_ForSpecSpawnFailure(t *testing.T) {
	reg, dir, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	if !reg.KillAndEvict("fakels", dir) {
		t.Fatal("KillAndEvict: no live client")
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/method",
	}), basicCtx())
	if err == nil || !strings.HasPrefix(err.Error(), "lsp:") {
		t.Fatalf("error = %v, want prefix lsp:", err)
	}
	if _, ok := errors.AsType[*exec.Error](err); !ok {
		t.Errorf("error does not unwrap to *exec.Error: %v", err)
	}
}

// TestRequest_ServerSelection pins the ext-match loop: first matching spec
// wins, fallback is specs[0], and non-matching earlier specs are skipped.
func TestRequest_ServerSelection(t *testing.T) {
	dir := t.TempDir()
	goParams := make(chan string, 2)
	tsParams := make(chan string, 2)
	go2Params := make(chan string, 2)
	specs := []lsp.ServerSpec{
		{Name: "tsrv", Language: "Ts", FileExts: []string{".ts"}, Command: "fake"},
		{Name: "go1", Language: "Go", FileExts: []string{".go"}, Command: "fake"},
		{Name: "go2", Language: "Go", FileExts: []string{".go"}, Command: "fake"},
	}
	reg, cleanup := manualFakeEnv(t, dir, specs, map[string]fakeHandler{
		"tsrv": func(method string, params json.RawMessage) (any, bool) {
			if method == "custom/method" {
				tsParams <- string(params)
			}
			return nil, false
		},
		"go1": func(method string, params json.RawMessage) (any, bool) {
			if method == "custom/method" {
				goParams <- string(params)
			}
			return nil, false
		},
		"go2": func(method string, params json.RawMessage) (any, bool) {
			if method == "custom/method" {
				go2Params <- string(params)
			}
			return nil, false
		},
	})
	defer cleanup()
	_ = os.WriteFile(filepath.Join(dir, "x.go"), []byte("package main\n"), 0o644)

	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/method", File: filepath.Join(dir, "x.go"),
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got, _ := res.Data.(string); !strings.HasPrefix(got, "go1 ← custom/method:") {
		t.Errorf("chosen server = %q, want go1", got)
	}
	recvNot(t, goParams, "go1 request")
	assertNoNot(t, tsParams, "tsrv must not be queried")
	assertNoNot(t, go2Params, "go2 must not be queried (first match wins)")
}

func TestRequest_NullResult(t *testing.T) {
	reg, _, cleanup := newFakeEnv(t, nil)
	defer cleanup()
	res, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/method",
	}), basicCtx())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if got, _ := res.Data.(string); got != "fakels ← custom/method:\nnull" {
		t.Errorf("null output = %q", got)
	}
}

// TestRequest_EnsureOpenSent: with a resolvable file the client receives
// didOpen carrying the on-disk content.
func TestRequest_EnsureOpenSent(t *testing.T) {
	didOpenCh := make(chan string, 2)
	reg, dir, cleanup := newFakeEnv(t, func(d string) fakeHandler {
		return func(method string, params json.RawMessage) (any, bool) {
			if method == "textDocument/didOpen" {
				didOpenCh <- string(params)
				return nil, false
			}
			return nil, false
		}
	})
	defer cleanup()
	src := filepath.Join(dir, "a.go")
	if err := os.WriteFile(src, []byte("package main\nfunc foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(reg).Call(context.Background(), mustInput(t, Input{
		Action: "request", Query: "custom/method", File: src, Symbol: "foo",
	}), basicCtxWithDir(t, dir))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var open struct {
		TextDocument struct {
			URI  string `json:"uri"`
			Text string `json:"text"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal([]byte(recvNot(t, didOpenCh, "didOpen")), &open); err != nil {
		t.Fatal(err)
	}
	if open.TextDocument.URI != lsp.FileToURI(src) || open.TextDocument.Text != "package main\nfunc foo() {}\n" {
		t.Errorf("didOpen = %+v", open.TextDocument)
	}
}
