package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	shellescape "al.essio.dev/pkg/shellescape"

	"github.com/liuy/gbot/pkg/tool"
)

// realPath resolves a temp dir to its physical form — `pwd -P` in the wrapped
// command reports physical paths, so expectations must match that form.
func realPath(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return real
}

// sessionCwd simulates the engine-side working dir store: SetWorkingDir
// captures updates, WorkingDir is whatever the caller last observed.
type sessionCwd struct {
	mu  sync.Mutex
	dir string
}

func (s *sessionCwd) set(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dir = dir
}

func (s *sessionCwd) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

func (s *sessionCwd) tctx(original string) *tool.ToolUseContext {
	return &tool.ToolUseContext{
		WorkingDir:         s.get(),
		OriginalWorkingDir: original,
		SetWorkingDir:      s.set,
	}
}

// Source: Shell.ts:385-421 — foreground commands write `pwd -P` to a temp
// file; the caller reads it back and updates the session cwd so the next
// command starts where `cd` left it.
func TestCdPersistence_AcrossCommands_NonPTY(t *testing.T) {
	// no t.Parallel: mutates package var ptySupported
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	dirA, dirB := t.TempDir(), t.TempDir()
	realA, realB := realPath(t, dirA), realPath(t, dirB)

	sess := &sessionCwd{dir: dirA}

	// Command 1: cd into dirB
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"cd `+dirB+`"}`), sess.tctx(realA))
	if err != nil {
		t.Fatalf("Execute cd: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (Stderr=%q)", out.ExitCode, out.Stderr)
	}
	if got := sess.get(); got != realB {
		t.Errorf("session cwd after cd = %q, want %q", got, realB)
	}
	if out.CWD != realB {
		t.Errorf("Output.CWD = %q, want effective cwd %q", out.CWD, realB)
	}

	// Command 2: pwd must run in dirB — proves the persistence round trip
	sess.set(realB)
	res2, err := Execute(context.Background(),
		json.RawMessage(`{"command":"pwd"}`), sess.tctx(realA))
	if err != nil {
		t.Fatalf("Execute pwd: %v", err)
	}
	out2 := res2.Data.(*Output)
	if got := strings.TrimSpace(out2.Stdout); got != realB {
		t.Errorf("pwd after cd = %q, want %q", got, realB)
	}
}

// PTY twin of the persistence test. CWD and the captured session dir come
// from the cwd file (exact), stdout rendering is not asserted exactly.
func TestCdPersistence_AcrossCommands_PTY(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	dirA, dirB := t.TempDir(), t.TempDir()
	realA, realB := realPath(t, dirA), realPath(t, dirB)
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"cd `+dirB+`"}`), sess.tctx(realA))
	if err != nil {
		t.Fatalf("Execute cd: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (Stderr=%q)", out.ExitCode, out.Stderr)
	}
	if got := sess.get(); got != realB {
		t.Errorf("session cwd after cd = %q, want %q", got, realB)
	}
	if out.CWD != realB {
		t.Errorf("Output.CWD = %q, want effective cwd %q", out.CWD, realB)
	}
}

// Source: bashProvider.ts:187 — `&& pwd -P` never runs when the user command
// fails, so the cwd file is absent and the session cwd must not change.
func TestCdPersistence_CommandFailureNoUpdate(t *testing.T) {
	// no t.Parallel: mutates package var ptySupported
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	dirA, dirB := t.TempDir(), t.TempDir()
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"cd `+dirB+` && exit 3"}`), sess.tctx(realPath(t, dirA)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", out.ExitCode)
	}
	if got := sess.get(); got != dirA {
		t.Errorf("session cwd after failed command = %q, want unchanged %q", got, dirA)
	}
	// No pwd -P ran, so the effective cwd is the spawn cwd verbatim.
	if out.CWD != dirA {
		t.Errorf("Output.CWD = %q, want spawn cwd %q", out.CWD, dirA)
	}
}

// Source: Shell.ts:395 — backgroundTaskId results skip the cwd read-back.
// gbot implements this by not appending pwd -P to background commands at all.
func TestCdPersistence_BackgroundNoUpdate(t *testing.T) {
	// no t.Parallel: mutates package var ptySupported
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	origReg := defaultRegistry
	freshReg := NewBackgroundJobRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	dirA, dirB := t.TempDir(), t.TempDir()
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"cd `+dirB+`; echo bgdone","run_in_background":true}`),
		sess.tctx(realPath(t, dirA)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	// spawnBackground reports the job ID in the Stdout text (BackgroundJobID
	// is reserved for the timeout-transition path).
	jobID := extractBgID(out.Stdout)
	if jobID == "" {
		t.Fatalf("no background job ID in %q", out.Stdout)
	}
	if _, err := freshReg.Wait(jobID); err != nil {
		t.Fatalf("wait background job: %v", err)
	}
	if got := sess.get(); got != dirA {
		t.Errorf("session cwd after background cd = %q, want unchanged %q", got, dirA)
	}
}

