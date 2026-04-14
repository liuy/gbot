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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	Stdout           string `json:"output"`
	Stderr           string `json:"stderr,omitempty"`
	ExitCode         int    `json:"exitCode"`
	TimedOut         bool   `json:"timed_out,omitempty"`
	BackgroundTaskID string `json:"backgroundTaskId,omitempty"`
	CWD              string `json:"cwd,omitempty"`
}

// DefaultTimeout is the default command timeout (2 minutes).
// Source: BashTool.ts — DEFAULT_TIMEOUT
const DefaultTimeout = 2 * time.Minute

// MaxTimeout is the maximum allowed timeout (10 minutes).
// Source: BashTool.ts — MAX_TIMEOUT
const MaxTimeout = 10 * time.Minute

// MaxOutputSize is the maximum output size sent to the LLM.
// Source: outputLimits.ts — BASH_MAX_OUTPUT_DEFAULT = 30_000
const MaxOutputSize = 30000

// New creates the Bash tool.
// Source: tools/BashTool/BashTool.ts
// New creates a Bash tool. If registry is nil, uses DefaultRegistry().
func New(registry *BackgroundTaskRegistry) tool.Tool {
	if registry == nil {
		registry = DefaultRegistry()
	}
	reg := registry
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
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, tctx, reg)
		},
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
			Prompt_: bashPrompt(),
			ExecuteStream_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
				return executeStream(ctx, input, tctx, onProgress, reg)
			},
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
				if out.BackgroundTaskID != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					fmt.Fprintf(&sb, "Command timed out and was moved to background (task ID: %s)", out.BackgroundTaskID)
				}
				return sb.String()
			},
	})
}

// Execute runs a bash command using the global default registry.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	return execute(ctx, input, tctx, DefaultRegistry())
}

// execute runs a bash command with the given registry.
func execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
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

// ExecuteStream runs a bash command with streaming progress events.
// Source: BashTool.tsx:826 — runShellCommand() yields progress events.
// ExecuteStream runs a bash command with streaming using the global default registry.
func ExecuteStream(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate)) (*tool.ToolResult, error) {
	return executeStream(ctx, input, tctx, onProgress, DefaultRegistry())
}

// executeStream runs a bash command with streaming and the given registry.
func executeStream(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext, onProgress func(tool.ProgressUpdate), registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
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

	// Background execution: spawn command and return immediately with task ID
	// Source: BashTool.tsx:988-1001 — run_in_background=true spawns immediately
	if in.RunInBackground {
		return spawnBackground(ctx, in, cwd, timeout, registry)
	}

	// Create streaming output with progress callback
	s := NewStreamingOutput(func(u StreamingUpdate) {
		if onProgress != nil {
			onProgress(tool.ProgressUpdate{
				Lines:      u.Lines,
				TotalLines: u.TotalLines,
				TotalBytes: u.TotalBytes,
			})
		}
	})

	// Determine if auto-backgrounding is allowed on timeout.
	// Source: BashTool.tsx:880 — shouldAutoBackground
	shouldAutoBg := isAutobackgroundingAllowed(in.Command)

	// Run the command, capturing output into StreamingOutput
	if isPTYAvailable() {
		return executePTYStreaming(ctx, in, cwd, timeout, s, shouldAutoBg, registry)
	}
	return executeNonPTYStreaming(ctx, in, cwd, timeout, s, shouldAutoBg, registry)
}

// executePTYStreaming runs a PTY command with streaming output capture.
func executePTYStreaming(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, shouldAutoBg bool, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
	if shouldAutoBg {
		return executePTYStreamingAutoBg(ctx, in, cwd, timeout, s, registry)
	}
	return executePTYStreamingSync(ctx, in, cwd, timeout, s)
}

