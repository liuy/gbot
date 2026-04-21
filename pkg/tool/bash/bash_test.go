package bash_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)

	if tt.Name() != "Bash" {
		t.Errorf("Name() = %q, want %q", tt.Name(), "Bash")
	}
	if tt.InterruptBehavior() != tool.InterruptCancel {
		t.Errorf("InterruptBehavior() = %d, want %d", tt.InterruptBehavior(), tool.InterruptCancel)
	}
	if tt.Prompt() == "" {
		t.Error("Prompt() is empty, want non-empty")
	}
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() = true, want false")
	}
	if tt.IsEnabled() != true {
		t.Error("IsEnabled() = false, want true")
	}
}

func TestNewAliases(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	aliases := tt.Aliases()
	if len(aliases) != 3 {
		t.Fatalf("Aliases() len = %d, want 3", len(aliases))
	}
	expected := []string{"bash", "shell", "sh"}
	for _, e := range expected {
		found := slices.Contains(aliases, e)
		if !found {
			t.Errorf("alias %q not found", e)
		}
	}
}

func TestNewInputSchema(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	schema := tt.InputSchema()
	var obj map[string]any
	if err := json.Unmarshal(schema, &obj); err != nil {
		t.Fatalf("InputSchema() is not valid JSON: %v", err)
	}
	if obj["type"] != "object" {
		t.Errorf("schema type = %v, want object", obj["type"])
	}
}

func TestDescription_WithDescription(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	input := json.RawMessage(`{"command":"ls","description":"List files in directory"}`)
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	// TS renderToolUseMessage: always shows command, not LLM description
	if desc != "ls" {
		t.Errorf("Description() = %q, want %q", desc, "ls")
	}
}

func TestDescription_WithCommand(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	input := json.RawMessage(`{"command":"echo hello"}`)
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "echo hello" {
		t.Errorf("Description() = %q, want %q", desc, "echo hello")
	}
}

func TestDescription_InvalidJSON(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	desc, err := tt.Description(json.RawMessage(`{invalid`))
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "Execute a bash command" {
		t.Errorf("Description() = %q, want %q", desc, "Execute a bash command")
	}
}

// ---------------------------------------------------------------------------
// IsReadOnly / IsDestructive
// ---------------------------------------------------------------------------

