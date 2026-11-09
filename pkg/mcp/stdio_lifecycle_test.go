//go:build !windows

// stdio server lifecycle tests: the omp five-step teardown (stdin EOF →
// group TERM → grace → group KILL sweep → conditional grace) behind gbot's
// own stdioProcTransport, plus the connect-failure and daemon-exit holes it
// closes. Source of the sequence: oh-my-pi
// packages/coding-agent/src/mcp/transports/stdio.ts (close/terminateStdioProcess).
package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Compile-time checks for the transport/conn pair (the SDK interfaces the
// factory hands to client.Connect).
var (
	_ sdk.Transport  = (*stdioProcTransport)(nil)
	_ sdk.Connection = (*stdioProcConn)(nil)
)

// watchMarker reports the pid of the marker-carrying process as soon as it
// appears (0 if it never does within 5s). Unlike waitPidOf it is safe to call
// from a goroutine — teardown may kill the tree before ConnectToServer
// returns, so lifecycle tests must capture pids concurrently with the
// handshake window, not after it.
func watchMarker(marker string) <-chan int {
	ch := make(chan int, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second) // REAL-TIME: process spawn timing
		for time.Now().Before(deadline) {
			out, err := exec.Command("pgrep", "-f", "# "+marker).Output()
			if err == nil {
				for f := range strings.FieldsSeq(string(out)) {
					var v int
					if _, err := fmt.Sscanf(f, "%d", &v); err == nil && v > 0 {
						ch <- v
						return
					}
				}
			}
			time.Sleep(20 * time.Millisecond) // REAL-TIME: process spawn timing
		}
		ch <- 0
	}()
	return ch
}

// R1 — hole 2: connectInner's FailedServer path must not leak grandchildren.
// A wrapper command whose handshake never completes (sleep 60 speaks no MCP)
// leaves an npx-shaped tree behind when the SDK kills only the direct child.
func TestConnectToServer_ConnectFailure_KillsProcessGroup(t *testing.T) {
	t.Setenv("MCP_TIMEOUT", "2000") // REAL-TIME: handshake timeout window
	cm := NewClientManager(TransportFactory{}, true, "")

	bg := pidMarker("r1-bg")
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{
			Command: "sh",
			Args:    []string{"-c", fmt.Sprintf("sh -c 'while :; do sleep 1; done # %s' & exec sleep 60", bg)},
		},
		Scope: ScopeUser,
	}
	t.Cleanup(cleanupMarkers)
	pidCh := watchMarker(bg)

	start := time.Now() // REAL-TIME: connect timeout + teardown
	result, err := cm.ConnectToServer(context.Background(), "failing-sleeper", cfg)
	if err != nil {
		t.Fatalf("ConnectToServer returned error: %v", err)
	}
	if result.ConnType() != "failed" {
		t.Fatalf("ConnType = %q, want %q", result.ConnType(), "failed")
	}

	grandchild := <-pidCh
	if grandchild == 0 {
		t.Fatalf("grandchild marker %q never appeared", bg)
	}
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME: group teardown after failure
	for time.Now().Before(deadline) {
		if !procAlive(grandchild) {
			return
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: poll for grandchild death
	}
	t.Fatalf("grandchild %d survived connect-failure teardown (elapsed %v)", grandchild, time.Since(start))
}

// waitProcDead polls until pid is gone (exited or zombie) or the deadline
// passes. Teardown signals are asynchronous — a freshly killed process stays
// visible in /proc until the kernel schedules it, so immediate procAlive
// checks flake under load.
func waitProcDead(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout) // REAL-TIME: signal delivery timing
	for time.Now().Before(deadline) {
		if !procAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond) // REAL-TIME: signal delivery timing
	}
	return !procAlive(pid)
}

