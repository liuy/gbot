package bash

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// PTYSession manages a PTY-based command execution.
// Owns the master/slave fd pair, screen, and output state.
// Methods follow the lifecycle: openPTYSession → Start → Drain → Wait → Close.
type PTYSession struct {
	Master    *os.File
	Slave     *os.File
	Screen    *tool.Screen
	Cmd       *exec.Cmd
	Output    *StreamingOutput
	StartedAt time.Time
}

// openPTYSession opens a new PTY master/slave pair.
func openPTYSession() (*PTYSession, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, fmt.Errorf("open PTY: %w", err)
	}
	return &PTYSession{
		Master: master,
		Slave:  slave,
	}, nil
}

// Start builds and starts the command in the PTY.
// Closes slave in parent process after starting.
func (s *PTYSession) Start(cmd string, dir string, env []string, screen *tool.Screen, onStart ...func(pid int)) error {
	// Set initial window size from terminal
	_ = setPTYWindowSize(s.Master.Fd())

	s.Screen = screen

	// Build command to run in PTY
	s.Cmd = exec.Command(shellCommand, "-c", cmd)
	s.Cmd.Dir = dir
	s.Cmd.Env = env
	s.Cmd.SysProcAttr = &syscall.SysProcAttr{
		Setctty: true,
		Setsid:  true,
	}
	s.Cmd.Stdin = s.Slave
	s.Cmd.Stdout = s.Slave
	s.Cmd.Stderr = s.Slave

	if err := s.Cmd.Start(); err != nil {
		_ = s.Slave.Close()
		return fmt.Errorf("start command: %w", err)
	}

	// Close slave in parent process — child has its own dup
	_ = s.Slave.Close()

	s.StartedAt = time.Now()

	// Notify PID to caller
	if len(onStart) > 0 && onStart[0] != nil {
		onStart[0](s.Cmd.Process.Pid)
	}

	return nil
}

