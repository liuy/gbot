package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/lsp"
	mcp "github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/plugins"
	"github.com/liuy/gbot/pkg/reload"
)

func TestCleanup_ClosesAllEngines(t *testing.T) {
	mgr := engine.NewEngineManager()
	var closeCount atomic.Int32
	for i := range 3 {
		eng := engine.New(&engine.Params{})
		eng.SetOnClose(func(sessionID string) {
			closeCount.Add(1)
		})
		mgr.Add(&engine.EngineViewState{
			Engine: eng,
			Model:  "test",
			ID:     fmt.Sprintf("e%d", i),
			Name:   fmt.Sprintf("e%d", i),
		})
	}
	inst := &Instance{EngineMgr: mgr}
	inst.Cleanup()
	if got := closeCount.Load(); got != 3 {
		t.Errorf("closeCount = %d, want 3", got)
	}
}

func TestCleanup_NilSafe(t *testing.T) {
	inst := &Instance{}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Cleanup panicked on zero-value Instance: %v", r)
		}
	}()
	inst.Cleanup()
}

func TestCleanup_EmptyEngineMgr(t *testing.T) {
	mgr := engine.NewEngineManager()
	inst := &Instance{EngineMgr: mgr}
	inst.Cleanup()
}

func TestCleanup_ShutsDownLSPRegistry(t *testing.T) {
	mainDir := t.TempDir()
	wtDir := t.TempDir()
	reg := lsp.NewRegistry(mainDir)
	spec := lsp.ServerSpec{Name: "fakels", Language: "Go", FileExts: []string{".go"}}

	// The server side is drained rather than closed: closing it first would EOF
	// each client's read loop on its own, so the Dead assertions below would
	// pass even if Cleanup never shut the registry down.
	clients := []*lsp.Client{
		drainedClient(t, reg, "fakels", mainDir, spec),
		drainedClient(t, reg, "fakels", wtDir, spec),
	}
	if got := len(reg.LiveServers()); got != 2 {
		t.Fatalf("len(LiveServers) before Cleanup = %d, want 2", got)
	}

	(&Instance{LSPReg: reg}).Cleanup()

	if got := len(reg.LiveServers()); got != 0 {
		t.Errorf("len(LiveServers) after Cleanup = %d, want 0", got)
	}
	for i, c := range clients {
		select {
		case <-c.Dead():
		case <-time.After(8 * time.Second):
			t.Errorf("client %d (%s) was not shut down by Cleanup", i, c.Name())
		}
	}
}

func drainedClient(t *testing.T, reg *lsp.Registry, name, root string, spec lsp.ServerSpec) *lsp.Client {
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
	c := lsp.NewTestClient(name, clientConn)
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	reg.InjectClientInRoot(name, root, spec, c)
	return c
}

func TestCleanup_ClosesProtocolRunners(t *testing.T) {
	mod := writeProtocolModule(t, "cam", `var ch = host.channel('cam')`)
	hub, runners := wireTransport(&plugins.LoadedPlugins{Protocols: []plugins.ProtocolModule{mod}})
	defer hub.Close()
	if len(runners) != 1 || !hub.Connected("cam") {
		t.Fatal("setup: runner must be wired")
	}
	// Hub deliberately NOT in the Instance: the only path that can
	// unregister "cam" is the runner's own Close.
	inst := &Instance{ProtocolRunners: runners}
	inst.Cleanup()
	if hub.Connected("cam") {
		t.Fatal("Cleanup must close protocol runners (unregister from the hub)")
	}
}

type cleanupFakeConn struct{}

func (cleanupFakeConn) ConnType() string { return "fake" }

// cleanupWarnCapture records Warn-and-above records so the test can pin the
// happy path to zero warnings — this is what kills the `err != nil` negation
// mutant on the cleanup.go error branch (same pattern as pkg/lsp/mutate_sweep_test.go:48,
// pkg/plugins/host_test.go:429, pkg/hub/hub_internal_test.go:33). Safe to swap the
// default logger: pkg/app has zero t.Parallel() tests.
type cleanupWarnCapture struct{ warned []string }

func (h *cleanupWarnCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *cleanupWarnCapture) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.warned = append(h.warned, r.Message)
	}
	return nil
}
func (h *cleanupWarnCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *cleanupWarnCapture) WithGroup(string) slog.Handler      { return h }

func TestCleanup_ClosesMcpRegistry(t *testing.T) {
	reg := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	reg.SetConnectionForTest("srv", cleanupFakeConn{})
	holder := reload.NewEnvHolder(&reload.Env{McpReg: reg})

	prev := slog.Default()
	logs := &cleanupWarnCapture{}
	slog.SetDefault(slog.New(logs))
	t.Cleanup(func() { slog.SetDefault(prev) })

	(&Instance{EnvHolder: holder}).Cleanup()

	if _, ok := reg.GetConnection("srv"); ok {
		t.Fatal("Cleanup left the MCP registry connection open")
	}
	// Happy path must be silent: DreamDone/Store are nil in this Instance, so
	// the only Warn Cleanup can emit here is the mcp-registry error branch —
	// which must not fire when Close succeeds.
	if len(logs.warned) != 0 {
		t.Fatalf("Cleanup logged warnings on the happy path: %v", logs.warned)
	}
}

// A reload-built env may carry no registry; the close must be nil-guarded.
func TestCleanup_NilMcpRegInEnv(t *testing.T) {
	holder := reload.NewEnvHolder(&reload.Env{})
	(&Instance{EnvHolder: holder}).Cleanup() // must not panic
}
