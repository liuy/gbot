//go:build !windows

package main

import "syscall"

func isProcessAliveImpl(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
