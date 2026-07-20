//go:build windows

package mcp

import (
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

func windowsOpenProcess(pid int) (syscall.Handle, error) {
	return syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
}

func windowsCloseHandle(h syscall.Handle) {
	_ = syscall.CloseHandle(h)
}

// windowsStillActive checks whether the process handle refers to a running
// process by querying its exit code. STILL_ACTIVE (259) means running.
func windowsStillActive(h syscall.Handle) bool {
	var exitCode uint32
	err := syscall.GetExitCodeProcess(h, &exitCode)
	if err != nil {
		return false
	}
	return exitCode == stillActive
}
