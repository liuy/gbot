package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestShutdown_SubprocessKilledAfterTimeout covers the path where
// Shutdown's 200ms timer fires and the subprocess must be SIGKILLed.
// Uses 'sleep infinity' style behavior via tail -f /dev/null which
// never exits on its own.
func TestShutdown_SubprocessKilledAfterTimeout(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// tail -f /dev/null reads stdin forever and ignores EOF.
	c, err := StartClient(ctx, "tail", "tail", []string{"-f", "/dev/null"}, "/tmp")
	if err != nil {
		t.Skipf("'tail' not available: %v", err)
	}
	if c.cmd == nil || c.cmd.Process == nil {
		t.Fatal("expected non-nil cmd.Process")
	}

	c.Shutdown(context.Background())

	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Dead channel not closed after SIGKILL")
	}
}

// TestReadLoop_NotificationDropped covers the "drop notification that
// isn't publishDiagnostics" branch.
func TestReadLoop_NotificationDropped(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","method":"window/showMessage","params":{"message":"hi"}}`))
	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","method":"$/progress","params":{}}`))
	drainReadLoop()
}

// TestReadLoop_BadProbeJSON covers the readLoop branch where the probe parse fails.
func TestReadLoop_BadProbeJSON(t *testing.T) {
	_, conn, cleanup := rawPipeTest(t)
	defer cleanup()
	// Body that isn't valid JSON at all.
	rawWrite(t, conn, toFramed(`not-valid-json-at-all`))
	drainReadLoop()
}

// TestRequest_TeardownDuringRequest exercises the `<-c.dead` branch in Request's
// select, which fires when teardown happens while waiting for a response.
func TestRequest_TeardownDuringRequest(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()
	// Block test/method so the server never responds — Request blocks until
	// teardown fires.
	srv.blockMethods = map[string]bool{"test/method": true}

	done := make(chan error, 1)
	go func() {
		_, err := c.Request(context.Background(), "test/method", nil)
		done <- err
	}()

	// REAL-TIME: give goroutine time to register pending slot before teardown.
	time.Sleep(50 * time.Millisecond)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error when teardown fires during Request")
		} else if !containsSubstring(err.Error(), "server is not running") {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request hung")
	}
}

func TestStoreDiagnostics_GoodJSON(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	params := json.RawMessage(`{"uri":"file:///x.go","diagnostics":[{"range":{"start":{"line":1,"character":0},"end":{"line":1,"character":5}},"message":"unused var","severity":2}]}`)
	c.storeDiagnostics(params)

	got := c.DiagnosticsFor("file:///x.go")
	if len(got) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(got))
	}
	if got[0].Message != "unused var" {
		t.Errorf("Message = %q", got[0].Message)
	}
	if got[0].Severity != 2 {
		t.Errorf("Severity = %d, want 2", got[0].Severity)
	}
}

func TestStoreDiagnostics_EmptyArray(t *testing.T) {
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	c.InjectDiagnostics("file:///x.go", []Diagnostic{{Message: "x"}})
	c.storeDiagnostics(json.RawMessage(`{"uri":"file:///x.go","diagnostics":[]}`))
	if got := c.DiagnosticsFor("file:///x.go"); got != nil {
		t.Errorf("expected nil after empty array publishDiagnostics, got %v", got)
	}
}

// TestRegistry_Start_WithMissingServer covers Start with specs that all fail discovery.
func TestRegistry_Start_WithMissingServer(t *testing.T) {
	r := NewRegistry("/tmp")
	r.Start(context.Background(), []ServerSpec{
		{Name: "missing", Command: "this-does-not-exist-xyz", FileExts: []string{".x"}, Language: "X"},
	})
	if n := r.NumServers(); n != 0 {
		t.Errorf("Start with missing server left %d specs, want 0", n)
	}
}

