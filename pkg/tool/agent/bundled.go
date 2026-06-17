package agent

import (
	"embed"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/markdown"
	"github.com/liuy/gbot/pkg/types"
)

//go:embed bundled/*.md
var bundledAgentFS embed.FS

// loadBundledAgentFiles parses all embedded bundled/*.md agent definitions.
// Mirrors skills.RegisterBundledSkills: lets agents/{executor,planner,reviewer}.md
// ship inside the binary instead of living in the project root.
//
// Returns markdownFileEntry slice so Loader.load() can route bundled agents
// through the same parse + override-resolution pipeline as user/project files.
// Source is AgentSourceBuiltIn so user/project files with the same AgentType
// can override them.
func loadBundledAgentFiles() []markdownFileEntry {
	entries, err := bundledAgentFS.ReadDir("bundled")
	if err != nil {
		return nil
	}
	var result []markdownFileEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join("bundled", entry.Name())
		content, err := bundledAgentFS.ReadFile(path)
		if err != nil {
			continue
		}
		parsed := markdown.ParseFrontmatter(string(content), path)
		result = append(result, markdownFileEntry{
			filePath:    path,
			baseDir:     "bundled",
			frontmatter: parsed.Frontmatter,
			content:     parsed.Content,
			source:      types.AgentSourceBuiltIn,
		})
	}
	return result
}
