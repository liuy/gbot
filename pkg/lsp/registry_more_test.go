package lsp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
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

func TestRegistry_LiveServers_EmptyAndDead(t *testing.T) {
	r := NewRegistry("/tmp")

	if n := len(r.LiveServers()); n != 0 {
		t.Errorf("LiveServers on empty registry = %d, want 0", n)
	}

	c, _, cleanup := newInProcessServer(t)
	defer cleanup()
	c.teardownOnce.Do(func() { close(c.done); close(c.dead) })
	r.mu.Lock()
	r.live[clientKey{"gopls", r.rootDir}] = c
	r.mu.Unlock()

	select {
	case <-c.Dead():
	case <-time.After(time.Second):
		t.Fatal("Dead did not close")
	}
	if n := len(r.LiveServers()); n != 0 {
		t.Errorf("LiveServers on dead client = %d, want 0: a dead entry must be skipped, not merely absent", n)
	}
}

func TestRegistry_LiveServers_AliveClient(t *testing.T) {
	r := NewRegistry("/tmp")

	c, _, cleanup := newInProcessServer(t)
	defer cleanup()

	r.mu.Lock()
	r.live[clientKey{"alive", r.rootDir}] = c
	r.mu.Unlock()

	live := r.LiveServers()
	if len(live) != 1 {
		t.Fatalf("len(LiveServers) = %d, want 1", len(live))
	}
	if live[0].Client != c {
		t.Error("LiveServers()[0].Client is not the injected client")
	}
	if live[0].Root != r.rootDir {
		t.Errorf("LiveServers()[0].Root = %q, want %q", live[0].Root, r.rootDir)
	}
	// This test inserts into live with no entry in specs, so Spec must come
	// from the name-only fallback: status groups by Spec.Name, and a zero Name
	// would drop that server's line.
	if live[0].Spec.Name != "alive" {
		t.Errorf("LiveServers()[0].Spec.Name = %q, want alive", live[0].Spec.Name)
	}
}

func TestRegistry_KillAndEvict_Missing(t *testing.T) {
	r := NewRegistry("/tmp")
	if r.KillAndEvict("gopls", r.rootDir) {
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
	r.live[clientKey{"fake", r.rootDir}] = c
	r.mu.Unlock()

	if !r.KillAndEvict("fake", r.rootDir) {
		t.Fatal("KillAndEvict returned false")
	}
	select {
	case <-c.Dead():
	case <-time.After(2 * time.Second):
		t.Fatal("Kill from KillAndEvict did not close Dead channel")
	}

	r.mu.RLock()
	_, present := r.live[clientKey{"fake", r.rootDir}]
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

	live := r.LiveServers()
	if len(live) != 1 {
		t.Fatalf("len(LiveServers) = %d, want 1", len(live))
	}
	if live[0].Client != c {
		t.Error("LiveServers()[0].Client is not the injected client")
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
		_, present := r.live[clientKey{"injected", r.rootDir}]
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

func TestRegistry_ScanSortsSpecsByName(t *testing.T) {
	orig := execLookPath
	defer func() { execLookPath = orig }()
	execLookPath = func(string) (string, error) { return "/usr/bin/fake-lsp", nil }

	// ScanServers keeps input order, so a shuffled input deterministically
	// proves Scan sorted the list.
	r := NewRegistry("/tmp")
	r.Scan([]ServerSpec{
		{Name: "rust-analyzer", Command: "fake-lsp", FileExts: []string{".rs"}, Language: "Rust"},
		{Name: "gopls", Command: "fake-lsp", FileExts: []string{".go"}, Language: "Go"},
		{Name: "clangd", Command: "fake-lsp", FileExts: []string{".c"}, Language: "C"},
	})

	want := []string{"clangd", "gopls", "rust-analyzer"}
	if got := snapshotNames(r); !slices.Equal(got, want) {
		t.Errorf("Scan spec order = %v, want %v", got, want)
	}
}

func TestRegistry_StartSortsSpecsByName(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	// Discover appends in goroutine-completion order, so five servers make an
	// unsorted result ~120x less likely to coincide with the expected order.
	specs := []ServerSpec{
		{Name: "rust-analyzer", Command: bin, FileExts: []string{".rs"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "clangd", Command: bin, FileExts: []string{".c"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "typescript-language-server", Command: bin, FileExts: []string{".ts"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "gopls", Command: bin, FileExts: []string{".go"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "pyright-langserver", Command: bin, FileExts: []string{".py"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	r := NewRegistry(t.TempDir())
	r.Start(ctx, specs)

	want := []string{"clangd", "gopls", "pyright-langserver", "rust-analyzer", "typescript-language-server"}
	if got := snapshotNames(r); !slices.Equal(got, want) {
		t.Errorf("Start spec order = %v, want %v", got, want)
	}
}

// slog's default handler is process-global, and a fake-LSP Client logs from its
// waitLoop goroutine while tearing down — which can outlive the test that started
// it and land in whichever buffer is default at that moment.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Start's lsp:startup record is the operator's only view of which servers
// actually passed the initialize handshake, so both its presence and the order
// of its names are pinned here.
func TestRegistry_StartLogsSortedServers(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	bin := buildFakeBinary(t, t.TempDir())
	specs := []ServerSpec{
		{Name: "typescript-language-server", Command: bin, FileExts: []string{".ts"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
		{Name: "clangd", Command: bin, FileExts: []string{".c"}, ExtraEnv: []string{"GBOT_FAKE_LSP=1"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var logBuf lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	NewRegistry(t.TempDir()).Start(ctx, specs)

	logged := logBuf.String()
	if !strings.Contains(logged, "lsp:startup") {
		t.Fatalf("no lsp:startup record; log was:\n%s", logged)
	}
	if !strings.Contains(logged, `servers="[clangd typescript-language-server]"`) {
		t.Errorf("lsp:startup servers = want sorted [clangd typescript-language-server]; log was:\n%s", logged)
	}
}

func TestRegistry_StartRecordsEmptyServerList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var logBuf lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	NewRegistry(t.TempDir()).Start(ctx, []ServerSpec{
		{Name: "absent-lsp", Command: "gbot-test-no-such-binary", FileExts: []string{".zz"}},
	})

	logged := logBuf.String()
	if !strings.Contains(logged, "lsp:startup") {
		t.Fatalf("no lsp:startup record although nothing validated — the empty case is the one that must be visible; log was:\n%s", logged)
	}
	if !strings.Contains(logged, "servers=[]") {
		t.Errorf("lsp:startup servers = want empty []; log was:\n%s", logged)
	}
}

func snapshotNames(r *Registry) []string {
	specs := r.Snapshot()
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	return names
}

// Ensure unused imports are still referenced.
var _ io.Reader = (io.Reader)(nil)
