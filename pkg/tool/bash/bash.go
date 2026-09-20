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
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	shellescape "al.essio.dev/pkg/shellescape"

	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

func init() {
	permission.RegisterContentChecker("Bash", func(input json.RawMessage, contentRules []permission.Rule) permission.RuleAction {
		cmd := permission.ExtractBashCommand(input)
		action, _, _ := permission.CheckBashPermission(cmd, contentRules)
		return action
	})
}

// Input is the bash tool input schema.
// Source: BashTool.ts — Zod schema for bash input.
type Input struct {
	Command         string `json:"command" validate:"required"`
	Timeout         int    `json:"timeout,omitempty"` // milliseconds, default 120000
	CWD             string `json:"cwd,omitempty"`
	Description     string `json:"description,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

// Output is the bash tool output.
// Source: BashTool.ts — tool result data.
type Output struct {
	Stdout          string `json:"output"`
	Stderr          string `json:"stderr,omitempty"`
	ExitCode        int    `json:"exitCode"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	BackgroundJobID string `json:"backgroundTaskId,omitempty"`
	CWD             string `json:"cwd,omitempty"`
}

// DefaultTimeout is the default command timeout (2 minutes).
// Source: BashTool.ts — DEFAULT_TIMEOUT
const DefaultTimeout = 2 * time.Minute

// MaxTimeout is the maximum allowed timeout (1 hour).
const MaxTimeout = 1 * time.Hour

const MaxOutputSize = 30000

// New creates the Bash tool.
// Source: tools/BashTool/BashTool.ts
// New creates a Bash tool. If registry is nil, uses DefaultRegistry().
func New(registry *BackgroundJobRegistry) tool.Tool {
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
				"description": "Optional timeout in milliseconds (max 3600000). Default 120000."
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
				"description": "Run command in background. Returns immediately with job ID."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Bash",
		Aliases_:     []string{"bash", "shell", "sh"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Execute a bash command", nil
			}
			// TS renderToolUseMessage: always show the command, not the LLM description
			if in.Command != "" {
				return in.Command, nil
			}
			return "", nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeBash(ctx, input, tctx, reg)
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
			// Serial execution for safety. Parallel Bash commands (e.g. 8x
			// npx vitest run from a single LLM response) deadlock on shared
			// file locks (node_modules/.vitest). Unlike Edit/Write which
			// only serialize per-file, Bash operates on shared global state
			// (node_modules, build artifacts, lock files) where conflict
			// detection is impractical.
			return false
		},
		IsSearchOrRead_:    IsSearchOrRead,
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 30000,
		Prompt_:            bashPrompt(),
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
			if out.BackgroundJobID != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				fmt.Fprintf(&sb, "Command timed out and was moved to background (job ID: %s)", out.BackgroundJobID)
			}
			return sb.String()
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o Output
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			// Same replay concern as grep, and bash is the highest-risk tool:
			// `cat package.json` or `curl api` produces wire text that is
			// itself a JSON object and would decode into an all-zero Output.
			// CWD must count — every Output construction sets it, and
			// silent-success commands (mkdir, rm, git init) carry no other
			// non-zero field.
			if o.Stdout == "" && o.Stderr == "" && !o.TimedOut && o.BackgroundJobID == "" && o.CWD == "" {
				return nil, fmt.Errorf("bash: decoded output lacks identifying fields (not a legacy JSON result)")
			}
			return &o, nil
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*Output)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			return []types.ContentBlock{types.NewTextBlock(wireText(out))}
		},
	})
}

// leadingBlankLinesRe strips leading blank/whitespace-only lines from stdout.
// Source: BashTool.tsx:584 — stdout.replace(/^(\s*\n)+/, ”). The group needs
// at least one \n, so "  hello" (spaces, no newline) is left untouched.
var leadingBlankLinesRe = regexp.MustCompile(`^(?:\s*\n)+`)