// Source: Shell.ts:220-238 — spawn cwd was deleted (e.g. `cd $(mktemp -d)
// && rm -rf $PWD`): fall back to the session's original directory and update
// the session state to it.
func TestCwdFallback_DeletedDir(t *testing.T) {
	dirA := t.TempDir()
	dirDead := t.TempDir()
	if err := os.RemoveAll(dirDead); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	sess := &sessionCwd{dir: dirDead}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"pwd"}`), sess.tctx(dirA))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (Stderr=%q)", out.ExitCode, out.Stderr)
	}
	if out.CWD != dirA {
		t.Errorf("Output.CWD = %q, want fallback %q", out.CWD, dirA)
	}
	if got := sess.get(); got != dirA {
		t.Errorf("session cwd after fallback = %q, want %q", got, dirA)
	}
	if !strings.Contains(out.Stdout, dirA) {
		t.Errorf("Stdout = %q, want to contain fallback dir %q", out.Stdout, dirA)
	}
}

// Source: Shell.ts:233-237 — original cwd is gone too: fail the command
// before spawning with the TS error message.
func TestCwdFallback_BothDirsDeleted(t *testing.T) {
	dirDead := t.TempDir()
	dirDead2 := t.TempDir()
	if err := os.RemoveAll(dirDead); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if err := os.RemoveAll(dirDead2); err != nil {
		t.Fatalf("RemoveAll2: %v", err)
	}
	sess := &sessionCwd{dir: dirDead}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"pwd"}`), sess.tctx(dirDead2))
	if err != nil {
		t.Fatalf("Execute: %v (pre-spawn failure is a result, not an error)", err)
	}
	out, ok := res.Data.(*Output)
	if !ok {
		t.Fatalf("result.Data type = %T, want *Output", res.Data)
	}
	if out.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", out.ExitCode)
	}
	wantMsg := fmt.Sprintf("Working directory %q no longer exists. Please restart Claude from an existing directory.", dirDead)
	if out.Stderr != wantMsg {
		t.Errorf("Stderr = %q, want %q", out.Stderr, wantMsg)
	}
	if got := sess.get(); got != dirDead {
		t.Errorf("session cwd = %q, want unchanged %q", got, dirDead)
	}
}

// Per-call cwd override pointing at a deleted dir hits the same liveness
// check; with no original dir available (nil tctx) the command fails.
func TestCwdFallback_DeletedInputCWD(t *testing.T) {
	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"pwd","cwd":"/nonexistent/gbot-cwd-test"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", out.ExitCode)
	}
	wantMsg := "Working directory \"/nonexistent/gbot-cwd-test\" no longer exists. Please restart Claude from an existing directory."
	if out.Stderr != wantMsg {
		t.Errorf("Stderr = %q, want %q", out.Stderr, wantMsg)
	}
}