func TestIsReadOnly(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)

	tests := []struct {
		cmd  string
		want bool
	}{
		{"ls", true},
		{"ls -la", true},
		{"cat file.txt", true},
		{"git status", true},
		{"git log --oneline", true},
		{"echo hello", true},
		{"pwd", true},
		{"whoami", true},
		{"grep pattern file", true},
		{"rm file.txt", false},
		{"mkdir dir", false},
		{"npm install", false},
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()
			input := json.RawMessage(`{"command":"` + tc.cmd + `"}`)
			got := tt.IsReadOnly(input)
			if got != tc.want {
				t.Errorf("IsReadOnly(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestIsDestructive(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)

	tests := []struct {
		cmd  string
		want bool
	}{
		{"rm -rf /", true},
		{"rm -rf /*", true},
		{"rm -rf ~", true},
		{"mkfs.ext4 /dev/sda1", true},
		{"dd if=/dev/zero of=/dev/sda", true},
		{"> /dev/sda", true},
		{"shutdown now", true},
		{"reboot", true},
		{"halt", true},
		{"init 0", true},
		{"init 6", true},
		{"echo hello", false},
		{"ls -la", false},
		{"rm file.txt", false},
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			t.Parallel()
			input := json.RawMessage(`{"command":"` + tc.cmd + `"}`)
			got := tt.IsDestructive(input)
			if got != tc.want {
				t.Errorf("IsDestructive(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestIsDestructive_InvalidJSON(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	// Invalid JSON should assume destructive (safe default)
	if !tt.IsDestructive(json.RawMessage(`{invalid`)) {
		t.Error("IsDestructive(invalid json) = false, want true")
	}
}

func TestIsReadOnly_InvalidJSON(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	// Invalid JSON should return false for read-only
	if tt.IsReadOnly(json.RawMessage(`{invalid`)) {
		t.Error("IsReadOnly(invalid json) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Execute — happy path
// ---------------------------------------------------------------------------

func TestExecute_Echo(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"echo hello world"}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output, ok := result.Data.(*bash.Output)
	if !ok {
		t.Fatalf("Data type = %T, want *bash.Output", result.Data)
	}
	if !strings.Contains(output.Stdout, "hello world") {
		t.Errorf("Stdout = %q, want to contain %q", output.Stdout, "hello world")
	}
	if output.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", output.ExitCode)
	}
	if output.TimedOut {
		t.Error("TimedOut = true, want false")
	}
}

func TestExecute_Stderr(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"echo error >&2"}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	// PTY mode merges stdout/stderr into Stdout; non-PTY has separate Stderr
	if !strings.Contains(output.Stderr, "error") && !strings.Contains(output.Stdout, "error") {
		t.Errorf("Stdout=%q Stderr=%q, want either to contain %q", output.Stdout, output.Stderr, "error")
	}
}

func TestExecute_NonZeroExit(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"exit 42"}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if output.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", output.ExitCode)
	}
}

func TestExecute_WorkingDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tctx := &types.ToolUseContext{WorkingDir: dir}
	input := json.RawMessage(`{"command":"pwd"}`)
	result, err := bash.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if !strings.Contains(output.Stdout, dir) {
		t.Errorf("Stdout = %q, want to contain %q", output.Stdout, dir)
	}
}

func TestExecute_CWDOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := json.RawMessage(`{"command":"pwd","cwd":"` + dir + `"}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if !strings.Contains(output.Stdout, dir) {
		t.Errorf("Stdout = %q, want to contain %q", output.Stdout, dir)
	}
}

// ---------------------------------------------------------------------------
// Execute — error paths
// ---------------------------------------------------------------------------

func TestExecute_InvalidJSON(t *testing.T) {
	t.Parallel()

	_, err := bash.Execute(context.Background(), json.RawMessage(`{invalid`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for invalid JSON")
	}
}

func TestExecute_EmptyCommand(t *testing.T) {
	t.Parallel()

	_, err := bash.Execute(context.Background(), json.RawMessage(`{"command":""}`), nil)
	if err == nil {
		t.Fatal("Execute() error = nil, want error for empty command")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("Error = %q, want to contain 'command is required'", err.Error())
	}
}

func TestExecute_Timeout(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"sleep 10","timeout":100}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if !output.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	// PTY mode returns signal-based exit codes (128+signal), non-PTY returns -1
	// Both are acceptable — the important invariant is TimedOut=true
	if output.ExitCode != -1 && output.ExitCode < 128 {
		t.Errorf("ExitCode = %d, want -1 or signal-based code (>=128)", output.ExitCode)
	}
}

func TestExecute_MaxTimeoutCapped(t *testing.T) {
	t.Parallel()

	// Verify that a huge timeout gets capped (doesn't hang for 1 hour)
	input := json.RawMessage(`{"command":"sleep 0.1","timeout":999999999}`)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		res, execErr := bash.Execute(ctx, input, nil)
		if execErr != nil {
			t.Logf("Execute() error (ok in timeout cap test): %v", execErr)
		}
		if res != nil {
			out := res.Data.(*bash.Output)
			t.Logf("Execute() completed: ExitCode=%d TimedOut=%v", out.ExitCode, out.TimedOut)
		}
		close(done)
	}()

	select {
	case <-done:
		// Good, completed within the context deadline
	case <-time.After(5 * time.Second):
		t.Fatal("Execute() hung, timeout cap may not be working")
	}
}

// ---------------------------------------------------------------------------
// Input / Output JSON
// ---------------------------------------------------------------------------

func TestInputJSON(t *testing.T) {
	t.Parallel()

	raw := `{"command":"ls -la","timeout":5000,"cwd":"/tmp","description":"list files"}`
	var in bash.Input
	if err := json.Unmarshal(json.RawMessage(raw), &in); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if in.Command != "ls -la" {
		t.Errorf("Command = %q, want %q", in.Command, "ls -la")
	}
	if in.Timeout != 5000 {
		t.Errorf("Timeout = %d, want 5000", in.Timeout)
	}
	if in.CWD != "/tmp" {
		t.Errorf("CWD = %q, want %q", in.CWD, "/tmp")
	}
	if in.Description != "list files" {
		t.Errorf("Description = %q, want %q", in.Description, "list files")
	}
}

func TestOutputJSON(t *testing.T) {
	t.Parallel()

	output := bash.Output{
		Stdout:   "hello\n",
		Stderr:   "",
		ExitCode: 0,
		TimedOut: false,
		CWD:      "/home",
	}

	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got bash.Output
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want %q", got.Stdout, "hello\n")
	}
	if got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", got.ExitCode)
	}
	if got.CWD != "/home" {
		t.Errorf("CWD = %q, want %q", got.CWD, "/home")
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	t.Parallel()

	if bash.DefaultTimeout != 2*time.Minute {
		t.Errorf("DefaultTimeout = %v, want 2m", bash.DefaultTimeout)
	}
	if bash.MaxTimeout != 10*time.Minute {
		t.Errorf("MaxTimeout = %v, want 10m", bash.MaxTimeout)
	}
	if bash.MaxOutputSize != 30000 {
		t.Errorf("MaxOutputSize = %d, want %d", bash.MaxOutputSize, 30000)
	}
}

// ---------------------------------------------------------------------------
// Description — long strings get truncated via truncate()
// ---------------------------------------------------------------------------

func TestDescription_Truncation(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)

	// Command longer than 80 chars should be truncated
	longCmd := strings.Repeat("a", 100)
	input := json.RawMessage(`{"command":"` + longCmd + `"}`)
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if len(desc) > 80 {
		t.Errorf("Description() length = %d, want <= 80", len(desc))
	}
	if !strings.HasSuffix(desc, "...") {
		t.Errorf("Description() = %q, should end with ...", desc)
	}

	// Command + description: Description shows command, ignores description field
	longDesc := strings.Repeat("b", 100)
	input = json.RawMessage(`{"command":"echo","description":"` + longDesc + `"}`)
	desc, err = tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "echo" {
		t.Errorf("Description() = %q, want %q", desc, "echo")
	}
}

// ---------------------------------------------------------------------------
// Execute — trigger truncateOutput via large output
// ---------------------------------------------------------------------------

func TestExecute_LargeOutputTruncation(t *testing.T) {
	t.Parallel()

	// Generate output larger than MaxOutputSize to test truncation
	input := json.RawMessage(`{"command":"python3 -c \"import sys; sys.stdout.write('x' * 11000000)\""}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	// Output should be capped at MaxOutputSize + truncation message
	if len(output.Stdout) > bash.MaxOutputSize+100 {
		t.Errorf("Stdout length = %d, should be capped around %d", len(output.Stdout), bash.MaxOutputSize)
	}
}

func TestExecute_LargeStderrTruncation(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"python3 -c \"import sys; sys.stderr.write('x' * 11000000)\""}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if len(output.Stderr) > bash.MaxOutputSize+100 {
		t.Errorf("Stderr length = %d, should be capped around %d", len(output.Stderr), bash.MaxOutputSize)
	}
}

// ---------------------------------------------------------------------------
// Execute — nil tctx falls back to os.Getwd()
// ---------------------------------------------------------------------------

func TestExecute_NilContext(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"echo ok"}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if !strings.Contains(output.Stdout, "ok") {
		t.Errorf("Stdout = %q, want to contain 'ok'", output.Stdout)
	}
	// CWD should be set even with nil context
	if output.CWD == "" {
		t.Error("CWD is empty, want a value from os.Getwd()")
	}
}

// ---------------------------------------------------------------------------
// Execute — tctx with empty WorkingDir falls back to os.Getwd()
// ---------------------------------------------------------------------------

func TestExecute_EmptyWorkingDir(t *testing.T) {
	t.Parallel()

	tctx := &types.ToolUseContext{WorkingDir: ""}
	input := json.RawMessage(`{"command":"echo hello"}`)
	result, err := bash.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if output.CWD == "" {
		t.Error("CWD is empty, want a value from os.Getwd()")
	}
}

// ---------------------------------------------------------------------------
// Execute — timeout=0 uses default
// ---------------------------------------------------------------------------

func TestExecute_ZeroTimeout(t *testing.T) {
	t.Parallel()

	input := json.RawMessage(`{"command":"echo fast","timeout":0}`)
	result, err := bash.Execute(context.Background(), input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	if output.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", output.ExitCode)
	}
}

func TestExecute_CancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	input := json.RawMessage(`{"command":"echo hello"}`)
	result, err := bash.Execute(ctx, input, nil)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := result.Data.(*bash.Output)
	// The cancelled context may result in a non-zero exit code
	// but should not panic
	if output.CWD == "" {
		t.Error("CWD should be set")
	}
}