// wireText renders the LLM-facing plain-text form.
// Source: BashTool.tsx:581-622 — mapToolResultToToolResultBlockParam:
// [processedStdout, errorMessage, backgroundInfo].filter(Boolean).join('\n').
// isImage/structuredContent/persistedOutputPath and the user/assistant
// background variants have no gbot counterpart (persistence is engine-level;
// gbot has a single background path).
func wireText(out *Output) string {
	var parts []string
	processedStdout := out.Stdout
	if out.Stdout != "" {
		processedStdout = leadingBlankLinesRe.ReplaceAllString(out.Stdout, "")
		processedStdout = strings.TrimRightFunc(processedStdout, unicode.IsSpace)
	}
	if processedStdout != "" {
		parts = append(parts, processedStdout)
	}
	errorMessage := strings.TrimSpace(out.Stderr)
	if out.TimedOut {
		// Source: BashTool.tsx:601-605 — the EOL gate checks the RAW stderr
		// (`if (stderr)`), not the trimmed message: whitespace-only stderr
		// still yields a leading newline. The tag text hangs off
		// is_error:interrupted in TS (user Esc abort); gbot has no
		// user-abort-success path, so TimedOut is the only producer.
		if out.Stderr != "" {
			errorMessage += "\n"
		}
		errorMessage += "<error>Command was aborted before completion</error>"
	}
	if errorMessage != "" {
		parts = append(parts, errorMessage)
	}
	if out.BackgroundJobID != "" {
		parts = append(parts, "Command running in background with ID: "+out.BackgroundJobID+". Poll its output with the Job tool.")
	}
	return strings.Join(parts, "\n")
}

// Execute runs a bash command using the global default registry.
func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	return executeBash(ctx, input, tctx, DefaultRegistry())
}

// executeBash is the unified entry point for bash command execution.
// It always uses StreamingOutput for output collection and reports progress
// through tctx.OnProgress.
//
// Source: BashTool.tsx:826 — runShellCommand() yields progress events.
func executeBash(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext, registry *BackgroundJobRegistry) (*tool.ToolResult, error) {
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
		timeout = min(time.Duration(in.Timeout)*time.Millisecond, MaxTimeout)
	}

	// Determine working directory
	cwd := in.CWD
	cwdFromSession := cwd == ""
	if cwdFromSession {
		if tctx != nil {
			cwd = tctx.WorkingDir
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
	}

	// Recover if the working directory no longer exists on disk — e.g.
	// `cd $(mktemp -d) && rm -rf $PWD` left the session in a deleted dir.
	// Source: Shell.ts:220-238 — fall back to the session's original dir;
	// when that is gone too, fail the command before spawning.
	if !dirExists(cwd) {
		fallback := ""
		if tctx != nil {
			fallback = tctx.OriginalWorkingDir
		}
		if dirExists(fallback) {
			// Only rewrite session state when the dead dir was the
			// session-tracked one — TS has no per-call cwd (Shell.ts:220-238
			// only ever sees the session cwd), so a one-shot override that
			// fails must not move the session.
			if cwdFromSession && tctx != nil && tctx.SetWorkingDir != nil {
				tctx.SetWorkingDir(fallback)
			}
			cwd = fallback
		} else {
			// Source: Shell.ts:234-236 — createFailedCommand message.
			return &tool.ToolResult{
				Data: &Output{
					Stderr:   fmt.Sprintf("Working directory %q no longer exists. Please restart Claude from an existing directory.", cwd),
					ExitCode: 1,
					CWD:      cwd,
				},
			}, nil
		}
	}

	// Background execution: spawn command and return immediately with job ID
	// Source: BashTool.tsx:988-1001 — run_in_background=true spawns immediately
	if in.RunInBackground {
		return spawnBackground(ctx, in, cwd, timeout, registry, tctx)
	}

	// Create streaming output with progress callback wired to tctx.OnProgress.
	// Tools that use StreamingOutput emit progress via OnProgress in ToolUseContext.
	s := NewStreamingOutput(func(u StreamingUpdate) {
		if tctx != nil && tctx.OnProgress != nil {
			tctx.OnProgress(tool.ProgressUpdate{
				Lines:      u.Lines,
				TotalLines: u.TotalLines,
				TotalBytes: u.TotalBytes,
			})
		}
	})

	// Determine if auto-backgrounding is allowed on timeout.
	// Source: BashTool.tsx:880 — shouldAutoBackground
	shouldAutoBg := isAutobackgroundingAllowed(in.Command)

	// Determine output cap: uncapped for REPL sub-tool calls, normal cap otherwise.
	outputCap := int64(MaxOutputSize)
	if tctx != nil && tctx.UncappedOutput {
		outputCap = tool.MaxUncappedOutput
	}

	// Run the command, capturing output into StreamingOutput
	if ptySupported {
		return executePTY(ctx, in, cwd, timeout, s, shouldAutoBg, registry, outputCap, tctx)
	}
	return executeNonPTY(ctx, in, cwd, timeout, s, shouldAutoBg, registry, outputCap, tctx)
}

