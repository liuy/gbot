package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if v := os.Getenv("GBOT_FAKE_LSP"); v != "" {
		serveFakeLSP()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// serveFakeLSP is a fake LSP server for subprocess tests.
// Only compiled into test binaries via _test.go.
func serveFakeLSP() {
	r := bufio.NewReader(os.Stdin)
	for {
		var contentLength int
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length:") {
				_, _ = fmt.Sscanf(line, "Content-Length: %d", &contentLength)
			}
		}
		if contentLength <= 0 {
			continue
		}
		body := make([]byte, contentLength)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeFakeResp(*req.ID, map[string]any{
				"capabilities": map[string]any{
					"referencesProvider":      true,
					"renameProvider":          map[string]bool{"prepareSupport": true},
					"hoverProvider":           true,
					"documentSymbolProvider":  true,
					"definitionProvider":      true,
					"implementationProvider":  true,
					"workspaceSymbolProvider": true,
					"codeActionProvider":      true,
				},
			})
		case "initialized":
		case "shutdown":
			writeFakeResp(*req.ID, nil)
		case "exit":
			return
		case "textDocument/references":
			writeFakeResp(*req.ID, []map[string]any{
				{
					"uri": "file:///fake/foo.go",
					"range": map[string]any{
						"start": map[string]any{"line": 1, "character": 2},
						"end":   map[string]any{"line": 1, "character": 8},
					},
				},
			})
		case "textDocument/rename":
			writeFakeResp(*req.ID, map[string]any{
				"changes": map[string]any{
					"file:///fake/foo.go": []map[string]any{
						{
							"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 3}},
							"newText": "baz",
						},
					},
				},
			})
		default:
			writeFakeResp(*req.ID, nil)
		}
	}
}

func writeFakeResp(id int64, result any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	body, _ := json.Marshal(msg)
	_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

// buildFakeBinary returns a path to the test binary itself.
// The test binary, when invoked with GBOT_FAKE_LSP=1, runs serveFakeLSP.
func buildFakeBinary(t *testing.T, cwd string) string {
	t.Helper()
	// Use the test binary itself (TestMain dispatcher).
	return os.Args[0]
}

// ---------- subprocess tests ----------

func TestStartClient_WithFakeBinary(t *testing.T) {
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
	defer c.Shutdown(context.Background())

	if err := c.Initialize(ctx, pathToURI(t.TempDir())); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Verify capabilities.
	caps := c.Capabilities()
	if len(caps) == 0 {
		t.Fatal("expected capabilities after initialize")
	}

	// Verify references request works.
	refs, err := References(ctx, c, "file:///fake/foo.go", Position{Line: 0, Character: 0})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("got %d refs, want 1", len(refs))
	}

	// Verify rename request works.
	edit, err := Rename(ctx, c, "file:///fake/foo.go", Position{Line: 0, Character: 0}, "newName")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if edit == nil {
		t.Fatal("expected non-nil WorkspaceEdit")
	}

	// Verify Name() works.
	if c.Name() != "fake" {
		t.Errorf("Name = %q, want fake", c.Name())
	}

	// Verify waitLoop triggers teardown on exit.
	c.Shutdown(context.Background())
	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead channel not closed after Shutdown")
	}
}

func TestRegistry_ClientFor_WithFakeBinary(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}

	dir := t.TempDir()

	// Manually seed the registry with a spec matching the fake binary.
	r := NewRegistry(dir)
	rec := ServerSpec{
		Name:     "fake",
		Command:  buildFakeBinary(t, dir),
		Args:     nil,
		FileExts: []string{".go"},
		Language: "Go",
		ExtraEnv: []string{"GBOT_FAKE_LSP=1"},
	}
	r.mu.Lock()
	r.extToSpec[".go"] = rec
	r.specs = append(r.specs, rec)
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := r.ForFile(ctx, filepath.Join(dir, "foo.go"))
	if err != nil {
		t.Fatalf("ForFile: %v", err)
	}
	if c == nil {
		t.Fatal("ForFile returned nil client")
	}
	if c.Name() != "fake" {
		t.Errorf("client name = %q, want fake", c.Name())
	}

	// Verify Shutdown is idempotent.
	r.Shutdown(ctx)
	r.Shutdown(ctx)
}

func TestRegistry_SpawnWithBudget_ExceedsRestarts(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	r.mu.Lock()
	r.restarts["fakie"] = 999 // exceeds maxRestarts=2
	r.extToSpec[".go"] = ServerSpec{
		Name:     "fakie",
		Command:  "nonexistent-binary",
		FileExts: []string{".go"},
	}
	r.mu.Unlock()

	_, err := r.clientFor(context.Background(), r.extToSpec[".go"])
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("expected 'exceeded' error, got %v", err)
	}
}

func TestStartClient_CommandNotFound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := StartClient(ctx, "bogus", "this-command-does-not-exist-xyz", nil, "/tmp")
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	_ = err.Error()
}

// When the caller's ctx is canceled after StartClient returns,
// the spawned subprocess must NOT be killed. The ctx is only for the
// handshake; the process should outlive it.
func TestStartClient_CtxCancel_DoesNotKillProcess(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}

	bin := buildFakeBinary(t, t.TempDir())

	// Use a short-lived ctx for spawn.
	spawnCtx, spawnCancel := context.WithTimeout(context.Background(), 5*time.Second)
	c, err := StartClient(spawnCtx, "fake", bin, nil, t.TempDir(), "GBOT_FAKE_LSP=1")
	if err != nil {
		t.Fatalf("StartClient: %v", err)
	}
	defer c.Shutdown(context.Background())

	// Cancel spawn ctx immediately.
	spawnCancel()

	// Give the kernel a moment to deliver SIGKILL if the bug exists.
	// REAL-TIME delay is unavoidable — testing observable kernel behavior, not goroutine scheduling.
	time.Sleep(100 * time.Millisecond)

	// Client must still be alive.
	select {
	case <-c.Dead():
		t.Fatal("subprocess died after spawn ctx cancel — process should outlive the spawn ctx")
	default:
	}

	// And still serve requests.
	initCtx, initCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer initCancel()
	if err := c.Initialize(initCtx, pathToURI(t.TempDir())); err != nil {
		t.Fatalf("Initialize after ctx cancel: %v", err)
	}
}