// Drain reads PTY output until EOF/error.
// Non-nil emitAskInput enables interactive input detection (e.g. password prompts).
// ctx is used to unblock the response channel wait on cancellation/timeout.
//
// Line tracking uses local variables to track partial-line state from raw
// PTY bytes, separate from StreamingOutput (which receives processed output
// via the Screen callback). This avoids double-write.
func (s *PTYSession) Drain(ctx context.Context, emitAskInput func(tail string, masked bool) chan types.AskResponse) {
	buf := make([]byte, 4096)
	// Local partial-line state for interaction detection.
	var partialLine bool
	var lastLines []string

	for {
		n, err := syscall.Read(int(s.Master.Fd()), buf)
		if n > 0 {
			// Update partial-line state from raw PTY bytes (before Screen processing)
			if emitAskInput != nil {
				partialLine, lastLines = trackPartialLines(buf[:n], partialLine, lastLines)

				// Interaction detection (serial, like AskPermission)
				if partialLine {
					tail := strings.Join(lastLines, "\n")
					if looksLikePrompt(tail) {
						masked := isPasswordPrompt(tail)
						respCh := emitAskInput(tail, masked)
						if respCh == nil {
							// Fix 3: expired deadline — skip prompt
							continue
						}
						// Fix 1: select on ctx.Done() to prevent deadlock
						var resp types.AskResponse
						select {
						case resp = <-respCh:
						case <-ctx.Done():
							_ = killProcessTree(s.Cmd.Process.Pid)
							return
						}
						if resp.Aborted {
							reason := "cancelled by user"
							if resp.Timeout {
								reason = "timed out"
							}
							s.Screen.Write(fmt.Appendf(nil, "\r\n[Interaction %s]\r\n", reason))
							s.Screen.Flush()
							_ = killProcessTree(s.Cmd.Process.Pid)
							return
						}
						if writeErr := s.WriteInput(resp.Text + "\n"); writeErr != nil {
							return
						}
					}
				}
			}
			s.Screen.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	s.Screen.Flush()
}

// trackPartialLines updates partial-line state from raw PTY bytes.
// Returns updated (partialLine, lastLines).
// Mirrors the line-splitting logic from StreamingOutput.Write without the
// heavy features (mutex, spill, progress callbacks).
func trackPartialLines(p []byte, partialLine bool, lastLines []string) (bool, []string) {
	parts := bytes.Split(p, []byte{'\n'})
	for i, part := range parts {
		text := string(part)
		isLast := i == len(parts)-1

		// Trailing empty fragment from '\n' at end of input — no new line
		if text == "" && isLast {
			partialLine = false
			continue
		}

		if partialLine && len(lastLines) > 0 {
			lastLines[len(lastLines)-1] += text
		} else {
			lastLines = append(lastLines, text)
		}

		partialLine = isLast

		// Trim rolling window
		if len(lastLines) > streamingLastLines {
			lastLines = lastLines[len(lastLines)-streamingLastLines:]
		}
	}
	return partialLine, lastLines
}

// WriteInput writes text to the PTY master fd.
// Serial model: only called from Drain after receiving user input.
func (s *PTYSession) WriteInput(text string) error {
	_, err := syscall.Write(int(s.Master.Fd()), []byte(text))
	if err != nil {
		return fmt.Errorf("pty write: %w", err)
	}
	return nil
}

// Close cleans up the PTY file descriptors.
func (s *PTYSession) Close() {
	if s.Master != nil {
		_ = s.Master.Close()
	}
}

// Run executes a command in a PTY session with full lifecycle management.
// This is the primary entry point replacing the old ptyCommand function.
//
// Timeout goroutines are started BEFORE Drain so that timeout/context cancellation
// can kill the process (unblocking Drain's syscall.Read) even when the command
// produces no output.
//
// If emitAskInput is non-nil, enables interactive input detection during Drain.
func (s *PTYSession) Run(ctx context.Context, cmd string, dir string, env []string,
	screen *tool.Screen, timeout time.Duration,
	emitAskInput func(tail string, masked bool) chan types.AskResponse,
	onStart ...func(pid int)) (exitCode int, interrupted bool, err error) {

	// Setup timeout — must be BEFORE Drain so timeout can kill the process
	deadline := time.Now().Add(timeout)
	deadlineCtx, deadlineCancel := context.WithDeadline(ctx, deadline)
	defer deadlineCancel()

	// Watch for SIGWINCH and forward to PTY (Linux only)
	var stopSigwinch chan struct{}
	if checkIsLinux() {
		stopSigwinch = make(chan struct{})
		go watchSigwinch(s.Master.Fd(), stopSigwinch)
		defer func() {
			if stopSigwinch != nil {
				close(stopSigwinch)
			}
		}()
	}

	// Start the command (builds exec.Command and starts process)
	if err := s.Start(cmd, dir, env, screen, onStart...); err != nil {
		return -1, false, err
	}

	// Timeout goroutine — fires killProcessTree on timeout
	timeoutFired := false
	graceCh := make(chan struct{})
	go func() {
		<-deadlineCtx.Done()
		if deadlineCtx.Err() == context.DeadlineExceeded && s.Cmd.Process != nil {
			_ = killProcessTree(s.Cmd.Process.Pid)
			timeoutFired = true
		}
		close(graceCh)
	}()

	// Context cancellation goroutine (user interrupt / Ctrl+C)
	go func() {
		<-ctx.Done()
		if ctx.Err() == context.Canceled && s.Cmd.Process != nil {
			_ = killProcessTree(s.Cmd.Process.Pid)
		}
	}()

	// Drain PTY output (blocks until EOF or process killed by timeout)
	s.Drain(ctx, emitAskInput)

	// Wait for process to exit
	waitErr := s.Cmd.Wait()

	// Cancel deadline context to ensure timeout goroutine exits
	deadlineCancel()

	// Determine exit code
	code := exitCodeFromWait(waitErr)

	// Wait for timeout goroutine to complete
	<-graceCh
	return code, timeoutFired, nil
}

// runPTYCommand creates a PTYSession, runs a command, and cleans up.
func runPTYCommand(ctx context.Context, cmd string, dir string, env []string,
	screen *tool.Screen, timeout time.Duration,
	emitAskInput func(tail string, masked bool) chan types.AskResponse,
	onStart ...func(pid int)) (exitCode int, interrupted bool, err error) {

	session, err := openPTYSession()
	if err != nil {
		return -1, false, err
	}
	defer session.Close()

	return session.Run(ctx, cmd, dir, env, screen, timeout, emitAskInput, onStart...)
}
