//go:build !windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"os"
	"syscall"
	"time"
)

// ProcessCleanupEscalation — Unix: SIGINT (100ms) → SIGTERM (400ms) → SIGKILL.
// Source: client.ts:1428-1557
func ProcessCleanupEscalation(proc *os.Process) {
	if proc == nil {
		return
	}
	pid := proc.Pid
	if !processExists(pid) {
		return
	}

	// Step 1: SIGINT — Source: client.ts:1438-1439
	_ = syscall.Kill(pid, syscall.SIGINT)
	if waitProcessGone(pid, 100*time.Millisecond) {
		return
	}

	// Step 2: SIGTERM — Source: client.ts:1492-1493
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitProcessGone(pid, 400*time.Millisecond) {
		return
	}

	// Step 3: SIGKILL — Source: client.ts:1523-1524
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// processExists checks if a process with the given PID is still running.
// Uses syscall.Kill(pid, 0) which checks existence without sending a signal.
// Source: client.ts:1453 — process.kill(pid, 0)
func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitProcessGone polls until the process exits or timeout elapses.
// Returns true if the process is gone within the timeout.
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
