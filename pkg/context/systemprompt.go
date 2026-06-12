package context

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func (b *Builder) BaseSystemPrompt() string {
	return "{{SYSTEM}}"
}

// DefaultBasePrompt returns the hardcoded fallback when SYSTEM.md doesn't exist.
func DefaultBasePrompt() string {
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
- When executing commands, prefer dedicated tools (Read, Edit, Write, Glob, Grep) over Bash

{{SOUL}}`, now)
}

func (b *Builder) RuntimeInfo() string {
	host, _ := os.Hostname()
	osName := detectOS()
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	goVer := runtime.Version()
	repo := detectRepoRoot(b.WorkingDir)
	workspace := detectWorkspace()

	parts := []string{
		fmt.Sprintf("host=%s", host),
		fmt.Sprintf("os=%s", osName),
		fmt.Sprintf("go=%s", goVer),
		fmt.Sprintf("shell=%s", shell),
	}
	if repo != "" {
		parts = append(parts, fmt.Sprintf("repo=%s", repo))
	}
	if workspace != "" {
		parts = append(parts, fmt.Sprintf("workspace=%s", workspace))
	}
	parts = append(parts, "model={{MODEL}}")

	return "\n\n# Environment\n\nRuntime: " + strings.Join(parts, " | ")
}

func detectOS() string {
	arch := runtime.GOARCH
	// Try /etc/os-release (standard Linux)
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name := strings.Trim(after, `"`)
				return fmt.Sprintf("%s (%s)", name, arch)
			}
		}
	}
	// Try sw_vers on macOS
	if out, err := exec.Command("sw_vers", "-productVersion").Output(); err == nil {
		return fmt.Sprintf("macOS %s (%s)", strings.TrimSpace(string(out)), arch)
	}
	// Fallback
	return fmt.Sprintf("%s (%s)", runtime.GOOS, arch)
}

func detectRepoRoot(workingDir string) string {
	out, err := exec.Command("git", "-C", workingDir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".gbot")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