// startTestStdio spawns script through the real transport (SetProcessGroup so
// the tree owns a group; Stderr = os.Stderr — a *os.File, so exec adds no
// copier goroutine and cmd.Wait never blocks on live grandchildren) and
// connects it. The framed connection's Close is registered as cleanup so the
// ioConn read goroutine always exits even when the test fails early.
func startTestStdio(t *testing.T, script string, detached bool) *stdioProcTransport {
	t.Helper()
	t.Cleanup(cleanupMarkers)
	cmd := exec.Command("sh", "-c", script)
	SetProcessGroup(cmd)
	cmd.Stderr = os.Stderr
	tr := newStdioProcTransport("lifecycle-test", cmd, detached)
	conn, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatalf("stdio transport connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return tr
}

// T1 — step 1 of the sequence: the stdin write end is closed before signals.
// The leader traps TERM and can only end via stdin EOF (marker write) or
// SIGKILL; a KILL-only teardown kills the leader mid-read and leaves no
// marker. The TERM-immune grandchild must fall to the KILL sweep.
func TestStdioTeardown_ClosesStdinBeforeSignals(t *testing.T) {
	eofMarker := filepath.Join(t.TempDir(), "stdin-eof")
	bg := pidMarker("t1-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"trap \"\" TERM; while IFS= read -r line; do :; done; echo eof > %s; while :; do sleep 1; done",
		bg, filepath.ToSlash(eofMarker))
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	tr.teardownGraces(400*time.Millisecond, 200*time.Millisecond) // REAL-TIME: short graces

	if _, err := os.Stat(eofMarker); err != nil {
		t.Fatalf("stdin EOF marker missing — stdin was not closed before signals: %v", err)
	}
	if !waitProcDead(leader, time.Second) {
		t.Fatalf("leader %d survived teardown", leader)
	}
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("grandchild %d survived teardown", grandchild)
	}
}

// T2 — step 2: group SIGTERM reaches a cooperative tree; both processes die
// without burning the escalation graces.
func TestStdioTeardown_GroupTermKillsCooperativeTree(t *testing.T) {
	bg := pidMarker("t2-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"exit 1\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'trap \"exit 1\" TERM; while :; do sleep 1; done'",
		bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	start := time.Now()                                            // REAL-TIME: TERM grace timing
	tr.teardownGraces(1500*time.Millisecond, 300*time.Millisecond) // REAL-TIME: TERM grace timing
	elapsed := time.Since(start)                                   // REAL-TIME: TERM grace timing

	if !waitProcDead(leader, time.Second) || !waitProcDead(grandchild, time.Second) {
		t.Fatalf("cooperative tree survived group TERM (leader=%d grandchild=%d)", leader, grandchild)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("teardown took %v — TERM grace was fully burned, cooperative exit not observed", elapsed)
	}
}

// T3 — steps 3-5: a TERM-immune tree must fall to the group SIGKILL sweep
// after the TERM grace burns.
func TestStdioTeardown_KillsStubbornTree(t *testing.T) {
	bg := pidMarker("t3-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'trap \"\" TERM; while :; do sleep 1; done'",
		bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	start := time.Now() // REAL-TIME: escalation ladder timing
	tr.teardownGraces(300*time.Millisecond, 200*time.Millisecond)
	elapsed := time.Since(start) // REAL-TIME: escalation ladder timing

	if !waitProcDead(leader, time.Second) || !waitProcDead(grandchild, time.Second) {
		t.Fatalf("stubborn tree survived the SIGKILL sweep (leader=%d grandchild=%d)", leader, grandchild)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("teardown returned in %v before the TERM grace burned — escalation skipped a step", elapsed)
	}
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("teardown took %v — exceeded the 300ms+200ms grace budget", elapsed)
	}
}

// T4 — idempotency: concurrent and repeat teardowns neither deadlock, panic,
// nor double-signal; the second serial call must skip past (grab-and-null),
// not block on a once-waiter.
func TestStdioTeardown_IdempotentAndConcurrent(t *testing.T) {
	bg := pidMarker("t4-bg")
	script := fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & exec sh -c 'while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			tr.Teardown()
		})
	}
	wg.Wait()

	start := time.Now()          // REAL-TIME: grab-and-null skip timing
	tr.Teardown()                // REAL-TIME: grab-and-null skip timing
	elapsed := time.Since(start) // REAL-TIME: grab-and-null skip timing

	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("tree survived concurrent teardown (grandchild=%d)", grandchild)
	}
	if elapsed >= 100*time.Millisecond {
		t.Fatalf("second Teardown took %v — repeat callers block instead of skipping", elapsed)
	}
}

