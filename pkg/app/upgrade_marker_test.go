//go:build !windows

package app

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cloudflare/tableflip"
)

// markerChildEnv is set to the name of the test a re-executed child should run as
// its body. These scenarios cannot run in-process: tableflip allows one Upgrader
// per process, so an in-process version would either be starved by an earlier
// test's Start() (passing without ever reaching the code under test) or starve
// every later test.
const markerChildEnv = "GBOT_TABLEFLIP_MARKER_CHILD"

// markerChildResult prefixes a child's verdict so the parent can tell it apart
// from the slog output booting an Upgrader writes.
const markerChildResult = "MARKER_CHILD_RESULT:"

// markerChildFds prefixes the child's fd table. These scenarios are only
// decidable in a process whose fd 3 or fd 4 is already occupied, so every run
// has to record the state it actually found — a run that silently stopped being
// destructive would otherwise keep reporting the bug as fixed.
const markerChildFds = "MARKER_CHILD_FDS:"

const (
	childOK           = 0
	childFailed       = 1
	childPrecondition = 2
)

// TestNewUpgrader_StaleMarkerKeepsNetpollUsable is the regression that matters.
// A process that merely INHERITED tableflip's marker — every shell and gbot
// spawned by a hot-restarted daemon does — used to hand that marker to
// tableflip.New, which wraps fds 3 and 4 as the handover pipes whatever is
// sitting there. In this build those are typically netpoll's eventpoll and
// eventfd — measured at 4 and 5, with a hole at 3 — and the *os.File finalizers
// close whatever they wrapped once the failed New is collected, so the process
// loses the ability to open any socket. The assertion is on the listen rather
// than on the marker because a marker-only assertion stays green while the
// descriptors stay corrupted.
func TestNewUpgrader_StaleMarkerKeepsNetpollUsable(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runStaleMarkerChild())
	}

	cmd, stdout, stderr := markerChild(t)
	cmd.Env = append(cmd.Env, tableflipEnvPrefix+"HAS_PARENT_7DIU3=yes")
	runErr := cmd.Run()

	// The child's verdict on stdout is authoritative: a process whose netpoll died
	// mid-listen takes the whole child down with a runtime fatal, which leaves no
	// verdict at all and is reported by the missing-verdict branch below.
	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("marker child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	// Logged before the branch below so that a skip — which prints nothing without
	// -v, and is the one way this sole regression guard could rot into a no-op —
	// still leaves the fd table that caused it in the log.
	if fds, ok := markerLine(stdout.String(), markerChildFds); ok {
		t.Logf("child fd table: %s", fds)
	}
	if rest, ok := strings.CutPrefix(verdict, "precondition-unmet:"); ok {
		t.Skipf("scenario cannot be destructive in this process: %s", rest)
	}
	if verdict != "ok" {
		t.Fatalf("stale tableflip marker corrupted the process: %s\nstderr:\n%s", verdict, stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

// TestTableflipHandoverFdsArePipes_GenuineChild covers the other side of the
// discriminator. A process exec'd the way tableflip exec's an upgraded child must
// be recognised as one, or the guard in newUpgrader would strip the marker from a
// real handover and the child would boot without the listeners its parent handed
// it. The child gets the pipe pair through ExtraFiles, which is how tableflip
// itself places them.
func TestTableflipHandoverFdsArePipes_GenuineChild(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runHandoverPipeChild())
	}

	readyW, namesR, _ := handoverPipes(t)

	cmd, stdout, stderr := markerChild(t, readyW, namesR)
	runErr := cmd.Run()

	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("pipe child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	if verdict != "ok" {
		t.Fatalf("genuine tableflip child not recognised: %s\nstderr:\n%s", verdict, stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

// TestTableflipHandoverFdsArePipes_NonHandoverDescriptors is the negative side of
// the same discriminator. Descriptors 3 and 4 that are not tableflip's pipe pair
// must not read as a handover, or a harness passing ExtraFiles (or a shell
// started with 3<) loses the marker-stripping protection. The last case pins the
// limit the guard's comment admits: two unrelated FIFOs at both numbers ARE read
// as a handover — documented behavior, not an accident.
func TestTableflipHandoverFdsArePipes_NonHandoverDescriptors(t *testing.T) {
	parentName := t.Name()
	if os.Getenv(markerChildEnv) == parentName {
		os.Exit(runHandoverFdVerdictChild())
	}
	regularFile := func(name string) *os.File {
		t.Helper()
		f, err := os.Create(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	fifo := func(name string) *os.File {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Fatalf("mkfifo %s: %v", name, err)
		}
		// O_NONBLOCK on the read end: opening a FIFO with no writer blocks.
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0o644)
		if err != nil {
			t.Fatalf("open fifo %s: %v", name, err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	pipeEnd := func() *os.File {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		t.Cleanup(func() {
			_ = r.Close()
			_ = w.Close()
		})
		return r
	}
	cases := []struct {
		name string
		fds  []*os.File
		want string
	}{
		{"two regular files", []*os.File{regularFile("a"), regularFile("b")}, "false"},
		{"one pipe, one regular file", []*os.File{pipeEnd(), regularFile("c")}, "false"},
		{"one regular file, one pipe", []*os.File{regularFile("d"), pipeEnd()}, "false"},
		{"two unrelated fifos", []*os.File{fifo("e"), fifo("f")}, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, stdout, stderr := markerChild(t, tc.fds...)
			// markerChild stamps the subtest's name; the child's dispatch
			// compares against its own top-level name, so the parent's name
			// has to win (os/exec keeps the last value of a duplicate).
			cmd.Env = append(cmd.Env, markerChildEnv+"="+parentName)
			runErr := cmd.Run()
			verdict, found := markerLine(stdout.String(), markerChildResult)
			if !found {
				t.Fatalf("fd child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
					runErr, stdout.String(), stderr.String())
			}
			if verdict != tc.want {
				t.Errorf("tableflipHandoverFdsArePipes = %s, want %s\nstderr:\n%s", verdict, tc.want, stderr.String())
			}
			if runErr != nil {
				t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
			}
		})
	}
}

// runHandoverFdVerdictChild prints what the fd guard concludes about the
// descriptors this process was exec'd with. It deliberately does not boot an
// upgrader: the discriminator is what is under test, and newParent's blocking
// gob.Decode on fd 4 would turn the FIFO case into a hang.
func runHandoverFdVerdictChild() int {
	fmt.Printf("%s %v\n", markerChildResult, tableflipHandoverFdsArePipes())
	return childOK
}

// TestNewUpgrader_KeepsMarkerForGenuineChild covers the guard in newUpgrader from// the other side: a process exec'd with tableflip's real handover descriptors and
// the marker in its environment must still come back from newUpgrader with a
// parent. A guard that scrubs this one is worse than no guard at all — a real
// upgraded child would boot with none of the listeners its parent handed it.
func TestNewUpgrader_KeepsMarkerForGenuineChild(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runGenuineHandoverChild())
	}

	readyW, namesR, namesW := handoverPipes(t)

	// newParent gob-decodes the inherited fd names from fd 4 before it looks at
	// anything else, and a real parent writes them as soon as it has exec'd the
	// child (child.go's writeNames), so putting an empty name list in the pipe up
	// front is what a real parent does. Without it the child's Decode blocks on a
	// parent that only ever writes during Upgrade.
	if err := gob.NewEncoder(namesW).Encode([][]string{}); err != nil {
		t.Fatalf("encode names: %v", err)
	}

	cmd, stdout, stderr := markerChild(t, readyW, namesR)
	cmd.Env = append(cmd.Env, tableflipEnvPrefix+"HAS_PARENT_7DIU3=yes")
	runErr := cmd.Run()

	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("handover child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	if verdict != "ok" {
		t.Fatalf("newUpgrader scrubbed a genuine handover: %s\nstderr:\n%s", verdict, stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

// handoverPipes builds the descriptor pair tableflip exec's an upgraded child
// with — the ready-write end of one pipe and the names-read end of a second one,
// which land at fd 3 and fd 4 in ExtraFiles order exactly as child.go's
// fds = {stdin, stdout, stderr, readyW, namesR} does them — and returns the two
// ends the child receives plus the parent's write half.
//
// The parent's halves stay open for the child's whole lifetime: closing them early
// would leave the child's read end at EOF, and tableflip's drain of fd 4 inside the
// child only completes once the parent's writer closes.
func handoverPipes(t *testing.T) (readyW, namesR, namesW *os.File) {
	t.Helper()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("ready pipe: %v", err)
	}
	namesR, namesW, err = os.Pipe()
	if err != nil {
		_ = readyR.Close()
		_ = readyW.Close()
		t.Fatalf("names pipe: %v", err)
	}
	t.Cleanup(func() {
		for _, f := range []*os.File{readyR, readyW, namesR, namesW} {
			_ = f.Close()
		}
	})
	return readyW, namesR, namesW
}

// markerChild re-executes this test binary running only this test, in an
// environment free of any marker this process itself inherited, with extraFiles
// landing at fd 3 upwards.
func markerChild(t *testing.T, extraFiles ...*os.File) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	// The child is a re-exec of this test binary carrying only -test.run, so it
	// inherits no -test.timeout: a child stuck in newParent's blocking Decode
	// would sit until the package's own timeout and take every other pkg/app
	// result with it, with a goroutine dump pointing at this Run rather than at
	// the child. The missing-verdict branch in each caller reports a kill
	// legibly, which is the point of bounding the wait.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^"+t.Name()+"$")
	cmd.Env = append(envWithoutTableflip(), markerChildEnv+"="+t.Name(), "GBOT_SUPERVISED=0")
	cmd.ExtraFiles = extraFiles
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd, stdout, stderr
}

// runStaleMarkerChild is the listen child's body: boot an upgrader with the
// marker in the environment, then try to open a socket.
func runStaleMarkerChild() int {
	fmt.Printf("%s %s\n", markerChildFds, openFds())
	if reason, destructive := staleMarkerScenarioDestructive(); !destructive {
		fmt.Printf("%s precondition-unmet: %s\n", markerChildResult, reason)
		return childPrecondition
	}
	if upg := newUpgrader(); upg != nil {
		upg.Stop()
	}
	// The descriptors are closed by a finalizer, so the damage only lands once the
	// *os.File values New created are collected. One runtime.GC is not enough: it
	// returns when the sweep finishes and only yields to the finalizer goroutine
	// when no concurrent sweep is left, so it can come back before the finalizers
	// have run. The second call closes that window.
	runtime.GC()
	runtime.GC()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("%s listen failed: %v\n", markerChildResult, err)
		return childFailed
	}
	if err := ln.Close(); err != nil {
		fmt.Printf("%s close failed: %v\n", markerChildResult, err)
		return childFailed
	}
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}

// runHandoverPipeChild is the pipes child's body. It reads the descriptors it was
// exec'd with and leaves; touching the network would be pointless and would put
// netpoll back in the picture.
func runHandoverPipeChild() int {
	if !tableflipHandoverFdsArePipes() {
		fmt.Printf("%s fds 3 and 4 were not read as handover pipes\n", markerChildResult)
		return childFailed
	}
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}

// runGenuineHandoverChild is the handover child's body: boot an upgrader the way a
// real upgraded child does, and report whether it still has a parent afterwards.
func runGenuineHandoverChild() int {
	upg := newUpgrader()
	hasParent := upg != nil && upg.HasParent()
	if upg != nil {
		upg.Stop()
	}
	if !hasParent {
		fmt.Printf("%s marker did not survive newUpgrader (upg nil: %v)\n", markerChildResult, upg == nil)
		return childFailed
	}
	// The genuine child must still scrub the marker: it is now the parent, and
	// every shell it spawns would otherwise believe it is a tableflip child.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, tableflipEnvPrefix) {
			fmt.Printf("%s marker leaked into the child env: %s\n", markerChildResult, kv)
			return childFailed
		}
	}
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}

// staleMarkerScenarioDestructive reports whether at least one of the two
// descriptors tableflip hard-codes is open here and is not a handover pipe — the
// state in which New() wraps an unrelated descriptor and its finalizer closes it.
// One is enough: closing netpoll's epoll fd alone is fatal, which is why the
// production symptom names only fd 4.
//
// With neither one open the scenario is not harmless, only undecidable: os.NewFile
// returns nil for a negative fd alone, so a hole at 3 still yields a *os.File
// whose finalizer closes whatever this process later allocates at that number.
// Skipping is still right — whether that finalizer lands before the listen is
// luck, so the child cannot report a verdict either way — but the residual hazard
// is a stolen future descriptor, not a clean no-op.
func staleMarkerScenarioDestructive() (string, bool) {
	for _, fd := range [2]int{3, 4} {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err == nil && st.Mode&syscall.S_IFMT != syscall.S_IFIFO {
			return "", true
		}
	}
	return "neither fd 3 nor fd 4 is an open non-pipe descriptor", false
}

// openFds describes this process's fd table, which is what decides whether the
// stale-marker scenario can do any damage here at all.
func openFds() string {
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return fmt.Sprintf("unreadable (%v)", err)
	}
	var b strings.Builder
	for _, e := range names {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s=%s ", e.Name(), target)
	}
	return strings.TrimSpace(b.String())
}

// envWithoutTableflip strips any marker this test process itself inherited, so a
// child's environment contains exactly the ones this test sets.
func envWithoutTableflip() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if name, _, ok := strings.Cut(kv, "="); ok && !strings.HasPrefix(name, tableflipEnvPrefix) {
			env = append(env, kv)
		}
	}
	return env
}

