package bash

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/liuy/gbot/pkg/tool"
)

// drainToScreen reads from an io.Reader and feeds bytes to a Screen.
// Replaces the old drainPTY for test-only use.
func drainToScreen(reader io.Reader, screen *tool.Screen) {
	buf := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			screen.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	screen.Flush()
}

// --- executeNonPTY tests (internal access) ---

func TestExecuteNonPTY_Echo(t *testing.T) {
	t.Parallel()

	in := Input{Command: "echo hello", Timeout: 10000}
	inputJSON, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	if result.Data == nil {
		t.Fatal("result.Data is nil")
	}
	output := result.Data.(*Output)
	if output.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", output.ExitCode)
	}
	if !strings.Contains(output.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain 'hello'", output.Stdout)
	}
	// Verify inputJSON round-trips correctly
	var roundTrip Input
	if err := json.Unmarshal(inputJSON, &roundTrip); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if roundTrip.Command != "echo hello" {
		t.Errorf("roundTrip.Command = %q, want %q", roundTrip.Command, "echo hello")
	}
}

func TestExecuteNonPTY_Stderr(t *testing.T) {
	t.Parallel()

	in := Input{Command: "echo error >&2", Timeout: 10000}
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Stderr, "error") {
		t.Errorf("Stderr = %q, want to contain 'error'", output.Stderr)
	}
}

func TestExecuteNonPTY_NonZeroExit(t *testing.T) {
	t.Parallel()

	in := Input{Command: "exit 42", Timeout: 10000}
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", output.ExitCode)
	}
}

func TestExecuteNonPTY_Timeout(t *testing.T) {
	in := Input{Command: "sleep 60", Timeout: 100}
	result, err := executeNonPTY(context.Background(), in, "", 100*time.Millisecond, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if !output.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if output.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", output.ExitCode)
	}
}

func TestExecuteNonPTY_WorkingDir(t *testing.T) {
	t.Parallel()

	dir := os.TempDir()
	in := Input{Command: "pwd", Timeout: 10000}
	result, err := executeNonPTY(context.Background(), in, dir, 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if !strings.Contains(output.Stdout, dir) {
		t.Errorf("Stdout = %q, want to contain %q", output.Stdout, dir)
	}
}

func TestExecuteNonPTY_CommandFailure(t *testing.T) {
	t.Parallel()

	in := Input{Command: "nonexistent_command_xyz", Timeout: 10000}
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.ExitCode == 0 {
		t.Error("ExitCode = 0, want non-zero for nonexistent command")
	}
}

func TestExecuteNonPTY_GenericError(t *testing.T) {
	// Cancelled context → generic error path (not timeout, not ExitError)
	// When context is already cancelled, cmd.Run() returns context.Canceled
	// which is not an ExitError, so executeNonPTYSync returns it as an error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := Input{Command: "echo hi", Timeout: 10000}
	_, err := executeNonPTY(ctx, in, "", 10*time.Second, NewStreamingOutput(nil), false, nil, MaxOutputSize)
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("error = %v, want to contain 'context canceled'", err)
	}
}

// --- openPTY removed: go-pty handles master/slave allocation internally. ---

// --- PTY command tests ---

func TestPtyCommand_MultilineOutput(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"echo line1; echo line2; echo line3",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") || !strings.Contains(joined, "line3") {
		t.Errorf("output = %q, want to contain line1, line2, line3", joined)
	}
}

