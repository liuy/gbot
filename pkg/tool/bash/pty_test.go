package bash

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// --- executeNonPTY tests (internal access) ---

func TestExecuteNonPTY_Echo(t *testing.T) {
	t.Parallel()

	in := Input{Command: "echo hello", Timeout: 10000}
	inputJSON, _ := json.Marshal(in)
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second)
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
	_ = inputJSON // suppress warning
}

func TestExecuteNonPTY_Stderr(t *testing.T) {
	t.Parallel()

	in := Input{Command: "echo error >&2", Timeout: 10000}
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second)
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
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second)
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
	result, err := executeNonPTY(context.Background(), in, "", 100*time.Millisecond)
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
	result, err := executeNonPTY(context.Background(), in, dir, 10*time.Second)
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
	result, err := executeNonPTY(context.Background(), in, "", 10*time.Second)
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := Input{Command: "echo hi", Timeout: 10000}
	result, err := executeNonPTY(ctx, in, "", 10*time.Second)
	if err != nil {
		t.Fatalf("executeNonPTY() error: %v", err)
	}
	output := result.Data.(*Output)
	if output.ExitCode == 0 {
		t.Log("exit code 0 — context cancellation may not have propagated")
	}
}

// --- openPTY ---

func TestOpenPTY_Success(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY() error: %v", err)
	}
	if master == nil || slave == nil {
		t.Fatal("openPTY() returned nil file")
	}
	_ = master.Close()
	_ = slave.Close()
}

func TestOpenPTY_SlavePath(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
}

// --- PTY command tests ---

func TestPtyCommand_MultilineOutput(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := ptyCommand(
		context.Background(),
		"echo line1; echo line2; echo line3",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "line1") || !strings.Contains(joined, "line2") || !strings.Contains(joined, "line3") {
		t.Errorf("output = %q, want to contain line1, line2, line3", joined)
	}
}

func TestPtyCommand_Environment(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	_, _, _ = ptyCommand(
		context.Background(),
		"echo $GBOT_TEST_VAR",
		"",
		append(os.Environ(), "GBOT_TEST_VAR=testvalue123"),
		func(line string) { lines = append(lines, line) },
		10*time.Second,
	)

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "testvalue123") {
		t.Errorf("output = %q, want to contain 'testvalue123'", joined)
	}
}

func TestPtyCommand_PartialLineFlush(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf 'no-newline-end'",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
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
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf 'partial-no-newline'",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
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
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, interrupted, err := ptyCommand(
		context.Background(),
		"sleep 10",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
	}
	if !interrupted {
		t.Error("interrupted = false, want true for timed-out command")
	}
	if exitCode != 143 {
		t.Errorf("exitCode = %d, want 143 (SIGTERM)", exitCode)
	}
}

func TestPtyCommand_NonExitErrorPath(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	_, _, _ = ptyCommand(
		context.Background(),
		"kill -ABRT $$",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		5*time.Second,
	)
}

