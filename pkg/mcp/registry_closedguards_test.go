package mcp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// parkingFailProvider blocks inside NewTransport until released, then fails
// through panic: client.go recovers provider panics into errors, while plain
// provider errors yield the (FailedServer, nil) sentinel instead.
type parkingFailProvider struct {
	entered chan struct{}
	release chan struct{}
}

func (p *parkingFailProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	close(p.entered)
	<-p.release
	panic("mock: late failure after close")
}

// The dial parks in NewTransport so Close lands mid-flight: the entry gate
// would otherwise return before the failure write-back guard is reachable.
func TestReconnectFailureAfterCloseLeavesClosedRegistryAlone(t *testing.T) {
	p := &parkingFailProvider{entered: make(chan struct{}), release: make(chan struct{})}
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"boom": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})

	done := make(chan error, 1)
	go func() {
		_, err := r.Reconnect(context.Background(), "boom")
		done <- err
	}()
	<-p.entered
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(p.release)
	if err := <-done; err == nil {
		t.Fatal("Reconnect must report the connect failure")
	}
	if conn, ok := r.GetConnection("boom"); ok {
		t.Fatalf("closed registry holds %T (%v) for %q — the failure write-back needs the r.closed guard", conn, conn, "boom")
	}
}

func TestReconnectAfterCloseNeverStartsANewConnect(t *testing.T) {
	provider := &selectivePanicProvider{panicNames: map[string]bool{"srv": true}}
	mgr := NewClientManager(provider, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"srv": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := r.Reconnect(context.Background(), "srv"); err == nil {
		t.Fatal("Reconnect on a closed registry must fail")
	}
	if n := provider.calls.Load(); n != 0 {
		t.Errorf("provider.NewTransport called %d times after Close, want 0 — Reconnect must gate on r.closed at entry", n)
	}
}

func TestAdoptConnectionAfterCloseClosesRejectedConnection(t *testing.T) {
	closed := make(chan struct{})
	cs := &ConnectedServer{Name: "late", Cleanup: func() error {
		close(closed)
		return nil
	}}
	mgr := NewClientManager(&selectivePanicProvider{}, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if r.adoptConnection("late", cs, nil) {
		t.Fatal("adoptConnection must report adopted=false on a closed registry")
	}
	if conn, ok := r.GetConnection("late"); ok {
		t.Fatalf("closed registry holds %T for %q", conn, "late")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second): // REAL-TIME: close is dispatched async (EvictIfSame idiom)
		t.Fatal("rejected connection was never closed — adoptConnection must Close what it discards on a closed registry")
	}
}

type parkingSuccessProvider struct {
	entered chan struct{}
	release chan struct{}
	t       *testing.T
}

func (p *parkingSuccessProvider) NewTransport(name string, cfg McpServerConfig, scope ConfigScope, trusted bool) (mcp.Transport, error) {
	close(p.entered)
	<-p.release
	return registeredInMemoryTransport(p.t, func(srv *mcp.Server) {
		mcp.AddTool(srv, &mcp.Tool{Name: "late", Description: "late"}, noopToolHandler)
	}), nil
}

func TestReconnectSuccessCompletedAfterCloseStopsWithoutTouchingRegistry(t *testing.T) {
	var fired atomic.Int32
	count := func(string, any) { fired.Add(1) }
	p := &parkingSuccessProvider{entered: make(chan struct{}), release: make(chan struct{}), t: t}
	mgr := NewClientManager(p, true, "")
	r := NewRegistry(mgr, ChangeCallbacks{
		OnToolsChanged:     func(n string, v []DiscoveredTool) { count(n, v) },
		OnResourcesChanged: func(n string, v []ServerResource) { count(n, v) },
		OnCommandsChanged:  func(n string, v []MCPCommand) { count(n, v) },
	})
	r.SetConfigsForTest(map[string]ScopedMcpServerConfig{
		"srv": {Config: &SSEConfig{URL: "http://example.com"}, Scope: ScopeUser},
	})

	type result struct {
		conn ServerConnection
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := r.Reconnect(context.Background(), "srv")
		done <- result{conn, err}
	}()
	<-p.entered
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(p.release)
	res := <-done
	if res.err == nil || !strings.Contains(res.err.Error(), "closed") {
		t.Fatalf("Reconnect returned %v — must report the closed-registry discard", res.err)
	}
	if res.conn != nil {
		t.Errorf("Reconnect returned %T for a discarded connection, want nil", res.conn)
	}
	if conn, ok := r.GetConnection("srv"); ok {
		t.Errorf("closed registry holds %T for %q", conn, "srv")
	}
	if n := fired.Load(); n != 0 {
		t.Errorf("change callbacks fired %d times after closed-registry discard, want 0", n)
	}
}
