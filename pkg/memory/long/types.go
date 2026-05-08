// Package long implements persistent typed-memory (memdir) for gbot.
// TS source: memdir/memoryTypes.ts, memdir/memdir.ts, memdir/paths.ts
package long

import (
	"strings"
)

// MemoryType constrains memories to a closed four-type taxonomy.
// Content derivable from current project state (code patterns, architecture,
// git history, file structure) is explicitly excluded.
type MemoryType string

const (
	MemoryTypeUser      MemoryType = "user"
	MemoryTypeFeedback  MemoryType = "feedback"
	MemoryTypeProject   MemoryType = "project"
	MemoryTypeReference MemoryType = "reference"
)

// ValidMemoryTypes is the closed set of allowed memory types.
var ValidMemoryTypes = []MemoryType{MemoryTypeUser, MemoryTypeFeedback, MemoryTypeProject, MemoryTypeReference}

// MemoryFile holds a parsed memory file with YAML frontmatter metadata.
type MemoryFile struct {
	Name        string     // from frontmatter
	Description string     // from frontmatter
	Type        MemoryType // from frontmatter
	Content     string     // body after frontmatter
	FilePath    string     // absolute path on disk
}

// ParseMemoryType parses a raw string into a MemoryType.
// Invalid or missing values return false — legacy files without a type field
// keep working, files with unknown types degrade gracefully.
// TS: parseMemoryType (memoryTypes.ts:28-31)
func ParseMemoryType(raw string) (MemoryType, bool) {
	for _, t := range ValidMemoryTypes {
		if MemoryType(raw) == t {
			return t, true
		}
	}
	return "", false
}

// ParseFrontmatter parses YAML frontmatter from a memory file content.
// Expected format:
//
//	---
//	name: {{memory name}}
//	description: {{one-line description}}
//	type: {{user|feedback|project|reference}}
//	---
//
// Returns (name, description, memType, body, ok).
// TS: no direct equivalent — TS parses via gray-matter or manual split.
func ParseFrontmatter(content string) (name, description string, memType MemoryType, body string, ok bool) {
	// Must start with "---\n"
	if !strings.HasPrefix(content, "---\n") {
		return "", "", "", content, false
	}

	// Find closing "---"
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return "", "", "", content, false
	}
	fm := content[4 : 4+end]
	body = content[4+end+5:] // skip "\n---\n"
	body = strings.TrimPrefix(body, "\n")

	// Parse YAML-like key-value pairs
	for line := range strings.SplitSeq(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		before, after, ok0 := strings.Cut(line, ":")
		if !ok0 {
			continue
		}
		key := strings.TrimSpace(before)
		val := strings.TrimSpace(after)

		switch key {
		case "name":
			name = val
		case "description":
			description = val
		case "type":
			if t, valid := ParseMemoryType(val); valid {
				memType = t
			}
		}
	}

	if name == "" {
		return "", "", "", content, false
	}

	return name, description, memType, body, true
}

// FormatFrontmatter generates YAML frontmatter for a memory file.
// TS: MEMORY_FRONTMATTER_EXAMPLE (memoryTypes.ts:261-271)
func FormatFrontmatter(name, description string, memType MemoryType, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(name)
	sb.WriteString("\n")
	sb.WriteString("description: ")
	sb.WriteString(description)
	sb.WriteString("\n")
	sb.WriteString("type: ")
	sb.WriteString(string(memType))
	sb.WriteString("\n")
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	return sb.String()
}
