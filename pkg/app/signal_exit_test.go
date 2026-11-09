package app

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/reload"
)

// The daemon's signal-exit goroutine calls os.Exit(0), which skips the
// deferred Instance.Cleanup — so the MCP registry close must be wired into
// the goroutine itself. These tests pin that wiring.

// signalExitActions must run the three pre-exit steps in this exact order:
// dream cancel first (stops new ticks), the supervised WS close (1012 frame),
// then the MCP registry close (process-tree teardown) — the most destructive
// step last, when nothing else needs the subprocesses.
func TestSignalExitActions_Order(t *testing.T) {
	var order []string
	rec := func(name string) func() {
		return func() { order = append(order, name) }
	}

	signalExitActions(rec("dream"), rec("ws"), rec("mcp"))

	want := []string{"dream", "ws", "mcp"}
	if len(order) != len(want) {
		t.Fatalf("called %d actions, want %d: %v", len(order), len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("action order = %v, want %v", order, want)
		}
	}
}

// Nil actions must be skipped, not panicked: before the late wires exist
// (early boot, TUI mode) every slot can be nil.
func TestSignalExitActions_NilSafe(t *testing.T) {
	signalExitActions(nil, nil, nil)
}

// mcpCloseFromHolder is the single source of the registry-close block: both
// Instance.Cleanup and the signal-exit path call it, so the signal path can't
// drift from the orderly one. It must actually close the registry.
func TestMcpCloseFromHolder_ClosesRegistry(t *testing.T) {
	reg := mcp.NewRegistry(nil, mcp.ChangeCallbacks{})
	reg.SetConnectionForTest("srv", cleanupFakeConn{})
	holder := reload.NewEnvHolder(&reload.Env{McpReg: reg})

	mcpCloseFromHolder(holder)()

	if _, ok := reg.GetConnection("srv"); ok {
		t.Fatal("mcpCloseFromHolder left the MCP registry connection open")
	}
}

// Cold start: no holder, or a holder whose env carries no registry — the
// returned close must be a no-op, not a panic.
func TestMcpCloseFromHolder_NilCases(t *testing.T) {
	mcpCloseFromHolder(nil)()

	holder := reload.NewEnvHolder(&reload.Env{})
	mcpCloseFromHolder(holder)()
}

// The signal goroutine's body: seams load, actions run in order, the PID
// gate fires, and the exit goes through the osExit seam with code 0.
// The gate flags are forced at entry — upgrade tests elsewhere flip
// handedOver and leave it set, and execution order must not matter.
func TestHandleDaemonSignal_WiresSeamsInOrder(t *testing.T) {
	handedOver.Store(false)
	steppedDown.Store(false)
	t.Cleanup(func() { handedOver.Store(false); steppedDown.Store(false) })

	var order []string
	var sup, mcp atomic.Value
	sup.Store(func() { order = append(order, "ws") })
	mcp.Store(func() { order = append(order, "mcp") })
	pidCalled := false
	exited := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, func() { order = append(order, "dream") }, func() { pidCalled = true })

	want := []string{"dream", "ws", "mcp"}
	if len(order) != len(want) {
		t.Fatalf("actions run = %v, want %v — a seam's late-wire load was skipped", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("action order = %v, want %v", order, want)
		}
	}
	if !pidCalled {
		t.Fatal("pidCleanup was not called on a normal exit — the liveness guard would outlive the daemon")
	}
	select {
	case <-exited:
	default:
		t.Fatal("handleDaemonSignal did not exit through the osExit seam")
	}
}

// Before the seams are stored (early boot), every step must be a skip, not a
// panic — and the exit still runs. A stored TYPED-NIL func must also skip:
// the load guards exist precisely so a nil close is dropped, not called.
func TestHandleDaemonSignal_EmptySeamsSkipCleanly(t *testing.T) {
	handedOver.Store(false)
	steppedDown.Store(false)
	t.Cleanup(func() { handedOver.Store(false); steppedDown.Store(false) })
	var sup, mcp atomic.Value
	pidCalled := false
	exited := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, nil, func() { pidCalled = true })

	if !pidCalled {
		t.Fatal("pidCleanup skipped with empty seams")
	}
	select {
	case <-exited:
	default:
		t.Fatal("no exit with empty seams")
	}

	var nilFn func()
	sup.Store(nilFn)
	mcp.Store(nilFn)
	pidCalled = false
	exited2 := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, nil, func() { pidCalled = true })

	if !pidCalled {
		t.Fatal("pidCleanup skipped with typed-nil seams")
	}
	select {
	case <-exited2:
	default:
		t.Fatal("typed-nil seam close was invoked or the exit never ran")
	}
}

// After a handover or a supervised step-down the PID file belongs to someone
// else; the gate must skip pidCleanup in both states.
func TestHandleDaemonSignal_GateSkipsPidCleanup(t *testing.T) {
	for name, set := range map[string]func(){
		"handed-over":  func() { handedOver.Store(true) },
		"stepped-down": func() { steppedDown.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			handedOver.Store(false)
			steppedDown.Store(false)
			set()
			t.Cleanup(func() { handedOver.Store(false); steppedDown.Store(false) })
			var sup, mcp atomic.Value
			pidCalled := false
			exited := interceptExit(t)

			handleDaemonSignal(&sup, &mcp, nil, func() { pidCalled = true })

			if pidCalled {
				t.Fatal("pidCleanup ran — it would delete the successor's PID file")
			}
			select {
			case <-exited:
			default:
				t.Fatal("no exit after the gate")
			}
		})
	}
}

// The upgrade handover exit (watchUpgradeExit) orphans MCP trees the same
// way the signal exit did: the mcpClose func must run in the drain, before
// the exit call.
func TestWatchUpgradeExit_ClosesMCP(t *testing.T) {
	exited := interceptExit(t)
	store, _ := newExitTestStore(t)
	upg := &exitUpgrader{exitCh: make(chan struct{})}

	closed := make(chan struct{})
	go watchUpgradeExit(upg, &http.Server{}, nil, nil, nil, store, func() { close(closed) })
	close(upg.exitCh)

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit did not run mcpClose before exiting")
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("watchUpgradeExit did not exit after upg.Exit() closed")
	}
	assertStoreOpen(t, store, "after mcpClose drain")
}
