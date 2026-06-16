package lsp

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRegistry_SpecForFile(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.extToSpec[".go"] = ServerSpec{Name: "gopls", Language: "Go"}
	r.mu.Unlock()

	spec, ok := r.SpecForFile("/x/foo.go")
	if !ok {
		t.Fatal("SpecForFile(.go) = false, want true")
	}
	if spec.Name != "gopls" {
		t.Errorf("spec.Name = %q, want gopls", spec.Name)
	}

	if _, ok := r.SpecForFile("/x/foo.py"); ok {
		t.Error("SpecForFile(.py) = true, want false")
	}

	if _, ok := r.SpecForFile("/x/README"); ok {
		t.Error("SpecForFile(README) = true, want false")
	}
}

func TestRegistry_NumServers(t *testing.T) {
	r := NewRegistry("/tmp")
	if n := r.NumServers(); n != 0 {
		t.Errorf("NumServers = %d, want 0", n)
	}
	r.mu.Lock()
	r.specs = []ServerSpec{
		{Name: "gopls"},
		{Name: "tsserver"},
	}
	r.mu.Unlock()
	if n := r.NumServers(); n != 2 {
		t.Errorf("NumServers = %d, want 2", n)
	}
}

func TestRegistry_StartedClient(t *testing.T) {
	r := NewRegistry("/tmp")

	if _, ok := r.StartedClient("gopls"); ok {
		t.Error("StartedClient on empty registry = true, want false")
	}

	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	r.mu.Lock()
	r.live["gopls"] = c
	r.mu.Unlock()

	if _, ok := r.StartedClient("gopls"); ok {
		t.Error("StartedClient on dead client = true, want false")
	}
}

func TestRegistry_StartedClient_Alive(t *testing.T) {
	r := NewRegistry("/tmp")

	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	r.mu.Lock()
	r.live["alive"] = c
	r.mu.Unlock()

	got, ok := r.StartedClient("alive")
	if !ok {
		t.Fatal("StartedClient on live client = false, want true")
	}
	if got != c {
		t.Error("StartedClient returned different client")
	}
}

func TestRegistry_KillAndEvict_Missing(t *testing.T) {
	r := NewRegistry("/tmp")
	if r.KillAndEvict("gopls") {
		t.Error("KillAndEvict on empty registry = true, want false")
	}
}

func TestRegistry_KillAndEvict_Subprocess(t *testing.T) {
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
	r := NewRegistry(t.TempDir())
	r.mu.Lock()
	r.live["fake"] = c
	r.mu.Unlock()

	if !r.KillAndEvict("fake") {
		t.Fatal("KillAndEvict returned false")
	}
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Kill from KillAndEvict did not close Dead channel")
	}

	r.mu.RLock()
	_, present := r.live["fake"]
	r.mu.RUnlock()
	if present {
		t.Error("client still in live map after KillAndEvict")
	}
}

func TestRegistry_InjectClient(t *testing.T) {
	r := NewRegistry(t.TempDir())

	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	c := NewTestClient("injected", clientConn)
	defer c.readWG.Wait()

	spec := ServerSpec{Name: "injected", Language: "Go", FileExts: []string{".go"}}
	r.InjectClient("injected", spec, c)

	gotSpec, ok := r.SpecForFile("/x/foo.go")
	if !ok {
		t.Fatal("SpecForFile(.go) = false after InjectClient, want true")
	}
	if gotSpec.Name != "injected" {
		t.Errorf("gotSpec.Name = %q, want injected", gotSpec.Name)
	}

	if _, ok := r.StartedClient("injected"); !ok {
		t.Error("StartedClient(injected) = false, want true")
	}

	_ = serverConn.Close()
	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead not closed")
	}
	deadline := time.After(time.Second)
	for {
		r.mu.RLock()
		_, present := r.live["injected"]
		r.mu.RUnlock()
		if !present {
			break
		}
		select {
		case <-deadline:
			t.Fatal("InjectClient did not evict after Dead")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRegistry_ForSpec(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	r.extToSpec[".go"] = ServerSpec{
		Name:     "bogus",
		Command:  "this-does-not-exist",
		FileExts: []string{".go"},
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.ForSpec(ctx, ServerSpec{Name: "bogus", Command: "this-does-not-exist"})
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	if !strings.Contains(err.Error(), "lookpath") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegistry_Start_Nil(t *testing.T) {
	r := NewRegistry("/tmp")
	r.Start(context.Background(), nil)
	if n := r.NumServers(); n != 0 {
		t.Errorf("Start(nil) left %d specs, want 0", n)
	}
}

func TestRegistry_Scan_Found(t *testing.T) {
	r := NewRegistry("/tmp")
	origLookPath := execLookPath
	defer func() { execLookPath = origLookPath }()
	execLookPath = func(file string) (string, error) {
		if file == "test-binary" {
			return "/fake/path/test-binary", nil
		}
		return "", os.ErrNotExist
	}
	specs := []ServerSpec{
		{Name: "test-binary", Command: "test-binary", FileExts: []string{".x"}, Language: "X"},
		{Name: "missing-binary", Command: "missing-binary", FileExts: []string{".y"}, Language: "Y"},
	}
	r.Scan(specs)

	if n := r.NumServers(); n != 1 {
		t.Errorf("NumServers = %d, want 1", n)
	}
	if !r.HasExtension(".x") {
		t.Error("HasExtension(.x) = false, want true")
	}
	if r.HasExtension(".y") {
		t.Error("HasExtension(.y) = true, want false")
	}
}

func TestRegistry_LSPStringLocked(t *testing.T) {
	r := NewRegistry("/tmp")
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.lspStringLocked(); s != "" {
		t.Errorf("lspStringLocked empty = %q, want empty", s)
	}

	r.specs = []ServerSpec{
		{Name: "gopls", Language: "Go"},
		{Name: "tsserver", Language: "TypeScript"},
	}
	s := r.lspStringLocked()
	if s != "gopls (Go) | tsserver (TypeScript)" {
		t.Errorf("lspStringLocked = %q", s)
	}
}

// Ensure unused imports are still referenced.
var _ io.Reader = (io.Reader)(nil)