// executePTY runs a command in PTY mode with streaming output capture.
// Source: Shell.ts:181-442 — exec() with provider.buildExecCommand + wrapSpawn.
//
// When shouldAutoBg is true and timeout fires, the command transitions to a
// background job instead of being killed.
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout → startBackgrounding
func executePTY(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, shouldAutoBg bool, registry *BackgroundJobRegistry, outputCap int64, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	wrappedCmd, cwdFile := buildCommand(in.Command, nil, true)

	baseEnv := os.Environ()
	if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
		baseEnv = applyEnvOverrides(baseEnv, overrides)
	}

	screen := tool.NewScreen(func(ev tool.ScreenEvent) {
		switch ev.Kind {
		case tool.ScreenAppend:
			_, _ = s.Write([]byte(ev.Content + "\n"))
		case tool.ScreenReplace:
			s.ReplaceLastLine(ev.Content)
		}
	})

	// Build emitAskInput callback from tctx.OnAskInput (if available).
	var emitAskInput func(string, bool) chan types.AskResponse
	if tctx != nil && tctx.OnAskInput != nil {
		onAskInput := tctx.OnAskInput
		startTime := time.Now()
		cmdTimeout := timeout
		emitAskInput = func(tail string, masked bool) chan types.AskResponse {
			remaining := cmdTimeout - time.Since(startTime)
			if remaining <= 0 {
				return nil
			}
			deadline := time.Now().Add(min(remaining, 60*time.Second))
			return onAskInput(tail, masked, deadline)
		}

	}
	if shouldAutoBg {
		return executePTYAutoBg(ctx, in, cwd, timeout, s, registry, wrappedCmd, cwdFile, baseEnv, screen, outputCap, emitAskInput, tctx)
	}
	return executePTYSync(ctx, in, cwd, timeout, s, wrappedCmd, cwdFile, baseEnv, screen, outputCap, emitAskInput, tctx)
}

// executePTYSync runs a PTY command synchronously.
// When timeout fires, the process is killed and TimedOut=true is returned.
func executePTYSync(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, wrappedCmd string, cwdFile string, baseEnv []string, screen *tool.Screen, outputCap int64, emitAskInput func(string, bool) chan types.AskResponse, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	exitCode, interrupted, err := runPTYCommand(ctx, wrappedCmd, cwd, baseEnv,
		screen,
		timeout,
		emitAskInput,
	)

	if err != nil {
		_ = os.Remove(cwdFile)
		return nil, err
	}

	s.FinalUpdate()

	stdout := s.ReadContent(outputCap)
	s.Cleanup()

	return &tool.ToolResult{
		Data: &Output{
			Stdout:   stdout,
			ExitCode: exitCode,
			TimedOut: interrupted,
			CWD:      syncCwd(cwdFile, cwd, tctx),
		},
	}, nil
}