// markerLine returns the payload of the child's line carrying the given prefix.
func markerLine(stdout, prefix string) (string, bool) {
	for line := range strings.SplitSeq(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// TestNewUpgrader_SupervisedNeverReachesTableflip pins the supervised
// short-circuit. In-process the assertion is unfalsifiable: an earlier test's
// Start() has already claimed tableflip's one-Upgrader slot, so newUpgrader
// returns nil there whether or not the short-circuit ran. The child has a fresh
// slot and reports whether New was reached — tableflip.New fails with "only a
// single Upgrader allowed" if it was.
func TestNewUpgrader_SupervisedNeverReachesTableflip(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runSupervisedChild())
	}
	cmd, stdout, stderr := markerChild(t)
	cmd.Env = append(cmd.Env, "GBOT_SUPERVISED=1")
	runErr := cmd.Run()
	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("supervised child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	if verdict != "ok" {
		t.Fatalf("supervised mode reached tableflip: %s\nstderr:\n%s", verdict, stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

func runSupervisedChild() int {
	if upg := newUpgrader(); upg != nil {
		upg.Stop()
		fmt.Printf("%s supervised mode constructed an upgrader\n", markerChildResult)
		return childFailed
	}
	u, err := tableflip.New(tableflip.Options{})
	if err != nil {
		fmt.Printf("%s tableflip slot already consumed: %v\n", markerChildResult, err)
		return childFailed
	}
	u.Stop()
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}

// TestNewUpgrader_PinsArgv0InFreshProcess is the falsifiable half of the argv0
// pin. In-process the test binary's argv[0] already equals os.Executable(), so
// the assertion passes with the line deleted. Here the child is exec'd through a
// path that is NOT what os.Executable reports, and the pin must rewrite it.
func TestNewUpgrader_PinsArgv0InFreshProcess(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runArgv0Child())
	}
	cmd, stdout, stderr := markerChild(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	rel, err := filepath.Rel(wd, exe)
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	// execve still resolves the real binary; only argv[0] carries the alias.
	cmd.Dir = wd
	cmd.Path = exe
	cmd.Args = []string{rel, "-test.run=^" + t.Name() + "$"}
	runErr := cmd.Run()
	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("argv0 child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	if verdict != "ok" {
		t.Fatalf("argv[0] not pinned to os.Executable(): %s\nstderr:\n%s", verdict, stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

func runArgv0Child() int {
	before := os.Args[0]
	upg := newUpgrader()
	if upg == nil {
		fmt.Printf("%s no upgrader in a fresh process\n", markerChildResult)
		return childFailed
	}
	upg.Stop()
	exe, err := os.Executable()
	if err != nil {
		fmt.Printf("%s os.Executable: %v\n", markerChildResult, err)
		return childFailed
	}
	if os.Args[0] != exe {
		fmt.Printf("%s argv[0]=%q (was %q), want %q\n", markerChildResult, os.Args[0], before, exe)
		return childFailed
	}
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}

// TestNewUpgrader_LogsDisabledOnNewFailure covers the one report newUpgrader has
// when tableflip refuses: the daemon silently loses hot-restart unless the
// warning names why. The child is exec'd with a real handover pipe pair, so the
// fd guard passes and New runs — and fails, because the names pipe carries
// something that is not the parent's name list.
func TestNewUpgrader_LogsDisabledOnNewFailure(t *testing.T) {
	if os.Getenv(markerChildEnv) == t.Name() {
		os.Exit(runNewFailureChild())
	}
	readyW, namesR, namesW := handoverPipes(t)
	// A real parent encodes [][]string here; a string decodes to an error.
	if err := gob.NewEncoder(namesW).Encode("not a name list"); err != nil {
		t.Fatalf("encode garbage: %v", err)
	}
	cmd, stdout, stderr := markerChild(t, readyW, namesR)
	cmd.Env = append(cmd.Env, tableflipEnvPrefix+"HAS_PARENT_7DIU3=yes")
	runErr := cmd.Run()
	verdict, found := markerLine(stdout.String(), markerChildResult)
	if !found {
		t.Fatalf("new-failure child never reported a verdict (run: %v)\nstdout:\n%s\nstderr:\n%s",
			runErr, stdout.String(), stderr.String())
	}
	if verdict != "ok" {
		t.Fatalf("newUpgrader did not degrade to nil on a failed New: %s\nstderr:\n%s", verdict, stderr.String())
	}
	if !strings.Contains(stderr.String(), "upgrade: disabled") {
		t.Errorf("stderr has no \"upgrade: disabled\" warning, so a disabled hot-restart went unreported:\n%s", stderr.String())
	}
	if runErr != nil {
		t.Fatalf("scenario passed but the child exited non-zero: %v\nstderr:\n%s", runErr, stderr.String())
	}
}

func runNewFailureChild() int {
	upg := newUpgrader()
	if upg != nil {
		upg.Stop()
		fmt.Printf("%s tableflip.New unexpectedly succeeded\n", markerChildResult)
		return childFailed
	}
	fmt.Printf("%s ok\n", markerChildResult)
	return childOK
}
