//go:build windows

package cron

import (
	"syscall"
)

const (
	winProcessQueryLimitedInfo = 0x1000
	winStillActive             = 259
)

// processAlive mirrors pkg/app/pid_windows.go: open with query-limited
// access and check the still-active exit code.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(winProcessQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == winStillActive
}
