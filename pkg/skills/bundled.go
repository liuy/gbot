package skills

import (
	"embed"
	"path/filepath"

	"github.com/liuy/gbot/pkg/types"
)

//go:embed bundled/*/SKILL.md
var bundledSkillFS embed.FS

// RegisterBundledSkills parses and registers all embedded bundled skills.
func (r *Registry) RegisterBundledSkills() {
	for _, cmd := range parseBundledSkills() {
		r.RegisterBundledSkill(cmd)
	}
}

// parseBundledSkills parses every embedded bundled skill without touching
// registry state, so Registry.Load can seed its rebuild from the embed FS
// alone (see Load for why stale registry state must not leak in).
func parseBundledSkills() []types.SkillCommand {
	// No error branch: the go:embed directive fails the build when the
	// bundled/ tree is missing, so ReadDir cannot fail here (on a nil
	// entries list the loop below yields nil, same outcome).
	entries, _ := bundledSkillFS.ReadDir("bundled")
	var cmds []types.SkillCommand
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
			cmds = append(cmds, *cmd)
		}
	}
	return cmds
}
