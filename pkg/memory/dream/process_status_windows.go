//go:build windows

package dream

import (
	"syscall"
)

const (
	windowsProcessQueryLimitedInfo = 0x1000
	windowsStillActive             = 259
)

// isProcessRunning checks if a process with the given PID is alive.
// Windows has no kill(pid, 0); uses OpenProcess + GetExitCodeProcess.
// TS source: utils/genericProcessUtils.ts — isProcessRunning.
func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(windowsProcessQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	return exitCode == windowsStillActive
}
