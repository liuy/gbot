//go:build !windows

package dream

import "syscall"

// isProcessRunning checks if a process with the given PID is alive.
// Uses signal 0 (existence check without sending a signal).
// TS source: utils/genericProcessUtils.ts — isProcessRunning.
func isProcessRunning(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
