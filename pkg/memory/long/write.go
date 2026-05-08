package long

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteMemoryFile writes a memory file with YAML frontmatter.
// TS: no direct equivalent — TS relies on LLM Write tool.
func WriteMemoryFile(memoryDir, name string, memType MemoryType, description, content string) error {
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	// Sanitize name for filename
	fileName := sanitizeFileName(name) + ".md"
	filePath := filepath.Join(memoryDir, fileName)

	fm := FormatFrontmatter(name, description, memType, content)
	if err := os.WriteFile(filePath, []byte(fm), 0o644); err != nil {
		return fmt.Errorf("write memory file: %w", err)
	}

	return nil
}

// UpdateMemoryIndex appends or updates an entry in MEMORY.md.
// Format: - [Title](file.md) — one-line hook
// Each entry should be one line, under ~150 characters.
func UpdateMemoryIndex(memoryDir, fileName, description string) error {
	entrypoint := filepath.Join(memoryDir, EntrypointName)

	// Read existing content
	data, err := os.ReadFile(entrypoint)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read MEMORY.md: %w", err)
	}

	existing := string(data)
	title := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	newLine := fmt.Sprintf("- [%s](%s) — %s", title, fileName, description)

	// Check if entry for this file already exists
	lines := strings.Split(existing, "\n")
	found := false
	for i, line := range lines {
		entry := parseIndexLine(strings.TrimSpace(line))
		if entry.FileName == fileName {
			lines[i] = newLine
			found = true
			break
		}
	}

	var result string
	if found {
		result = strings.Join(lines, "\n")
	} else {
		// Append new entry
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		result = existing + newLine + "\n"
	}

	// Truncate if needed
	trunc := TruncateEntrypoint(result)
	content := trunc.Content
	if !trunc.WasLineTruncated && !trunc.WasByteTruncated {
		content = result // Use original if no truncation needed
	}

	if err := os.WriteFile(entrypoint, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write MEMORY.md: %w", err)
	}

	return nil
}

// RemoveMemoryFile removes a memory file and its MEMORY.md entry.
func RemoveMemoryFile(memoryDir, fileName string) error {
	// Remove the file
	filePath := filepath.Join(memoryDir, fileName)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove memory file: %w", err)
	}

	// Remove from MEMORY.md index
	entrypoint := filepath.Join(memoryDir, EntrypointName)
	data, err := os.ReadFile(entrypoint)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read MEMORY.md: %w", err)
	}

	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		entry := parseIndexLine(strings.TrimSpace(line))
		if entry.FileName == fileName {
			continue // skip removed file's entry
		}
		kept = append(kept, line)
	}

	result := strings.Join(kept, "\n")
	if err := os.WriteFile(entrypoint, []byte(result), 0o644); err != nil {
		return fmt.Errorf("write MEMORY.md: %w", err)
	}

	return nil
}

// sanitizeFileName makes a name safe for use as a filename.
func sanitizeFileName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	if result == "" {
		result = "memory"
	}
	return result
}
