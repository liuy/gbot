package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// CommandExecutor — source: hooks.ts:265-405 (command execution)
//
// Runs hook commands via bash, captures stdout/stderr, parses exit codes.
// ---------------------------------------------------------------------------

// CommandExecutor runs hook commands via bash.
// Source: hooks.ts:265-405 — executeTool() for command hooks.
type CommandExecutor struct {
	// Env contains extra environment variables injected into every hook command.
	// Typically: GBOT_PROJECT_DIR, GBOT_SESSION_ID.
	Env []string
}

// ExecuteHook runs a hook command and returns the result.
// Source: hooks.ts:265-405 — executeTool() / execHook().
//
// Exit code semantics (aligned with TS):
//   - 0 → HookOutcomeSuccess
//   - 2 → HookOutcomeBlocking (TS: exit 2 = blocking error)
//   - other non-zero → HookOutcomeNonBlockingError
//   - timeout → HookOutcomeTimeout (passthrough behavior)
//   - context cancellation → HookOutcomeCancelled
func (e *CommandExecutor) ExecuteHook(ctx context.Context, command string, input *HookInput, timeout time.Duration) HookResult {
	// Serialize input as JSON for stdin.
	// Source: hooks.ts — hook input passed via stdin pipe.
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return HookResult{
			Outcome:  HookOutcomeNonBlockingError,
			Stderr:   fmt.Sprintf("hooks: marshal input: %v", err),
			HookName: command,
		}
	}

	// Create timeout context.
	// Source: hooks.ts:166 — TOOL_HOOK_EXECUTION_TIMEOUT_MS
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Build command.
	// Source: hooks.ts — shell execution via $SHELL (bash).
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	// Set environment: inherit parent + inject extra vars.
	if len(e.Env) > 0 {
		cmd.Env = append(cmd.Environ(), e.Env...)
	}

	// Pipe stdin for hook input JSON.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return HookResult{
			Outcome:  HookOutcomeNonBlockingError,
			Stderr:   fmt.Sprintf("hooks: stdin pipe: %v", err),
			HookName: command,
		}
	}

	// Capture stdout and stderr.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Start the command.
	if err := cmd.Start(); err != nil {
		return HookResult{
			Outcome:  HookOutcomeNonBlockingError,
			Stderr:   fmt.Sprintf("hooks: start command: %v", err),
			HookName: command,
		}
	}

	// Write input JSON to stdin in a goroutine (avoids deadlock if command
	// exits before reading all stdin).
	go func() {
		_, _ = stdin.Write(inputJSON)
		_ = stdin.Close()
	}()

	// Wait for completion.
	err = cmd.Wait()

	// Classify outcome based on error and context state.
	result := HookResult{
		HookName: command,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}

	// Check for context cancellation / timeout first.
	if ctx.Err() == context.DeadlineExceeded {
		result.Outcome = HookOutcomeTimeout
		return result
	}
	if ctx.Err() == context.Canceled {
		result.Outcome = HookOutcomeCancelled
		return result
	}

	if err != nil {
		// Parse exit code.
		// Source: hooks.ts — exit 2 = blocking, other = non-blocking error.
		if exitErr, ok := err.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 2:
				result.Outcome = HookOutcomeBlocking
			default:
				result.Outcome = HookOutcomeNonBlockingError
			}
		} else {
			result.Outcome = HookOutcomeNonBlockingError
			result.Stderr = err.Error()
		}
	} else {
		result.Outcome = HookOutcomeSuccess
	}

	// Try to parse stdout as JSON.
	// Source: types/hooks.ts:169-176 — hookJSONOutputSchema.
	stdoutStr := strings.TrimSpace(result.Stdout)
	if stdoutStr != "" {
		var output HookOutput
		if json.Unmarshal([]byte(stdoutStr), &output) == nil {
			result.Output = &output
			// Extract fields from parsed output into result.
			if output.Decision == "block" {
				result.Outcome = HookOutcomeBlocking
			}
			if output.StopReason != "" {
				result.StopReason = output.StopReason
			}
			if output.SystemMessage != "" {
				result.SystemMessage = output.SystemMessage
			}
			if output.Continue != nil && !*output.Continue {
				result.PreventContinuation = true
			}
		}
	}

	return result
}

// FormatEnvVar formats a KEY=VALUE environment variable entry.
func FormatEnvVar(key, value string) string {
	return fmt.Sprintf("%s=%s", key, value)
}