// ---------------------------------------------------------------------------
// Description — empty command returns empty string (line 97)
// ---------------------------------------------------------------------------

func TestDescription_EmptyCommand(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	input := json.RawMessage(`{"command":""}`)
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "" {
		t.Errorf("Description() = %q, want empty string for empty command", desc)
	}
}

// ---------------------------------------------------------------------------
// RenderResult — covers lines 120-141 in bash.go
// ---------------------------------------------------------------------------

func TestRenderResult_StdoutOnly(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stdout:   "hello world",
		ExitCode: 0,
	})
	if result != "hello world" {
		t.Errorf("RenderResult(stdout only) = %q, want %q", result, "hello world")
	}
}

func TestRenderResult_StderrOnly(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stderr:   "error msg",
		ExitCode: 1,
	})
	if result != "error msg" {
		t.Errorf("RenderResult(stderr only) = %q, want %q", result, "error msg")
	}
}

func TestRenderResult_StdoutAndStderr(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stdout:   "output",
		Stderr:   "error",
		ExitCode: 1,
	})
	want := "output\nerror"
	if result != want {
		t.Errorf("RenderResult(stdout+stderr) = %q, want %q", result, want)
	}
}

func TestRenderResult_TimedOutOnly(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		TimedOut: true,
		ExitCode: -1,
	})
	if result != "Command timed out" {
		t.Errorf("RenderResult(timedout only) = %q, want %q", result, "Command timed out")
	}
}