// TestPtyCommand_WorkingDirRespected reproduces a Windows bug where bash
// ignored the cwd argument and ran in / (root) instead. The symptom was
// Glob/Grep scanning the entire drive when invoked from the bundled gbot.exe.
// On Linux this test should always pass; on Windows it exercises the path
// translation in go-pty's CreateProcess call.
func TestPtyCommand_WorkingDirRespected(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	tmpDir := t.TempDir()
	// Mark the dir with a sentinel file so we can distinguish "pwd is tmpDir"
	// from "pwd is some other dir that happens to have the same name".
	sentinel := filepath.Join(tmpDir, "gbot-cwd-test.marker")
	if err := os.WriteFile(sentinel, []byte("ok"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	var lines []string
	_, _, err := runPTYCommand(
		context.Background(),
		"pwd && ls gbot-cwd-test.marker",
		tmpDir,
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}

	joined := strings.Join(lines, "\n")
	// Clean tmpDir for assertion: we expect pwd's output to appear and to
	// mention the basename of tmpDir (not "/" or a Windows-drive-root form).
	base := filepath.Base(tmpDir)
	if !strings.Contains(joined, base) {
		t.Errorf("output = %q, want pwd output containing %q", joined, base)
	}
	// The ls of the sentinel must succeed (file is listed by name).
	if !strings.Contains(joined, "gbot-cwd-test.marker") {
		t.Errorf("output = %q, want sentinel file to be listed by ls", joined)
	}
}

func TestPtyCommand_Environment(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, interrupted, ptyErr := runPTYCommand(
		context.Background(),
		"echo $GBOT_TEST_VAR",
		"",
		append(os.Environ(), "GBOT_TEST_VAR=testvalue123"),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if ptyErr != nil {
		t.Fatalf("runPTYCommand() error: %v", ptyErr)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if interrupted {
		t.Error("interrupted = true, want false")
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "testvalue123") {
		t.Errorf("output = %q, want to contain 'testvalue123'", joined)
	}
}

func TestPtyCommand_PartialLineFlush(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf 'no-newline-end'",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "no-newline-end") {
		t.Errorf("output = %q, want to contain 'no-newline-end'", joined)
	}
}

func TestPtyCommand_PartialLine(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf 'partial-no-newline'",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "partial-no-newline") {
		t.Errorf("output = %q, want to contain 'partial-no-newline'", joined)
	}
}

func TestPtyCommand_ExitBySignal(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, interrupted, err := runPTYCommand(
		context.Background(),
		"sleep 10",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		200*time.Millisecond,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if !interrupted {
		t.Error("interrupted = false, want true for timed-out command")
	}
	// TS sends SIGKILL directly (no SIGTERM grace period)
	// Source: ShellCommand.ts:340 — treeKill(pid, 'SIGKILL')
	if exitCode != 137 {
		t.Errorf("exitCode = %d, want 137 (SIGKILL)", exitCode)
	}
}

func TestPtyCommand_NonExitErrorPath(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"kill -ABRT $$",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		500*time.Millisecond,
		nil,
	)
	// Command should terminate with signal (exit code != 0) or error
	if exitCode == 0 && err == nil {
		t.Errorf("expected non-zero exit code or error for ABRT signal, got exitCode=%d err=%v", exitCode, err)
	}
}

func TestPtyCommand_LongLine(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	longStr := strings.Repeat("A", 8192)
	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"printf '%s\\n' "+longStr,
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "AAAA") {
		t.Errorf("output too short, want to contain long string (len=%d)", len(joined))
	}
}

func TestPtyCommand_ReadError(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := runPTYCommand(
		context.Background(),
		"exec cat",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		500*time.Millisecond,
		nil,
	)
	// Command times out, should get interrupted (exit code 137) or error
	if exitCode == 0 && err == nil {
		t.Errorf("expected non-zero exit code or error for timeout, got exitCode=%d err=%v", exitCode, err)
	}
}

func TestPtyCommand_SigkillExit(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	// killProcessTree sends SIGKILL directly (matching TS behavior)
	var lines []string
	exitCode, interrupted, err := runPTYCommand(
		context.Background(),
		"sleep 10",
		"",
		os.Environ(),
		tool.NewScreen(func(ev tool.ScreenEvent) {
			lines = append(lines, ev.Content)
		}),
		200*time.Millisecond,
		nil,
	)
	if err != nil {
		t.Fatalf("runPTYCommand() error: %v", err)
	}
	if !interrupted {
		t.Error("interrupted = false, want true")
	}
	// SIGKILL = 9 → exit code 137
	if exitCode != 137 {
		t.Errorf("exitCode = %d, want 137 (SIGKILL)", exitCode)
	}
}

// --- ensureSocketInitialized concurrent path ---

func TestEnsureSocketInitialized_ConcurrentInit(t *testing.T) {
	resetSocketState()

	done := make(chan struct{})
	var initErr error
	go func() {
		initErr = ensureSocketInitialized()
		close(done)
	}()

	select {
	case <-done:
		// OK — error is expected when tmux is not available
		if initErr != nil {
			t.Logf("ensureSocketInitialized() error (expected without tmux): %v", initErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSocketInitializer hung")
	}

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSocketInitializer hung")
	}
}

// --- makeRaw / restoreTerminal removed: go-pty sets raw mode internally. ---

// --- applyEnvOverrides (PTY context) ---

func TestPtyApplyEnvOverrides(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "B=2", "C=3"}
	overrides := map[string]string{"B": "override", "D": "4"}

	result := applyEnvOverrides(base, overrides)

	foundB := slices.Contains(result, "B=override")
	if !foundB {
		t.Errorf("result = %v, want B=override", result)
	}

	for _, want := range []string{"A=1", "C=3", "D=4"} {
		found := slices.Contains(result, want)
		if !found {
			t.Errorf("result = %v, want %s", result, want)
		}
	}
}

func TestPtyApplyEnvOverrides_NoOverrides(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "B=2"}
	result := applyEnvOverrides(base, nil)
	if len(result) != len(base) {
		t.Errorf("len(result) = %d, want %d", len(result), len(base))
	}
}