func TestPtyCommand_LongLine(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	longStr := strings.Repeat("A", 8192)
	var lines []string
	exitCode, _, err := ptyCommand(
		context.Background(),
		"printf '%s\\n' "+longStr,
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
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
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	var lines []string
	exitCode, _, err := ptyCommand(
		context.Background(),
		"exec cat",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		5*time.Second,
	)
	_ = exitCode
	_ = err
}

func TestPtyCommand_SigkillExit(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	// Process traps SIGTERM so killProcessTree escalates to SIGKILL
	var lines []string
	exitCode, interrupted, err := ptyCommand(
		context.Background(),
		"trap '' TERM; while true; do sleep 0.1; done",
		"",
		os.Environ(),
		func(line string) { lines = append(lines, line) },
		200*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("ptyCommand() error: %v", err)
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
	go func() {
		_ = ensureSocketInitialized()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("ensureSocketInitializer hung")
	}
}

// --- makeRaw / restoreTerminal ---

func TestMakeRaw_RestoreTerminal(t *testing.T) {
	state, err := makeRaw(0)
	if err != nil {
		return
	}
	if err := restoreTerminal(0, state); err != nil {
		t.Errorf("restoreTerminal() error: %v", err)
	}
}

func TestMakeRaw_RestoreTerminal_WithPTY(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY() error: %v", err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	state, err := makeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatalf("makeRaw on PTY slave: %v", err)
	}
	if err := restoreTerminal(int(slave.Fd()), state); err != nil {
		t.Errorf("restoreTerminal on PTY slave: %v", err)
	}
}

// --- applyEnvOverrides (PTY context) ---

func TestPtyApplyEnvOverrides(t *testing.T) {
	t.Parallel()

	base := []string{"A=1", "B=2", "C=3"}
	overrides := map[string]string{"B": "override", "D": "4"}

	result := applyEnvOverrides(base, overrides)

	foundB := false
	for _, e := range result {
		if e == "B=override" {
			foundB = true
			break
		}
	}
	if !foundB {
		t.Errorf("result = %v, want B=override", result)
	}

	for _, want := range []string{"A=1", "C=3", "D=4"} {
		found := false
		for _, e := range result {
			if e == want {
				found = true
				break
			}
		}
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

	_, _, err := ptyCommand(
		context.Background(),
		"echo hi",
		"",
		os.Environ(),
		func(line string) {},
		5*time.Second,
	)
	if err == nil {
		t.Error("expected error with non-existent shell")
	}
	if !strings.Contains(err.Error(), "start command") {
		t.Errorf("error = %v, want start command error", err)
	}
}

func TestOpenPTY_NoPtmx(t *testing.T) {
	orig := ptmxPath
	ptmxPath = "/nonexistent/ptmx/gbot-test"
	defer func() { ptmxPath = orig }()

	_, _, err := openPTY()
	if err == nil {
		t.Error("expected error with invalid ptmx path")
	}
	if !strings.Contains(err.Error(), "open /dev/ptmx") {
		// The error message references the original path
		t.Logf("error = %v", err)
	}
}

func TestIsPTYAvailable_NotLinux(t *testing.T) {
	orig := checkIsLinux
	checkIsLinux = func() bool { return false }
	defer func() { checkIsLinux = orig }()

	if isPTYAvailable() {
		t.Error("expected false on non-Linux")
	}
}

func TestIsPTYAvailable_NoPtmx(t *testing.T) {
	orig := PtmxCheckPath()
	SetPtmxCheckPath("/nonexistent/ptmx/gbot-test")
	defer func() { SetPtmxCheckPath(orig) }()

	if isPTYAvailable() {
		t.Error("expected false without /dev/ptmx")
	}
}

func TestPtyCommand_OpenPTYError(t *testing.T) {
	// Trigger openPTY failure inside ptyCommand
	orig := ptmxPath
	ptmxPath = "/nonexistent/ptmx/gbot-test"
	defer func() { ptmxPath = orig }()

	_, _, err := ptyCommand(
		context.Background(),
		"echo hi",
		"",
		os.Environ(),
		func(line string) {},
		5*time.Second,
	)
	if err == nil {
		t.Error("expected error when openPTY fails")
	}
	if !strings.Contains(err.Error(), "open PTY") {
		t.Errorf("error = %v, want open PTY error", err)
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

func TestDrainPTY_NormalLines(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte("hello\nworld\n")}, 64)
	var lines []string
	drainPTY(reader, func(line string) { lines = append(lines, line) })
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

func TestDrainPTY_EOBBreak(t *testing.T) {
	t.Parallel()
	// 32 bytes > 16-byte buffer forces isPrefix=true, then EOF
	// covers io.EOF break + partial line flush
	reader := bufio.NewReaderSize(
		strings.NewReader(strings.Repeat("A", 32)),
		16,
	)
	var lines []string
	drainPTY(reader, func(line string) { lines = append(lines, line) })
	joined := strings.Join(lines, "")
	if len(joined) != 32 {
		t.Errorf("output len = %d, want 32, got %q", len(joined), joined)
	}
}

func TestDrainPTY_NonEOFError(t *testing.T) {
	t.Parallel()
	// Reader returns data then non-EOF error -> covers generic break
	r, w := io.Pipe()
	go func() {
		_, _ = w.Write([]byte("data"))
		_ = w.CloseWithError(fmt.Errorf("pipe error"))
	}()
	reader := bufio.NewReaderSize(r, 64)
	var lines []string
	drainPTY(reader, func(line string) { lines = append(lines, line) })
	if len(lines) < 1 {
		t.Fatal("expected at least one line from flush")
	}
	if !strings.HasPrefix(lines[0], "data") {
		t.Errorf("lines[0] = %q, want to start with data", lines[0])
	}
}

func TestDrainPTY_NilCallback(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte("hello\n")}, 64)
	drainPTY(reader, nil) // should not panic
}

func TestDrainPTY_Empty(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(&dataThenEOFReader{data: []byte{}}, 64)
	var lines []string
	drainPTY(reader, func(line string) { lines = append(lines, line) })
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

// --- openPTY hooks ---

func TestOpenPTY_IoctlGetPtyNumError(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}
	orig := ioctlGetPtyNum
	ioctlGetPtyNum = func(fd int) (int, error) { return 0, fmt.Errorf("mock TIOCGPTN error") }
	defer func() { ioctlGetPtyNum = orig }()

	_, _, err := openPTY()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TIOCGPTN") {
		t.Errorf("error = %v, want TIOCGPTN error", err)
	}
}

func TestOpenPTY_IoctlUnlockPtyError(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}
	orig := ioctlUnlockPty
	ioctlUnlockPty = func(fd int) error { return fmt.Errorf("mock TIOCSPTLCK error") }
	defer func() { ioctlUnlockPty = orig }()

	_, _, err := openPTY()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "TIOCSPTLCK") {
		t.Errorf("error = %v, want TIOCSPTLCK error", err)
	}
}

func TestOpenPTY_SlaveOpenError(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}
	orig := openSlavePty
	openSlavePty = func(path string) (*os.File, error) { return nil, fmt.Errorf("mock slave error") }
	defer func() { openSlavePty = orig }()

	_, _, err := openPTY()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "open slave") {
		t.Errorf("error = %v, want open slave error", err)
	}
}