// executePTYAutoBg runs a PTY command with auto-background on timeout.
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout → startBackgrounding
//
// Uses MaxTimeout for ptyCommand (so it doesn't kill internally) and manages
// the actual timeout via a timer. When timeout fires, transitions to background.
func executePTYAutoBg(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, registry *BackgroundJobRegistry, wrappedCmd string, cwdFile string, baseEnv []string, screen *tool.Screen, outputCap int64, emitAskInput func(string, bool) chan types.AskResponse, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Run ptyCommand in a goroutine with MaxTimeout (don't let it kill the process).
	// Source: ShellCommand.ts:349-366 — background() clears the timeout timer.
	ptyDone := make(chan struct{})
	var ptyExitCode int
	var ptyInterrupted bool
	var ptyPID atomic.Int64

	go func() {
		defer close(ptyDone)
		ptyExitCode, ptyInterrupted, _ = runPTYCommand(ctx, wrappedCmd, cwd, baseEnv,
			screen,
			MaxTimeout, // long timeout — we manage the real timeout externally
			emitAskInput,
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
		stdout := s.ReadContent(outputCap)
		s.Cleanup()
		return &tool.ToolResult{
			Data: &Output{
				Stdout:   stdout,
				ExitCode: ptyExitCode,
				TimedOut: ptyInterrupted,
				CWD:      syncCwd(cwdFile, cwd, tctx),
			},
		}, nil

	case <-timer.C:
		// Timeout fired — transition to background job
		// Source: BashTool.tsx:924-963 — startBackgrounding
		// No cwd read-back: the process is still running, so the file
		// contents are not trustworthy yet (Shell.ts:395 skips background
		// results).
		return transitionToBackground(registry, in.Command, int(ptyPID.Load()), s, in, cwd, func(job *BackgroundJob) {
			<-ptyDone
			s.FinalUpdate()
			s.Cleanup()
			// Cleanup must wait for exit: the command's pwd -P tail rewrites
			// the file when it finishes, so removing it at transition time
			// would leave a fresh copy behind. TS hangs unlink off
			// result.then() (Shell.ts:416-420), which only resolves in
			// #handleExit. Before Complete so Wait() implies the file is gone.
			_ = os.Remove(cwdFile)
			job.Complete(ptyExitCode, ptyInterrupted)
		})
	}
}

// executeNonPTY runs a command without PTY (fallback mode) with streaming output capture.
// When shouldAutoBg is true and timeout fires, the command transitions to a
// background job instead of being killed.
func executeNonPTY(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, shouldAutoBg bool, registry *BackgroundJobRegistry, outputCap int64, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	if shouldAutoBg {
		return executeNonPTYAutoBg(ctx, in, cwd, timeout, s, registry, outputCap, tctx)
	}
	return executeNonPTYSync(ctx, in, cwd, timeout, s, outputCap, tctx)
}

// executeNonPTYSync runs a non-PTY command synchronously.
// When timeout fires, the process is killed and TimedOut=true is returned.
func executeNonPTYSync(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, outputCap int64, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("command %q exceeded %s", in.Command, timeout))
	defer cancel()

	// Same buildCommand wrapping as the PTY path — without it cd would not
	// persist on Linux non-PTY spawns (bare `bash -c <cmd>` drops the
	// pwd -P tracking tail).
	wrappedCmd, cwdFile := buildCommand(in.Command, nil, true)
	cmd := exec.CommandContext(ctx, resolveShellCommand(), "-c", wrappedCmd)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	setSysProcAttrForGroup(cmd)
	// The wrapped chain forks children (eval'd command, pwd tail) that inherit
	// the stdout pipe, so killing only the shell would leave cmd.Run blocked
	// in pipe-drain until they exit. Group-kill matches TS treeKill
	// (ShellCommand.ts:337-343); bare commands previously exec-optimized into
	// the shell process and hid this.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return killProcessTree(cmd.Process.Pid)
		}
		return nil
	}

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
			_ = os.Remove(cwdFile)
			return nil, err
		}
	}

	stdout := s.ReadContent(outputCap)
	s.Cleanup()

	return &tool.ToolResult{
		Data: &Output{
			Stdout:   stdout,
			Stderr:   stderr.String(),
			ExitCode: exitCode,
			TimedOut: interrupted,
			CWD:      syncCwd(cwdFile, cwd, tctx),
		},
	}, nil
}