// --- Test hook coverage ---

func TestPtyCommand_StartError(t *testing.T) {
	// Temporarily use a non-existent shell to trigger Start error
	orig := shellCommand
	shellCommand = "/nonexistent/shell/xyz"
	defer func() { shellCommand = orig }()

	_, _, err := runPTYCommand(
		context.Background(),
		"echo hi",
		"",
		os.Environ(),
		tool.NewScreen(nil),
		5*time.Second,
		nil,
	)
	if err == nil {
		t.Error("expected error with non-existent shell")
	}
	if !strings.Contains(err.Error(), "start command") {
		t.Errorf("error = %v, want start command error", err)
	}
}

// TestRunPTYCommand_OpenPTYError exercises the ptyNew hook to verify that
// a pty.New() failure is surfaced via openPTYSession's "open PTY" wrap.
// Without this hook, the error path is unreachable from tests (real pty.New
// only fails on exhausted fds / kernel PTY starvation).
func TestRunPTYCommand_OpenPTYError(t *testing.T) {
	orig := ptyNew
	defer func() { ptyNew = orig }()
	ptyNew = func() (pty.Pty, error) {
		return nil, errors.New("synthetic pty.New failure")
	}

	_, _, err := runPTYCommand(
		context.Background(),
		"echo hi",
		"",
		os.Environ(),
		tool.NewScreen(nil),
		5*time.Second,
		nil,
	)
	if err == nil {
		t.Fatal("expected error from openPTYSession, got nil")
	}
	if !strings.Contains(err.Error(), "open PTY") {
		t.Errorf("error = %v, want 'open PTY' substring", err)
	}
	if !strings.Contains(err.Error(), "synthetic pty.New failure") {
		t.Errorf("error = %v, want wrapped 'synthetic pty.New failure'", err)
	}
}

// TestDetectPTYSupport_FailureReturnsFalse drives the ptyNew hook to fail and
// confirms detectPTYSupport surfaces false rather than panicking. The branch
// is otherwise unreachable: real pty.New only fails under kernel PTY
// starvation, which CI cannot reproduce reliably.
func TestDetectPTYSupport_FailureReturnsFalse(t *testing.T) {
	// no t.Parallel: mutates the package-level ptyNew hook
	orig := ptyNew
	ptyNew = func() (pty.Pty, error) { return nil, errors.New("synthetic failure") }
	defer func() { ptyNew = orig }()

	if got := detectPTYSupport(); got != false {
		t.Errorf("detectPTYSupport() = %v, want false", got)
	}
}

// TestDetectPTYSupport_SuccessReturnsTrue covers the happy path where ptyNew
// returns a usable pty; detectPTYSupport must Close it and return true.
func TestDetectPTYSupport_SuccessReturnsTrue(t *testing.T) {
	// no t.Parallel: mutates the package-level ptyNew hook
	orig := ptyNew
	ptyNew = func() (pty.Pty, error) { return newFakePty(nil), nil }
	defer func() { ptyNew = orig }()

	if got := detectPTYSupport(); got != true {
		t.Errorf("detectPTYSupport() = %v, want true", got)
	}
}

// --- drainPTY ---

// dataThenEOFReader returns its data once, then io.EOF.
type dataThenEOFReader struct {
	data []byte
	read bool
}

func (r *dataThenEOFReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	n := copy(p, r.data)
	return n, nil
}

func TestDrainToScreen_NormalLines(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte("hello\nworld\n")}, 64)
	var lines []string
	drainToScreen(reader, tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	}))
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2 lines", lines)
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want hello", lines[0])
	}
	if lines[1] != "world" {
		t.Errorf("lines[1] = %q, want world", lines[1])
	}
}

func TestDrainToScreen_EOBBreak(t *testing.T) {
	t.Parallel()
	// 32 bytes > 16-byte buffer forces isPrefix=true, then EOF
	// covers io.EOF break + partial line flush
	reader := bufio.NewReaderSize(
		strings.NewReader(strings.Repeat("A", 32)),
		16,
	)
	var lines []string
	drainToScreen(reader, tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	}))
	joined := strings.Join(lines, "")
	if len(joined) != 32 {
		t.Errorf("output len = %d, want 32, got %q", len(joined), joined)
	}
}