// T5 — teardown against an already-dead group (ESRCH): prompt, no panic.
func TestStdioTeardown_AlreadyDeadGroupIsPrompt(t *testing.T) {
	bg := pidMarker("t5-bg")
	script := fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & exec sh -c 'while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)

	tr.Teardown()
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("tree survived first teardown (grandchild=%d)", grandchild)
	}

	start := time.Now()          // REAL-TIME: dead-group skip timing
	tr.Teardown()                // REAL-TIME: dead-group skip timing
	elapsed := time.Since(start) // REAL-TIME: dead-group skip timing
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("second teardown on a dead group took %v", elapsed)
	}
}

// T6 — non-detached (windows-shaped): the leader exiting on TERM ends the
// teardown with no KILL sweep, and signaling is direct-child only, so a
// TERM-immune grandchild survives. SetProcessGroup is still applied so a
// buggy group-signal's blast radius stays inside the tree.
func TestStdioTeardown_NonDetached_StopsAfterLeaderExit(t *testing.T) {
	bg := pidMarker("t6-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'while :; do sleep 1; done'",
		bg)
	tr := startTestStdio(t, script, false)
	grandchild := waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	tr.teardownGraces(400*time.Millisecond, 200*time.Millisecond)

	if !waitProcDead(leader, time.Second) {
		t.Fatalf("leader %d survived direct-child TERM", leader)
	}
	if !procAlive(grandchild) {
		t.Fatalf("grandchild %d was killed in non-detached mode — KILL sweep fired where omp returns early", grandchild)
	}
}

// T6-adjacent — the early-return branch must be observable in the log (the
// branch's only unique output; its fd/return parts are behaviorally covered
// by T6 above).
func TestStdioTeardown_NonDetached_LogsLeaderTermExit(t *testing.T) {
	bg := pidMarker("t6l-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'while :; do sleep 1; done'",
		bg)
	tr := startTestStdio(t, script, false)
	waitPidOf(t, bg)

	recs := withCapturedLogs(t, func() {
		tr.teardownGraces(400*time.Millisecond, 200*time.Millisecond)
	})

	exits := logsFor(recs, "mcp: stdio server exited after SIGTERM")
	if len(exits) != 1 {
		t.Fatalf("logged %d leader-TERM-exit records, want 1: %v", len(exits), exits)
	}
	if exits[0]["server"] != "lifecycle-test" {
		t.Errorf("server attr = %q, want lifecycle-test — the name must reach the teardown log", exits[0]["server"])
	}
}

