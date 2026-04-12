package bash

import (
	"fmt"
	"syscall"
	"time"
)

// ProcessKillGracePeriod is the time to wait after SIGTERM before escalating to SIGKILL.
const ProcessKillGracePeriod = 5 * time.Second

// killProcessTree kills the entire process group of pid.
// Sends SIGTERM first, waits up to ProcessKillGracePeriod, then escalates to SIGKILL.
//
// Source: ShellCommand.ts:337-343 (#doKill) — treeKill(package) equivalent.
// Uses negative PID to signal the entire process group, matching TS's
// tree-kill behavior which walks the process tree and kills all children.
func killProcessTree(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Process already exited — nothing to kill
		return nil
	}

	// 1. SIGTERM the process group (negative PID = process group)
	// Source: ShellCommand.ts:339 — treeKill(this.#childProcess.pid, 'SIGKILL')
	// We use SIGTERM first for graceful shutdown, then SIGKILL after grace period.
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("SIGTERM process group %d: %w", -pgid, err)
	}

	// 2. Wait up to ProcessKillGracePeriod for processes to exit
	deadline := time.Now().Add(ProcessKillGracePeriod)
	for time.Now().Before(deadline) {
		// Check if process group still exists
		if err := syscall.Kill(-pgid, 0); err != nil {
			// Process group no longer exists — all exited
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 3. SIGKILL the process group after grace period
	// Source: ShellCommand.ts:340 — treeKill(pid, 'SIGKILL')
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// killProcess sends SIGKILL to the entire process group.
// Used when immediate termination is needed (e.g., size watchdog exceeded).
//
// Source: ShellCommand.ts:337-343 (#doKill) — treeKill(pid, 'SIGKILL')
func killProcess(pid int) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
