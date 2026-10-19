//go:build !windows

package mcp

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// spawnGroupTree mirrors newStdioTransport's spawn (own process group) with a
// two-level tree: a top shell plus a backgrounded grandchild — the shape
// npx → node → chromium has. Children are marker-commented sh loops so pgrep
// can find them and so they survive until signalled.
func spawnGroupTree(t *testing.T, top, bg string) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & exec sh -c 'while :; do sleep 1; done # %s'", bg, top))
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tree: %v", err)
	}
	grandchild := waitPidOf(t, bg)
	return cmd, grandchild
}

func waitPidOf(t *testing.T, marker string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second) // REAL-TIME: process spawn timing
	for time.Now().Before(deadline) {           // REAL-TIME: process spawn timing
		out, err := exec.Command("pgrep", "-f", "# "+marker).Output()
		if err == nil {
			for f := range strings.FieldsSeq(string(out)) {
				var v int
				if _, err := fmt.Sscanf(f, "%d", &v); err == nil && v > 0 {
					return v
				}
			}
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: process spawn timing
	}
	t.Fatalf("marker %q never appeared", marker)
	return 0
}

func procAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil && !isZombie(pid)
}

func cleanupMarkers() {
	_ = exec.Command("pkill", "-9", "-f", "gbot-grp-").Run()
}

// The orphan mechanism from the 2026-10-18 incident: a mid-tree crash (SIGKILL
// to the top process) leaves the grandchild parentless but alive and inside
// the group. Cleanup called afterwards must reap it via the group signal.
func TestCleanupReapsCrashOrphanedGrandchild(t *testing.T) {
	cmd, grandchild := spawnGroupTree(t, "gbot-grp-orphan-top", "gbot-grp-orphan-bg")
	t.Cleanup(cleanupMarkers)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill top: %v", err)
	}
	// Let the group leader die (and turn zombie until reaped) while the
	// grandchild keeps running — the crash-orphan shape.
	time.Sleep(150 * time.Millisecond) // REAL-TIME: process death timing
	if !procAlive(grandchild) {
		t.Fatal("precondition: grandchild should survive the top's SIGKILL")
	}

	ProcessCleanupEscalation(cmd.Process, "test")

	if procAlive(grandchild) {
		t.Fatalf("orphaned grandchild %d survived cleanup — group reaping broken", grandchild)
	}
}

// The normal shutdown path: cleanup on a live tree must take down the top
// process AND the grandchild together, and return only after the whole group
// is gone (a caller respawns immediately after).
func TestCleanupKillsWholeTreeSynchronously(t *testing.T) {
	cmd, grandchild := spawnGroupTree(t, "gbot-grp-sync-top", "gbot-grp-sync-bg")
	t.Cleanup(cleanupMarkers)

	start := time.Now() // REAL-TIME: escalation ladder timing
	ProcessCleanupEscalation(cmd.Process, "test")
	elapsed := time.Since(start) // REAL-TIME: escalation ladder timing

	if procAlive(grandchild) {
		t.Fatalf("grandchild %d survived cleanup", grandchild)
	}
	if procAlive(cmd.Process.Pid) {
		t.Fatal("top process survived cleanup")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cleanup took %v — escalation ladder exceeded its budget", elapsed)
	}
}

// Group members that ignore SIGINT/SIGTERM must still fall to the group
// SIGKILL step.
func TestCleanupEscalatesPastStubbornProcesses(t *testing.T) {
	cmd := exec.Command("sh", "-c",
		"sh -c 'trap \"\" TERM INT; while :; do sleep 1; done # gbot-grp-stub-bg' & exec sh -c 'trap \"\" TERM INT; while :; do sleep 1; done # gbot-grp-stub-top'")
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(cleanupMarkers)
	grandchild := waitPidOf(t, "gbot-grp-stub-bg")

	ProcessCleanupEscalation(cmd.Process, "test")

	if procAlive(grandchild) || procAlive(cmd.Process.Pid) {
		t.Fatal("stubborn tree survived the SIGKILL step")
	}
}

// Concurrent cleanups of the same tree must not deadlock or misfire.
func TestConcurrentCleanupSameTree(t *testing.T) {
	cmd, grandchild := spawnGroupTree(t, "gbot-grp-conc-top", "gbot-grp-conc-bg")
	t.Cleanup(cleanupMarkers)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			ProcessCleanupEscalation(cmd.Process, "test")
		})
	}
	wg.Wait()

	if procAlive(grandchild) {
		t.Fatal("tree survived concurrent cleanup")
	}
}