func TestRenderResult_StdoutAndTimedOut(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stdout:   "partial output",
		TimedOut: true,
		ExitCode: -1,
	})
	want := "partial output\nCommand timed out"
	if result != want {
		t.Errorf("RenderResult(stdout+timedout) = %q, want %q", result, want)
	}
}

func TestRenderResult_StdoutStderrAndTimedOut(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stdout:   "output",
		Stderr:   "warning",
		TimedOut: true,
		ExitCode: -1,
	})
	want := "output\nwarning\nCommand timed out"
	if result != want {
		t.Errorf("RenderResult(stdout+stderr+timedout) = %q, want %q", result, want)
	}
}

func TestRenderResult_NonOutputType(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult("some random string")
	if !strings.Contains(result, "some random string") {
		t.Errorf("RenderResult(non-Output) = %q, should contain the input", result)
	}
}

func TestRenderResult_EmptyOutput(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		ExitCode: 0,
	})
	if result != "" {
		t.Errorf("RenderResult(empty output) = %q, want empty string", result)
	}
}

func TestRenderResult_StderrAndTimedOut(t *testing.T) {
	t.Parallel()

	tt := bash.New(nil)
	result := tt.RenderResult(&bash.Output{
		Stderr:   "error",
		TimedOut: true,
		ExitCode: -1,
	})
	want := "error\nCommand timed out"
	if result != want {
		t.Errorf("RenderResult(stderr+timedout) = %q, want %q", result, want)
	}
}

func TestExecute_WithToolContextCWD(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	input := json.RawMessage(`{"command":"pwd"}`)
	tctx := &types.ToolUseContext{WorkingDir: dir}
	result, err := bash.Execute(context.Background(), input, tctx)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	output := result.Data.(*bash.Output)
	if !strings.Contains(output.Stdout, dir) {
		t.Errorf("Stdout = %q, want to contain %q", output.Stdout, dir)
	}
}