// The session-tracked dir is live, but the per-call cwd override points at a
// deleted dir: spawn falls back to the original dir, yet the session state
// must stay untouched — a one-shot override failing is not a session event
// (TS has no per-call cwd; Shell.ts:220-238 only ever sees the session cwd).
func TestCwdFallback_ExplicitCWD_DoesNotRewriteSession(t *testing.T) {
	// no t.Parallel: mutates package var ptySupported
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	dirLive := t.TempDir()
	dirOrig := t.TempDir()
	dirDead := t.TempDir()
	if err := os.RemoveAll(dirDead); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	sess := &sessionCwd{dir: dirLive}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"pwd","cwd":"`+dirDead+`"}`), sess.tctx(dirOrig))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (Stderr=%q)", out.ExitCode, out.Stderr)
	}
	if out.CWD != dirOrig {
		t.Errorf("Output.CWD = %q, want fallback %q", out.CWD, dirOrig)
	}
	if got := sess.get(); got != dirLive {
		t.Errorf("session cwd = %q, want unchanged %q", got, dirLive)
	}
	if !strings.Contains(out.Stdout, realPath(t, dirOrig)) {
		t.Errorf("Stdout = %q, want to contain fallback dir %q", out.Stdout, realPath(t, dirOrig))
	}
}

// leakedCwdFiles globs the tracking-file pattern in os.TempDir(). Tests that
// probe for leaks set TMPDIR to a private dir first, so any hit belongs to
// the code under test.
func leakedCwdFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "gbot-*-cwd"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return matches
}

// Error returns that happen after buildCommand pre-created the tracking file
// must still remove it. The shell is pointed at a nonexistent binary so the
// spawn itself fails. Subtests cover the three leak sites: PTY sync
// (runPTYCommand error), non-PTY sync (cmd.Run non-ExitError), and non-PTY
// auto-background (cmd.Start failure).
func TestCwdFile_CleanedOnErrorReturn(t *testing.T) {
	forceSpawnFailure := func(t *testing.T) {
		t.Helper()
		t.Setenv("TMPDIR", t.TempDir())
		origShell := shellCommand
		shellCommand = "/nonexistent/gbot-shell-spawn-failure"
		t.Cleanup(func() { shellCommand = origShell })
	}

	t.Run("pty sync", func(t *testing.T) {
		if !ptySupported {
			t.Skip("PTY not available")
		}
		// no t.Parallel: mutates shellCommand and TMPDIR
		forceSpawnFailure(t)
		// "sleep" is on the auto-background denylist → executePTYSync
		_, err := Execute(context.Background(),
			json.RawMessage(`{"command":"sleep 0.1"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("Execute err = %v, want shell spawn failure", err)
		}
		if leaks := leakedCwdFiles(t); len(leaks) != 0 {
			t.Errorf("cwd tracking file leaked on error return: %v", leaks)
		}
	})

	t.Run("non-pty sync", func(t *testing.T) {
		// no t.Parallel: mutates ptySupported, shellCommand, TMPDIR
		origPty := ptySupported
		ptySupported = false
		t.Cleanup(func() { ptySupported = origPty })
		forceSpawnFailure(t)
		_, err := Execute(context.Background(),
			json.RawMessage(`{"command":"sleep 0.1"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("Execute err = %v, want shell spawn failure", err)
		}
		if leaks := leakedCwdFiles(t); len(leaks) != 0 {
			t.Errorf("cwd tracking file leaked on error return: %v", leaks)
		}
	})

	t.Run("non-pty auto-bg start", func(t *testing.T) {
		// no t.Parallel: mutates ptySupported, shellCommand, TMPDIR
		origPty := ptySupported
		ptySupported = false
		t.Cleanup(func() { ptySupported = origPty })
		forceSpawnFailure(t)
		// "echo" is auto-backgroundable → executeNonPTYAutoBg → cmd.Start
		_, err := Execute(context.Background(),
			json.RawMessage(`{"command":"echo hi"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("Execute err = %v, want shell spawn failure", err)
		}
		if leaks := leakedCwdFiles(t); len(leaks) != 0 {
			t.Errorf("cwd tracking file leaked on error return: %v", leaks)
		}
	})
}

// Timeout-to-background: the command's `pwd -P >|` tail rewrites the tracking
// file when the process finally exits, so deleting the file at transition
// time is useless — every timed-out-but-completing command would leak one.
// TS hangs unlink off result.then() (Shell.ts:416-420), which only resolves
// in #handleExit, so cleanup belongs at job completion.
func TestCwdFile_CleanedAfterTimeoutTransitionCompletes(t *testing.T) {
	// no t.Parallel: mutates ptySupported, defaultRegistry, TMPDIR
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	origReg := defaultRegistry
	freshReg := NewBackgroundJobRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	t.Setenv("TMPDIR", t.TempDir())

	dirA := t.TempDir()
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 1","timeout":100}`), sess.tctx(realPath(t, dirA)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID is empty, want auto-background on timeout")
	}
	if _, err := freshReg.Wait(out.BackgroundJobID); err != nil {
		t.Fatalf("wait background job: %v", err)
	}
	if leaks := leakedCwdFiles(t); len(leaks) != 0 {
		t.Errorf("cwd tracking file leaked after background job exit: %v", leaks)
	}
}

// PTY variant of the timeout-transition leak test above: same guarantee
// through executePTYAutoBg's completionFunc.
func TestCwdFile_CleanedAfterTimeoutTransitionCompletes_PTY(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}
	// no t.Parallel: mutates defaultRegistry, TMPDIR
	origReg := defaultRegistry
	freshReg := NewBackgroundJobRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	t.Setenv("TMPDIR", t.TempDir())

	dirA := t.TempDir()
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 1","timeout":100}`), sess.tctx(realPath(t, dirA)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID is empty, want auto-background on timeout")
	}
	if _, err := freshReg.Wait(out.BackgroundJobID); err != nil {
		t.Fatalf("wait background job: %v", err)
	}
	if leaks := leakedCwdFiles(t); len(leaks) != 0 {
		t.Errorf("cwd tracking file leaked after background job exit: %v", leaks)
	}
}