// executePTYStreamingSync runs a PTY command synchronously (original behavior).
func executePTYStreamingSync(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput) (*tool.ToolResult, error) {
	id := fmt.Sprintf("%04x", time.Now().UnixNano()%0x10000)
	cwdFile := buildCwdFilePath(id)
	wrappedCmd := buildCommand(in.Command, nil, cwdFile)

	baseEnv := os.Environ()
	if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
		baseEnv = applyEnvOverrides(baseEnv, overrides)
	}

	exitCode, interrupted, err := ptyCommand(ctx, wrappedCmd, cwd, baseEnv,
		func(line string) {
			_, _ = s.Write([]byte(line + "\n"))
		},
		timeout,
	)

	if err != nil {
		return nil, err
	}

	s.FinalUpdate()

	newCwd := trackCwd(cwdFile, cwd)
	_ = os.Remove(cwdFile)

	return &tool.ToolResult{
		Data: &Output{
			Stdout:   s.String(),
			ExitCode: exitCode,
			TimedOut: interrupted,
			CWD:      newCwd,
		},
	}, nil
}

// executePTYStreamingAutoBg runs a PTY command with auto-background on timeout.
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout → startBackgrounding
//
// Uses MaxTimeout for ptyCommand (so it doesn't kill internally) and manages
// the actual timeout via a timer. When timeout fires, transitions to background.
func executePTYStreamingAutoBg(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
	id := fmt.Sprintf("%04x", time.Now().UnixNano()%0x10000)
	cwdFile := buildCwdFilePath(id)
	wrappedCmd := buildCommand(in.Command, nil, cwdFile)

	baseEnv := os.Environ()
	if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
		baseEnv = applyEnvOverrides(baseEnv, overrides)
	}

	// Run ptyCommand in a goroutine with MaxTimeout (don't let it kill the process).
	// Source: ShellCommand.ts:349-366 — background() clears the timeout timer.
	ptyDone := make(chan struct{})
	var ptyExitCode int
	var ptyInterrupted bool
	var ptyPID atomic.Int64

	go func() {
		defer close(ptyDone)
		ptyExitCode, ptyInterrupted, _ = ptyCommand(ctx, wrappedCmd, cwd, baseEnv,
			func(line string) {
				_, _ = s.Write([]byte(line + "\n"))
			},
			MaxTimeout, // long timeout — we manage the real timeout externally
			func(pid int) {
				ptyPID.Store(int64(pid))
			},
		)
	}()

	// Race: ptyCommand completion vs timeout timer
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ptyDone:
		// Process completed before timeout — normal path
		s.FinalUpdate()
		newCwd := trackCwd(cwdFile, cwd)
		_ = os.Remove(cwdFile)
		return &tool.ToolResult{
			Data: &Output{
				Stdout:   s.String(),
				ExitCode: ptyExitCode,
				TimedOut: ptyInterrupted,
				CWD:      newCwd,
			},
		}, nil

	case <-timer.C:
		// Timeout fired — transition to background task
		// Source: BashTool.tsx:924-963 — startBackgrounding
		return transitionToBackground(registry, in.Command, int(ptyPID.Load()), s, in, cwd, func(task *BackgroundTask) {
			<-ptyDone
			s.FinalUpdate()
			task.Complete(ptyExitCode, ptyInterrupted)
			_ = os.Remove(cwdFile)
		})
	}
}

// executeNonPTYStreaming runs a non-PTY command with streaming output capture.
func executeNonPTYStreaming(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, shouldAutoBg bool, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
	if shouldAutoBg {
		return executeNonPTYStreamingAutoBg(ctx, in, cwd, timeout, s, registry)
	}
	return executeNonPTYStreamingSync(ctx, in, cwd, timeout, s)
}

// executeNonPTYStreamingSync runs a non-PTY command synchronously (original behavior).
// When timeout fires, the process is killed and TimedOut=true is returned.
func executeNonPTYStreamingSync(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput) (*tool.ToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", in.Command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stderr bytes.Buffer
	cmd.Stdout = s
	cmd.Stderr = &stderr

	err := cmd.Run()

	s.FinalUpdate()

	exitCode := 0
	interrupted := false
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			interrupted = true
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &tool.ToolResult{
		Data: &Output{
			Stdout:   s.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
			TimedOut: interrupted,
			CWD:      cwd,
		},
	}, nil
}

