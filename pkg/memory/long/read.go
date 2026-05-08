package long

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MemoryIndex holds the parsed MEMORY.md index.
type MemoryIndex struct {
	Entries []IndexEntry
	Raw     string // original MEMORY.md content
}

// IndexEntry represents a single line in MEMORY.md index.
// Format: - [Title](file.md) — one-line hook
type IndexEntry struct {
	FileName    string // e.g., "user_role.md"
	Title       string // e.g., "User Profile"
	Description string // one-line hook after —
}

// LoadMemoryIndex reads and parses MEMORY.md.
// Returns empty index (not error) if file doesn't exist.
// TS: no direct equivalent — TS reads MEMORY.md as raw content into prompt.
func LoadMemoryIndex(memoryDir string) (*MemoryIndex, error) {
	entrypoint := filepath.Join(memoryDir, EntrypointName)
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		if os.IsNotExist(err) {
			return &MemoryIndex{}, nil
		}
		return nil, fmt.Errorf("read MEMORY.md: %w", err)
	}

	raw := string(data)
	idx := &MemoryIndex{Raw: raw}

	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		entry := parseIndexLine(line)
		if entry.FileName != "" {
			idx.Entries = append(idx.Entries, entry)
		}
	}

	return idx, nil
}

// parseIndexLine parses "- [Title](file.md) — one-line hook"
func parseIndexLine(line string) IndexEntry {
	// Extract title between [ and ]
	titleStart := strings.Index(line, "[")
	titleEnd := strings.Index(line, "]")
	if titleStart < 0 || titleEnd < 0 || titleEnd <= titleStart {
		return IndexEntry{}
	}
	title := line[titleStart+1 : titleEnd]

	// Extract filename between ( and )
	parenStart := strings.Index(line, "(")
	parenEnd := strings.Index(line, ")")
	if parenStart < 0 || parenEnd < 0 || parenEnd <= parenStart {
		return IndexEntry{}
	}
	fileName := line[parenStart+1 : parenEnd]

	// Extract description after — (em dash or --)
	desc := ""
	if _, after, ok := strings.Cut(line, " — "); ok {
		desc = strings.TrimSpace(after)
	} else if _, after, ok := strings.Cut(line, " -- "); ok {
		desc = strings.TrimSpace(after)
	}

	return IndexEntry{
		FileName:    fileName,
		Title:       title,
		Description: desc,
	}
}

// LoadMemoryFile reads a single memory file (frontmatter + body).
func LoadMemoryFile(filePath string) (*MemoryFile, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read memory file %s: %w", filePath, err)
	}

	content := string(data)
	name, description, memType, body, ok := ParseFrontmatter(content)
	if !ok {
		// Legacy file without frontmatter — treat entire content as body
		name = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
		description = ""
		memType = MemoryTypeProject
		body = content
	}

	return &MemoryFile{
		Name:        name,
		Description: description,
		Type:        memType,
		Content:     body,
		FilePath:    filePath,
	}, nil
}

// LoadAllMemoryFiles reads all .md files (excluding MEMORY.md) from the
// memory directory, sorted by filename for deterministic order.
func LoadAllMemoryFiles(memoryDir string) ([]MemoryFile, error) {
	entries, err := os.ReadDir(memoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read memory dir %s: %w", memoryDir, err)
	}

	// Sort for deterministic order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var files []MemoryFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isMarkdownFile(name) || name == EntrypointName {
			continue
		}

		fullPath := filepath.Join(memoryDir, name)
		mf, err := LoadMemoryFile(fullPath)
		if err != nil {
			continue // skip unreadable files
		}
		files = append(files, *mf)
	}

	return files, nil
}

// isMarkdownFile checks if a filename has a markdown extension.
func isMarkdownFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown" || ext == ".mdx"
}
