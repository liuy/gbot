package context

import (
	"fmt"
	"os"
	"path/filepath"
	"cmp"
	"slices"
	"strings"
)

// MemoryFile holds a loaded memory file with metadata.
// Source: utils/claudemd.ts MemoryFileInfo — simplified Phase 1 version.
type MemoryFile struct {
	Path    string
	Content string
}

// LoadMemoryFiles loads memory files from the gbot memory directory.
// Source: memdir/memdir.ts loadMemoryPrompt — simplified Phase 1 version.
// Phase 1: Load all .md files from ~/.gbot/memory/ (no @include, no team memory, no frontmatter).
func LoadMemoryFiles(workingDir string) []MemoryFile {
	dirs := memoryDirs(workingDir)
	seen := make(map[string]bool)
	var files []MemoryFile

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		// Sort for deterministic order
		slices.SortFunc(entries, func(a, b os.DirEntry) int { return cmp.Compare(a.Name(), b.Name()) })

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isMarkdownFile(name) {
				continue
			}

			fullPath := filepath.Join(dir, name)
			absPath, err := filepath.Abs(fullPath)
			if err != nil {
				absPath = fullPath
			}

			if seen[absPath] {
				continue
			}
			seen[absPath] = true

			data, err := os.ReadFile(fullPath)
			if err != nil {
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			files = append(files, MemoryFile{
				Path:    fullPath,
				Content: content,
			})
		}
	}

	return files
}

// FormatMemorySection formats memory files for inclusion in the system prompt.
// Source: utils/claudemd.ts getClaudeMds — simplified Phase 1 version.
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

// memoryDirs returns candidate memory directories in priority order.
func memoryDirs(workingDir string) []string {
	dirs := []string{
		filepath.Join(workingDir, ".gbot", "memory"),
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(homeDir, ".gbot", "memory"))
	}

	return dirs
}

// isMarkdownFile checks if a filename has a markdown extension.
func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown" || ext == ".mdx"
}