// executeNonPTYAutoBg runs a non-PTY command with auto-background on timeout.
// Source: BashTool.tsx:967-971 — shellCommand.onTimeout → startBackgrounding
//
// When timeout fires, the process transitions to a background job instead of
// being killed. The foreground result returns immediately with BackgroundJobID set.
// The process continues running; when it exits, job.Complete is called.
func executeNonPTYAutoBg(ctx context.Context, in Input, cwd string, timeout time.Duration, s *StreamingOutput, registry *BackgroundJobRegistry, outputCap int64, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Use a cancellable context — NOT WithTimeout — so we control timeout manually.
	// Source: ShellCommand.ts:349-366 — background() clears the timeout timer.
	taskCtx, taskCancel := context.WithCancel(context.Background())

	wrappedCmd, cwdFile := buildCommand(in.Command, nil, true)
	cmd := exec.CommandContext(taskCtx, resolveShellCommand(), "-c", wrappedCmd)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	setSysProcAttrForGroup(cmd)
	// Group-kill on cancel — same reasoning as executeNonPTYSync: the wrapped
	// chain's children inherit the output pipe and would stall cmd.Wait.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return killProcessTree(cmd.Process.Pid)
		}
		return nil
	}

	var stderr bytes.Buffer
	cmd.Stdout = s
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		taskCancel()
		_ = os.Remove(cwdFile)
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
		stdout := s.ReadContent(outputCap)
		s.Cleanup()
		return &tool.ToolResult{
			Data: &Output{
				Stdout:   stdout,
				Stderr:   stderr.String(),
				ExitCode: exitCode,
				CWD:      syncCwd(cwdFile, cwd, tctx),
			},
		}, nil

	case <-timer.C:
		// Timeout fired — transition to background job
		// Source: BashTool.tsx:924-963 — startBackgrounding
		// No cwd read-back: the process is still running and would write the
		// file later (Shell.ts:395 skips background results).
		return transitionToBackground(registry, in.Command, cmd.Process.Pid, s, in, cwd, func(job *BackgroundJob) {
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
			s.Cleanup()
			// Cleanup must wait for exit: the command's pwd -P tail rewrites
			// the file when it finishes, so removing it at transition time
			// would leave a fresh copy behind. TS hangs unlink off
			// result.then() (Shell.ts:416-420), which only resolves in
			// #handleExit. Before Complete so Wait() implies the file is gone.
			_ = os.Remove(cwdFile)
			job.Complete(exitCode, false)
		})
	}
}

// ---------------------------------------------------------------------------
// Command wrapper
// Source: bashProvider.ts:77-198 — buildExecCommand
// ---------------------------------------------------------------------------

// buildCommand wraps the user command with snapshot sourcing, session env,
// extglob disable, eval quoting, and (when trackCwd) cwd tracking.
//
// Source: bashProvider.ts:77-198 — buildExecCommand().
// The wrapper: source snapshot → sessionEnv → disable extglob → eval cmd
// [→ pwd -P >| cwdFile]
//
// trackCwd appends `pwd -P >| <cwdFile>` so the caller can read back the
// post-command cwd (cd persistence). The `&&` chain means the file is only
// written when the user command succeeded. Background spawns pass false —
// nothing would read the file back (Shell.ts:395 skips backgroundTaskId
// results), so appending would only leak temp files.
//
// Returns the wrapped command and the cwd tracking file path ("" when
// trackCwd is false or the temp file could not be created — tracking
// degrades to off rather than failing the command).
func buildCommand(cmd string, snapshot *EnvSnapshot, trackCwd bool) (string, string) {
	var parts []string

	// 0. Normalize: rewrite Windows >nul redirects (bashProvider.ts:127)
	cmd = rewriteWindowsNullRedirect(cmd)

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

	// 4. Execute user command via eval with proper quoting (bashProvider.ts:184)
	// Source: bashProvider.ts:128-153 — quoteShellCommand + shouldAddStdinRedirect
	// Single-quote wrapping preserves $VAR for eval and newlines for heredocs.
	addStdinRedirect := shouldAddStdinRedirect(cmd)
	quotedCmd := quoteShellCommand(cmd, addStdinRedirect)
	evalCmd := "eval " + quotedCmd

	// 4.5 Disable git pager so git log/show/diff don't hang waiting for input.
	// Always set GIT_PAGER=cat — harmless for non-git commands.
	evalCmd = "GIT_PAGER=cat " + evalCmd
	parts = append(parts, evalCmd)

	cwdFile := ""
	if trackCwd {
		// CreateTemp claims the name atomically, so parallel tool calls can
		// never collide on the same tracking file (TS's random 4-hex id has
		// a 1/65536 collision window instead).
		f, err := os.CreateTemp("", "gbot-*-cwd")
		if err != nil {
			return strings.Join(parts, " && "), ""
		}
		cwdFile = f.Name()
		// The shell overwrites the file via `>|`; pre-creating only reserves
		// the name. `pwd -P` matches the physical path the engine records.
		_ = f.Close()
		// Source: bashProvider.ts:186 — quote the path like TS's quote().
		parts = append(parts, "pwd -P >| "+shellescape.Quote(cwdFile))
	}

	return strings.Join(parts, " && "), cwdFile
}

