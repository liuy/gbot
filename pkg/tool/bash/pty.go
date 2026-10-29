package bash

import (
	"log/slog"
	"os/exec"
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
)

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

// ptyNew is a test hook wrapping go-pty's pty.New so tests can inject a fake.
var ptyNew = func() (pty.Pty, error) {
	return pty.New()
}

// ptySupported caches whether go-pty.Pty allocation works in this process.
// Probed once at package init via detectPTYSupport; production code reads it
// directly. Tests that override it MUST NOT call t.Parallel(): concurrent
// writes race with readers and trip the race detector.
var ptySupported = detectPTYSupport()

// detectPTYSupport probes go-pty.Pty allocation once at package load. On
// failure, logs a warning and disables the PTY path; the non-PTY fallback
// handles command execution correctly, just without interactive features
// (input prompts, SIGWINCH resize forwarding).
func detectPTYSupport() bool {
	p, err := ptyNew()
	if err != nil {
		slog.Warn("pty:unavailable_falling_back_to_nonpty", "err", err)
		return false
	}
	_ = p.Close()
	return true
}
