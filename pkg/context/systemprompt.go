package context

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"
)

// BaseSystemPrompt returns the base system prompt template.
// Source: utils/systemPrompt.ts — the main system prompt.
func (b *Builder) BaseSystemPrompt() string {
	now := time.Now().Format("2006-01-02")
	return fmt.Sprintf(`You are gbot, an interactive AI coding assistant. You help users with software engineering tasks.

Current date: %s

You can:
- Read and write files
- Execute shell commands
- Search codebases
- Answer questions about code

Guidelines:
- Use tools to accomplish tasks rather than guessing
- Read files before modifying them
- Prefer editing existing files over creating new ones
- Be concise in your responses
- When executing commands, prefer dedicated tools (Read, Edit, Write, Glob, Grep) over Bash`, now)
}

// PlatformInfo returns platform information for the system prompt.
// Source: context.ts — platform injection.
func (b *Builder) PlatformInfo() string {
	var result string
	result = fmt.Sprintf("\n\nPlatform: %s/%s", runtime.GOOS, runtime.GOARCH)
	result += fmt.Sprintf("\nWorking directory: %s", b.WorkingDir)

	// Detect shell
	if shell := os.Getenv("SHELL"); shell != "" {
		result += fmt.Sprintf("\nShell: %s", shell)
	} else {
		result += "\nShell: /bin/bash"
	}

	return result
}

// EscapeJSONString escapes a string for JSON embedding.
// Uses json.Marshal to get proper JSON escaping, then strips surrounding quotes.
func EscapeJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	// json.Marshal wraps in quotes; strip them
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		return string(b[1 : len(b)-1])
	}
	return string(b)
}