// Source: bashProvider.ts:186 — the wrapper appends `pwd -P >| <file>` with
// the cwd file path shell-quoted; trackCwd=false (background spawns) omits it.
func TestBuildCommand_CwdTracking(t *testing.T) {
	t.Parallel()

	wrapped, cwdFile := buildCommand("echo hi", nil, true)
	if cwdFile == "" {
		t.Fatal("cwdFile is empty, want a temp file path")
	}
	wantTail := "pwd -P >| " + shellescape.Quote(cwdFile)
	if !strings.HasSuffix(wrapped, wantTail) {
		t.Errorf("wrapped = %q, want suffix %q", wrapped, wantTail)
	}

	wrappedBg, cwdFileBg := buildCommand("echo hi", nil, false)
	if cwdFileBg != "" {
		t.Errorf("cwdFile = %q, want empty for trackCwd=false", cwdFileBg)
	}
	if strings.Contains(wrappedBg, "pwd -P") {
		t.Errorf("wrappedBg = %q, want no pwd -P for trackCwd=false", wrappedBg)
	}
}

// If the temp file for cwd tracking cannot be created, tracking degrades to
// off — the command still runs, cwd just isn't persisted for that call.
func TestBuildCommand_TempFileCreationFailure(t *testing.T) {
	t.Setenv("TMPDIR", "/nonexistent/gbot-tmpdir-test")

	wrapped, cwdFile := buildCommand("echo hi", nil, true)
	if cwdFile != "" {
		t.Errorf("cwdFile = %q, want empty when temp creation fails", cwdFile)
	}
	if strings.Contains(wrapped, "pwd -P") {
		t.Errorf("wrapped = %q, want no pwd -P when temp creation fails", wrapped)
	}
}

func TestSyncCwd(t *testing.T) {
	t.Parallel()

	writeCwdFile := func(content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "cwd")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return path
	}

	t.Run("changed dir updates session and removes file", func(t *testing.T) {
		t.Parallel()
		file := writeCwdFile("/new/cwd\n")
		sess := &sessionCwd{dir: "/old"}
		got := syncCwd(file, "/old", sess.tctx("/orig"))
		if got != "/new/cwd" {
			t.Errorf("syncCwd = %q, want /new/cwd", got)
		}
		if sess.get() != "/new/cwd" {
			t.Errorf("session cwd = %q, want /new/cwd", sess.get())
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("cwd file still exists after syncCwd (stat err=%v)", err)
		}
	})

	t.Run("unchanged dir keeps session and removes file", func(t *testing.T) {
		t.Parallel()
		file := writeCwdFile("/old\n")
		sess := &sessionCwd{dir: "/old"}
		got := syncCwd(file, "/old", sess.tctx("/orig"))
		if got != "/old" {
			t.Errorf("syncCwd = %q, want /old", got)
		}
		if sess.get() != "/old" {
			t.Errorf("session cwd = %q, want unchanged /old", sess.get())
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("cwd file still exists after syncCwd (stat err=%v)", err)
		}
	})

	t.Run("missing file keeps spawn cwd", func(t *testing.T) {
		t.Parallel()
		sess := &sessionCwd{dir: "/old"}
		got := syncCwd(filepath.Join(t.TempDir(), "never-created"), "/old", sess.tctx("/orig"))
		if got != "/old" {
			t.Errorf("syncCwd = %q, want /old", got)
		}
		if sess.get() != "/old" {
			t.Errorf("session cwd = %q, want unchanged /old", sess.get())
		}
	})

	t.Run("nil tctx still returns effective cwd", func(t *testing.T) {
		t.Parallel()
		file := writeCwdFile("/next\n")
		got := syncCwd(file, "/old", nil)
		if got != "/next" {
			t.Errorf("syncCwd = %q, want /next", got)
		}
	})

	t.Run("empty cwd file path is a no-op", func(t *testing.T) {
		t.Parallel()
		sess := &sessionCwd{dir: "/old"}
		got := syncCwd("", "/old", sess.tctx("/orig"))
		if got != "/old" {
			t.Errorf("syncCwd = %q, want /old", got)
		}
	})
}

// Auto-background on timeout must not read the cwd file back: the process is
// still running, the file is not trustworthy yet.
func TestCdPersistence_TimeoutTransitionNoUpdate(t *testing.T) {
	// no t.Parallel: mutates package var ptySupported
	orig := ptySupported
	ptySupported = false
	defer func() { ptySupported = orig }()

	origReg := defaultRegistry
	freshReg := NewBackgroundJobRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = origReg }()

	dirA := t.TempDir()
	sess := &sessionCwd{dir: dirA}

	res, err := Execute(context.Background(),
		json.RawMessage(`{"command":"echo start; sleep 10","timeout":100}`), sess.tctx(realPath(t, dirA)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := res.Data.(*Output)
	if out.BackgroundJobID == "" {
		t.Fatalf("BackgroundJobID is empty, want auto-background on timeout (TimedOut=%v)", out.TimedOut)
	}
	if got := sess.get(); got != dirA {
		t.Errorf("session cwd after timeout transition = %q, want unchanged %q", got, dirA)
	}
	_ = freshReg.Kill(out.BackgroundJobID)
	_, _ = freshReg.Wait(out.BackgroundJobID)
}
