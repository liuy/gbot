package context

import (
	"fmt"
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
func (b *Builder) GitStatusSection() string {
	if !b.GitStatus.IsGit {
		return "\n\nNot a git repository."
	}
	var buf strings.Builder
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
