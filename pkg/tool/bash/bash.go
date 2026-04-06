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
	"strings"
	"syscall"
	"time"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
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
			if in.Description != "" {
				return truncate(in.Description, 80), nil
			}
			return truncate(in.Command, 80), nil
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
	})
}

// Execute runs a bash command.
// Source: BashTool.ts:call() — 1:1 port.
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

	startTime := time.Now()
	err := cmd.Run()
	_ = time.Since(startTime)

	output := &Output{
		Stdout:   truncateOutput(stdout.String(), MaxOutputSize),
		Stderr:   truncateOutput(stderr.String(), MaxOutputSize),
		CWD:      cwd,
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
