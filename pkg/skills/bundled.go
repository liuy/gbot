package skills

import (
	"embed"
	"path/filepath"

	"github.com/liuy/gbot/pkg/types"
)

//go:embed bundled/*/SKILL.md
var bundledSkillFS embed.FS

// RegisterBundledSkills parses and registers all embedded bundled skills.
// Called by Registry.Load() after disk-based skills are loaded.
func (r *Registry) RegisterBundledSkills() {
	entries, err := bundledSkillFS.ReadDir("bundled")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join("bundled", entry.Name(), "SKILL.md")
		content, err := bundledSkillFS.ReadFile(skillPath)
		if err != nil {
			continue
		}
		cmd := ParseSkill(entry.Name(), skillPath, string(content), types.SkillSourceBundled)
		if cmd != nil {
			r.RegisterBundledSkill(*cmd)
		}
	}
}
