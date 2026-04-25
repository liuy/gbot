package skills

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Shell command execution in skill content
// Source: src/utils/promptShellExecution.ts
// ---------------------------------------------------------------------------

// ShellCommandError represents a shell command execution failure.
// Source: promptShellExecution.ts — MalformedCommandError
type ShellCommandError struct {
	Pattern string
	Message string
}

func (e *ShellCommandError) Error() string {
	return fmt.Sprintf("shell command failed for pattern %q: %s", e.Pattern, e.Message)
}

// Block pattern: ```! command ```
// Source: promptShellExecution.ts:49
var blockPattern = regexp.MustCompile("(?s)```!\\s*\n?(.*?)\n?```")

// Inline pattern: !`command`
// Requires whitespace or start-of-line before !
// Source: promptShellExecution.ts:56 — (?<=^|\s)!`([^`]+)`
var inlinePattern = regexp.MustCompile(`(?m)(?:^|\s)!` + "`" + `([^` + "`" + `]+)` + "`")

// ShellExecutor executes shell commands via a tool interface.
// This decouples shell.go from the concrete bash tool implementation.
type ShellExecutor interface {
	// Execute runs a shell command and returns stdout+stderr output.
	// Returns an error if the command fails.
	Execute(ctx *types.ToolUseContext, command string) (string, error)
}

// ExecuteShellBlocks finds and executes !`backtick` blocks and ```! code blocks
// in skill content. Returns modified content with block outputs substituted in.
//
// Security: MCP skills should set isMCP=true to skip shell execution entirely.
// Each command execution is gated by the ShellExecutor's permission check.
//
// Source: promptShellExecution.ts:69-143 — executeShellCommandsInPrompt
func ExecuteShellBlocks(content string, executor ShellExecutor, toolCtx *types.ToolUseContext, isMCP bool) (string, error) {
	if isMCP {
		// Security: MCP skills are remote and untrusted — never execute inline
		// shell commands from their markdown body.
		// Source: loadSkillsDir.ts:371-373
		return content, nil
	}

	result := content

	// Collect all matches (block + inline)
	type matchInfo struct {
		pattern string
		command string
	}

	var matches []matchInfo

	// Block matches: ```! command ```
	blockMatches := blockPattern.FindAllStringSubmatchIndex(content, -1)
	for _, m := range blockMatches {
		if len(m) >= 4 {
			cmd := strings.TrimSpace(content[m[2]:m[3]])
			if cmd != "" {
				fullMatch := content[m[0]:m[1]]
				matches = append(matches, matchInfo{pattern: fullMatch, command: cmd})
			}
		}
	}

	// Inline matches: !`command` — only scan if content contains !`
	// Source: promptShellExecution.ts:90 — performance gate
	if strings.Contains(content, "!`") {
		inlineMatches := inlinePattern.FindAllStringSubmatchIndex(content, -1)
		for _, m := range inlineMatches {
			if len(m) >= 4 {
				cmd := strings.TrimSpace(content[m[2]:m[3]])
				if cmd != "" {
					fullMatch := content[m[0]:m[1]]
					matches = append(matches, matchInfo{pattern: fullMatch, command: cmd})
				}
			}
		}
	}

	if len(matches) == 0 {
		return content, nil
	}

	// Execute all commands in parallel with bounded concurrency.
	// Source: promptShellExecution.ts:92 — Promise.all
	type execResult struct {
		pattern string
		output  string
		err     error
	}

	results := make([]execResult, len(matches))
	var wg sync.WaitGroup
	const maxWorkers = 10
	sem := make(chan struct{}, maxWorkers)

	for i, m := range matches {
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int, mi matchInfo) {
			defer func() { <-sem; wg.Done() }()
			output, err := executor.Execute(toolCtx, mi.command)
			results[idx] = execResult{pattern: mi.pattern, output: output, err: err}
		}(i, m)
	}
	wg.Wait()

	// Apply replacements
	for _, r := range results {
		if r.err != nil {
			slog.Error("skills: shell command failed",
				"pattern", r.pattern,
				"error", r.err,
			)
			return "", &ShellCommandError{
				Pattern: r.pattern,
				Message: r.err.Error(),
			}
		}
		// Use function-based replacement to avoid $ expansion issues
		// Source: promptShellExecution.ts:131 — result.replace(match[0], () => output)
		result = strings.Replace(result, r.pattern, r.output, 1)
	}

	return result, nil
}
