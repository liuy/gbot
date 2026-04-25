package skills

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Shell block execution tests
// Source: src/utils/promptShellExecution.ts
// ---------------------------------------------------------------------------

// mockExecutor is a test ShellExecutor that returns canned output.
type mockExecutor struct {
	calls    atomic.Int32
	outputs  map[string]string // command → output
	failCmd  string            // command that should fail
	errMsg   string
}

func (m *mockExecutor) Execute(_ *types.ToolUseContext, command string) (string, error) {
	m.calls.Add(1)
	if command == m.failCmd {
		return "", &ShellCommandError{Pattern: command, Message: m.errMsg}
	}
	if out, ok := m.outputs[command]; ok {
		return out, nil
	}
	return "output: " + command, nil
}

func TestExecuteShellBlocks_BlockPattern(t *testing.T) {
	t.Parallel()

	content := "Before\n```!\necho hello\n```\nAfter"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "output: echo hello") {
		t.Errorf("expected shell output substituted, got %q", result)
	}
	if strings.Contains(result, "```!") {
		t.Errorf("block pattern should be replaced, got %q", result)
	}
}

func TestExecuteShellBlocks_InlinePattern(t *testing.T) {
	t.Parallel()

	content := "Current date: !`date +%Y` end"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "output: date +%Y") {
		t.Errorf("expected inline output substituted, got %q", result)
	}
	if strings.Contains(result, "!`") {
		t.Errorf("inline pattern should be replaced, got %q", result)
	}
}

func TestExecuteShellBlocks_MCPSkip(t *testing.T) {
	t.Parallel()

	content := "```!\necho hello\n```"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, true) // isMCP=true
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("MCP skills should skip shell execution, got %q", result)
	}
	if exec.calls.Load() != 0 {
		t.Error("no commands should execute for MCP skills")
	}
}

func TestExecuteShellBlocks_NoBlocks(t *testing.T) {
	t.Parallel()

	content := "Plain text without any shell commands."
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("expected unchanged content, got %q", result)
	}
}

func TestExecuteShellBlocks_MultipleBlocks(t *testing.T) {
	t.Parallel()

	content := "```!\ncmd1\n```\nMiddle\n```!\ncmd2\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"cmd1": "OUTPUT1",
			"cmd2": "OUTPUT2",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "OUTPUT1") {
		t.Errorf("expected OUTPUT1 in result, got %q", result)
	}
	if !strings.Contains(result, "OUTPUT2") {
		t.Errorf("expected OUTPUT2 in result, got %q", result)
	}
	if exec.calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", exec.calls.Load())
	}
}

func TestExecuteShellBlocks_Error(t *testing.T) {
	t.Parallel()

	content := "```!\nbad-command\n```"
	exec := &mockExecutor{
		failCmd: "bad-command",
		errMsg:  "command not found",
	}
	_, err := ExecuteShellBlocks(content, exec, nil, false)
	if err == nil {
		t.Fatal("expected error for failing command")
	}
	if !strings.Contains(err.Error(), "command not found") && !strings.Contains(err.Error(), "bad-command") {
		t.Fatalf("error should mention command or message, got %v", err)
	}
	sce, ok := err.(*ShellCommandError)
	if !ok {
		t.Fatalf("expected ShellCommandError, got %T: %v", err, err)
	}
	if !strings.Contains(sce.Message, "command not found") {
		t.Errorf("error message should contain 'command not found', got %q", sce.Message)
	}
}

func TestExecuteShellBlocks_MixedInlineAndBlock(t *testing.T) {
	t.Parallel()

	content := "Inline !`echo hi` and block:\n```!\necho bye\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"echo hi":  "HI",
			"echo bye": "BYE",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "HI") {
		t.Errorf("expected inline output HI, got %q", result)
	}
	if !strings.Contains(result, "BYE") {
		t.Errorf("expected block output BYE, got %q", result)
	}
}

func TestExecuteShellBlocks_EmptyBlock(t *testing.T) {
	t.Parallel()

	content := "```!\n\n```"
	exec := &mockExecutor{}
	_, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty block should not be executed
	if exec.calls.Load() != 0 {
		t.Errorf("empty block should not execute, got %d calls", exec.calls.Load())
	}
}

func TestExecuteShellBlocks_ParallelExecution(t *testing.T) {
	t.Parallel()

	content := "```!\ncmd1\n```\n```!\ncmd2\n```\n```!\ncmd3\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"cmd1": "OUT1",
			"cmd2": "OUT2",
			"cmd3": "OUT3",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.calls.Load() != 3 {
		t.Errorf("expected 3 parallel calls, got %d", exec.calls.Load())
	}
	for _, expected := range []string{"OUT1", "OUT2", "OUT3"} {
		if !strings.Contains(result, expected) {
			t.Errorf("expected %s in result, got %q", expected, result)
		}
	}
}

func TestShellCommandError(t *testing.T) {
	t.Parallel()

	err := &ShellCommandError{Pattern: "```!bad```", Message: "exit code 1"}
	msg := err.Error()
	if !strings.Contains(msg, "shell command failed") {
		t.Errorf("error should contain 'shell command failed', got %q", msg)
	}
	if !strings.Contains(msg, "```!bad```") {
		t.Errorf("error should contain pattern, got %q", msg)
	}
}
