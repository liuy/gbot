package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
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
	if exe, err := os.Executable(); err == nil {
		os.Args[0] = exe
	}
	u, err := tableflip.New(tableflip.Options{})
	if err != nil {
		slog.Warn("upgrade: disabled", "error", err)
		return nil
	}
	return u
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
