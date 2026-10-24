package mcp

// Mutation-escape kill tests for death_recovery.go. Each test pins a behavior
// that a specific escaped mutant breaks; assertions are exact on purpose.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// 39 concurrency/goroutine-remove: EvictIfSame must launch the evicted
// connection's Close on its own goroutine — Close tears down a real server
// process and can block, so a synchronous call would stall the eviction path
// while it holds the registry mutex.
func TestEvictIfSameClosesEvictedConnectionAsynchronously(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	closeFinished := make(chan struct{})
	cs := &ConnectedServer{
		Name: "s",
		Cleanup: func() error {
			<-release
			close(closeFinished)
			return nil
		},
	}
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	reg.SetConnectionForTest("s", cs)
	evicted := make(chan struct{})
	go func() {
		defer close(evicted)
		reg.EvictIfSame("s", cs)
	}()
	// While Close is parked, EvictIfSame itself must return: eviction cannot
	// wait on process teardown. (The synchronous-close mutant parks here.)
	select {
	case <-evicted:
	case <-time.After(2 * time.Second): // REAL-TIME: scheduling bound
		close(release)
		t.Fatal("EvictIfSame must not block on the evicted connection's Close")
	}
	close(release)
	select {
	case <-closeFinished:
	case <-time.After(5 * time.Second): // REAL-TIME: goroutine drainage bound
		t.Fatal("evicted connection was never closed")
	}
	select {
	case <-evicted:
	case <-time.After(5 * time.Second): // REAL-TIME: goroutine drainage bound
		t.Fatal("eviction goroutine did not finish after Close was released")
	}
}

// 38 statement/remove: EvictIfSame must rebuild the aggregates from the
// discovery caches. r.tools can be stale relative to the caches (Reconnect's
// fetch window mutates caches between rebuilds), so an eviction that skips the
// rebuild leaves dead entries published in the tool table.
func TestEvictIfSameRebuildsAggregatesFromCaches(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	cs := &ConnectedServer{Name: "s"}
	reg.SetConnectionForTest("s", cs)
	// Aggregate entries with no backing cache (the stale shape the rebuild
	// exists to purge).
	reg.SetToolsForTest([]DiscoveredTool{{OriginalName: "stale__tool"}})
	if !reg.EvictIfSame("s", cs) {
		t.Fatal("EvictIfSame must evict the stored connection")
	}
	if tools := reg.GetTools(); len(tools) != 0 {
		t.Fatalf("aggregates must be rebuilt from (empty) caches on eviction, got %d tools: %+v", len(tools), tools)
	}
}

// 56 branch/if + 56 statement/remove: adoptConnection on a closed registry
// must not store the connection, and must leave the mutex released (the
// closed path unlocks by hand — dropping either half stores into a closed
// registry or wedges every later caller).
func TestAdoptConnectionOnClosedRegistryStoresNothing(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	if err := reg.Close(); err != nil {
		t.Fatalf("close registry: %v", err)
	}
	reg.adoptConnection("s", &ConnectedServer{Name: "s"}, nil)
	if !reg.mu.TryLock() {
		t.Fatal("adoptConnection's closed path must leave the registry mutex unlocked")
	}
	reg.mu.Unlock()
	if _, ok := reg.GetConnection("s"); ok {
		t.Fatal("adoptConnection on a closed registry must not store the connection")
	}
}

// 61 branch/if + 61 statement/remove: adoptConnection must record a non-nil
// cfg in r.configs — that record is what keeps the config-driven tool table
// alive across evictions.
func TestAdoptConnectionRecordsConfig(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	cfg := &ScopedMcpServerConfig{Scope: ScopeProject}
	reg.adoptConnection("s", &ConnectedServer{Name: "s"}, cfg)
	got, ok := reg.GetConfigs()["s"]
	if !ok {
		t.Fatal("adoptConnection must record a non-nil cfg in the registry configs")
	}
	if got.Scope != ScopeProject {
		t.Fatalf("recorded config scope = %q, want %q", got.Scope, ScopeProject)
	}
	reg.adoptConnection("nilcfg", &ConnectedServer{Name: "nilcfg"}, nil)
	if _, ok := reg.GetConfigs()["nilcfg"]; ok {
		t.Fatal("adoptConnection with nil cfg must not create a configs entry")
	}
}

// 33 statement/defer-remove: hoisting EvictIfSame's unlock leaves its map
// read/delete unsynchronized against locked adoptConnection writers — the
// runtime's concurrent-map guard must abort. The intended signal, same class
// as the manager batch's churn kills.
func TestEvictIfSameStaysSerializedWithAdoptWriters(t *testing.T) {
	t.Parallel()
	reg := NewRegistry(NewClientManager(TransportFactory{}, true, ""), ChangeCallbacks{})
	t.Cleanup(func() { _ = reg.Close() })
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for range 300 {
				reg.adoptConnection(fmt.Sprintf("w%d", w), &ConnectedServer{Name: "live"}, nil)
			}
		}(w)
		go func(w int) {
			defer wg.Done()
			for range 300 {
				reg.EvictIfSame(fmt.Sprintf("w%d", w), &ConnectedServer{Name: "stale"})
			}
		}(w)
	}
	wg.Wait()
	if _, ok := reg.GetConnection("w0"); !ok {
		t.Fatal("registry must stay usable after the churn")
	}
}