// T6b — non-detached stubborn leader: with exitedOnTerm=false the early
// return must NOT fire; the direct-child KILL still ends the leader. The
// marker grandchild is spawned AFTER the leader's trap arms, so its
// appearance proves TERM will be ignored — otherwise the TERM could win the
// trap-installation race and the leader dies without exercising the gate.
func TestStdioTeardown_NonDetached_StubbornLeaderStillKilled(t *testing.T) {
	bg := pidMarker("t6b-bg")
	script := fmt.Sprintf(
		"exec sh -c 'trap \"\" TERM; sh -c \"while :; do sleep 1; done # %s\" & while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, false)
	waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	tr.teardownGraces(300*time.Millisecond, 200*time.Millisecond)

	if !waitProcDead(leader, time.Second) {
		t.Fatalf("stubborn leader %d survived non-detached teardown — the early return fired before the direct-child KILL", leader)
	}
}

// T10 — detached + cooperative leader + TERM-immune grandchild: a detached
// leader's exit does NOT prove the group is empty; the KILL sweep must fire
// even after a clean leader exit.
func TestStdioTeardown_SweepFiresAfterCooperativeLeaderExit(t *testing.T) {
	bg := pidMarker("t10-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & exec sh -c 'while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)
	leader := tr.cmd.Process.Pid

	tr.teardownGraces(400*time.Millisecond, 200*time.Millisecond)

	if !waitProcDead(leader, time.Second) {
		t.Fatalf("leader %d survived teardown", leader)
	}
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("TERM-immune grandchild %d survived — the post-exit KILL sweep did not fire", grandchild)
	}
}

// The connection-level Close (the path the SDK drives on handshake failure
// and via session.Close's Closer) must tear the tree down through the
// transport teardown — not just close the framing pipes. A server that
// ignores stdin EOF (sleep never reads stdin) only dies if the signals run.
func TestStdioConn_CloseTearsDownTree(t *testing.T) {
	bg := pidMarker("conn-bg")
	script := fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & exec sleep 300", bg)
	cmd := exec.Command("sh", "-c", script)
	SetProcessGroup(cmd)
	cmd.Stderr = os.Stderr
	tr := newStdioProcTransport("lifecycle-test", cmd, processGroupApplied())
	conn, err := tr.Connect(context.Background())
	if err != nil {
		t.Fatalf("transport connect: %v", err)
	}
	t.Cleanup(cleanupMarkers)
	t.Cleanup(func() { _ = conn.Close() })
	grandchild := waitPidOf(t, bg)

	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}
	if !waitProcDead(tr.cmd.Process.Pid, time.Second) {
		t.Fatalf("leader %d survived conn.Close — framing-only close", tr.cmd.Process.Pid)
	}
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("grandchild %d survived conn.Close", grandchild)
	}
}

// Double Connect on one transport must fail loudly (the SDK contract is
// exactly one Connect per transport): exec.Cmd rejects the reuse with
// "already started", so no second live connection can ever be handed out.
func TestStdioTransport_DoubleConnectFails(t *testing.T) {
	tr := startTestStdio(t, "exec sh -c 'while :; do sleep 1; done'", true)

	_, err := tr.Connect(context.Background())
	if err == nil {
		t.Fatal("second Connect returned nil error — one transport must accept exactly one connection")
	}
	if !strings.Contains(err.Error(), "already started") {
		t.Fatalf("second Connect err = %q, want the exec already-started rejection", err)
	}
}

// Production-grace logging: the TERM signal step runs (logged with the
// server name) and a cooperative leader exit is observed within the real
// TERM grace — with the graces collapsed (e.g. 1000/Duration integer
// division → 0ns), the "leader exited on SIGTERM" record disappears because
// the grace expires before the exit can be observed.
func TestStdioTeardown_LogsTermStepAndLeaderExit(t *testing.T) {
	bg := pidMarker("log-bg")
	script := fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & exec sh -c 'while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)

	recs := withCapturedLogs(t, func() {
		tr.Teardown()
	})
	if !waitProcDead(grandchild, time.Second) {
		t.Fatal("tree survived teardown")
	}

	term := logsFor(recs, "mcp: signaled stdio server process group")
	foundTerm := false
	for _, r := range term {
		if r["server"] == "lifecycle-test" && r["signal"] == "terminated" {
			foundTerm = true
		}
	}
	if !foundTerm {
		t.Fatalf("no group-SIGTERM record with server=lifecycle-test signal=terminated — the TERM step or the name wiring is gone: %v", recs)
	}

	exits := logsFor(recs, "mcp: stdio server leader exited on SIGTERM, sweeping group")
	if len(exits) != 1 {
		t.Fatalf("logged %d leader-exit records, want exactly 1 (cooperative exit within the production TERM grace): %v", len(exits), recs)
	}
	if exits[0]["server"] != "lifecycle-test" {
		t.Errorf("server attr = %q, want lifecycle-test", exits[0]["server"])
	}
}

