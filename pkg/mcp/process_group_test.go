//go:build !windows

package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidMarker(name string) string {
	return fmt.Sprintf("gbot-grp-%d-%s", os.Getpid(), name)
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
	_ = exec.Command("pkill", "-9", "-f", fmt.Sprintf("gbot-grp-%d-", os.Getpid())).Run()
}

// spawnGroupTree spawns a two-level marker tree through the real stdio
// transport (own process group): a top shell plus a backgrounded grandchild —
// the shape npx → node → chromium has. Children are marker-commented sh loops
// so pgrep can find them and so they survive until signalled. Markers carry
// the test binary's pid: parallel go test invocations (mutate reruns) must
// not see or kill each other's trees.
func spawnGroupTree(t *testing.T, top, bg string) (*stdioProcTransport, int) {
	t.Helper()
	top, bg = pidMarker(top), pidMarker(bg)
	script := "sh -c 'while :; do sleep 1; done # " + bg + "' & exec sh -c 'while :; do sleep 1; done # " + top + "'"
	tr := startTestStdio(t, script, true)
	return tr, waitPidOf(t, bg)
}

// The orphan mechanism from the 2026-10-18 incident: a mid-tree crash (SIGKILL
// to the top process) leaves the grandchild parentless but alive and inside
// the group. Teardown called afterwards must reap it via the group signal —
// the recovery scenario for the omp sweep.
func TestTeardownReapsCrashOrphanedGrandchild(t *testing.T) {
	tr, grandchild := spawnGroupTree(t, "orphan-top", "orphan-bg")

	if err := tr.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill top: %v", err)
	}
	// Let the group leader die (and turn zombie until reaped) while the
	// grandchild keeps running — the crash-orphan shape.
	time.Sleep(150 * time.Millisecond) // REAL-TIME: process death timing
	if !procAlive(grandchild) {
		t.Fatal("precondition: grandchild should survive the top's SIGKILL")
	}

	tr.Teardown()

	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("orphaned grandchild %d survived teardown — group reaping broken", grandchild)
	}
}

// The normal shutdown path: production Teardown on a live tree must take down
// the top process AND the grandchild together, synchronously (a caller
// respawns immediately after).
func TestTeardownKillsWholeTreeSynchronously(t *testing.T) {
	tr, grandchild := spawnGroupTree(t, "sync-top", "sync-bg")

	start := time.Now()          // REAL-TIME: teardown ladder timing
	tr.Teardown()                // REAL-TIME: teardown ladder timing
	elapsed := time.Since(start) // REAL-TIME: teardown ladder timing

	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("grandchild %d survived teardown", grandchild)
	}
	if !waitProcDead(tr.cmd.Process.Pid, time.Second) {
		t.Fatal("top process survived teardown")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("teardown took %v — exceeded its grace budget", elapsed)
	}
}

// Stubborn-tree escalation and concurrent/repeat teardown are covered by
// TestStdioTeardown_KillsStubbornTree and TestStdioTeardown_IdempotentAndConcurrent
// in stdio_lifecycle_test.go — not duplicated here.
