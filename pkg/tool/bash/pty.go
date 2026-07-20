package bash

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/aymanbagabas/go-pty"
)

// ptyPty captures the subset of go-pty's Pty interface used by the bash
// package. Declared locally so tests can substitute a fake without depending
// on the concrete *pty.unixPty / *pty.conPty types.
type ptyPty interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(width int, height int) error
	Close() error
}

// Test hooks — package-level vars that can be overridden in tests.
// Default values match production behavior.
var (
	shellCommand = "bash"
	checkIsLinux = isLinux

	ptmxMu         sync.Mutex
	ptmxCheckValue atomic.Value // holds string, protected by ptmxMu
)

// init sets the default PTY check path.
func init() {
	ptmxCheckValue.Store("/dev/ptmx")
}

// PtmxCheckPath returns the current PTY check path (thread-safe).
func PtmxCheckPath() string {
	return ptmxCheckValue.Load().(string)
}

// SetPtmxCheckPath sets the PTY check path (thread-safe, for tests).
func SetPtmxCheckPath(path string) {
	ptmxMu.Lock()
	defer ptmxMu.Unlock()
	ptmxCheckValue.Store(path)
}

// exitCodeFromWait determines the exit code from a cmd.Wait() error.
// Source: ShellCommand.ts:196-202 — #exitHandler
func exitCodeFromWait(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		return -1
	}
	ws := exitErr.Sys().(syscall.WaitStatus)
	if !ws.Signaled() {
		return ws.ExitStatus()
	}
	sig := ws.Signal()
	switch sig {
	case syscall.SIGKILL:
		return 137 // SIGKILL = 128 + 9
	case syscall.SIGTERM:
		return 143 // SIGTERM = 128 + 15
	default:
		return 128 + int(sig)
	}
}

// isPTYAvailable checks if PTY allocation is possible on this system.
//
// Windows: ConPTY ships in Windows 10 1809+ (the minimum version Microsoft
// recommends for new development), so we treat it as always available.
// Without this branch, the runtime would fall through to executeNonPTY
// and leave the ConPTY-backed shell_windows.go / pty_windows.go code dead.
//
// macOS and other Unix: conservatively disabled. /dev/ptmx exists on macOS
// but the package's PTY path is Linux-tested only; the non-PTY fallback
// covers macOS adequately.
//
// Linux: keep the existing /dev/ptmx stat so tests can flip PtmxCheckPath
// to a nonexistent path and exercise the non-PTY fallback.
func isPTYAvailable() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if !checkIsLinux() {
		return false
	}
	if _, err := os.Stat(PtmxCheckPath()); err != nil {
		return false
	}
	return true
}

// applyEnvOverrides applies the given overrides to the environment slice.
// Source: bashProvider.ts:228-253 — env overrides for TMUX isolation
func applyEnvOverrides(env []string, overrides map[string]string) []string {
	result := make([]string, 0, len(env))
	overrideKeys := make(map[string]bool)
	for k := range overrides {
		overrideKeys[k] = true
	}
	// Copy env vars that are NOT overridden
	for _, e := range env {
		before, _, ok := strings.Cut(e, "=")
		if !ok {
			result = append(result, e)
			continue
		}
		key := before
		if !overrideKeys[key] {
			result = append(result, e)
		}
	}
	// Add overrides
	for k, v := range overrides {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}

// ptyNew is a test hook wrapping go-pty's pty.New so tests can inject a fake.
var ptyNew = func() (pty.Pty, error) {
	return pty.New()
}
