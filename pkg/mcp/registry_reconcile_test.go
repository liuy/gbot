package mcp

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Reconcile test fixtures — a transport provider that stands up a real
// in-memory MCP server per connect, so changed-server reconnects get a fresh
// pipe instead of reusing the previous (already closed) one.
// ---------------------------------------------------------------------------

type reconcileProvider struct {
	mu       sync.Mutex
	failConn map[string]bool
	sessions []*mcp.ServerSession
	servers  []*mcp.Server
}

func newReconcileProvider() *reconcileProvider {
	return &reconcileProvider{failConn: make(map[string]bool)}
}

func (p *reconcileProvider) NewTransport(name string, _ McpServerConfig, _ ConfigScope, _ bool) (mcp.Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failConn[name] {
		return nil, fmt.Errorf("mock: connection failed for %q", name)
	}
	t1, t2 := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "reconcile-server", Version: "1.0.0"}, nil)
	session, err := server.Connect(context.Background(), t1, nil)
	if err != nil {
		return nil, err
	}
	p.sessions = append(p.sessions, session)
	p.servers = append(p.servers, server)
	return t2, nil
}

func (p *reconcileProvider) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sessions {
		_ = s.Close()
	}
}

func newReconcileRegistry(t *testing.T) (*Registry, *reconcileProvider) {
	t.Helper()
	p := newReconcileProvider()
	t.Cleanup(p.closeAll)
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	t.Cleanup(func() { _ = r.Close() })
	return r, p
}

func stdioCfg(args ...string) ScopedMcpServerConfig {
	return ScopedMcpServerConfig{Config: &StdioConfig{Command: "echo", Args: args}, Scope: ScopeUser}
}

// ---------------------------------------------------------------------------
// EnterCall / DrainServer
// ---------------------------------------------------------------------------

func TestEnterCallDrainServer_WaitsForInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		done := r.EnterCall("s")
		returned := make(chan struct{})
		go func() {
			idle, remaining := r.DrainServer("s", 5*time.Second)
			if !idle {
				t.Error("DrainServer reported not-idle after the call finished")
			}
			if remaining != 0 {
				t.Errorf("DrainServer remaining = %d after the call finished, want 0", remaining)
			}
			close(returned)
		}()
		// Let the drain goroutine run until it blocks on its poll loop.
		synctest.Wait()
		select {
		case <-returned:
			t.Fatal("DrainServer returned while the in-flight call was still held")
		default:
		}
		// Advance well past a few 25ms poll intervals; the drain must keep waiting.
		time.Sleep(200 * time.Millisecond)
		select {
		case <-returned:
			t.Fatal("DrainServer returned while the in-flight call was still held, even after 200ms of polls")
		default:
		}
		done()
		<-returned
	})
}

func TestDrainServer_TimeoutReturnsRemaining(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		done := r.EnterCall("s")
		defer done()
		idle, remaining := r.DrainServer("s", 100*time.Millisecond)
		if idle {
			t.Error("DrainServer reported idle while a call was still in flight")
		}
		if remaining != 1 {
			t.Errorf("DrainServer remaining = %d, want 1", remaining)
		}
	})
}

func TestDrainServer_IdleImmediately(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	idle, remaining := r.DrainServer("never-seen", time.Second)
	if !idle {
		t.Error("DrainServer on a server with no calls must report idle")
	}
	if remaining != 0 {
		t.Errorf("DrainServer remaining = %d, want 0", remaining)
	}
}

// ---------------------------------------------------------------------------
// Reconcile
// ---------------------------------------------------------------------------

func TestReconcile_DrainsBeforeDisconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := newReconcileRegistry(t)
		oldCfg := stdioCfg("v1")
		r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"a": oldCfg})
		r.connectSingle(context.Background(), "a", oldCfg)
		oldConn, ok := r.GetConnection("a")
		if !ok {
			t.Fatal("fixture: server a did not connect")
		}

		callDone := r.EnterCall("a")
		type outcome struct {
			res *ReconcileResult
			ok  bool
		}
		resCh := make(chan outcome, 1)
		go func() {
			res, ok := r.Reconcile(context.Background(),
				map[string]ScopedMcpServerConfig{"a": stdioCfg("v2")},
				5*time.Second)
			resCh <- outcome{res, ok}
		}()
		// Quiesce: Reconcile must now be parked inside DrainServer.
		synctest.Wait()
		if conn, ok := r.GetConnection("a"); !ok || conn != oldConn {
			t.Fatal("Reconcile tore the connection down while a call was still in flight")
		}
		select {
		case got := <-resCh:
			t.Fatalf("Reconcile completed before the in-flight call finished: %+v", got)
		default:
		}
		callDone()
		out := <-resCh
		if !out.ok {
			t.Fatal("Reconcile reported busy; nothing else held reloadMu")
		}
		if out.res == nil {
			t.Fatal("Reconcile returned nil result with ok=true")
		}
		if len(out.res.Changed) != 1 || out.res.Changed[0] != "a" {
			t.Fatalf("Reconcile Changed = %v, want [a]", out.res.Changed)
		}
		newConn, ok := r.GetConnection("a")
		if !ok {
			t.Fatal("changed server must have a connection after reconcile")
		}
		if newConn == oldConn {
			t.Fatal("changed server kept its old connection object; reconnect did not happen")
		}
	})
}

