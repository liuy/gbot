//go:build windows

package app

import "github.com/liuy/gbot/pkg/connector/wui"

// syscall.SIGUSR2 does not compile on Windows, so the signal trigger simply
// does not exist there; POST /api/admin/restart reports 501 instead.
func notifyUpgradeSignal(_ Upgrader, _ func() error, _ func() wui.BusyReport, _ bool) {}

// tableflipHandoverFdsArePipes is always false here, and that is the honest
// answer rather than a placeholder: tableflip refuses to run on Windows at all
// (isSupportedOS is runtime.GOOS != "windows", and its stdEnv is a nil *env on
// this platform), so no Windows process can ever have been exec'd as an
// upgraded child and a marker found in the environment is always stale.
// Reporting false therefore only ever drops a marker that was going to be
// ignored anyway — newUpgrader still gets ErrNotSupported from New and the
// daemon keeps its plain-listener path.
func tableflipHandoverFdsArePipes() bool { return false }
