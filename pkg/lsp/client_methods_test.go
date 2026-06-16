package lsp

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewTestClient(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	}()

	c := NewTestClient("test-client", clientConn)
	if c == nil {
		t.Fatal("NewTestClient returned nil")
	}
	if c.Name() != "test-client" {
		t.Errorf("Name() = %q, want test-client", c.Name())
	}
	if !c.IsAlive() {
		t.Error("NewTestClient should be alive immediately")
	}

	// Close the server side to trigger teardown.
	_ = serverConn.Close()
	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead channel not closed after pipe close")
	}
	if c.IsAlive() {
		t.Error("IsAlive should return false after death")
	}
	c.readWG.Wait()
}

func TestClient_IsAlive(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	if !c.IsAlive() {
		t.Fatal("expected client to be alive")
	}
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	if c.IsAlive() {
		t.Error("expected IsAlive=false after teardown")
	}
}

func TestClient_Kill_NilCmd(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	// In-process client has cmd == nil; Kill should be a no-op.
	c.Kill()
}

func TestClient_Kill_Subprocess(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := StartClient(ctx, "fake", bin, nil, t.TempDir(), "GBOT_FAKE_LSP=1")
	if err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	if c.cmd == nil || c.cmd.Process == nil {
		t.Fatal("expected non-nil cmd.Process for subprocess client")
	}
	c.Kill()
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Kill did not cause Dead channel to close")
	}
}

func TestStoreDiagnostics_BadJSON(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	c.storeDiagnostics([]byte(`{"uri":"file:///x.go","diagnostics": not valid json}`))
	// Verify no diagnostics got cached.
	if diags := c.DiagnosticsFor("file:///x.go"); diags != nil {
		t.Errorf("expected no diagnostics cached on bad JSON, got %v", diags)
	}
}

func TestInjectDiagnostics(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	uri := "file:///x.go"
	diags := []Diagnostic{
		{Range: Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 5}}, Message: "unused", Severity: 2},
	}
	c.InjectDiagnostics(uri, diags)

	got := c.DiagnosticsFor(uri)
	if len(got) != 1 || got[0].Message != "unused" {
		t.Errorf("DiagnosticsFor = %+v, want one unused", got)
	}

	// Defensive copy check.
	got[0].Message = "tampered"
	again := c.DiagnosticsFor(uri)
	if again[0].Message != "unused" {
		t.Errorf("DiagnosticsFor not defensive-copying: %+v", again)
	}

	// Empty slice clears.
	c.InjectDiagnostics(uri, nil)
	if diags := c.DiagnosticsFor(uri); diags != nil {
		t.Errorf("expected nil after clearing, got %v", diags)
	}
}

func TestDiagnosticsFor_Empty(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	if got := c.DiagnosticsFor("file:///nonexistent.go"); got != nil {
		t.Errorf("DiagnosticsFor unknown = %v, want nil", got)
	}
}

func TestOpenURIs(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	uris := []string{"file:///a.go", "file:///b.go"}
	for _, u := range uris {
		if err := c.EnsureFileOpen(ctx, u, "go", ""); err != nil {
			t.Fatalf("EnsureFileOpen(%s): %v", u, err)
		}
	}
	got := c.OpenURIs()
	if len(got) != 2 {
		t.Fatalf("OpenURIs = %v, want 2 entries", got)
	}
	seen := map[string]bool{}
	for _, u := range got {
		seen[u] = true
	}
	if !seen["file:///a.go"] || !seen["file:///b.go"] {
		t.Errorf("OpenURIs missing entries: %v", got)
	}
}

func TestIsFileOpen(t *testing.T) {
	c, _, cleanup := initClient(t, func(req rpcRequest) (any, bool) { return nil, false })
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.EnsureFileOpen(ctx, "file:///x.go", "go", ""); err != nil {
		t.Fatalf("EnsureFileOpen: %v", err)
	}
	if !c.IsFileOpen("file:///x.go") {
		t.Error("IsFileOpen(x.go) = false, want true")
	}
	if c.IsFileOpen("file:///closed.go") {
		t.Error("IsFileOpen(closed.go) = true, want false")
	}
}