func TestReconcile_AddRemoveChanged(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"a": stdioCfg("v1"),
		"b": stdioCfg(),
	})
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{
		"a": stdioCfg("v2"),
		"c": stdioCfg(),
	}, time.Second)
	if !ok {
		t.Fatal("Reconcile returned ok=false; nothing else held reloadMu")
	}
	if res == nil {
		t.Fatal("Reconcile returned nil result")
	}
	if len(res.Added) != 1 || res.Added[0] != "c" {
		t.Errorf("Added = %v, want [c]", res.Added)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "b" {
		t.Errorf("Removed = %v, want [b]", res.Removed)
	}
	if len(res.Changed) != 1 || res.Changed[0] != "a" {
		t.Errorf("Changed = %v, want [a]", res.Changed)
	}
	if len(res.ReconnectFailed) != 0 {
		t.Errorf("ReconnectFailed = %v, want empty", res.ReconnectFailed)
	}
	if _, still := r.GetConnection("b"); still {
		t.Error("removed server b still has a connection")
	}
	if _, hasC := r.GetConfigs()["c"]; !hasC {
		t.Error("added server c missing from registry configs")
	}
}

func TestReconcile_PreservesAgentAndPluginEntries(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	agentCfg := ScopedMcpServerConfig{Config: &StdioConfig{Command: "agent-cmd"}, Scope: ScopeDynamic}
	pluginCfg := ScopedMcpServerConfig{
		Config:       &StdioConfig{Command: "plug-cmd"},
		Scope:        ScopeDynamic,
		PluginSource: "plugin:p:x",
	}
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"agent-run-1-0": agentCfg,
		"plugin:p:x":    pluginCfg,
	})

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{}, time.Second)
	if !ok {
		t.Fatal("Reconcile returned ok=false")
	}
	if res == nil {
		t.Fatal("Reconcile returned nil result")
	}
	for _, name := range []string{"agent-run-1-0", "plugin:p:x"} {
		if slices_contains(res.Removed, name) {
			t.Errorf("carried-forward entry %q landed in Removed", name)
		}
	}
	configs := r.GetConfigs()
	if _, has := configs["agent-run-1-0"]; !has {
		t.Error("agent-prefixed entry dropped from registry configs")
	}
	if _, has := configs["plugin:p:x"]; !has {
		t.Error("PluginSource entry dropped from registry configs")
	}
}

func TestReconcile_BusyWhenWatcherHoldsLock(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	unlock := r.LockReloadForTest()
	defer unlock()
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{"x": stdioCfg()}, time.Second)
	if ok {
		t.Fatal("Reconcile must report busy (ok=false) when reloadMu is held")
	}
	if res != nil {
		t.Fatalf("Reconcile busy path must return nil result, got %+v", res)
	}
}

func TestReconcile_ReconnectFailureRecorded(t *testing.T) {
	r, p := newReconcileRegistry(t)
	p.mu.Lock()
	p.failConn["c"] = true
	p.mu.Unlock()

	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{"c": stdioCfg()}, time.Second)
	if !ok {
		t.Fatal("Reconcile returned ok=false; nothing else held reloadMu")
	}
	if res == nil {
		t.Fatal("Reconcile returned nil result")
	}
	if len(res.ReconnectFailed) != 1 || res.ReconnectFailed[0] != "c" {
		t.Fatalf("ReconnectFailed = %v, want [c]", res.ReconnectFailed)
	}
	conn, has := r.GetConnection("c")
	if !has {
		t.Fatal("failed server must keep a FailedServer connection entry")
	}
	if conn.ConnType() != "failed" {
		t.Errorf("failed server connection type = %q, want failed", conn.ConnType())
	}
}

func TestReconcile_NoChangesReportsEmptyResult(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	same := stdioCfg("v1")
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{"a": same})
	res, ok := r.Reconcile(context.Background(), map[string]ScopedMcpServerConfig{"a": same}, time.Second)
	if !ok || res == nil {
		t.Fatalf("Reconcile ok=%v res=%v, want ok=true and a result", ok, res)
	}
	if len(res.Added)+len(res.Removed)+len(res.Changed)+len(res.ReconnectFailed) != 0 {
		t.Errorf("unchanged reconcile reported changes: %+v", res)
	}
}

// slices_contains avoids importing slices for two call sites in this file.
func slices_contains(list []string, s string) bool {
	return slices.Contains(list, s)
}

// Compiles the atomic counter contract: concurrent enter/exit bursts leave
// the counter at zero — the precondition DrainServer relies on.
func TestEnterCall_ConcurrentEntersExits(t *testing.T) {
	r, _ := newReconcileRegistry(t)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			done := r.EnterCall("s")
			done()
		})
	}
	wg.Wait()
	idle, remaining := r.DrainServer("s", time.Second)
	if !idle || remaining != 0 {
		t.Fatalf("after all calls finished: idle=%v remaining=%d, want idle with 0", idle, remaining)
	}
}
