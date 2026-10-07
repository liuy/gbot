package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cloudflare/tableflip"
	"github.com/liuy/gbot/pkg/connector/wui"
)

// Upgrader is the tableflip surface Start needs. An interface keeps the
// signal wiring (unix-only symbol) separable and documents the contract.
type Upgrader interface {
	Listen(network, addr string) (net.Listener, error) // promoted from *tableflip.Fds
	Ready() error
	Upgrade() error
	Exit() <-chan struct{}
	HasParent() bool
	Stop()
}

var _ Upgrader = (*tableflip.Upgrader)(nil)

// upgradeGate returns the upgrade trigger only for daemon-mode processes.
// TUI mode must never hot-restart: the exec'd child inherits the parent's
// tty and both contend for the same termios — the child's raw-mode entry
// fails with EIO AFTER Ready() (2026-09-22 incident), so the parent has
// already exited and neither process serves. Industry practice (claude
// code, codex) is a manual restart for TUI; hot-restart stays daemon-only.
//
// The wrapper also flips handedOver BEFORE forking: the child rewrites the
// PID file during its multi-second boot, well before this parent observes
// upg.Exit() — without the early flag, a mid-boot SIGINT here would delete
// the live child's PID file (orphaned liveness guard). A failed upgrade
// resets the flag; a stale file on the error path is tolerated by the
// resolver.
func upgradeGate(upg Upgrader, daemonMode bool) func() error {
	if upg == nil || !daemonMode {
		return nil
	}
	return func() error {
		handedOver.Store(true)
		if err := upg.Upgrade(); err != nil {
			handedOver.Store(false)
			return err
		}
		return nil
	}
}

// newUpgrader returns nil on unsupported platforms/errors: the daemon then
// takes the plain-listener path and the restart endpoint reports 501.
// Re-exec must resolve the CURRENT binary path — `make build` atomically
// renames a new binary over the old one, and argv[0] may be relative or a
// PATH lookup — so pin argv[0] to os.Executable() before any Upgrade runs
// (tableflip reads os.Args[0] at upgrade time).
func newUpgrader() Upgrader {
	// Supervised daemons never tableflip: the app orchestrates REUSEPORT
	// overlaps instead (an upgraded tableflip child is an orphan grandchild
	// the Android phantom killer targets).
	if supervisedMode() {
		unsetTableflipEnv()
		return nil
	}
	// The marker must not reach tableflip.New unless this process really is an
	// upgraded child, because reading it is destructive: newParent wraps fd 3 and
	// fd 4 — hard-coded as the handover pipes — with os.NewFile before checking
	// anything else, and a process that only inherited the marker in its
	// environment hands it whatever it holds at those numbers. What sits there is
	// build-dependent — in this build typically netpoll's eventpoll and eventfd,
	// and a plain test binary was measured with a hole at 3 — and either way the
	// *os.File finalizers close the descriptor once the failed New is collected:
	// that does not merely report a wrong HasParent() but takes the process down
	// (`epollwait on fd 4 failed with 9`), leaves every later socket call with
	// EBADF, and over a hole hands whatever the process next allocates at that
	// number to a stale finalizer.
	if !tableflipHandoverFdsArePipes() {
		unsetTableflipEnv()
	}
	if exe, err := os.Executable(); err == nil {
		os.Args[0] = exe
	}
	u, err := tableflip.New(tableflip.Options{})
	// The post-New scrub is a separate concern and cannot replace the guard
	// above: a genuine handover leaves that guard untouched, and without this call
	// the marker is inherited by every shell and tool process the daemon spawns.
	unsetTableflipEnv()
	if err != nil {
		slog.Warn("upgrade: disabled", "error", err)
		return nil
	}
	return u
}

// tableflipEnvPrefix namespaces tableflip's handover marker
// (TABLEFLIP_HAS_PARENT_7DIU3). The exact name is an unexported constant in
// the library, so match the prefix instead of hard-coding a name that can
// change underneath us.
const tableflipEnvPrefix = "TABLEFLIP_"

// unsetTableflipEnv drops tableflip's handover marker from this process's
// environment.
//
// It is safe to call at any point after this process's own HasParent() has been
// decided, for two reasons: the parent New records is fixed during construction
// rather than something re-read later, and the next Upgrade rebuilds the child's
// environment from scratch — tableflip filters any stale marker out of
// os.Environ() before appending a fresh one, so a scrubbed parent still hands
// over correctly.
//
// What is NOT safe is relying on this call alone: it runs after New, so it cannot
// undo the descriptors New already wrapped. See the guard in newUpgrader.
func unsetTableflipEnv() {
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(name, tableflipEnvPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}

// drainTimeout bounds graceful drain after a handover; retunable.
const drainTimeout = 3 * time.Second

// handedOver flips as soon as this process has handed its listener to
// the upgraded child. From that moment the child owns the PID file (it
// rewrote the file during boot), so the parent's SIGINT/SIGTERM handler
// must skip pidCleanup — deleting the file here would orphan the new
// process's liveness guard.
var handedOver atomic.Bool

// watchUpgradeExit drains this process after it handed its listener to the
// upgraded child. Order matters: the 1012 close frame must reach the
// browser BEFORE teardown begins. The PID file is deliberately NOT
// removed: the child rewrote it during boot and now owns it.
func watchUpgradeExit(upg Upgrader, srv *http.Server, wc *wui.WUIConnector, dreamCancel context.CancelFunc) {
	<-upg.Exit()
	handedOver.Store(true)
	if dreamCancel != nil {
		dreamCancel()
	}
	wc.CloseForUpgrade()
	// srv.Close() hard-cuts in-flight responses and Go's HTTP transport does
	// NOT retry a partially received response — the continuous e2e probe
	// (and real browser requests) would see sporadic failures. Shutdown
	// stops accepting immediately and lets active requests finish; hijacked
	// connections (the device WS) are not tracked by Shutdown, so the Close
	// fallback reaps them once the timeout elapses.
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
	}
	cancel()
	os.Exit(0)
}