// executeNonPTYStreamingAutoBg runs a non-PTY command with auto-background on timeout.
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout → startBackgrounding
//
// When timeout fires, the process transitions to a background task instead of
// being killed. The foreground result returns immediately with BackgroundTaskID set.
// The process continues running; when it exits, task.Complete is called.
func executeNonPTYStreamingAutoBg(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
	// Use a cancellable context — NOT WithTimeout — so we control timeout manually.
	// Source: ShellCommand.ts:349-366 — background() clears the timeout timer.
	taskCtx, taskCancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(taskCtx, "bash", "-c", in.Command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stderr bytes.Buffer
	cmd.Stdout = s
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		taskCancel()
		return nil, err
	}

	// Race: command completion vs timeout timer
	done := make(chan struct{})
	var waitErr error
	go func() {
		defer close(done)
		waitErr = cmd.Wait()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		// Process completed before timeout — normal path
		taskCancel()
		s.FinalUpdate()

		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
		}
		return &tool.ToolResult{
			Data: &Output{
				Stdout:   s.String(),
				Stderr:   stderr.String(),
				ExitCode: exitCode,
				CWD:      cwd,
			},
		}, nil

	case <-timer.C:
		// Timeout fired — transition to background task
		// Source: BashTool.tsx:924-963 — startBackgrounding
		return transitionToBackground(registry, in.Command, cmd.Process.Pid, s, in, cwd, func(task *BackgroundTask) {
			defer taskCancel()
			<-done
			exitCode := 0
			if waitErr != nil {
				if exitErr, ok := waitErr.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
			}
			// Flush stderr into StreamingOutput so it's not lost
			if stderr.Len() > 0 {
				_, _ = s.Write(stderr.Bytes())
			}
			s.FinalUpdate()
			task.Complete(exitCode, false)
		})
	}
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

// disallowedAutoBackgroundCommands lists commands that should NOT be auto-backgrounded.
// Source: BashTool.tsx:219-221 — DISALLOWED_AUTO_BACKGROUND_COMMANDS
var disallowedAutoBackgroundCommands = []string{"sleep"}

