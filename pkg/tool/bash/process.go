package bash

import (
	"syscall"
)

// killProcessTree kills the entire process group of pid with SIGKILL.
//
// Source: ShellCommand.ts:337-343 (#doKill) — treeKill(pid, 'SIGKILL')
// TS always sends SIGKILL directly (no SIGTERM grace period).
// Uses negative PID to signal the entire process group, matching TS's
// tree-kill behavior which walks the process tree and kills all children.
func killProcessTree(pid int) error {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Process already exited — nothing to kill
		return nil
	}

	// SIGKILL the process group (negative PID = process group)
	// Source: ShellCommand.ts:340 — treeKill(this.#childProcess.pid, 'SIGKILL')
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
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