// Production graces on a stubborn tree: the 500ms KILL grace must observe
// the exit (no survival error logged). A collapsed grace (e.g. the const
// mutated to 500/Duration = 0 via integer division) logs the error even
// though the KILL works.
func TestStdioTeardown_ProductionGraces_ObserveStubbornExit(t *testing.T) {
	bg := pidMarker("prod-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'trap \"\" TERM; while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	grandchild := waitPidOf(t, bg)

	recs := withCapturedLogs(t, func() {
		tr.Teardown()
	})
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("stubborn tree survived production teardown (grandchild=%d)", grandchild)
	}
	if got := logsFor(recs, "mcp: stdio server still alive after SIGKILL"); len(got) != 0 {
		t.Fatalf("production KILL grace did not observe the exit: %v", got)
	}
}

// The SIGKILL survival error: with a kill grace too short to observe the
// exit, the error record must still be emitted.
func TestStdioTeardown_LogsSurvivedSIGKILL(t *testing.T) {
	bg := pidMarker("kill-log-bg")
	script := fmt.Sprintf(
		"sh -c 'trap \"\" TERM; while :; do sleep 1; done # %s' & "+
			"exec sh -c 'trap \"\" TERM; while :; do sleep 1; done'", bg)
	tr := startTestStdio(t, script, true)
	waitPidOf(t, bg)

	recs := withCapturedLogs(t, func() {
		// 1ns kill grace: the KILL was just sent on this goroutine — the
		// exit cannot have been observed yet, so the error branch is
		// deterministic.
		tr.teardownGraces(200*time.Millisecond, time.Nanosecond)
	})

	errs := logsFor(recs, "mcp: stdio server still alive after SIGKILL")
	if len(errs) != 1 {
		t.Fatalf("logged %d SIGKILL-survival records, want 1: %v", len(errs), recs)
	}
	if errs[0]["server"] != "lifecycle-test" {
		t.Errorf("server attr = %q, want lifecycle-test", errs[0]["server"])
	}
}

// The stdin-close guard: production never runs p.teardown twice (the
// transport grabs-and-nulls), but the guard must log on an already-closed
// stdin when it happens — and stay silent on the first close.
func TestStdioTeardown_LogsAlreadyClosedStdinOnlyOnRepeat(t *testing.T) {
	tr := startTestStdio(t, "exec sh -c 'while :; do sleep 1; done'", true)

	first := withCapturedLogs(t, func() {
		tr.proc.teardown(time.Nanosecond, time.Nanosecond)
	})
	if got := logsFor(first, "mcp: stdio stdin already closed"); len(got) != 0 {
		t.Fatalf("first teardown logged the already-closed record: %v", got)
	}

	second := withCapturedLogs(t, func() {
		tr.proc.teardown(time.Nanosecond, time.Nanosecond)
	})
	got := logsFor(second, "mcp: stdio stdin already closed")
	if len(got) != 1 {
		t.Fatalf("repeat teardown logged %d already-closed records, want 1: %v", len(got), second)
	}
	if got[0]["server"] != "lifecycle-test" {
		t.Errorf("server attr = %q, want lifecycle-test", got[0]["server"])
	}
}

// T7 — fast path: teardown against an already-exited leader (exited fed into
// the channel) must not wait out any grace window.
func TestStdioTeardown_LeaderAlreadyExitedIsPrompt(t *testing.T) {
	tr := startTestStdio(t, "exec sh -c 'exit 0'", true)
	leader := tr.cmd.Process.Pid

	// Wait for the leader to die and the Wait goroutine to feed exited; the
	// sleep absorbs the reap→send ordering so the timed teardown below is not
	// racing the channel write.
	deadline := time.Now().Add(2 * time.Second) // REAL-TIME: process exit timing
	for time.Now().Before(deadline) && procAlive(leader) {
		time.Sleep(10 * time.Millisecond) // REAL-TIME: process exit timing
	}
	if procAlive(leader) {
		t.Fatalf("leader %d never exited", leader)
	}
	time.Sleep(50 * time.Millisecond) // REAL-TIME: exited-channel feed ordering

	start := time.Now()                                            // REAL-TIME: dead-leader skip timing
	tr.teardownGraces(1500*time.Millisecond, 500*time.Millisecond) // REAL-TIME: dead-leader skip timing
	elapsed := time.Since(start)                                   // REAL-TIME: dead-leader skip timing
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("teardown on an exited leader took %v — grace window was waited out", elapsed)
	}
}