// syncCwd reads back the cwd tracking file after a foreground command and
// returns the effective cwd: the new physical cwd when the command moved it,
// otherwise the spawn cwd. When the file reports a change the session state
// is updated through tctx.SetWorkingDir (nil-safe).
//
// Source: Shell.ts:385-421 — readFileSync(...).trim() → compare against the
// spawn cwd → setCwd; unlinkSync with errors ignored. A missing file (user
// command failed — the `&&` chain never reached pwd -P) simply leaves the
// spawn cwd in place; TS reaches the same outcome via its try/catch.
func syncCwd(cwdFile, spawnCwd string, tctx *tool.ToolUseContext) string {
	if cwdFile == "" {
		return spawnCwd
	}
	newCwd := ""
	if data, err := os.ReadFile(cwdFile); err == nil {
		newCwd = strings.TrimSpace(string(data))
	}
	// An empty file must not read as a cwd change; TS's setCwd would
	// realpath("") → throw → caught, achieving the same no-op.
	changed := newCwd != "" && newCwd != spawnCwd
	if changed && tctx != nil && tctx.SetWorkingDir != nil {
		tctx.SetWorkingDir(newCwd)
	}
	// Source: Shell.ts:416-420 — temp file cleanup, failure ignored (the
	// file legitimately doesn't exist when the command failed).
	_ = os.Remove(cwdFile)
	if changed {
		return newCwd
	}
	return spawnCwd
}

// dirExists reports whether path is present on disk. Pure existence probe —
// mirrors Shell.ts:222-238 using realpath() only to detect the deleted cwd,
// never to canonicalize the spawn directory.
func dirExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
	return !slices.Contains(disallowedAutoBackgroundCommands, baseCommand)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// spawnBackground starts a command in the background and returns immediately
