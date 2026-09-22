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
