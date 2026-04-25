package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CommandExecutor — exit code semantics
// Source: hooks.ts — exit 0=success, 2=blocking, other=non-blocking
// ---------------------------------------------------------------------------

func TestExecuteHook_Exit0_Success(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "echo hello", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want HookOutcomeSuccess", result.Outcome)
	}
	if !strings.Contains(result.Stdout, "hello") {
		t.Errorf("Stdout = %q, want to contain 'hello'", result.Stdout)
	}
}

func TestExecuteHook_Exit2_Blocking(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "echo blocked >&2 && exit 2", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeBlocking {
		t.Errorf("Outcome = %v, want HookOutcomeBlocking", result.Outcome)
	}
	if !strings.Contains(result.Stderr, "blocked") {
		t.Errorf("Stderr = %q, want to contain 'blocked'", result.Stderr)
	}
}

func TestExecuteHook_Exit1_NonBlockingError(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "echo oops >&2 && exit 1", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeNonBlockingError {
		t.Errorf("Outcome = %v, want HookOutcomeNonBlockingError", result.Outcome)
	}
	if !strings.Contains(result.Stderr, "oops") {
		t.Errorf("Stderr = %q, want to contain 'oops'", result.Stderr)
	}
}

func TestExecuteHook_Exit3_NonBlockingError(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "exit 3", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeNonBlockingError {
		t.Errorf("Outcome = %v, want HookOutcomeNonBlockingError for exit 3", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Stdin content — hook input JSON passed via stdin
// ---------------------------------------------------------------------------

func TestExecuteHook_StdinContainsHookInput(t *testing.T) {
	e := &CommandExecutor{}
	input := &HookInput{
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
		SessionID:     "test-session",
	}
	// Command that echoes stdin back to stdout
	result := e.ExecuteHook(context.Background(), "cat", input, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Fatalf("Outcome = %v, want Success", result.Outcome)
	}

	// Verify stdin contains the hook input JSON
	var parsed HookInput
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &parsed); err != nil {
		t.Fatalf("stdin JSON parse error: %v, stdout: %q", err, result.Stdout)
	}
	if parsed.HookEventName != "PreToolUse" {
		t.Errorf("hook_event_name = %q, want PreToolUse", parsed.HookEventName)
	}
	if parsed.ToolName != "Bash" {
		t.Errorf("tool_name = %q, want Bash", parsed.ToolName)
	}
	if parsed.SessionID != "test-session" {
		t.Errorf("session_id = %q, want test-session", parsed.SessionID)
	}
}

// ---------------------------------------------------------------------------
// Stdout JSON parsing — HookOutput
// Source: types/hooks.ts:169-176 — hookJSONOutputSchema
// ---------------------------------------------------------------------------

func TestExecuteHook_StdoutJSONDecideBlock(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(),
		`echo '{"decision":"block","reason":"unsafe command"}'`,
		&HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeBlocking {
		t.Errorf("Outcome = %v, want HookOutcomeBlocking", result.Outcome)
	}
	if result.Output == nil {
		t.Fatal("Output is nil, want parsed HookOutput")
	}
	if result.Output.Decision != "block" {
		t.Errorf("Output.Decision = %q, want block", result.Output.Decision)
	}
	if result.Output.Reason != "unsafe command" {
		t.Errorf("Output.Reason = %q, want 'unsafe command'", result.Output.Reason)
	}
}

func TestExecuteHook_StdoutJSONDecideApprove(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(),
		`echo '{"decision":"approve"}'`,
		&HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want HookOutcomeSuccess", result.Outcome)
	}
	if result.Output == nil {
		t.Fatal("Output is nil, want parsed HookOutput")
	}
	if result.Output.Decision != "approve" {
		t.Errorf("Output.Decision = %q, want approve", result.Output.Decision)
	}
}

func TestExecuteHook_StdoutJSONContinueFalse(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(),
		`echo '{"continue":false,"stopReason":"blocked by policy"}'`,
		&HookInput{}, 10*time.Second)
	if !result.PreventContinuation {
		t.Error("PreventContinuation = false, want true")
	}
	if result.StopReason != "blocked by policy" {
		t.Errorf("StopReason = %q, want 'blocked by policy'", result.StopReason)
	}
}

