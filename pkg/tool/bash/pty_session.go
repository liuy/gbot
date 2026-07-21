package bash

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"

	"log/slog"
)

// PTYSession manages a PTY-based command execution.
// Wraps go-pty's Pty + Cmd so the layer above (bash.go) doesn't need to
// know whether the runtime is Unix or Windows — go-pty abstracts both.
//
// Lifecycle: openPTYSession → Start → Drain → Wait → Close.
type PTYSession struct {
	Pty       pty.Pty
	Cmd       *pty.Cmd
	Screen    *tool.Screen
	Output    *StreamingOutput
	StartedAt time.Time
	closeOnce sync.Once
}

// openPTYSession opens a new go-pty Pty.
func openPTYSession() (*PTYSession, error) {
	p, err := ptyNew()
	if err != nil {
		return nil, fmt.Errorf("open PTY: %w", err)
	}
	return &PTYSession{
		Pty: p,
	}, nil
}

// Start builds and starts the command attached to the PTY.
// On Unix, closeSlaveAfterStart closes the parent's slave end so the master
// receives EOF when the child exits — without this, Drain would block forever
// on commands that exit naturally.
func (s *PTYSession) Start(cmd string, dir string, env []string, screen *tool.Screen, onStart ...func(pid int)) error {
	// Set initial window size from terminal
	_ = setPTYWindowSize(s.Pty)

	s.Screen = screen

	// Build command attached to PTY. go-pty sets SysProcAttr (Setctty/Setsid
	// on Unix, ConPTY pseudoconsole attribute on Windows) internally.
	s.Cmd = s.Pty.Command(resolveShellCommand(), "-c", cmd)
	s.Cmd.Dir = dir
	s.Cmd.Env = env

	if err := s.Cmd.Start(); err != nil {
		return fmt.Errorf("start command: %w", err)
	}

	// Close slave end in parent on Unix so master receives EOF when child exits.
	// No-op on Windows (ConPTY uses pipes, not master/slave).
	closeSlaveAfterStart(s.Pty)

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

	// readCh receives PTY read results from a goroutine, allowing the main
	// loop to select between data arrival and a stall timer.
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		for {
			n, err := s.Pty.Read(buf)
			var data []byte
			if n > 0 {
				data = make([]byte, n)
				copy(data, buf[:n])
			}
			select {
			case readCh <- readResult{data: data, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var stallTimer *time.Timer
	stallCh := make(chan struct{}, 1)
	resetStall := func() {
		if stallTimer != nil {
			stallTimer.Stop()
		}
		stallTimer = time.AfterFunc(getDrainStallThreshold(), func() {
			select {
			case stallCh <- struct{}{}:
			default:
			}
		})
	}
	defer func() {
		if stallTimer != nil {
			stallTimer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = killProcessTree(s.Cmd.Process.Pid)
			s.Screen.Flush()
			return
		case <-stallCh:
			// Output stalled for drainStallThreshold — check for prompt.
			// Streaming output (git diff, curl) resets the timer on every
			// chunk so this only fires when the process is genuinely blocked.
			if emitAskInput != nil && partialLine {
				tail := strings.Join(lastLines, "\n")
				if looksLikePrompt(tail) {
					masked := isPasswordPrompt(tail)
					slog.Info("pty:prompt_detected_after_stall", "tail", tail, "masked", masked)
					respCh := emitAskInput(tail, masked)
					if respCh == nil {
						continue
					}
					var resp types.AskResponse
					select {
					case resp = <-respCh:
					case <-ctx.Done():
						_ = killProcessTree(s.Cmd.Process.Pid)
						s.Screen.Flush()
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
						s.Screen.Flush()
						return
					}
					if writeErr := s.WriteInput(resp.Text + "\n"); writeErr != nil {
						s.Screen.Flush()
						return
					}
				}
			}
		case res := <-readCh:
			if len(res.data) > 0 {
				resetStall()
				if emitAskInput != nil {
					partialLine, lastLines = trackPartialLines(res.data, partialLine, lastLines)
				}
				s.Screen.Write(res.data)
			}
			if res.err != nil {
				s.Screen.Flush()
				return
			}
		}
	}
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

// WriteInput writes text to the PTY.
// Serial model: only called from Drain after receiving user input.
func (s *PTYSession) WriteInput(text string) error {
	_, err := s.Pty.Write([]byte(text))
	if err != nil {
		return fmt.Errorf("pty write: %w", err)
	}
	return nil
}

// Close cleans up the PTY. Safe to call multiple times — go-pty's unixPty
// tracks a closed flag; ConPty's pipes are idempotent to close.
// Close releases PTY resources. Safe to call multiple times (sync.Once);
// go-pty's conPty.Close is not idempotent on Windows, so the once guard
// prevents double-close races when waitAndCloseAfterExit and the deferred
// session.Close in runPTYCommand both run.
func (s *PTYSession) Close() {
	s.closeOnce.Do(func() {
		if s.Pty != nil {
			_ = s.Pty.Close()
		}
	})
}

// Run executes a command in a PTY session with full lifecycle management.
// This is the primary entry point replacing the old ptyCommand function.
//
// The deadline goroutine is started BEFORE Drain so the process is killed
// on timeout (unblocking Drain's read) even when the command produces no
// output. Context cancellation is handled by Drain itself via ctx.Done.
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

	// watchSigwinch is a no-op on Windows; on Unix it polls terminal size and
	// forwards resizes to the PTY. Unconditional call is safe — session.Run is
	// only reached when ptySupported=true.
	stopSigwinch := make(chan struct{})
	go watchSigwinch(s.Pty, stopSigwinch)
	defer close(stopSigwinch)

	// Start the command (builds pty.Cmd and starts process)
	if err := s.Start(cmd, dir, env, screen, onStart...); err != nil {
		return -1, false, err
	}

	// waitAndCloseAfterExit runs in a goroutine on Windows to work around
	// ConPTY's lack of automatic EOF on child exit (microsoft/terminal#4564).
	// On Unix, no-op: the kernel delivers EOF naturally via closeSlaveAfterStart.
	// See pty_unix.go / pty_windows.go.
	waitErrCh := waitAndCloseAfterExit(ctx, s)

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

	// Drain PTY output (blocks until EOF or process killed by timeout).
	// On Windows, waitAndCloseAfterExit's goroutine closes the PTY after
	// Wait() returns, which forces Drain's Read to unblock.
	s.Drain(ctx, emitAskInput)

	// Wait for process to exit (or for Windows goroutine to have closed PTY).
	var waitErr error
	if waitErrCh != nil {
		waitErr = <-waitErrCh
	} else {
		waitErr = s.Cmd.Wait()
	}

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
