package lsp

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// projectRootFor walks up to "/", so the tests here whose premise is "this
// directory has no project" depend on /tmp and / carrying no go.mod,
// package.json or Cargo.toml. If TestRegistry_RootFor_NoProjectFallsBack or
// TestRegistry_CheckWriteRoot_ForeignNoMarker goes red without a code change,
// look for a stray marker in /tmp before looking at the code.

func fakeGoSpec(t *testing.T, r *Registry) ServerSpec {
	t.Helper()
	spec := ServerSpec{
		Name:     "fake",
		Command:  buildFakeBinary(t, r.rootDir),
		FileExts: []string{".go"},
		Language: "Go",
		ExtraEnv: []string{"GBOT_FAKE_LSP=1"},
	}
	// Direct seeding, not Scan: ScanServers drops any spec whose Command is not
	// a PATH entry, which would surface as a routing error rather than the
	// spawn behaviour under test.
	r.mu.Lock()
	r.extToSpec[".go"] = spec
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return spec
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// moduleDir returns a temp dir that projectRootFor recognises as a project root.
func moduleDir(t *testing.T, module string) string {
	t.Helper()
	dir := cleanTestDir(t)
	writeFileAt(t, filepath.Join(dir, "go.mod"), "module "+module+"\n\ngo 1.27\n")
	writeFileAt(t, filepath.Join(dir, "a.go"), "package a\n")
	return dir
}

func TestRegistry_RootFor_InsideDefault(t *testing.T) {
	mainDir := cleanTestDir(t)
	writeFileAt(t, filepath.Join(mainDir, "go.mod"), "module example.com/main\n\ngo 1.27\n")
	r := NewRegistry(mainDir)

	got := r.RootFor(filepath.Join(mainDir, "pkg", "x.go"))
	if got != mainDir {
		t.Errorf("RootFor = %q, want %q", got, mainDir)
	}
}

func TestRegistry_RootFor_ForeignModule(t *testing.T) {
	mainDir := cleanTestDir(t)
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)

	got := r.RootFor(filepath.Join(wtDir, "pkg", "x.go"))
	if got != wtDir {
		t.Errorf("RootFor = %q, want %q", got, wtDir)
	}
}

func TestRegistry_RootFor_NestedModuleUsesDefault(t *testing.T) {
	mainDir := cleanTestDir(t)
	writeFileAt(t, filepath.Join(mainDir, "go.mod"), "module example.com/main\n\ngo 1.27\n")
	sub := filepath.Join(mainDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFileAt(t, filepath.Join(sub, "go.mod"), "module example.com/sub\n\ngo 1.27\n")
	r := NewRegistry(mainDir)

	got := r.RootFor(filepath.Join(sub, "x.go"))
	if got != mainDir {
		t.Errorf("RootFor = %q, want %q: the launch workspace wins over a marker nested inside it", got, mainDir)
	}
}

func TestRegistry_RootFor_NoProjectFallsBack(t *testing.T) {
	mainDir := cleanTestDir(t)
	bareDir := cleanTestDir(t)
	writeFileAt(t, filepath.Join(bareDir, "x.go"), "package x\n")
	r := NewRegistry(mainDir)

	got := r.RootFor(filepath.Join(bareDir, "x.go"))
	if got != mainDir {
		t.Errorf("RootFor = %q, want %q: a file with no project stays on the launch server", got, mainDir)
	}
}

func TestRegistry_RootFor_DoesNotConsultLiveMap(t *testing.T) {
	mainDir := cleanTestDir(t)
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)

	clientConn, serverConn := net.Pipe()
	c := NewTestClient("injected", clientConn)
	defer func() {
		_ = serverConn.Close()
		c.readWG.Wait()
		_ = clientConn.Close()
	}()
	spec := ServerSpec{Name: "injected", Language: "Go", FileExts: []string{".go"}}
	r.InjectClientInRoot("injected", wtDir, spec, c)

	got := r.RootFor(filepath.Join(mainDir, "x.go"))
	if got != mainDir {
		t.Errorf("RootFor = %q, want %q: root choice is path-derived, not map-derived", got, mainDir)
	}
}

