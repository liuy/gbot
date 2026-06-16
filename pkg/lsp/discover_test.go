package lsp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbeOne_LookPathFails(t *testing.T) {
	origLookPath := execLookPath
	defer func() { execLookPath = origLookPath }()
	execLookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if probeOne(ctx, ServerSpec{Name: "x", Command: "x"}, "/tmp") {
		t.Error("probeOne should return false when LookPath fails")
	}
}

func TestProbeOne_StartFails(t *testing.T) {
	origLookPath := execLookPath
	origExecCommand := execCommand
	defer func() {
		execLookPath = origLookPath
		execCommand = origExecCommand
	}()
	execLookPath = func(file string) (string, error) {
		return "/fake/" + file, nil
	}
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("this-binary-does-not-exist-xyz123")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if probeOne(ctx, ServerSpec{Name: "x", Command: "x"}, "/tmp") {
		t.Error("probeOne should return false when Start fails")
	}
}

func TestProbeOne_InitializeFails(t *testing.T) {
	origLookPath := execLookPath
	origExecCommand := execCommand
	defer func() {
		execLookPath = origLookPath
		execCommand = origExecCommand
	}()
	execLookPath = func(file string) (string, error) {
		return "/fake/" + file, nil
	}
	// 'cat' will read stdin and never reply to initialize.
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("cat")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if probeOne(ctx, ServerSpec{Name: "x", Command: "x"}, "/tmp") {
		t.Error("probeOne should return false when Initialize fails")
	}
}

func TestProbeOne_Success(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if !probeOne(ctx, spec, t.TempDir()) {
		t.Error("probeOne should return true for working fake server")
	}
}

func TestDiscover_MixedSpecs(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	specs := []ServerSpec{
		{Name: "fake", Command: bin, FileExts: []string{".go"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "missing1", Command: "this-does-not-exist-1", FileExts: []string{".a"}},
		{Name: "missing2", Command: "this-does-not-exist-2", FileExts: []string{".b"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	alive := Discover(ctx, specs, t.TempDir())
	if len(alive) != 1 {
		t.Fatalf("Discover = %v, want 1 (just fake)", alive)
	}
	if alive[0].Name != "fake" {
		t.Errorf("alive[0].Name = %q, want fake", alive[0].Name)
	}
}

func TestPathToURI_AbsFallback(t *testing.T) {
	// pathToURI's filepath.Abs almost never fails on Linux, but we exercise
	// the code path anyway.
	uri := pathToURI("/tmp/x")
	if !strings.HasPrefix(uri, "file://") {
		t.Errorf("pathToURI missing file:// prefix: %q", uri)
	}
}

func TestScanServers_Mixed(t *testing.T) {
	origLookPath := execLookPath
	defer func() { execLookPath = origLookPath }()
	execLookPath = func(file string) (string, error) {
		if file == "found-binary" {
			return "/fake/found-binary", nil
		}
		return "", errors.New("not found")
	}
	specs := []ServerSpec{
		{Name: "found", Command: "found-binary", FileExts: []string{".x"}},
		{Name: "missing", Command: "missing-binary", FileExts: []string{".y"}},
	}
	alive := ScanServers(specs)
	if len(alive) != 1 {
		t.Fatalf("ScanServers = %v, want 1", alive)
	}
	if alive[0].Name != "found" {
		t.Errorf("alive[0].Name = %q, want found", alive[0].Name)
	}
}

// TestRegistry_ClientFor_ClosedRegistry covers the r.closed branch.
func TestRegistry_ClientFor_ClosedRegistry(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.closed = true
	r.extToSpec[".go"] = ServerSpec{Name: "x", Command: "x", FileExts: []string{".go"}}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.clientFor(ctx, ServerSpec{Name: "x", Command: "x"})
	if err == nil {
		t.Fatal("expected error on closed registry")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRegistry_ClientFor_LiveNotDead covers the fast path.
func TestRegistry_ClientFor_LiveNotDead(t *testing.T) {
	r := NewRegistry("/tmp")
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	r.mu.Lock()
	r.live["x"] = c
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := r.clientFor(ctx, ServerSpec{Name: "x"})
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if got != c {
		t.Error("clientFor did not return existing live client")
	}
}

// TestRegistry_ClientFor_LiveButDead covers the branch where the live entry
// exists but is dead — should fall through to spawn (which will fail).
func TestRegistry_ClientFor_LiveButDead(t *testing.T) {
	r := NewRegistry("/tmp")
	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })

	r.mu.Lock()
	r.live["x"] = c
	r.extToSpec[".go"] = ServerSpec{Name: "x", Command: "this-does-not-exist-xyz", FileExts: []string{".go"}}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := r.clientFor(ctx, ServerSpec{Name: "x", Command: "this-does-not-exist-xyz"})
	if err == nil {
		t.Fatal("expected error when spawn fails after live-but-dead")
	}
	if !strings.Contains(err.Error(), "lookpath") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRegistry_ClientFor_SingleFlight_WaitThenSucceed covers the
// "wait for another goroutine's spawn, then re-check" branch.
func TestRegistry_ClientFor_SingleFlight_WaitThenSucceed(t *testing.T) {
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
	r.mu.Lock()
	// Pre-populate an in-progress session that we'll close after a delay.
	sess := &spawnSession{done: make(chan struct{})}
	r.sessions["fake"] = sess
	// And pre-populate live with the client we'll "produce" once the
	// waiting goroutine wakes up.
	clientToReturn, _, cleanup := newInProcessServer(t)
	defer cleanup()
	r.mu.Unlock()

	// Spawn a waiter that will block on sess.done, then re-check live.
	var (
		wg      sync.WaitGroup
		gotName string
		gotErr  error
	)
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, err := r.clientFor(ctx, spec)
		if err == nil {
			gotName = c.Name()
		} else {
			gotErr = err
		}
	})

	// Give the waiter a moment to register.
	// REAL-TIME: coordinating goroutine scheduling for single-flight test.
	time.Sleep(50 * time.Millisecond)

	// Now populate live with the "produced" client and close the session.
	r.mu.Lock()
	r.live["fake"] = clientToReturn
	delete(r.sessions, "fake")
	r.mu.Unlock()
	close(sess.done)

	wg.Wait()
	if gotErr != nil {
		t.Fatalf("clientFor returned error after waiting: %v", gotErr)
	}
	if gotName != "fake" {
		t.Errorf("got name = %q, want fake", gotName)
	}
}

// TestRegistry_ClientFor_SingleFlight_WaitCtxCancel covers the ctx.Done
// branch inside the wait loop.
func TestRegistry_ClientFor_SingleFlight_WaitCtxCancel(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	sess := &spawnSession{done: make(chan struct{})}
	r.sessions["x"] = sess
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := r.clientFor(ctx, ServerSpec{Name: "x", Command: "x"})
	if err == nil {
		t.Fatal("expected ctx.Done error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}
