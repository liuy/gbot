//go:build windows

package app

import "github.com/liuy/gbot/pkg/connector/wui"

// syscall.SIGUSR2 does not compile on Windows, so the signal trigger simply
// does not exist there; POST /api/admin/restart reports 501 instead.
func notifyUpgradeSignal(_ Upgrader, _ func() error, _ func() wui.BusyReport, _ bool) {}