// isAutobackgroundingAllowed checks if a command can be automatically backgrounded on timeout.
// Source: BashTool.tsx:307-315 — isAutobackgroundingAllowed
func isAutobackgroundingAllowed(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return true
	}
	// Get first word
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return true
	}
	baseCommand := parts[0]
	for _, disallowed := range disallowedAutoBackgroundCommands {
		if baseCommand == disallowed {
			return false
		}
	}
	return true
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// spawnBackground starts a command in the background and returns immediately
// with a task ID. The command runs asynchronously; its completion is tracked
// in the BackgroundTaskRegistry.
//
// Source: BashTool.tsx:904-921 — spawnBackgroundTask()
// Source: LocalShellTask.tsx:180-252 — spawnShellTask()
func spawnBackground(ctx context.Context, in Input, cwd string, timeout time.Duration, registry *BackgroundTaskRegistry) (*tool.ToolResult, error) {
	s := NewStreamingOutput(nil)

	// Generate task ID (matches TS taskOutput.taskId generation)
	id := fmt.Sprintf("bg-%d", time.Now().UnixNano()%0x100000)

	// Create an independent context for the background task.
	// Background tasks must outlive the query context — cancelling the parent
	// (query ending) must NOT kill the background process.
	// Source: TS shellCommand lifecycle is independent of query lifecycle.
	taskCtx, taskCancel := context.WithCancel(context.Background())

	// Register the task BEFORE starting the goroutine (PID=0 initially).
	// Matches TS: spawnShellTask registers before shellCommand.background().
	task := registry.Spawn(in.Command, 0, s)
	task.CWD = cwd
	task.Description = in.Description
	task.ToolUseID = id

	// Source: LocalShellTask.tsx:221 — startStallWatchdog after registration
	task.startStallWatchdog()

	if isPTYAvailable() {
		// PTY path: run in a goroutine with PTY
		go func() {
			defer taskCancel()
			defer s.FinalUpdate()

			idHex := fmt.Sprintf("%04x", time.Now().UnixNano()%0x10000)
			cwdFile := buildCwdFilePath(idHex)
			wrappedCmd := buildCommand(in.Command, nil, cwdFile)
			baseEnv := os.Environ()
			if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
				baseEnv = applyEnvOverrides(baseEnv, overrides)
			}

			// Pipe PTY output to StreamingOutput
			r, w := io.Pipe()

			// Start PTY in a goroutine so we can get the PID.
			// Use a channel to synchronize: must wait for ptyCommand to finish
			// before calling task.Complete.
			ptyDone := make(chan struct{})
			var ptyExitCode int
			go func() {
				defer close(ptyDone)
				ptyExitCode, _, _ = ptyCommand(taskCtx, wrappedCmd, cwd, baseEnv,
					func(line string) {
						_, _ = w.Write([]byte(line + "\n"))
					},
					timeout,
					func(pid int) {
						task.mu.Lock()
						task.PID = pid
						task.mu.Unlock()
					},
				)
			}()

			// Pump PTY output to StreamingOutput
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := r.Read(buf)
					if n > 0 {
						_, _ = s.Write(buf[:n])
					}
					if err != nil {
						break
					}
				}
			}()

			// Wait for ptyCommand to finish before completing the task
			<-ptyDone
			_ = w.Close()
			_ = r.Close()
			task.Complete(ptyExitCode, taskCtx.Err() == context.Canceled)
			_ = os.Remove(cwdFile)
		}()
	} else {
		// Non-PTY path: use exec.Command
		go func() {
			defer taskCancel()
			defer s.FinalUpdate()

			cmd := exec.CommandContext(taskCtx, "bash", "-c", in.Command)
			cmd.Dir = cwd
			cmd.Env = os.Environ()
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout = s
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			if err := cmd.Start(); err != nil {
				task.Complete(-1, false)
				return
			}

			// Update PID now that we have it
			task.mu.Lock()
			task.PID = cmd.Process.Pid
			task.mu.Unlock()

			err := cmd.Wait()
			exitCode := 0
			if err != nil {
				if taskCtx.Err() == context.Canceled {
					// interrupted
				} else if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
			}

			task.Complete(exitCode, taskCtx.Err() == context.Canceled)
		}()
	}

	// Return immediately with task ID (matches TS: backgroundTaskId returned)
	return &tool.ToolResult{
		Data: &Output{
			Stdout:   fmt.Sprintf("Background task started with ID: %s\nOutput is being captured. Use the background task registry to read output.", id),
			ExitCode: 0,
			CWD:      cwd,
		},
	}, nil
}

// truncateOutput truncates output to maxSize and appends a line count notice.
// transitionToBackground spawns a background task and returns immediately.
// The completionFunc runs in a goroutine after the task is registered — it should
// wait for the process to exit and call task.Complete().
//
// Source: BashTool.tsx:924-963 — startBackgrounding
func transitionToBackground(registry *BackgroundTaskRegistry, command string, pid int, s *StreamingOutput, in Input, cwd string, completionFunc func(*BackgroundTask)) (*tool.ToolResult, error) {
	task := registry.Spawn(command, pid, s)
	task.CWD = cwd
	task.Description = in.Description
	task.startStallWatchdog()

	// Stop foreground progress updates
	s.mu.Lock()
	s.onProgress = nil
	s.mu.Unlock()

	go completionFunc(task)

	return &tool.ToolResult{
		Data: &Output{
			BackgroundTaskID: task.ID,
			CWD:              cwd,
		},
	}, nil
}

// Source: BashTool/utils.ts:133-165 (formatOutput) — keeps head + "... [N lines truncated] ..."
func truncateOutput(s string, maxSize int) string {
	if len(s) <= maxSize {
		return s
	}
	remainingLines := strings.Count(s[maxSize:], "\n") + 1
	return s[:maxSize] + fmt.Sprintf("\n\n... [%d lines truncated] ...", remainingLines)
}
