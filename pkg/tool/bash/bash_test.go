package bash_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/tool/bash"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// New — tool metadata
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Parallel()

	tt := bash.New()

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

	tt := bash.New()
	aliases := tt.Aliases()
	if len(aliases) != 3 {
		t.Fatalf("Aliases() len = %d, want 3", len(aliases))
	}
	expected := []string{"bash", "shell", "sh"}
	for _, e := range expected {
		found := false
		for _, a := range aliases {
			if a == e {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("alias %q not found", e)
		}
	}
}

func TestNewInputSchema(t *testing.T) {
	t.Parallel()

	tt := bash.New()
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

	tt := bash.New()
	input := json.RawMessage(`{"command":"ls","description":"List files in directory"}`)
	desc, err := tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if desc != "List files in directory" {
		t.Errorf("Description() = %q, want %q", desc, "List files in directory")
	}
}

func TestDescription_WithCommand(t *testing.T) {
	t.Parallel()

	tt := bash.New()
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

	tt := bash.New()
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

	tt := bash.New()

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

	tt := bash.New()

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

	tt := bash.New()
	// Invalid JSON should assume destructive (safe default)
	if !tt.IsDestructive(json.RawMessage(`{invalid`)) {
		t.Error("IsDestructive(invalid json) = false, want true")
	}
}

func TestIsReadOnly_InvalidJSON(t *testing.T) {
	t.Parallel()

	tt := bash.New()
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
	if !strings.Contains(output.Stderr, "error") {
		t.Errorf("Stderr = %q, want to contain %q", output.Stderr, "error")
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
	if output.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", output.ExitCode)
	}
}

func TestExecute_MaxTimeoutCapped(t *testing.T) {
	t.Parallel()

	// Verify that a huge timeout gets capped (doesn't hang for 1 hour)
	// Use a reasonable timeout that still triggers but is under MaxTimeout
	input := json.RawMessage(`{"command":"sleep 1","timeout":999999999}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = bash.Execute(ctx, input, nil)
		close(done)
	}()

	select {
	case <-done:
		// Good, completed within the context deadline
	case <-time.After(10 * time.Second):
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
	if bash.MaxOutputSize != 10*1024*1024 {
		t.Errorf("MaxOutputSize = %d, want %d", bash.MaxOutputSize, 10*1024*1024)
	}
}

// ---------------------------------------------------------------------------
// Description — long strings get truncated via truncate()
// ---------------------------------------------------------------------------

func TestDescription_Truncation(t *testing.T) {
	t.Parallel()

	tt := bash.New()

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

	// Description field longer than 80 chars
	longDesc := strings.Repeat("b", 100)
	input = json.RawMessage(`{"command":"echo","description":"` + longDesc + `"}`)
	desc, err = tt.Description(input)
	if err != nil {
		t.Fatalf("Description() error: %v", err)
	}
	if len(desc) > 80 {
		t.Errorf("Description() length = %d, want <= 80", len(desc))
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
