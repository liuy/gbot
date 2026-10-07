//go:build !windows

package app

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/liuy/gbot/pkg/connector/wui"
)

// notifyUpgradeSignal funnels SIGUSR2 through the same wui.RequestRestart
// entry as POST /api/admin/restart: busy → refused with a log line naming
// what is running; TUI mode → refused (hot-restart is daemon-only, see
// upgradeGate); idle daemon → the upgrade starts. `upgrade` is the SAME
// gated closure the admin endpoint uses — one wrapper, both entries.
// The handler stays armed in every mode — a bare SIGUSR2
// default-terminates the process.
func notifyUpgradeSignal(upg Upgrader, upgrade func() error, busy func() wui.BusyReport, tuiMode bool) {
	if upg == nil {
		return
	}
	ch := make(chan os.Signal, 1)
	// Notify runs synchronously in the caller and the receive loop in a
	// goroutine: a signal sent immediately after this call returns must
	// find the handler armed, not kill the process with the default action.
	signal.Notify(ch, syscall.SIGUSR2)
	go func() {
		for range ch {
			if tuiMode {
				slog.Warn("upgrade: sigusr2 refused — TUI mode cannot hot-restart (two processes cannot share one tty); exit and restart manually")
				continue
			}
			outcome, report := wui.RequestRestart(busy, upgrade)
			switch outcome {
			case wui.RestartStarted:
				slog.Info("upgrade: sigusr2 accepted")
			case wui.RestartBusy:
				slog.Warn("upgrade: sigusr2 refused — busy", "items", len(report.Items))
			case wui.RestartUnsupported:
				slog.Warn("upgrade: sigusr2 refused — unsupported")
			}
		}
	}()
}

// tableflipHandoverFdsArePipes reports whether fds 3 and 4 are the two pipe ends
// a tableflip child is exec'd with — the only proof available that this process
// really is an upgraded child rather than one that merely inherited the marker
// from some other process's environment. See the guard in newUpgrader for why the
// answer is needed before tableflip.New runs.
//
// Both ends must be FIFOs, not just one: tableflip exec's a genuine child with the
// pair together, so a process holding one pipe at 3 or 4 is by definition not one.
// That state is reachable — a harness passing ExtraFiles, a shell started with 3<
// — which is exactly why a single FIFO must not be read as a handover.
//
// What this cannot rule out is a marker-bearing process holding unrelated FIFOs at
// both numbers: it is reported as genuine, and newParent then runs a blocking
// gob.Decode on fd 4, hanging boot until whoever holds the write end writes or
// closes. That is not a regression — before this guard existed every
// marker-bearing process took that path, and this only narrows the exposure.
func tableflipHandoverFdsArePipes() bool {
	for _, fd := range [2]int{3, 4} {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			return false
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return false
		}
	}
	return true
}
