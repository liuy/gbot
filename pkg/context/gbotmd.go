package context

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadGBOTMD loads GBOT.md instructions.
// Source: utils/claudemd.ts.
// Load single GBOT.md file (no @include, frontmatter, dedup, or rules glob).
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