func TestDrainToScreen_NonEOFError(t *testing.T) {
	t.Parallel()
	// Reader returns data then non-EOF error -> covers generic break
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte("data"))
		_ = w.CloseWithError(fmt.Errorf("pipe error"))
	}()
	reader := bufio.NewReaderSize(r, 64)
	var lines []string
	drainToScreen(reader, tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	}))
	if len(lines) < 1 {
		t.Fatal("expected at least one line from flush")
	}
	if !strings.HasPrefix(lines[0], "data") {
		t.Errorf("lines[0] = %q, want to start with data", lines[0])
	}
}

func TestDrainToScreen_NilCallback(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte("hello\n")}, 64)
	drainToScreen(reader, tool.NewScreen(nil)) // should not panic
}

func TestDrainToScreen_Empty(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte{}}, 64)
	var lines []string
	drainToScreen(reader, tool.NewScreen(func(ev tool.ScreenEvent) {
		lines = append(lines, ev.Content)
	}))
	if len(lines) != 0 {
		t.Errorf("lines = %v, want empty", lines)
	}
}

// --- exitCodeFromWait ---

func TestExitCodeFromWait_Nil(t *testing.T) {
	t.Parallel()
	if code := exitCodeFromWait(nil); code != 0 {
		t.Errorf("exitCodeFromWait(nil) = %d, want 0", code)
	}
}

func TestExitCodeFromWait_NonExitError(t *testing.T) {
	t.Parallel()
	if code := exitCodeFromWait(fmt.Errorf("some error")); code != -1 {
		t.Errorf("exitCodeFromWait(generic error) = %d, want -1", code)
	}
}

// --- openPTY hook tests removed: openPTY and its ioctl hooks are gone. ---

// --- exitCodeFromWait — signal-based exit codes ---

func TestExitCodeFromWait_SignalSIGTERM(t *testing.T) {
	t.Parallel()
	// SIGTERM (signal 15) → exit code 128+15 = 143
	cmd := exec.Command("bash", "-c", "kill -TERM $$")
	err := cmd.Run()
	if err == nil {
		t.Skip("command exited cleanly, can't test signal path")
	}
	code := exitCodeFromWait(err)
	if code != 143 {
		t.Errorf("exitCodeFromWait(SIGTERM) = %d, want 143", code)
	}
}

// --- WriteInput ---

func TestPTYSession_WriteInput_Success(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	session, err := openPTYSession()
	if err != nil {
		t.Fatalf("openPTYSession() error: %v", err)
	}
	defer session.Close()

	// Start a cat command that echoes back input
	screen := tool.NewScreen(func(ev tool.ScreenEvent) {})
	if err := session.Start("cat", "", os.Environ(), screen); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Write input should succeed
	if err := session.WriteInput("hello\n"); err != nil {
		t.Fatalf("WriteInput() error: %v", err)
	}

	// Close the PTY to signal EOF to cat
	session.Close()

	// Wait for process to exit — cat gets SIGHUP on master close, which is expected.
	// Accept either clean exit (nil) or signal-terminated (*exec.ExitError).
	waitErr := session.Cmd.Wait()
	if waitErr != nil {
		if _, ok := waitErr.(*exec.ExitError); !ok {
			t.Fatalf("Wait() unexpected error: %v", waitErr)
		}
	}
}

func TestPTYSession_WriteInput_ClosedFD(t *testing.T) {
	if !ptySupported {
		t.Skip("PTY not available")
	}

	session, err := openPTYSession()
	if err != nil {
		t.Fatalf("openPTYSession() error: %v", err)
	}
	// Close the PTY before writing
	session.Close()

	err = session.WriteInput("hello\n")
	if err == nil {
		t.Error("expected error writing to closed PTY fd")
	}
	if !strings.Contains(err.Error(), "pty write") {
		t.Errorf("error = %v, want pty write error", err)
	}
}

func TestExitCodeFromWait_SignalSIGUSR1(t *testing.T) {
	t.Parallel()
	// SIGUSR1 (signal 10) → exit code 128+10 = 138
	cmd := exec.Command("bash", "-c", "kill -USR1 $$")
	err := cmd.Run()
	if err == nil {
		t.Skip("command exited cleanly, can't test signal path")
	}
	code := exitCodeFromWait(err)
	if code != 138 {
		t.Errorf("exitCodeFromWait(SIGUSR1) = %d, want 138", code)
	}
}