// T8 — happy-path integration: ConnectedServer.Close drives the cleanup
// closure (Teardown then session.Close) and reaps the whole tree, helper
// process (re-exec'd test binary) and background grandchild alike.
func TestConnectToServer_ConnectedClose_KillsProcessGroup(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bg := pidMarker("t8-bg")
	pidFile := filepath.Join(t.TempDir(), "pids")
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{
			Command: "sh",
			Args: []string{"-c", fmt.Sprintf(
				"sh -c 'while :; do sleep 1; done # %s' & exec %q -test.run=TestHelperNeverRuns", bg, exe)},
			Env: map[string]string{
				"GBOT_TEST_MCP_HELPER":   "1",
				"GBOT_TEST_MCP_PID_FILE": pidFile,
			},
		},
		Scope: ScopeUser,
	}
	cm := NewClientManager(TransportFactory{}, true, "")

	result, err := cm.ConnectToServer(context.Background(), "helper-tree", cfg)
	if err != nil {
		t.Fatalf("ConnectToServer: %v", err)
	}
	if result.ConnType() != "connected" {
		t.Fatalf("ConnType = %q, want connected (%s)", result.ConnType(), result.(*FailedServer).Error)
	}
	cs := result.(*ConnectedServer)
	grandchild := waitPidOf(t, bg)

	data, err := os.ReadFile(pidFile)
	if err != nil || len(strings.Fields(string(data))) == 0 {
		t.Fatalf("helper pid file unreadable: %v", err)
	}
	var helperPid int
	if _, err := fmt.Sscanf(strings.Fields(string(data))[0], "%d", &helperPid); err != nil {
		t.Fatalf("parse helper pid: %v", err)
	}

	if err := cs.Close(); err != nil {
		t.Fatalf("ConnectedServer.Close: %v", err)
	}
	if !waitProcDead(helperPid, 2*time.Second) {
		t.Fatalf("helper %d survived Close", helperPid)
	}
	if !waitProcDead(grandchild, 2*time.Second) {
		t.Fatalf("grandchild %d survived Close", grandchild)
	}
}

// T9 — pin (not red): a spawn failure (nonexistent command) must surface as
// FailedServer without panicking. After the transport owns spawning, this
// drives Connect's cmd.Start error → fds closed, err returned, proc never
// stored → connectInner's Teardown hits the nil-proc guard.
func TestConnectToServer_SpawnFailure_ReturnsFailedServer(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{Command: "gbot-nonexistent-bin-xyz"},
		Scope:  ScopeUser,
	}

	result, err := cm.ConnectToServer(context.Background(), "missing-bin", cfg)
	if err != nil {
		t.Fatalf("ConnectToServer returned error: %v", err)
	}
	if result.ConnType() != "failed" {
		t.Fatalf("ConnType = %q, want failed", result.ConnType())
	}
	failed := result.(*FailedServer)
	if !strings.Contains(failed.Error, "gbot-nonexistent-bin-xyz") {
		t.Fatalf("FailedServer.Error = %q, want the failing command named", failed.Error)
	}
}

