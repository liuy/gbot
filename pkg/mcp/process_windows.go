//go:build windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"os"
	"time"
)

// ProcessCleanupEscalation ensures a process terminates.
// Exported for testing. Source: client.ts:1428-1557
//
// Windows path: TerminateProcess (no signal concept). The Unix signal
// escalation (SIGINT → SIGTERM → SIGKILL) has no equivalent — Windows
// processes cannot receive POSIX signals, so we go straight to Kill.
// The graceful close already happened via transport.Close() before this
// function is called.
func ProcessCleanupEscalation(proc *os.Process) {
	if proc == nil {
		return
	}
	_ = proc.Kill()

	// Give the OS a moment to reap the process so callers that check
	// immediately after have an accurate view.
	_ = waitProcessGone(proc.Pid, 100*time.Millisecond)
}

// processExists checks if a process with the given PID is still running.
// Windows has no kill(pid, 0) equivalent; uses OpenProcess + wait hint.
func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windowsOpenProcess(pid)
	if err != nil {
		return false
	}
	defer windowsCloseHandle(h)
	return windowsStillActive(h)
}

// waitProcessGone polls until the process exits or timeout elapses.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !processExists(pid)
}