func TestExecuteHook_StdoutJSONSystemMessage(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(),
		`echo '{"systemMessage":"Running in restricted mode"}'`,
		&HookInput{}, 10*time.Second)
	if result.SystemMessage != "Running in restricted mode" {
		t.Errorf("SystemMessage = %q, want 'Running in restricted mode'", result.SystemMessage)
	}
}

func TestExecuteHook_StdoutNonJSON(t *testing.T) {
	// Non-JSON stdout is fine — Output stays nil
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "echo 'just plain text'", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want Success", result.Outcome)
	}
	if result.Output != nil {
		t.Errorf("Output = %+v, want nil for non-JSON stdout", result.Output)
	}
}

// ---------------------------------------------------------------------------
// Timeout
// ---------------------------------------------------------------------------

func TestExecuteHook_Timeout(t *testing.T) {
	e := &CommandExecutor{}
	// Command that sleeps longer than timeout
	result := e.ExecuteHook(context.Background(), "sleep 10", &HookInput{}, 100*time.Millisecond)
	if result.Outcome != HookOutcomeTimeout {
		t.Errorf("Outcome = %v, want HookOutcomeTimeout", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestExecuteHook_ContextCancelled(t *testing.T) {
	e := &CommandExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	result := e.ExecuteHook(ctx, "sleep 10", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeCancelled {
		t.Errorf("Outcome = %v, want HookOutcomeCancelled", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Environment injection
// ---------------------------------------------------------------------------

func TestExecuteHook_EnvInjection(t *testing.T) {
	e := &CommandExecutor{
		Env: []string{
			"GBOT_PROJECT_DIR=/home/user/project",
			"GBOT_SESSION_ID=test-123",
		},
	}
	result := e.ExecuteHook(context.Background(),
		"echo $GBOT_PROJECT_DIR && echo $GBOT_SESSION_ID",
		&HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Fatalf("Outcome = %v, want Success", result.Outcome)
	}
	if !strings.Contains(result.Stdout, "/home/user/project") {
		t.Errorf("Stdout = %q, want to contain GBOT_PROJECT_DIR", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "test-123") {
		t.Errorf("Stdout = %q, want to contain GBOT_SESSION_ID", result.Stdout)
	}
}

// ---------------------------------------------------------------------------
// HookName tracking
// ---------------------------------------------------------------------------

func TestExecuteHook_HookName(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "echo hello", &HookInput{}, 10*time.Second)
	if result.HookName != "echo hello" {
		t.Errorf("HookName = %q, want 'echo hello'", result.HookName)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestExecuteHook_EmptyCommand(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "true", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want Success for 'true'", result.Outcome)
	}
}

func TestExecuteHook_CommandNotFound(t *testing.T) {
	e := &CommandExecutor{}
	result := e.ExecuteHook(context.Background(), "nonexistent_command_xyz", &HookInput{}, 10*time.Second)
	if result.Outcome != HookOutcomeNonBlockingError {
		t.Errorf("Outcome = %v, want NonBlockingError for nonexistent command", result.Outcome)
	}
}

func TestExecuteHook_NilInput(t *testing.T) {
	e := &CommandExecutor{}
	// nil input should not panic — json.Marshal(nil HookInput) produces {}
	result := e.ExecuteHook(context.Background(), "cat", nil, 10*time.Second)
	// cat reads {} from stdin, outputs nothing, exits 0 → Success
	if result.Outcome != HookOutcomeSuccess {
		t.Errorf("Outcome = %v, want Success for nil input with cat", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// FormatEnvVar helper
// ---------------------------------------------------------------------------

func TestFormatEnvVar(t *testing.T) {
	got := FormatEnvVar("GBOT_PROJECT_DIR", "/tmp/test")
	if got != "GBOT_PROJECT_DIR=/tmp/test" {
		t.Errorf("FormatEnvVar = %q, want 'GBOT_PROJECT_DIR=/tmp/test'", got)
	}
}