func TestRegistry_ForFile_DifferentRootsDifferentClients(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	mainDir := moduleDir(t, "example.com/main")
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)
	fakeGoSpec(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c1, err := r.ForFile(ctx, filepath.Join(mainDir, "a.go"))
	if err != nil {
		t.Fatalf("ForFile(main): %v", err)
	}
	c2, err := r.ForFile(ctx, filepath.Join(wtDir, "b.go"))
	if err != nil {
		t.Fatalf("ForFile(worktree): %v", err)
	}
	c3, err := r.ForFile(ctx, filepath.Join(mainDir, "c.go"))
	if err != nil {
		t.Fatalf("ForFile(main again): %v", err)
	}

	if c1 == c2 {
		t.Error("a foreign root reused the launch-workspace client: one server cannot index two trees")
	}
	if c1 != c3 {
		t.Error("the same root produced a second client: the pool must key on (spec, root), not path")
	}
	if n := len(r.LiveServers()); n != 2 {
		t.Errorf("len(LiveServers) = %d, want 2", n)
	}
}

func TestRegistry_ForFile_ForeignRootServerInitializedAtThatRoot(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	mainDir := moduleDir(t, "example.com/main")
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)
	fakeGoSpec(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := r.ForFile(ctx, filepath.Join(wtDir, "a.go"))
	if err != nil {
		t.Fatalf("ForFile: %v", err)
	}
	syms, err := WorkspaceSymbol(ctx, c, "rootprobe")
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("len(symbols) = %d, want 1", len(syms))
	}
	got := URItoPath(syms[0].Location.URI)
	want := wtDir + "/pkg.go"
	if got != want {
		t.Errorf("server rootUri produced %q, want %q", got, want)
	}
}