// rawInitializeScript builds a raw sh server: a marker grandchild, a 2s
// delay (the grandchild's pid is captured concurrently while the handshake
// pends — teardown kills the tree the moment the handshake resolves, so the
// capture window must be generous), then a read loop answering ONLY the
// initialize request with the given protocol version. Afterwards it ignores
// everything: the hung-server shape. The re-exec'd SDK helper cannot serve
// these cases — it always negotiates a supported version and answers calls.
func rawInitializeScript(bg, protocolVersion string) string {
	return fmt.Sprintf(
		"sh -c 'while :; do sleep 1; done # %s' & sleep 2; "+
			"while IFS= read -r line; do case \"$line\" in *initialize*) "+
			"printf '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"%s\",\"capabilities\":{},\"serverInfo\":{\"name\":\"raw\",\"version\":\"1\"}}}\\n' ;; esac; done; "+
			"while :; do sleep 1; done",
		bg, protocolVersion)
}

// The SDK's unsupported-protocol-version path returns from client.Connect
// WITHOUT closing the connection — connectInner's explicit Teardown is the
// only thing reaping the tree there.
func TestConnectToServer_UnsupportedProtocolVersion_KillsProcessGroup(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	bg := pidMarker("uprot-bg")
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{
			Command: "sh",
			Args:    []string{"-c", rawInitializeScript(bg, "9999-99-99")},
		},
		Scope: ScopeUser,
	}
	t.Cleanup(cleanupMarkers)
	pidCh := watchMarker(bg)

	result, err := cm.ConnectToServer(context.Background(), "raw-bad-version", cfg)
	if err != nil {
		t.Fatalf("ConnectToServer returned error: %v", err)
	}
	if result.ConnType() != "failed" {
		t.Fatalf("ConnType = %q, want failed", result.ConnType())
	}
	failed := result.(*FailedServer)
	if !strings.Contains(failed.Error, "unsupported protocol version") {
		t.Fatalf("FailedServer.Error = %q, want the unsupported-protocol-version cause", failed.Error)
	}

	grandchild := <-pidCh
	if grandchild == 0 {
		t.Fatalf("grandchild marker %q never appeared", bg)
	}
	deadline := time.Now().Add(3 * time.Second) // REAL-TIME: group teardown after failure
	for time.Now().Before(deadline) {
		if !procAlive(grandchild) {
			return
		}
		time.Sleep(20 * time.Millisecond) // REAL-TIME: poll for grandchild death
	}
	t.Fatalf("grandchild %d survived the unsupported-version teardown — the SDK close never ran", grandchild)
}

// ConnectedServer.Close must kill the process BEFORE session.Close:
// jsonrpc2's graceful close waits for in-flight calls, and against a hung
// server (answers initialize, ignores tools/list) that parks indefinitely.
// Killing first fails the in-flight call fast and Close returns promptly.
func TestConnectedServerClose_ReturnsPromptlyAgainstHungServer(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	bg := pidMarker("hung-bg")
	cfg := ScopedMcpServerConfig{
		Config: &StdioConfig{
			Command: "sh",
			Args:    []string{"-c", rawInitializeScript(bg, "2025-06-18")},
		},
		Scope: ScopeUser,
	}
	t.Cleanup(cleanupMarkers)

	result, err := cm.ConnectToServer(context.Background(), "raw-hung", cfg)
	if err != nil {
		t.Fatalf("ConnectToServer: %v", err)
	}
	if result.ConnType() != "connected" {
		t.Fatalf("ConnType = %q, want connected", result.ConnType())
	}
	cs := result.(*ConnectedServer)
	grandchild := waitPidOf(t, bg)

	go func() { _, _ = cs.Session.ListTools(context.Background(), nil) }()
	time.Sleep(100 * time.Millisecond) // REAL-TIME: let tools/list go in flight

	closed := make(chan error, 1)
	go func() { closed <- cs.Close() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second): // REAL-TIME: kill-first close latency
		t.Fatal("cs.Close parked on the hung server — the process teardown did not run before session.Close")
	}
	if !waitProcDead(grandchild, time.Second) {
		t.Fatalf("grandchild %d survived Close", grandchild)
	}
}
