//go:build windows

package main

import (
	"syscall"
)

const (
	winProcessQueryLimitedInfo = 0x1000
	winStillActive             = 259
)

func isProcessAliveImpl(pid int) bool {
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
