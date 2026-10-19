//go:build windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// SetProcessGroup is a Unix-only concept (process groups). On Windows it is a
// no-op; ProcessCleanupEscalation therefore terminates only the top-level
// process — descendants that outlive a crashed parent are not reaped here.
// The equivalent mechanism would be job objects; deliberately out of scope.
func SetProcessGroup(*exec.Cmd) {}

// ProcessCleanupEscalation ensures a process terminates.
// Exported for testing. Source: client.ts:1428-1557
//
// Windows path: TerminateProcess (no signal concept). The Unix signal
// escalation (SIGINT → SIGTERM → SIGKILL) has no equivalent — Windows
// processes cannot receive POSIX signals, so we go straight to Kill.
// The graceful close already happened via transport.Close() before this
// function is called.
func ProcessCleanupEscalation(proc *os.Process, server string) {
	if proc == nil {
		return
	}
	slog.Info("mcp: killing server process (windows: top-level only)", "server", server, "pid", proc.Pid)
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
