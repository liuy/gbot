// Package bash implements the Bash tool for executing shell commands.
//
// Source reference: tools/BashTool/BashTool.ts
// 1:1 port from the TypeScript source.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Input is the bash tool input schema.
// Source: BashTool.ts — Zod schema for bash input.
type Input struct {
	Command string `json:"command" validate:"required"`
	Timeout int    `json:"timeout,omitempty"`  // milliseconds, default 120000
	CWD     string `json:"cwd,omitempty"`
	Description string `json:"description,omitempty"`
	RunInBackground bool `json:"run_in_background,omitempty"`
}

// Output is the bash tool output.
// Source: BashTool.ts — tool result data.
type Output struct {
	Stdout   string `json:"output"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exitCode"`
	TimedOut bool   `json:"timed_out,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

// DefaultTimeout is the default command timeout (2 minutes).
// Source: BashTool.ts — DEFAULT_TIMEOUT
const DefaultTimeout = 2 * time.Minute

// MaxTimeout is the maximum allowed timeout (10 minutes).
// Source: BashTool.ts — MAX_TIMEOUT
const MaxTimeout = 10 * time.Minute

// MaxOutputSize is the maximum output size (10MB).
// Source: BashTool.ts — MAX_OUTPUT_BYTES
const MaxOutputSize = 10 * 1024 * 1024

// New creates the Bash tool.
// Source: tools/BashTool/BashTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["command"],
		"properties": {
			"command": {
				"type": "string",
				"description": "The bash command to run."
			},
			"timeout": {
				"type": "number",
				"description": "Optional timeout in milliseconds (max 600000). Default 120000."
			},
			"cwd": {
				"type": "string",
				"description": "The working directory for the command. Default: current working directory."
			},
			"description": {
				"type": "string",
				"description": "Clear, concise description of what this command does (max 80 chars)."
			},
			"run_in_background": {
				"type": "boolean",
				"description": "Run command in background. Returns immediately with task ID."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "Bash",
		Aliases_: []string{"bash", "shell", "sh"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Execute a bash command", nil
			}
			// TS renderToolUseMessage: always show the command, not the LLM description
			if in.Command != "" {
				return truncate(in.Command, 78), nil
			}
			return "", nil
		},
		Call_: Execute,
		IsReadOnly_: func(input json.RawMessage) bool {
			// Source: BashTool.ts — command classifier determines read-only
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return isReadOnlyCommand(in.Command)
		},
		IsDestructive_: func(input json.RawMessage) bool {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return true // assume destructive if can't parse
			}
			return isDestructiveCommand(in.Command)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false // Bash commands are never concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
			Prompt_: "Use this tool to execute terminal commands. Commands run in a bash shell.",
			RenderResult_: func(data any) string {
				out, ok := data.(*Output)
				if !ok {
					return fmt.Sprintf("%v", data)
				}
				var sb strings.Builder
				if out.Stdout != "" {
					sb.WriteString(out.Stdout)
				}
				if out.Stderr != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(out.Stderr)
				}
				if out.TimedOut {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString("Command timed out")
				}
				return sb.String()
			},
	})
}

// Execute runs a bash command.
// Source: BashTool.ts:call() — 1:1 port.
// Uses PTY when available (Linux with /dev/ptmx), falls back to non-PTY mode.
// Source: Shell.ts:181-442 — exec() dispatches to provider which builds command.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	// Determine timeout
	timeout := DefaultTimeout
	if in.Timeout > 0 {
		timeout = time.Duration(in.Timeout) * time.Millisecond
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	// Determine working directory
	cwd := in.CWD
	if cwd == "" {
		if tctx != nil {
			cwd = tctx.WorkingDir
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
	}

	// Try PTY mode first, fallback to non-PTY
	// Source: Plan Step 1.9 — graceful degradation
	if isPTYAvailable() {
		return executePTY(ctx, in, cwd, timeout)
	}
	return executeNonPTY(ctx, in, cwd, timeout)
}

// executePTY runs a command in PTY mode.
// Source: Shell.ts:181-442 — exec() with provider.buildExecCommand + wrapSpawn.
func executePTY(ctx context.Context, in Input, cwd string, timeout time.Duration) (*tool.ToolResult, error) {
	// Build the wrapped command
	// Source: bashProvider.ts:77-198 — buildExecCommand
	id := fmt.Sprintf("%04x", time.Now().UnixNano()%0x10000)
	cwdFile := buildCwdFilePath(id)
	wrappedCmd := buildCommand(in.Command, nil, cwdFile)

	// Build environment with TMUX isolation
	// Source: bashProvider.ts:208-253 — getEnvironmentOverrides
	baseEnv := os.Environ()
	if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
		baseEnv = applyEnvOverrides(baseEnv, overrides)
	}

	// Execute in PTY
	var outputBuf strings.Builder

	exitCode, interrupted, err := ptyCommand(ctx, wrappedCmd, cwd, baseEnv,
		func(line string) {
			outputBuf.WriteString(line)
			outputBuf.WriteByte('\n')
		},
		timeout,
	)

	if err != nil {
		return nil, err
	}

	// Track CWD after command completes
	// Source: Shell.ts:396-420 — read cwdFilePath, update engine cwd
	newCwd := trackCwd(cwdFile, cwd)

	// Clean up cwd temp file
	// Source: Shell.ts:416-420 — unlinkSync(nativeCwdFilePath)
	_ = os.Remove(cwdFile)

	output := &Output{
		Stdout:   truncateOutput(outputBuf.String(), MaxOutputSize),
		ExitCode: exitCode,
		TimedOut: interrupted,
		CWD:      newCwd,
	}

	return &tool.ToolResult{Data: output}, nil
}

// executeNonPTY runs a command without PTY (fallback mode).
// This is the original implementation, used when PTY is not available.
func executeNonPTY(ctx context.Context, in Input, cwd string, timeout time.Duration) (*tool.ToolResult, error) {
	// Create context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build command
	// Source: BashTool.ts — uses process group for cleanup
	cmd := exec.CommandContext(cmdCtx, "bash", "-c", in.Command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	// Set process group for killing entire process tree
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := &Output{
		Stdout: truncateOutput(stdout.String(), MaxOutputSize),
		Stderr: truncateOutput(stderr.String(), MaxOutputSize),
		CWD:    cwd,
	}

	// Determine exit code
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			output.TimedOut = true
			output.ExitCode = -1
			output.Stderr += fmt.Sprintf("\nCommand timed out after %s", timeout)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			output.ExitCode = exitErr.ExitCode()
		} else {
			output.ExitCode = -1
			output.Stderr += "\n" + err.Error()
		}
	}

	return &tool.ToolResult{Data: output}, nil
}

// ---------------------------------------------------------------------------
// Command wrapper + CWD tracking
// Source: bashProvider.ts:77-198 — buildExecCommand
// ---------------------------------------------------------------------------

// buildCommand wraps the user command with snapshot sourcing, session env,
// alias expansion, and CWD tracking.
//
// Source: bashProvider.ts:77-198 — buildExecCommand().
// The wrapper: source snapshot → sessionEnv → disable extglob → eval cmd → pwd tracking
func buildCommand(cmd string, snapshot *EnvSnapshot, cwdFile string) string {
	var parts []string

	// 1. Source snapshot (bashProvider.ts:161-167)
	if snapshot != nil {
		parts = append(parts, fmt.Sprintf("source %s 2>/dev/null || true", snapshot.Path))
	}

	// 2. Source session environment variables (bashProvider.ts:169-173)
	if sessionScript := SessionEnvScript(); sessionScript != "" {
		parts = append(parts, sessionScript)
	}

	// 3. Disable extended glob for security (bashProvider.ts:176-179)
	parts = append(parts, "shopt -u extglob 2>/dev/null || true")

	// 4. Execute user command via eval for alias expansion (bashProvider.ts:184)
	parts = append(parts, fmt.Sprintf("eval %q", cmd))

	// 5. Track cwd after command (bashProvider.ts:186)
	parts = append(parts, fmt.Sprintf("pwd -P >| %s", cwdFile))

	return strings.Join(parts, " && ")
}

// buildCwdFilePath generates a temp file path for CWD tracking.
// Source: bashProvider.ts:118-121 — cwdFilePath = join(tmpdir, "claude-{id}-cwd")
func buildCwdFilePath(id string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("gbot-%s-cwd", id))
}

// trackCwd reads the CWD temp file and validates the directory exists.
// Falls back to originalCwd if file is missing or directory was deleted.
//
// Source: Shell.ts:396-420 — reads cwdFilePath, calls setCwd() if changed.
// Shell.ts:221-238 — CWD recovery when directory no longer exists.
func trackCwd(cwdFile string, originalCwd string) string {
	data, err := os.ReadFile(cwdFile)
	if err != nil {
		return originalCwd
	}
	newCwd := strings.TrimSpace(string(data))
	if newCwd != "" && dirExists(newCwd) {
		return newCwd
	}
	return originalCwd // recover: Shell.ts:221-238
}

// dirExists checks if a directory exists.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isReadOnlyCommand classifies a command as read-only.
// Source: utils/permissions/bashClassifier.ts
func isReadOnlyCommand(cmd string) bool {
	readOnlyPrefixes := []string{
		"ls", "cat", "head", "tail", "find", "which", "where",
		"git status", "git log", "git diff", "git show", "git branch",
		"echo", "pwd", "whoami", "hostname", "uname",
		"wc", "sort", "uniq", "diff", "comm",
		"grep", "rg", "ag", "ack",
		"file", "stat", "du", "df",
		"env", "printenv", "set",
		"type", "command -v",
		"node --version", "npm --version", "go version",
		"python --version", "python3 --version",
	}

	trimmed := strings.TrimSpace(cmd)
	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(trimmed, prefix+" ") || trimmed == prefix {
			return true
		}
	}
	return false
}

// isDestructiveCommand detects known destructive patterns.
// Source: utils/permissions/bashClassifier.ts
func isDestructiveCommand(cmd string) bool {
	destructivePatterns := []string{
		"rm -rf /", "rm -rf /*", "rm -rf ~",
		"mkfs.", "dd if=", "> /dev/sd",
		"shutdown", "reboot", "halt",
		"init 0", "init 6",
	}

	lower := strings.ToLower(cmd)
	for _, pattern := range destructivePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func truncateOutput(s string, maxSize int) string {
	if len(s) <= maxSize {
		return s
	}
	return s[:maxSize] + "\n... [output truncated]"
}
