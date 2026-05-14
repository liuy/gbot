package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	KeyClaudeMd        = "claudeMd"
	KeyProjectClaudeMd = "projectClaudeMd"
	KeyCurrentDate     = "currentDate"
)

// LoadContextFiles reads AGENTS.md and CLAUDE.md from user-global (~/.gbot/)
// and project paths (CWD to root walk).
// Returns a map matching TS getUserContext() structure.
func LoadContextFiles(workingDir string) map[string]string {
	result := make(map[string]string)

	// User-global: ~/.gbot/AGENTS.md and ~/.gbot/CLAUDE.md
	if homeDir, err := os.UserHomeDir(); err == nil {
		var globalParts []string
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(homeDir, ".gbot", name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content != "" {
				globalParts = append(globalParts, fmt.Sprintf("Contents of %s:\n\n%s", p, content))
			}
		}
		if len(globalParts) > 0 {
			result[KeyClaudeMd] = strings.Join(globalParts, "\n\n")
		}
	}

	// Project: walk from workingDir upward to filesystem root
	type dirContent struct {
		depth   int
		content string
	}
	var projectParts []dirContent

	dir := workingDir
	depth := 0
	for {
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			p := filepath.Join(dir, name)
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content != "" {
				projectParts = append(projectParts, dirContent{
					depth:   depth,
					content: fmt.Sprintf("Contents of %s:\n\n%s", p, content),
				})
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		depth++
	}

	if len(projectParts) > 0 {
		// Sort by depth descending: root first (highest depth), CWD last (depth 0 = highest priority)
		sort.Slice(projectParts, func(i, j int) bool {
			return projectParts[i].depth > projectParts[j].depth
		})
		var parts []string
		for _, pc := range projectParts {
			parts = append(parts, pc.content)
		}
		result[KeyProjectClaudeMd] = strings.Join(parts, "\n\n")
	}

	return result
}

// BuildPrependUserContext builds the <system-reminder> wrapped string matching
// TS prependUserContext format. Returns "" if contextMap is empty.
func BuildPrependUserContext(contextMap map[string]string) string {
	if len(contextMap) == 0 {
		return ""
	}

	var sections []string
	// Fixed key order: claudeMd, projectClaudeMd, currentDate
	for _, key := range []string{KeyClaudeMd, KeyProjectClaudeMd, KeyCurrentDate} {
		if value, ok := contextMap[key]; ok {
			sections = append(sections, fmt.Sprintf("# %s\n%s", key, value))
		}
	}

	return fmt.Sprintf(
		"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n%s\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>",
		strings.Join(sections, "\n"),
	)
}
