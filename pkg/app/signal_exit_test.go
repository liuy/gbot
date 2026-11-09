package app

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/reload"
)

// The daemon's signal-exit goroutine calls os.Exit(0), which skips the
// deferred Instance.Cleanup — so the MCP registry close must be wired into
// the goroutine itself. These tests pin that wiring.

// osExit is a package-level var and production never calls signal.Stop, so
// every Start() in this package leaves a signal goroutine parked on the
// signal package. Assigning the seam from inside a test therefore races with
// those goroutines reading it. The seam is installed once, before any test
// goroutine exists, and routed through an atomic pointer: tests swap the hook,
// never the var. A nil hook means "exit is a no-op", which is also what keeps
// a stray signal from killing the test binary.
var pinExitHook atomic.Pointer[func(int)]

func init() { osExit = pinRouteExit }

func pinRouteExit(code int) {
	if h := pinExitHook.Load(); h != nil {
		(*h)(code)
	}
}

// onExit installs an exit hook for the duration of t.
func onExit(t *testing.T, hook func(code int)) {
	t.Helper()
	prev := pinExitHook.Swap(&hook)
	t.Cleanup(func() { pinExitHook.Store(prev) })
}

// Start spawns the signal goroutine ~500 lines before the dream engine exists,
// so the dream-cancel slot is shared by the boot goroutine (which writes it)
// and the signal goroutine (which reads it). It must be late-wired like
// supervisedClose/mcpExitClose: a plain captured var leaves the two goroutines
// with no happens-before edge, and a mid-boot signal then cancels a
// half-constructed cancelCtx. -race is the oracle for the first test; the
// second pins the nil-slot path.
func TestStartSignalAfterBootRereadsDreamSlot(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	handedOver.Store(false)
	steppedDown.Store(false)
	var exits atomic.Int32
	onExit(t, func(code int) {
		if code != 0 {
			t.Errorf("signal exit code = %d, want 0", code)
		}
		exits.Add(1)
	})

	inst, err := Start(Options{NoTUI: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// DreamDone is created exactly where the slot is written: nil here means the
	// boot never wrote it and the test would prove nothing.
	if inst.DreamDone == nil {
		t.Fatal("the boot never reached the dream-cancel wire")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("kill -INT: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for exits.Load() == 0 { // REAL-TIME: bounded poll — signal delivery has no channel to select on
		if time.Now().After(deadline) { // REAL-TIME: bounded wait for signal delivery
			t.Fatal("SIGINT never reached the exit seam")
		}
		time.Sleep(5 * time.Millisecond) // REAL-TIME: signal delivery exposes no channel to select on
	}
	select {
	case <-inst.DreamDone:
	case <-time.After(5 * time.Second): // REAL-TIME: the cancel must stop the dream timer
		t.Fatal("the signal path never cancelled the wired dream slot")
	}
	shutdownInstance(t, inst)
}

// A SIGINT that lands before the dream engine exists must skip the cancel step
// instead of reading a func value the boot is still writing: the boot has to
// survive it, finish, and still wire the slot.
func TestStartSignalDuringBootSkipsUnwiredDreamSlot(t *testing.T) {
	t.Setenv("HOME", pinHome(t, pinSettings))
	t.Setenv("GBOT_PPROF_ADDR", "off")
	port := freeTCPPort(t)
	t.Setenv("GBOT_WS_ADDR", "127.0.0.1:"+port)
	startInTempProject(t)

	handedOver.Store(false)
	steppedDown.Store(false)
	var exits atomic.Int32
	onExit(t, func(code int) {
		if code != 0 {
			t.Errorf("signal exit code = %d, want 0", code)
		}
		exits.Add(1)
	})
	// The hammer starts before Start subscribes; without a guard channel the
	// first SIGINT would take the default action and kill the test binary.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)
	stop := make(chan struct{})
	hammerDone := make(chan struct{})
	go func() {
		defer close(hammerDone)
		// Bounded and sparse: every registered channel receives every signal, so
		// a long hammer would fan out to every other boot's parked goroutine and
		// perturb sibling tests. Sparse shots cover the window between
		// signal.Notify and the dream wire, and we stop at the first hit.
		for range 6 {
			select {
			case <-stop:
				return
			default:
			}
			if exits.Load() > 0 {
				return
			}
			if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
				t.Errorf("kill -INT: %v", err)
				return
			}
			time.Sleep(20 * time.Millisecond) // REAL-TIME: the signal must land inside the real boot window
		}
	}()
	inst, err := Start(Options{NoTUI: true})
	close(stop)
	<-hammerDone
	if err != nil {
		t.Fatalf("Start with a mid-boot SIGINT: %v", err)
	}
	if inst == nil {
		t.Fatal("Start returned a nil Instance after a mid-boot SIGINT")
	}
	if inst.DreamDone == nil {
		t.Fatal("the mid-boot SIGINT stopped the boot from wiring the dream slot")
	}
	if exits.Load() == 0 {
		t.Fatal("no SIGINT reached the exit seam — SIGINT is process-wide and signal.Notify is never stopped, so a non-zero count only proves at least one delivery reached some boot, not that the hammer landed inside this boot's window")
	}
	shutdownInstance(t, inst)
}

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
	var sup, mcp, dream atomic.Value
	sup.Store(func() { order = append(order, "ws") })
	mcp.Store(func() { order = append(order, "mcp") })
	dream.Store(context.CancelFunc(func() { order = append(order, "dream") }))
	pidCalled := false
	exited := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, &dream, func() { pidCalled = true })

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
	var sup, mcp, dream atomic.Value
	pidCalled := false
	exited := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, &dream, func() { pidCalled = true })

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
	dream.Store(context.CancelFunc(nil))
	pidCalled = false
	exited2 := interceptExit(t)

	handleDaemonSignal(&sup, &mcp, &dream, func() { pidCalled = true })

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
			var sup, mcp, dream atomic.Value
			pidCalled := false
			exited := interceptExit(t)

			handleDaemonSignal(&sup, &mcp, &dream, func() { pidCalled = true })

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
