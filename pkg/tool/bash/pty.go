package bash

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/liuy/gbot/pkg/tool"
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
	ioctlGetPtyNum  = func(fd int) (int, error) {
		return unix.IoctlGetInt(fd, unix.TIOCGPTN)
	}
	ioctlUnlockPty = func(fd int) error {
		return unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0)
	}
	openSlavePty = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	}
)

// ptyCommand runs cmd in a PTY, streaming stripped lines to the callback.
// Returns exit code, interrupted flag, and any error.
// onOutput receives lines with ANSI already stripped.
// Partial lines at buffer boundaries are held until '\n' or EOF.
//
// Source: ShellCommand.ts — ShellCommandImpl wraps a child process.
// PTY allocation is the Go-native equivalent of TS's spawn() with file-mode stdio.
// All TS algorithms (timeout escalation, process tree kill) are preserved 1:1.
func ptyCommand(ctx context.Context, cmd string, dir string, env []string,
	onOutput func(line string), timeout time.Duration, onStart ...func(pid int)) (exitCode int, interrupted bool, err error) {

	// Open PTY master/slave pair
	ptyMaster, ptySlave, err := openPTY()
	if err != nil {
		return -1, false, fmt.Errorf("open PTY: %w", err)
	}
	defer func() { _ = ptyMaster.Close() }()

	// Set initial window size from terminal
	_ = setPTYWindowSize(ptyMaster.Fd())

	// Build command to run in PTY
	execCmd := exec.Command(shellCommand, "-c", cmd)
	execCmd.Dir = dir
	execCmd.Env = env
	execCmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}
	execCmd.Stdin = ptySlave
	execCmd.Stdout = ptySlave
	execCmd.Stderr = ptySlave

	// Watch for SIGWINCH and forward to PTY (Linux only)
	var stopSigwinch chan struct{}
	if isLinux() {
		stopSigwinch = make(chan struct{})
		go watchSigwinch(ptyMaster.Fd(), stopSigwinch)
		defer func() {
			if stopSigwinch != nil {
				close(stopSigwinch)
			}
		}()
	}

	// Start the command
	if err := execCmd.Start(); err != nil {
		_ = ptySlave.Close()
		return -1, false, fmt.Errorf("start command: %w", err)
	}
	// Close slave in parent process — child has its own dup
	_ = ptySlave.Close()

	// Notify PID to caller (for background task Kill support)
	if len(onStart) > 0 && onStart[0] != nil {
		onStart[0](execCmd.Process.Pid)
	}

	// Setup timeout handling
	// Source: ShellCommand.ts:275-279 — setTimeout(#handleTimeout, timeout)
	deadline := time.Now().Add(timeout)
	deadlineCtx, deadlineCancel := context.WithDeadline(ctx, deadline)
	defer deadlineCancel()

	// Timeout goroutine — fires killProcessTree on timeout
	// Source: ShellCommand.ts:135-141 — #handleTimeout → #doKill
	timeoutFired := false
	graceCh := make(chan struct{})
	go func() {
		<-deadlineCtx.Done()
		if deadlineCtx.Err() == context.DeadlineExceeded && execCmd.Process != nil {
			_ = killProcessTree(execCmd.Process.Pid)
			timeoutFired = true
		}
		close(graceCh)
	}()

	// Context cancellation goroutine (user interrupt / Ctrl+C)
	// Source: ShellCommand.ts:186-192 — #abortHandler
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.Canceled && execCmd.Process != nil {
			_ = killProcessTree(execCmd.Process.Pid)
		}
	}()

	// Read loop — read from PTY master, strip ANSI, emit lines
	// Source: ShellCommand.ts — file mode reads from output file;
	// PTY reads from master fd with partial-line buffering.
	reader := bufio.NewReader(ptyMaster)
	drainPTY(reader, onOutput)

	// Wait for process to exit
	waitErr := execCmd.Wait()

	// Cancel deadline context to ensure timeout goroutine exits.
	// Must happen before reading graceCh to avoid deadlock.
	deadlineCancel()

	// Determine exit code
	// Source: ShellCommand.ts:196-202 — #exitHandler
	code := exitCodeFromWait(waitErr)

	// Wait for timeout goroutine to complete before reading timeoutFired.
	// graceCh is always closed (either by deadline exceeded or by cancel above).
	<-graceCh
	return code, timeoutFired, nil
}

// drainPTY reads from a PTY master reader, strips ANSI, and emits lines.
// Handles partial lines at buffer boundaries — accumulated data is flushed
// when a newline is seen or when the reader returns an error/EOF.
//
// Source: ShellCommand.ts — file mode reads from output file;
// PTY reads from master fd with partial-line buffering.
func drainPTY(reader *bufio.Reader, onOutput func(string)) {
	var partialLine strings.Builder

	for {
		line, isPrefix, readErr := reader.ReadLine()
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			// EIO when PTY closes — flush and break
			break
		}

		// Strip ANSI from the chunk
		stripped := tool.StripANSI(string(line))
		partialLine.WriteString(stripped)

		if !isPrefix {
			// Complete line — emit it
			if onOutput != nil {
				onOutput(partialLine.String())
			}
			partialLine.Reset()
		}
	}

	// Flush remaining partial line
	if partialLine.Len() > 0 {
		if onOutput != nil {
			onOutput(partialLine.String())
		}
	}
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
		idx := strings.Index(e, "=")
		if idx < 0 {
			result = append(result, e)
			continue
		}
		key := e[:idx]
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
