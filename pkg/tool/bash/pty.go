package bash

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Test hooks — package-level vars that can be overridden in tests.
// Default values match production behavior.
var (
	shellCommand = "bash"
	ptmxPath     = "/dev/ptmx"
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

// openPTY hooks — allow mocking ioctl and slave open in tests
var (
	ioctlGetPtyNum = func(fd int) (int, error) {
		return unix.IoctlGetInt(fd, unix.TIOCGPTN)
	}
	ioctlUnlockPty = func(fd int) error {
		return unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0)
	}
	openSlavePty = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	}
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

// openPTY opens a new PTY master/slave pair using /dev/ptmx and ioctl.
// Uses golang.org/x/sys/unix for PTY number retrieval and unlock.
// Uses golang.org/x/term for terminal operations on the PTY fds.
func openPTY() (master *os.File, slave *os.File, err error) {
	// Open master PTY via /dev/ptmx
	master, err = os.OpenFile(ptmxPath, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// Get PTY number via ioctl TIOCGPTN (x/sys/unix)
	ptyNum, err := ioctlGetPtyNum(int(master.Fd()))
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("TIOCGPTN: %w", err)
	}

	// Unlock slave PTY via ioctl TIOCSPTLCK (x/sys/unix)
	// TIOCSPTLCK expects a pointer to int — use IoctlSetPointerInt
	if err := ioctlUnlockPty(int(master.Fd())); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("TIOCSPTLCK: %w", err)
	}

	// Open slave PTY
	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNum)
	slave, err = openSlavePty(slavePath)
	if err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open slave %s: %w", slavePath, err)
	}

	return master, slave, nil
}

// watchSigwinch monitors terminal resize and forwards to PTY master.
// Only runs on Linux. Stops when the stop channel is closed.
//
// Source: ink.tsx:226 — process.on('SIGWINCH', handleResize)
func watchSigwinch(ptyFd uintptr, stop <-chan struct{}) {
	ticker := time.NewTicker(SigwinchPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = setPTYWindowSize(ptyFd)
		}
	}
}

// SigwinchPollInterval is the interval for checking terminal resize events.
const SigwinchPollInterval = 500 * time.Millisecond

// isPTYAvailable checks if PTY allocation is possible on this system.
func isPTYAvailable() bool {
	if !checkIsLinux() {
		return false
	}
	if _, err := os.Stat(PtmxCheckPath()); err != nil {
		return false
	}
	return true
}

// makeRaw puts the terminal fd into raw mode.
// Delegates to golang.org/x/term.MakeRaw.
func makeRaw(fd int) (*term.State, error) {
	return term.MakeRaw(fd)
}

// restoreTerminal restores terminal state on the given fd.
// Delegates to golang.org/x/term.Restore.
func restoreTerminal(fd int, state *term.State) error {
	return term.Restore(fd, state)
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