// with a job ID. The command runs asynchronously; its completion is tracked
// in the BackgroundJobRegistry.
//
// Source: BashTool.tsx:904-921 — spawnBackgroundJob()
// Source: LocalShellTask.tsx:180-252 — spawnShellTask()
func spawnBackground(ctx context.Context, in Input, cwd string, timeout time.Duration, registry *BackgroundJobRegistry, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	s := NewStreamingOutput(nil)

	// Build emitAskInput callback from tctx.OnAskInput (if available).
	// Deadline mechanism prevents Drain from blocking forever if TUI disconnects.
	var emitAskInput func(string, bool) chan types.AskResponse
	if tctx != nil && tctx.OnAskInput != nil {
		onAskInput := tctx.OnAskInput
		startTime := time.Now()
		cmdTimeout := timeout
		emitAskInput = func(tail string, masked bool) chan types.AskResponse {
			remaining := cmdTimeout - time.Since(startTime)
			if remaining <= 0 {
				return nil
			}
			deadline := time.Now().Add(min(remaining, 60*time.Second))
			return onAskInput(tail, masked, deadline)
		}
	}

	// Create an independent context for the background job.
	// background jobs must outlive the query context — cancelling the parent
	// (query ending) must NOT kill the background process.
	// Source: TS shellCommand lifecycle is independent of query lifecycle.
	taskCtx, taskCancel := context.WithCancel(context.Background())

	// Register the job BEFORE starting the goroutine (PID=0 initially).
	// Matches TS: spawnShellTask registers before shellCommand.background().
	// The job ID is generated by registry.Spawn() — must use job.ID everywhere.
	job := registry.Spawn(in.Command, 0, s)
	job.CWD = cwd
	job.Description = in.Description
	job.ToolUseID = job.ID

	// Source: LocalShellTask.tsx:221 — startStallWatchdog after registration
	job.startStallWatchdog()

	if ptySupported {
		// PTY path: run in a goroutine with PTY
		go func() {
			defer taskCancel()
			defer s.FinalUpdate()

			// trackCwd=false: nothing reads the file back for background
			// jobs, so appending pwd -P would only leak a temp file
			// (Shell.ts:395 skips backgroundTaskId results).
			wrappedCmd, _ := buildCommand(in.Command, nil, false)
			baseEnv := os.Environ()
			if overrides := getEnvironmentOverrides(in.Command); overrides != nil {
				baseEnv = applyEnvOverrides(baseEnv, overrides)
			}

			// Start PTY in a goroutine so we can get the PID.
			// Use a channel to synchronize: must wait for ptyCommand to finish
			// before calling job.Complete.
			screen := tool.NewScreen(func(ev tool.ScreenEvent) {
				switch ev.Kind {
				case tool.ScreenAppend:
					_, _ = s.Write([]byte(ev.Content + "\n"))
				case tool.ScreenReplace:
					s.ReplaceLastLine(ev.Content)
				}
			})

			ptyDone := make(chan struct{})
			var ptyExitCode int
			go func() {
				defer close(ptyDone)
				ptyExitCode, _, _ = runPTYCommand(taskCtx, wrappedCmd, cwd, baseEnv,
					screen,
					timeout,
					emitAskInput,
					func(pid int) {
						job.mu.Lock()
						job.PID = pid
						job.mu.Unlock()
					},
				)
			}()

			// Wait for ptyCommand to finish before completing the job
			<-ptyDone
			s.Cleanup()
			job.Complete(ptyExitCode, taskCtx.Err() == context.Canceled)
		}()
	} else {
		// Non-PTY path: use exec.Command
		go func() {
			defer taskCancel()
			defer s.FinalUpdate()

			// Same wrapper as every other path (eval quoting, extglob,
			// GIT_PAGER) minus cwd tracking — see PTY branch note.
			wrappedCmd, _ := buildCommand(in.Command, nil, false)
			cmd := exec.CommandContext(taskCtx, resolveShellCommand(), "-c", wrappedCmd)
			cmd.Dir = cwd
			cmd.Env = os.Environ()
			setSysProcAttrForGroup(cmd)
			cmd.Stdout = s
			var stderr bytes.Buffer
			cmd.Stderr = &stderr

			if err := cmd.Start(); err != nil {
				job.Complete(-1, false)
				return
			}

			// Update PID now that we have it
			job.mu.Lock()
			job.PID = cmd.Process.Pid
			job.mu.Unlock()

			err := cmd.Wait()
			exitCode := 0
			if err != nil {
				if taskCtx.Err() == context.Canceled {
					// interrupted
				} else if exitErr, ok := err.(*exec.ExitError); ok {
					exitCode = exitErr.ExitCode()
				}
			}

			s.Cleanup()
			job.Complete(exitCode, taskCtx.Err() == context.Canceled)
			s.Cleanup()
		}()
	}

	// Return immediately with job ID (matches TS: backgroundTaskId returned)
	return &tool.ToolResult{
		Data: &Output{
			Stdout:   fmt.Sprintf("Background job started with ID: %s\nOutput is being captured. Use the Job tool to read output.", job.ID),
			ExitCode: 0,
			CWD:      cwd,
		},
	}, nil
}

// transitionToBackground spawns a background job and returns immediately.
// The completionFunc runs in a goroutine after the job is registered — it should
// wait for the process to exit and call job.Complete().
//
// Source: BashTool.tsx:924-963 — startBackgrounding
func transitionToBackground(registry *BackgroundJobRegistry, command string, pid int, s *StreamingOutput, in Input, cwd string, completionFunc func(*BackgroundJob)) (*tool.ToolResult, error) {
	job := registry.Spawn(command, pid, s)
	job.CWD = cwd
	job.Description = in.Description
	job.startStallWatchdog()

	// Stop foreground progress updates
	s.mu.Lock()
	s.onProgress = nil
	s.mu.Unlock()

	go completionFunc(job)

	return &tool.ToolResult{
		Data: &Output{
			BackgroundJobID: job.ID,
			CWD:             cwd,
		},
	}, nil
}