func TestRegistry_Cap_EvictsOldestExtraRoot(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	mainDir := moduleDir(t, "example.com/main")
	r1 := moduleDir(t, "example.com/r1")
	r2 := moduleDir(t, "example.com/r2")
	r3 := moduleDir(t, "example.com/r3")
	r := NewRegistry(mainDir)
	fakeGoSpec(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	if _, err := r.ForFile(ctx, filepath.Join(mainDir, "a.go")); err != nil {
		t.Fatalf("ForFile(main): %v", err)
	}
	c1, err := r.ForFile(ctx, filepath.Join(r1, "a.go"))
	if err != nil {
		t.Fatalf("ForFile(r1): %v", err)
	}
	if _, err := r.ForFile(ctx, filepath.Join(r2, "a.go")); err != nil {
		t.Fatalf("ForFile(r2): %v", err)
	}
	if _, err := r.ForFile(ctx, filepath.Join(r3, "a.go")); err != nil {
		t.Fatalf("ForFile(r3): %v", err)
	}

	live := r.LiveServers()
	if len(live) != 3 {
		t.Fatalf("len(LiveServers) = %d, want 3 (default root is never an extra)", len(live))
	}
	if live[0].Root != mainDir {
		t.Errorf("LiveServers()[0].Root = %q, want %q", live[0].Root, mainDir)
	}
	roots := make([]string, 0, len(live))
	for _, ls := range live {
		roots = append(roots, ls.Root)
	}
	slices.Sort(roots)
	want := []string{mainDir, r2, r3}
	slices.Sort(want)
	if !slices.Equal(roots, want) {
		t.Errorf("live roots = %v, want %v", roots, want)
	}
	for _, ls := range live {
		if ls.Root == r1 {
			t.Errorf("evicted root %q is still live", r1)
		}
	}
	select {
	case <-c1.Dead():
	case <-time.After(5 * time.Second):
		t.Error("the evicted root's client was not killed")
	}
}

func TestRegistry_ForFile_AfterEvict_Respawns(t *testing.T) {
	if os.Getenv("GBOT_TEST_SKIP_SUBPROCESS") != "" {
		t.Skip("GBOT_TEST_SKIP_SUBPROCESS is set")
	}
	mainDir := moduleDir(t, "example.com/main")
	r1 := moduleDir(t, "example.com/r1")
	r2 := moduleDir(t, "example.com/r2")
	r3 := moduleDir(t, "example.com/r3")
	r := NewRegistry(mainDir)
	fakeGoSpec(t, r)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := r.ForFile(ctx, filepath.Join(mainDir, "a.go")); err != nil {
		t.Fatalf("ForFile(main): %v", err)
	}
	c1, err := r.ForFile(ctx, filepath.Join(r1, "a.go"))
	if err != nil {
		t.Fatalf("ForFile(r1): %v", err)
	}
	for _, dir := range []string{r2, r3} {
		if _, err := r.ForFile(ctx, filepath.Join(dir, "a.go")); err != nil {
			t.Fatalf("ForFile(%s): %v", dir, err)
		}
	}
	revived, err := r.ForFile(ctx, filepath.Join(r1, "a.go"))
	if err != nil {
		t.Fatalf("ForFile(r1) after eviction: %v", err)
	}
	if revived == nil {
		t.Fatal("ForFile returned a nil client")
	}
	if revived == c1 {
		t.Error("the respawn returned the evicted client object instead of a fresh one")
	}

	live := r.LiveServers()
	if len(live) != 3 {
		t.Fatalf("len(LiveServers) = %d, want 3", len(live))
	}
	roots := make([]string, 0, len(live))
	for _, ls := range live {
		roots = append(roots, ls.Root)
	}
	slices.Sort(roots)
	want := []string{mainDir, r1, r3}
	slices.Sort(want)
	if !slices.Equal(roots, want) {
		t.Errorf("live roots = %v, want %v", roots, want)
	}
	for _, ls := range live {
		if ls.Root == r2 {
			t.Errorf("root %q should have been evicted to make room for the respawn", r2)
		}
	}
}

func TestRegistry_KillAndEvict_PerRoot(t *testing.T) {
	mainDir := cleanTestDir(t)
	rootA := moduleDir(t, "example.com/a")
	rootB := moduleDir(t, "example.com/b")
	r := NewRegistry(mainDir)
	spec := ServerSpec{Name: "fakels", Language: "Go", FileExts: []string{".go"}}

	pipeClient(t, r, "fakels", rootA, spec)
	pipeClient(t, r, "fakels", rootB, spec)

	if !r.KillAndEvict("fakels", rootA) {
		t.Fatal("KillAndEvict(rootA) = false, want true")
	}
	live := r.LiveServers()
	if len(live) != 1 {
		t.Fatalf("len(LiveServers) = %d, want 1", len(live))
	}
	if live[0].Root != rootB {
		t.Errorf("LiveServers()[0].Root = %q, want %q", live[0].Root, rootB)
	}
	if r.KillAndEvict("fakels", "/nope") {
		t.Error("KillAndEvict on an unknown root = true, want false")
	}
}

func TestRegistry_LiveServers_DefaultFirst(t *testing.T) {
	mainDir := cleanTestDir(t)
	extra := moduleDir(t, "example.com/extra")
	r := NewRegistry(mainDir)
	spec := ServerSpec{Name: "injected", Language: "Go", FileExts: []string{".go"}}

	pipeClient(t, r, "injected", mainDir, spec)
	pipeClient(t, r, "injected", extra, spec)

	live := r.LiveServers()
	if len(live) != 2 {
		t.Fatalf("len(LiveServers) = %d, want 2", len(live))
	}
	if live[0].Root != r.DefaultRoot() {
		t.Errorf("LiveServers()[0].Root = %q, want the default root %q", live[0].Root, r.DefaultRoot())
	}
	if live[1].Root != extra {
		t.Errorf("LiveServers()[1].Root = %q, want %q", live[1].Root, extra)
	}
	for i, ls := range live {
		if ls.Spec.Name != "injected" {
			t.Errorf("LiveServers()[%d].Spec.Name = %q, want injected", i, ls.Spec.Name)
		}
		if ls.Client == nil {
			t.Errorf("LiveServers()[%d].Client = nil", i)
		}
	}
}

func TestRegistry_Shutdown_ClosesExtraRoots(t *testing.T) {
	mainDir := cleanTestDir(t)
	r1 := moduleDir(t, "example.com/r1")
	r2 := moduleDir(t, "example.com/r2")
	r := NewRegistry(mainDir)
	spec := ServerSpec{Name: "injected", Language: "Go", FileExts: []string{".go"}}

	// Drain, not close-first: closing the server side would EOF each readLoop
	// and close Dead on its own, so the Dead assertions below would pass even
	// if Shutdown never touched the client.
	clients := []*Client{
		drainedPipeClient(t, r, "injected", mainDir, spec),
		drainedPipeClient(t, r, "injected", r1, spec),
		drainedPipeClient(t, r, "injected", r2, spec),
	}
	if n := len(r.LiveServers()); n != 3 {
		t.Fatalf("len(LiveServers) before Shutdown = %d, want 3", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r.Shutdown(ctx)

	if n := len(r.LiveServers()); n != 0 {
		t.Errorf("len(LiveServers) after Shutdown = %d, want 0", n)
	}
	for i, c := range clients {
		select {
		case <-c.Dead():
		case <-time.After(5 * time.Second):
			t.Errorf("client %d (%s) was not shut down", i, c.Name())
		}
	}
}

func TestRegistry_CheckWriteRoot_InsideDefault(t *testing.T) {
	mainDir := cleanTestDir(t)
	r := NewRegistry(mainDir)

	if err := r.CheckWriteRoot(filepath.Join(mainDir, "x.py")); err != nil {
		t.Errorf("CheckWriteRoot(inside default) = %v, want nil", err)
	}
}

func TestRegistry_CheckWriteRoot_ForeignWithMarker(t *testing.T) {
	mainDir := cleanTestDir(t)
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)

	if err := r.CheckWriteRoot(filepath.Join(wtDir, "x.go")); err != nil {
		t.Errorf("CheckWriteRoot(foreign module) = %v, want nil", err)
	}
}

// A marker only roots a server for the extensions that marker belongs to: a .py
// file under a go.mod still has no server of its own, so its writes are refused
// exactly like a marker-less directory's.
func TestRegistry_CheckWriteRoot_ForeignMarkerWrongExt(t *testing.T) {
	mainDir := cleanTestDir(t)
	wtDir := moduleDir(t, "example.com/wt")
	r := NewRegistry(mainDir)

	file := filepath.Join(wtDir, "x.py")
	want := "file " + wtDir + "/x.py is outside the workspace root " + mainDir +
		" and its directory has no project marker for its file type (go.mod, package.json or Cargo.toml), so no language server" +
		" can be rooted at its directory; pass a file inside " + mainDir + " or a file whose directory has a marker for that extension"

	err := r.CheckWriteRoot(file)
	if err == nil {
		t.Fatalf("CheckWriteRoot(%s) = nil, want the guard error", file)
	}
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestRegistry_CheckWriteRoot_ForeignNoMarker(t *testing.T) {
	mainDir := cleanTestDir(t)
	bareDir := cleanTestDir(t)
	r := NewRegistry(mainDir)

	file := filepath.Join(bareDir, "x.py")
	want := "file " + bareDir + "/x.py is outside the workspace root " + mainDir +
		" and its directory has no project marker for its file type (go.mod, package.json or Cargo.toml), so no language server" +
		" can be rooted at its directory; pass a file inside " + mainDir + " or a file whose directory has a marker for that extension"

	err := r.CheckWriteRoot(file)
	if err == nil {
		t.Fatalf("CheckWriteRoot(%s) = nil, want the guard error", file)
	}
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// pipeClient injects a net.Pipe-backed client and closes it at test end.
func pipeClient(t *testing.T, r *Registry, name, root string, spec ServerSpec) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	c := NewTestClient(name, clientConn)
	t.Cleanup(func() {
		_ = clientConn.Close()
		c.readWG.Wait()
		_ = serverConn.Close()
	})
	r.InjectClientInRoot(name, root, spec, c)
	return c
}

// drainedPipeClient is pipeClient with the server side drained, so a
// Client.Shutdown's write can complete instead of blocking on a peer that
// never reads.
func drainedPipeClient(t *testing.T, r *Registry, name, root string, spec ServerSpec) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := serverConn.Read(buf); err != nil {
				return
			}
		}
	}()
	c := NewTestClient(name, clientConn)
	t.Cleanup(func() {
		_ = clientConn.Close()
		c.readWG.Wait()
		_ = serverConn.Close()
	})
	r.InjectClientInRoot(name, root, spec, c)
	return c
}
