package lsp

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// rawPipeTest creates a client connected to a bare net.Conn (no serve goroutine).
// The test writes framed messages directly to conn to simulate the server.
func rawPipeTest(t *testing.T) (*Client, net.Conn, func()) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	c := &Client{
		name:     "raw",
		pending:  make(map[int64]chan *rpcResponse),
		openURIs: make(map[string]int),
		diags:    make(map[string][]Diagnostic),
		done:     make(chan struct{}),
		dead:     make(chan struct{}),
		stdin:    &pipeWriteCloser{conn: clientConn},
		stdout:   clientConn,
	}
	c.teardownOnce = sync.Once{}
	c.readWG.Go(func() {
		c.readLoop()
	})

	cleanup := func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		c.readWG.Wait()
		select {
		case <-c.Dead():
		case <-time.After(100 * time.Millisecond):
		}
	}
	return c, serverConn, cleanup
}

func rawWrite(t *testing.T, conn net.Conn, data string) {
	t.Helper()
	_, err := conn.Write([]byte(data))
	if err != nil {
		t.Fatalf("raw write: %v", err)
	}
}

// drainReadLoop yields to the readLoop goroutine so it can decode and dispatch
// messages written via rawWrite before we proceed or close the pipe.
func drainReadLoop() { runtime.Gosched(); runtime.Gosched() }

func TestReadLoop_BadJSON(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	rawWrite(t, conn, "Content-Length: 6\r\n\r\n{}{}{}")
	rawWrite(t, conn, "Content-Length: 2\r\n\r\n{}")
	drainReadLoop()
}

func TestReadLoop_BadContentLength(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	rawWrite(t, conn, "Content-Length: abc\r\n\r\n{}")
	rawWrite(t, conn, "Content-Length: 2\r\n\r\n{}")
	drainReadLoop()
}

func TestHandleServerRequest_WorkspaceFolders(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":1,"method":"workspace/workspaceFolders"}`))
	drainReadLoop()
}

func TestHandleServerRequest_WorkDoneProgress(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":2,"method":"window/workDoneProgress/create","params":{"token":"x"}}`))
	drainReadLoop()
}

func TestHandleServerRequest_ApplyEdit(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":3,"method":"workspace/applyEdit","params":{"edit":{"changes":{}}}}`))
	drainReadLoop()
}

func TestHandleServerRequest_UnknownMethod(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":4,"method":"some/unknown/request"}`))
	drainReadLoop()
}

func TestReadLoop_ServerNotification(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics","params":{"uri":"file:///x.go","diagnostics":[]}}`))
	drainReadLoop()
}

func TestReadLoop_ResponseWithNoPending(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":99999,"result":null}`))
	drainReadLoop()
}

func TestHandleServerRequest_NilParams(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":5,"method":"workspace/configuration","params":null}`))
	drainReadLoop()
}

func TestDocumentSymbols_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/documentSymbol" {
			return "not a symbol array", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := DocumentSymbols(ctx, c, "file:///x.go")
	if err == nil {
		t.Fatal("DocumentSymbols: expected error for bad JSON")
	}
	t.Logf("DocumentSymbols error: %v", err)
}

func TestCodeActions_BadJSON(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) {
		if req.Method == "textDocument/codeAction" {
			return "not a code action array", true
		}
		return nil, false
	})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := CodeActions(ctx, c, "file:///x.go", Range{}, CodeActionContext{TriggerKind: 1})
	if err == nil {
		t.Fatal("CodeActions: expected error for bad JSON")
	}
	t.Logf("CodeActions error: %v", err)
}

func TestApplyEditsToPath_FileNotFound(t *testing.T) {
	err := applyEditsToPath("/nonexistent/path.go", []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 1}}, NewText: "x"},
	})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyEditsToPath_WriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "readonly.go")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0444); err != nil {
		t.Fatal(err)
	}
	err := applyEditsToPath(path, []TextEdit{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 1}}, NewText: "x"},
	})
	if err == nil {
		t.Fatal("expected error for readonly file")
	}
	_ = err.Error()
}

func TestExtractTextDocumentEdit_NonTextOp(t *testing.T) {
	dc := map[string]any{"kind": "create", "uri": "file:///x.go"}
	uri, edits, ok := extractTextDocumentEdit(dc)
	if ok {
		t.Errorf("expected ok=false, got uri=%q edits=%v", uri, edits)
	}
	if uri != "" {
		t.Errorf("expected empty uri for resource op")
	}
}

func TestUriToPath_Absolute(t *testing.T) {
	got := uriToPath("file:///tmp/test%20space/file.go")
	if !strings.Contains(got, "test space") {
		t.Errorf("uriToPath did not decode %%20: %q", got)
	}
}

func TestRequest_CancelledContext(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()
	// Block the method so the server doesn't respond — Request must block on
	// ctx.Done() instead of racing against a quick echo response.
	srv.blockMethods = map[string]bool{"textDocument/references": true}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Request(ctx, "textDocument/references", nil)
	if err == nil {
		t.Fatal("Request: expected error with cancelled context")
	}
}

func TestShutdown_AlreadyDead(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	cleanup()
	c.Shutdown(context.Background())
}

func TestWriteMessage_ClosedStdin(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	err := c.writeMessage(rpcRequest{JSONRPC: "2.0", ID: 1, Method: "test"})
	if err == nil {
		t.Fatal("writeMessage: expected error with closed stdin")
	}
	t.Logf("writeMessage error: %v", err)
}

// ------ mock system call tests ------

func TestStartClient_ExecCommandFailsStart(t *testing.T) {
	saved := execCommand
	defer func() { execCommand = saved }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("this-binary-does-not-exist-xyz")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := StartClient(ctx, "bad", "bad", nil, "/tmp")
	if err == nil {
		t.Fatal("StartClient: expected error when command fails to start")
	}
	t.Logf("StartClient error: %v", err)
}

func TestSpawnClient_LookPathFails(t *testing.T) {
	saved := execLookPath
	defer func() { execLookPath = saved }()

	execLookPath = func(name string) (string, error) {
		return "", fmt.Errorf("mock not found: %s", name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := spawnClient(ctx, ServerSpec{Name: "bogus", Command: "bogus"}, "/tmp")
	if err == nil {
		t.Fatal("spawnClient: expected lookpath error")
	}
	if !strings.Contains(err.Error(), "lookpath") {
		t.Errorf("spawnClient: expected lookpath error, got %v", err)
	}
}

func TestSpawnClient_InitializeFails(t *testing.T) {
	saved := execCommand
	defer func() { execCommand = saved }()

	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("echo", "-n")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := spawnClient(ctx, ServerSpec{Name: "echo", Command: "echo"}, "/tmp")
	if err == nil {
		t.Fatal("spawnClient: expected error when echo doesn't speak LSP")
	}
	t.Logf("spawnClient error: %v", err)
}

func TestRegistry_SpawnClient_ExceedsRestarts(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	r.mu.Lock()
	r.restarts["bogus"] = 999
	r.extToSpec[".go"] = ServerSpec{
		Name:     "bogus",
		Command:  "bogus",
		FileExts: []string{".go"},
	}
	r.mu.Unlock()

	_, err := r.clientFor(context.Background(), r.extToSpec[".go"])
	if err == nil {
		t.Fatal("expected 'exceeded' error")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEncodeMessage_MarshalError(t *testing.T) {
	_, err := encodeMessage(make(chan int))
	if err == nil {
		t.Fatal("encodeMessage: expected error for unencodable value")
	}
	t.Logf("encodeMessage error: %v", err)
}

func toFramed(s string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(s), s)
}
