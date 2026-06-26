package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/memory/long"
)

// MemoryFile holds a loaded memory file with metadata.
// Source: utils/claudemd.ts MemoryFileInfo.
type MemoryFile struct {
	Path    string
	Content string
}

// LoadMemoryFiles loads memory files from the gbot memory directory.
// Falls back to scanning .md files if MEMORY.md index doesn't exist.
// If memoryDirOverride is non-empty, it replaces the workingDir-derived path.
func LoadMemoryFiles(workingDir string, memoryDirOverride string) []MemoryFile {
	if !long.IsAutoMemoryEnabled() {
		return nil
	}

	memDir := long.GetMemoryPath(workingDir)
	if memoryDirOverride != "" {
		memDir = memoryDirOverride
	}

	// Try loading via MEMORY.md index first
	idx, err := long.LoadMemoryIndex(memDir)
	if err == nil && len(idx.Entries) > 0 {
		return loadFromIndex(memDir, idx)
	}

	// Fallback: scan all .md files (backward compat / no index yet)
	return scanMemoryFiles(memDir)
}

// FormatMemorySection formats memory files for inclusion in the system prompt.
// Source: utils/claudemd.ts getClaudeMds.
// When typed memory is enabled, uses long.BuildMemoryPrompt() for full instructions.
func FormatMemorySection(files []MemoryFile) string {
	if len(files) == 0 {
		return ""
	}

	var buf strings.Builder
	buf.WriteString("\n\n## Memory\n")
	for _, f := range files {
		relPath := f.Path
		if homeDir, err := os.UserHomeDir(); err == nil {
			if strings.HasPrefix(f.Path, homeDir) {
				relPath = "~" + f.Path[len(homeDir):]
			}
		}
		fmt.Fprintf(&buf, "\n- [%s](%s)\n", filepath.Base(f.Path), relPath)
		buf.WriteString(f.Content)
		buf.WriteString("\n")
	}

	return buf.String()
}

// FormatMemoryPrompt generates the full typed-memory system prompt section.
// Uses long.BuildMemoryPrompt which includes type taxonomy, save instructions,
// MEMORY.md content (truncated), and all behavioral guidance.
// Returns empty string if memory is disabled.
func FormatMemoryPrompt(workingDir string, memoryDirOverride string) string {
	return long.BuildMemoryPrompt(workingDir, memoryDirOverride)
}

// loadFromIndex loads memory files listed in the MEMORY.md index.
func loadFromIndex(memDir string, idx *long.MemoryIndex) []MemoryFile {
	var files []MemoryFile
	for _, entry := range idx.Entries {
		fp := filepath.Join(memDir, entry.FileName)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		files = append(files, MemoryFile{
			Path:    fp,
			Content: content,
		})
	}
	return files
}

// scanMemoryFiles scans all .md files in memory dir (fallback when no index).
func scanMemoryFiles(memDir string) []MemoryFile {
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return nil
	}

	var files []MemoryFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isMarkdownFile(name) || name == long.EntrypointName {
			continue
		}

		fp := filepath.Join(memDir, name)
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		files = append(files, MemoryFile{
			Path:    fp,
			Content: content,
		})
	}

	return files
}

// isMarkdownFile checks if a filename has a markdown extension.
func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown" || ext == ".mdx"
}
