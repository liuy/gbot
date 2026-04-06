// Package context assembles the system prompt context for each LLM call.
//
// Source reference: context.ts, utils/claudemd.ts
package context

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Builder assembles the system prompt context.
// Source: context.ts — builds the full system prompt from components.
type Builder struct {
	// WorkingDir is the current working directory.
	WorkingDir string

	// GBOTMDContent is the loaded GBOT.md instruction content.
	GBOTMDContent string

	// GitStatus is the injected git status information.
	GitStatus *GitStatusInfo

	// ToolPrompts are system prompt contributions from tools.
	ToolPrompts []string

	// MaxTokens is the token budget for the system prompt.
	MaxTokens int
}

// GitStatusInfo holds git repository status.
// Source: context.ts — injected into system prompt.
type GitStatusInfo struct {
	IsGit        bool
	Branch       string
	DefaultBranch string
	IsDirty      bool
}

// NewBuilder creates a new context builder.
func NewBuilder(workingDir string) *Builder {
	return &Builder{
		WorkingDir: workingDir,
		MaxTokens:  100000, // Will be dynamically calculated
	}
}

// Build assembles the full system prompt.
// Source: context.ts — the complete context assembly algorithm.
func (b *Builder) Build() (json.RawMessage, error) {
	var buf bytes.Buffer

	// 1. Base system prompt template
	buf.WriteString(b.BaseSystemPrompt())

	// 2. Platform info
	buf.WriteString(b.PlatformInfo())

	// 3. Git status
	if b.GitStatus != nil {
		buf.WriteString(b.GitStatusSection())
	}

	// 4. GBOT.md instructions
	if b.GBOTMDContent != "" {
		buf.WriteString("\n\n## Instructions\n\n")
		buf.WriteString(b.GBOTMDContent)
	}

	// 5. Tool prompts
	for _, prompt := range b.ToolPrompts {
		if prompt != "" {
			buf.WriteString("\n\n")
			buf.WriteString(prompt)
		}
	}

	encoded, err := json.Marshal(buf.String())
	if err != nil {
		return nil, fmt.Errorf("encode system prompt: %w", err)
	}
	return json.RawMessage(encoded), nil
}

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
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\n\nPlatform: %s/%s", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&buf, "\nWorking directory: %s", b.WorkingDir)

	// Detect shell
	if shell := os.Getenv("SHELL"); shell != "" {
		fmt.Fprintf(&buf, "\nShell: %s", shell)
	} else {
		buf.WriteString("\nShell: /bin/bash")
	}

	return buf.String()
}

// GitStatusSection formats git status for the system prompt.
func (b *Builder) GitStatusSection() string {
	if !b.GitStatus.IsGit {
		return "\n\nNot a git repository."
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\n\nGit branch: %s", b.GitStatus.Branch)
	if b.GitStatus.DefaultBranch != "" {
		fmt.Fprintf(&buf, "\nDefault branch: %s", b.GitStatus.DefaultBranch)
	}
	if b.GitStatus.IsDirty {
		buf.WriteString("\nWorking tree: dirty (uncommitted changes)")
	} else {
		buf.WriteString("\nWorking tree: clean")
	}
	return buf.String()
}

// LoadGitStatus loads git status for the working directory.
// Source: context.ts — git status injection.
func LoadGitStatus(workingDir string) *GitStatusInfo {
	info := &GitStatusInfo{}

	// Check if we're in a git repo
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = workingDir
	if output, err := cmd.Output(); err != nil || strings.TrimSpace(string(output)) != "true" {
		return info
	}
	info.IsGit = true

	// Get current branch
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workingDir
	if output, err := cmd.Output(); err == nil {
		info.Branch = strings.TrimSpace(string(output))
	}

	// Get default branch
	cmd = exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = workingDir
	if output, err := cmd.Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(output)), "/")
		if len(parts) > 0 {
			info.DefaultBranch = parts[len(parts)-1]
		}
	}

	// Check if dirty
	cmd = exec.Command("git", "status", "--porcelain")
	cmd.Dir = workingDir
	if output, err := cmd.Output(); err == nil {
		info.IsDirty = len(strings.TrimSpace(string(output))) > 0
	}

	return info
}

// LoadGBOTMD loads GBOT.md instructions.
// Source: utils/claudemd.ts — simplified Phase 1 version.
// Phase 1: Load single GBOT.md file (no @include, frontmatter, dedup, or rules glob).
func LoadGBOTMD(workingDir string) string {
	// Try GBOT.md at working directory root
	candidates := []string{
		filepath.Join(workingDir, "GBOT.md"),
		filepath.Join(workingDir, ".gbot", "GBOT.md"),
	}

	// Also try user home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(homeDir, ".gbot", "GBOT.md"),
		)
	}

	var contents []string
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content != "" {
			contents = append(contents, content)
		}
	}

	return strings.Join(contents, "\n\n")
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
