package context

import (
	"os/exec"
	"strings"
)

// GitStatusInfo holds git repository status.
// Source: context.ts — injected into system prompt.
type GitStatusInfo struct {
	IsGit         bool
	Branch        string
	DefaultBranch string
	IsDirty       bool
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

// GitStatusSection formats git status for the system prompt.
//
// Only the "is this a git repo" signal is injected — the <env> block in
// EnhanceSystemPrompt already says "Is directory a git repo: Yes/No". Branch,
// default branch, and dirty/clean state are intentionally omitted: they are
// computed once at startup and go stale the moment the user runs git checkout,
// commit, or edits a file. Sub-agents that need live state should run
// `git status` / `git branch --show-current` themselves.
func (b *Builder) GitStatusSection() string {
	return ""
}
