//go:build !windows

// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
package mcp

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// SetProcessGroup puts a spawned MCP server in its own process group
// (pgid == child pid). Group ownership is what lets ProcessCleanupEscalation
// reap the whole tree — including descendants that outlived their parents
// (e.g. a browser child surviving a crashed server) — via kill(-pgid, sig).
// Without it, signals reach only the top-level wrapper process.
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// ProcessCleanupEscalation — Unix: SIGINT (100ms) → SIGTERM (400ms) →
// SIGKILL, each delivered to the whole process group, then a bounded
// synchronous wait for the group to disappear so a follow-up spawn (Reconnect)
// never races a dying tree over shared resources (profile locks).
// Source: client.ts:1428-1557; group semantics are gbot's addition — the TS
// original never needed them because node's child trees die with the parent.
// Every stage is logged: "who killed the server" must be answerable from the
// daemon log alone (2026-10-18 hunt was blind because kills left no trace).
func ProcessCleanupEscalation(proc *os.Process, server string) {
	if proc == nil {
		return
	}
	pid := proc.Pid
	if !treeExists(pid) {
		return
	}
	slog.Info("mcp: killing server process tree", "server", server, "pid", pid)

	// Step 1: SIGINT — Source: client.ts:1438-1439
	signalTree(pid, syscall.SIGINT)
	if waitTreeGone(pid, 100*time.Millisecond) {
		return
	}

	// Step 2: SIGTERM — Source: client.ts:1492-1493
	slog.Warn("mcp: server tree survived SIGINT, escalating to SIGTERM", "server", server, "pid", pid)
	signalTree(pid, syscall.SIGTERM)
	if waitTreeGone(pid, 400*time.Millisecond) {
		return
	}

	// Step 3: SIGKILL — Source: client.ts:1523-1524. The final wait is
	// unconditional: callers respawn immediately after cleanup, and a
	// lingering group member holding a resource (browser profile lock) turns
	// every subsequent launch into "Browser is already in use".
	slog.Warn("mcp: server tree survived SIGTERM, escalating to SIGKILL", "server", server, "pid", pid)
	signalTree(pid, syscall.SIGKILL)
	if !waitTreeGone(pid, 2*time.Second) {
		slog.Error("mcp: server tree still alive after SIGKILL", "server", server, "pid", pid)
	}
}

// signalTree delivers sig to the process itself and, when a dedicated group
// exists, to every member. Signalling both keeps escalation effective for
// spawns that predate SetProcessGroup (group signal returns ESRCH then).
func signalTree(pid int, sig syscall.Signal) {
	_ = syscall.Kill(pid, sig)
	_ = syscall.Kill(-pid, sig)
}

// treeExists reports whether the process or any of its group members lives.
// A dead top process with live descendants (mid-tree crash orphan) still
// counts: those orphans are exactly what group cleanup must reach. Zombies
// count as gone — an unreaped group leader would otherwise pin waitTreeGone
// for its full budget.
func treeExists(pid int) bool {
	if syscall.Kill(pid, 0) == nil && !isZombie(pid) {
		return true
	}
	return syscall.Kill(-pid, 0) == nil
}

// isZombie reads the process state on Linux; on systems without /proc it
// reports false (the caller then treats the process as live — safe side).
func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// state follows the "(comm)" field; comm can itself contain parens/spaces,
	// so anchor on the last ')'.
	for i := len(b) - 2; i >= 0; i-- {
		if b[i] == ')' {
			return i+2 < len(b) && b[i+2] == 'Z'
		}
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// waitTreeGone polls until both the process and its group are gone.
func waitTreeGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !treeExists(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !treeExists(pid)
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