// TestRegistry_ClientFor_SpawnSuccess_DeadEviction verifies the post-spawn
// eviction goroutine removes the client from live and increments restarts.
func TestRegistry_ClientFor_SpawnSuccess_DeadEviction(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	spec := ServerSpec{
		Name:     "fake",
		Command:  bin,
		FileExts: []string{".go"},
		ExtraEnv: []string{"GBOT_FAKE_LSP=1"},
	}
	r := NewRegistry(t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := r.clientFor(ctx, spec)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	c.Kill()
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Dead not closed after Kill")
	}

	deadline := time.After(2 * time.Second)
	for {
		r.mu.RLock()
		_, present := r.live["fake"]
		rest := r.restarts["fake"]
		r.mu.RUnlock()
		if !present && rest > 0 {
			break
		}
		select {
		case <-deadline:
			r.mu.RLock()
			p := r.live["fake"]
			rest := r.restarts["fake"]
			r.mu.RUnlock()
			t.Fatalf("eviction goroutine did not run: present=%v restarts=%d", p != nil, rest)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestRegistry_ClientFor_RegistryDone covers the r.done branch in the eviction goroutine.
func TestRegistry_ClientFor_RegistryDone(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	spec := ServerSpec{
		Name:     "fake",
		Command:  bin,
		FileExts: []string{".go"},
		ExtraEnv: []string{"GBOT_FAKE_LSP=1"},
	}
	r := NewRegistry(t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := r.clientFor(ctx, spec)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}

	r.mu.Lock()
	r.closed = true
	close(r.done)
	r.mu.Unlock()

	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
	}
}

// TestReadLoop_ResponseForKnownID exercises the happy path of readLoop's
// response delivery.
func TestReadLoop_ResponseForKnownID(t *testing.T) {
	c, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[42] = ch
	c.mu.Unlock()

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":42,"result":{"x":1}}`))
	drainReadLoop()

	select {
	case resp := <-ch:
		if resp == nil {
			t.Fatal("got nil response")
		}
		if !strings.Contains(string(resp.result), `"x":1`) {
			t.Errorf("result = %s, want x:1", string(resp.result))
		}
	case <-time.After(time.Second):
		t.Fatal("response not delivered to pending channel")
	}
}

// TestReadLoop_ResponseWithError covers the env.Error branch.
func TestReadLoop_ResponseWithError(t *testing.T) {
	c, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[7] = ch
	c.mu.Unlock()

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"method not found"}}`))
	drainReadLoop()

	select {
	case resp := <-ch:
		if resp == nil {
			t.Fatal("got nil response")
		}
		if resp.err == nil {
			t.Fatal("expected error in response")
		}
		if !strings.Contains(resp.err.Error(), "method not found") {
			t.Errorf("err = %v", resp.err)
		}
	case <-time.After(time.Second):
		t.Fatal("error response not delivered")
	}
}

// TestReadLoop_ResponseAfterPendingRemoved exercises the default branch
// in the channel send (teardown already cleared the slot).
func TestReadLoop_ResponseAfterPendingRemoved(t *testing.T) {
	c, conn, cleanup := rawPipeTest(t)
	defer cleanup()

	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[99] = ch
	delete(c.pending, 99)
	c.mu.Unlock()

	rawWrite(t, conn, toFramed(`{"jsonrpc":"2.0","id":99,"result":null}`))
	drainReadLoop()
}

// TestRequest_CancelWithNotify exercises the ctx.Done branch where
// Notify gets called to cancel the request.
func TestRequest_CancelWithNotify(t *testing.T) {
	c, srv, cleanup := newInProcessServer(t)
	defer cleanup()
	// Block the method so Request doesn't get a quick echo response.
	srv.blockMethods = map[string]bool{"server/never-responds": true}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Request(ctx, "server/never-responds", nil)
	if err == nil {
		t.Fatal("expected ctx.Err from cancelled Request")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

// TestStartClient_NoExtraEnv exercises the `len(extraEnv) == 0` branch.
func TestStartClient_NoExtraEnv(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := StartClient(ctx, "fake", bin, nil, t.TempDir())
	if err != nil {
		t.Skipf("StartClient without extraEnv: %v", err)
	}
	c.Shutdown(context.Background())
}

// TestApplyWorkspaceEdit_DocumentChanges_MixedSuccess covers the case
// where extractTextDocumentEdit returns edits that fail to apply,
// alongside a resource op that succeeds.
func TestApplyWorkspaceEdit_DocumentChanges_MixedSuccess(t *testing.T) {
	dir := t.TempDir()
	goodPath := dir + "/good.go"
	if err := osWriteFile(goodPath, []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	goodURI := pathToURI(goodPath)
	goodDC := map[string]any{
		"textDocument": map[string]any{"uri": goodURI, "version": 1},
		"edits": []map[string]any{
			{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "newText": "x"},
		},
	}
	badDC := map[string]any{
		"textDocument": map[string]any{"uri": "file:///nonexistent/path/bad.go", "version": 1},
		"edits": []map[string]any{
			{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "newText": "x"},
		},
	}
	edit := &WorkspaceEdit{DocumentChanges: []map[string]any{goodDC, badDC}}

	changed, err := ApplyWorkspaceEdit(edit)
	if err != nil {
		t.Fatalf("ApplyWorkspaceEdit: %v", err)
	}
	if len(changed) != 1 {
		t.Errorf("changed = %v, want 1 (only good.go)", changed)
	}
}

// TestUriToPath_Relative covers the filepath.Abs fallback path.
func TestUriToPath_Relative(t *testing.T) {
	got := uriToPath("file://relative/path")
	if strings.Contains(got, "file://") {
		t.Errorf("uriToPath left file:// prefix: %q", got)
	}
}

// osWriteFile is a thin wrapper around os.WriteFile to keep imports minimal
// in this test file.
func osWriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

// TestRegistry_ScanServers_RealGoBinary covers ScanServers finding 'go'
// (or any binary likely on PATH).
func TestRegistry_ScanServers_RealGoBinary(t *testing.T) {
	specs := []ServerSpec{
		{Name: "go-binary", Command: "go", FileExts: []string{".x"}, Language: "X"},
	}
	alive := ScanServers(specs)
	// If 'go' is on PATH, alive should have 1. Otherwise 0.
	for _, s := range alive {
		if s.Command != "go" {
			t.Errorf("alive command = %q, want go", s.Command)
		}
	}
}

// TestSpawnClient_InitializeTimeout covers the spawnClient path where
// Initialize fails after Start succeeds.
func TestSpawnClient_InitializeTimeout(t *testing.T) {
	origExecCommand := execCommand
	defer func() { execCommand = origExecCommand }()
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Use 'cat' which reads stdin forever and never replies to initialize.
		return exec.Command("cat")
	}
	origLookPath := execLookPath
	defer func() { execLookPath = origLookPath }()
	execLookPath = func(file string) (string, error) {
		return "/usr/bin/cat", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := spawnClient(ctx, ServerSpec{Name: "cat", Command: "cat"}, "/tmp")
	if err == nil {
		t.Fatal("expected error when Initialize times out")
	}
	if !containsSubstring(err.Error(), "initialize") {
		t.Errorf("unexpected error: %v", err)
	}
}
