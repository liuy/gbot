package app

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/lsp"
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