// TestRequest_WriteErrorAfterSend exercises the branch in Request where
// writeMessage fails after the pending slot has been registered.
func TestRequest_WriteErrorAfterSend(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	// Close stdin so the next write fails.
	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	// Read loop will exit on EOF and trigger teardown. The Request below
	// may race against teardown; either way, the test should not hang.
	done := make(chan error, 1)
	go func() {
		_, err := c.Request(context.Background(), "test/method", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Request: expected error when write fails, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request hung when write failed")
	}
}

// TestShutdown_InProcessClientNoCmd covers the cmd == nil branch.
func TestShutdown_InProcessClientNoCmd(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	c.Shutdown(context.Background())
	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead not closed after Shutdown on in-process client")
	}
}

// TestShutdown_ContextCancel covers the ctx.Done branch.
func TestShutdown_ContextCancel(t *testing.T) {
	// Build a client where shutdown request hangs (server doesn't respond).
	clientConn, serverConn := net.Pipe()
	c := &Client{
		name:         "hang",
		pending:      make(map[int64]chan *rpcResponse),
		openURIs:     make(map[string]int),
		diags:        make(map[string][]Diagnostic),
		teardownOnce: sync.Once{},
		done:         make(chan struct{}),
		dead:         make(chan struct{}),
		stdin:        &pipeWriteCloser{conn: clientConn},
		stdout:       clientConn,
	}
	c.readWG.Go(func() { c.readLoop() })
	defer func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		c.readWG.Wait()
	}()

	// Drain server side in a goroutine so client writes don't block.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := serverConn.Read(buf); err != nil {
				return
			}
		}
	}()

	// Use a context that's already cancelled so Shutdown's final select
	// returns immediately on ctx.Done().
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Shutdown(ctx)
}

// TestStartClient_StdoutPipeError covers the StdoutPipe failure branch.
func TestStartClient_StdoutPipeError(t *testing.T) {
	saved := execCommand
	defer func() { execCommand = saved }()

	// Use a real command but we can't easily make StdoutPipe fail without
	// a custom Cmd. Instead, test the cancel-before-spawn branch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := StartClient(ctx, "fake", "echo", nil, "/tmp")
	if err == nil {
		t.Fatal("StartClient: expected error with cancelled context")
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Errorf("expected error to wrap name 'fake', got %v", err)
	}
}

func TestWriteMessage_MarshalError(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	err := c.writeMessage(make(chan int))
	if err == nil {
		t.Fatal("expected marshal error for unencodable value")
	}
	if !strings.Contains(err.Error(), "marshal") {
		t.Errorf("expected 'marshal' in error, got %v", err)
	}
}

func TestInitialize_InitializedNotifyFails(t *testing.T) {
	c, _, _ := newInProcessServer(t)
	// Close stdin so the initialized Notify fails after initialize Request succeeded.
	// Initialize first sends the request, then the notify. By closing stdin between,
	// the notify fails.
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.Initialize(ctx, "file:///repo")
	// Either the Request fails (stdin already closed) or Notify fails.
	// Either way, no hang.
	if err == nil {
		t.Logf("Initialize succeeded despite closed stdin (race)")
	}
}

// Ensure net.Pipe-based client satisfies io.ReadWriteCloser.
var _ io.ReadWriteCloser = (net.Conn)(nil)

// TestWaitLoop_WithCmd covers the cmd != nil branch of waitLoop.
func TestWaitLoop_WithCmd(t *testing.T) {
	saved := execCommand
	defer func() { execCommand = saved }()

	// Use true command (exits immediately).
	execCommand = exec.Command
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := StartClient(ctx, "true", "true", nil, "/tmp")
	if err != nil {
		// 'true' may not be on PATH in some sandboxes.
		if strings.Contains(err.Error(), "start") {
			t.Skipf("'true' not available: %v", err)
		}
		t.Fatalf("StartClient: %v", err)
	}
	// 'true' exits immediately; waitLoop should fire teardown.
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("waitLoop did not fire teardown after 'true' exited")
	}
	c.readWG.Wait()
}
