package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Skill preloading for sub-agents
// Source: runAgent.ts:578-646 — skill resolution + loading + injection
// ---------------------------------------------------------------------------

// SkillInfo represents a discovered skill with its content.
type SkillInfo struct {
	ID      string // skill identifier (filename without .md, e.g. "commit" or "plugin:skill")
	Content string // skill body content
	Path    string // source file path
}

// ResolveSkillNames resolves skill names using 3 strategies from TS.
// Source: runAgent.ts:945-973 — resolveSkillName()
//
// Strategy order:
// 1. Exact match on skill ID
// 2. Plugin prefix match: agentType prefix + ":" + skillName
// 3. Suffix match: skill ID ends with ":" + skillName
//
// Returns resolved SkillInfo for each successfully matched name.
// Names that don't match are silently skipped (TS logs a warning).
func ResolveSkillNames(names []string, allSkills []SkillInfo, agentType string) []SkillInfo {
	var result []SkillInfo
	for _, name := range names {
		if si := resolveOneSkillName(name, allSkills, agentType); si != nil {
			result = append(result, *si)
		}
	}
	return result
}

// resolveOneSkillName tries all 3 strategies for a single skill name.
// Source: runAgent.ts:945-973
func resolveOneSkillName(name string, allSkills []SkillInfo, agentType string) *SkillInfo {
	// Strategy 1: Exact match on ID
	// Source: runAgent.ts:950-953 — hasCommand(skillName, allSkills)
	for i := range allSkills {
		if allSkills[i].ID == name {
			return &allSkills[i]
		}
	}

	// Strategy 2: Plugin prefix match
	// Source: runAgent.ts:955-963 — prefix from agentType.split(':')[0]
	prefix, _, _ := strings.Cut(agentType, ":")
	prefixed := prefix + ":" + name
	for i := range allSkills {
		if allSkills[i].ID == prefixed {
			return &allSkills[i]
		}
	}

	// Strategy 3: Suffix match — first skill whose ID ends with ":" + name
	// Source: runAgent.ts:965-970 — allSkills.find(cmd => cmd.name.endsWith(":" + skillName))
	suffix := ":" + name
	for i := range allSkills {
		if strings.HasSuffix(allSkills[i].ID, suffix) {
			return &allSkills[i]
		}
	}

	return nil
}

// LoadSkills discovers skills from ~/.gbot/skills/*.md and <cwd>/.gbot/skills/*.md.
// Local skills override global skills with the same ID.
// Source: runAgent.ts:580 — getSkillToolCommands(cwd), simplified for filesystem-based skills.
func LoadSkills(cwd string) []SkillInfo {
	skillMap := make(map[string]SkillInfo)

	// Global skills: ~/.gbot/skills/*.md
	if home, err := os.UserHomeDir(); err == nil {
		loadSkillsFromDir(filepath.Join(home, ".gbot", "skills"), skillMap)
	}

	// Local skills: <cwd>/.gbot/skills/*.md (overrides global)
	loadSkillsFromDir(filepath.Join(cwd, ".gbot", "skills"), skillMap)

	// Convert map to slice
	result := make([]SkillInfo, 0, len(skillMap))
	for _, si := range skillMap {
		result = append(result, si)
	}
	return result
}

// loadSkillsFromDir reads all .md files from a directory and adds them to the map.
func loadSkillsFromDir(dir string, dest map[string]SkillInfo) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return // directory doesn't exist — not an error
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".md")
		fullPath := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		dest[id] = SkillInfo{
			ID:      id,
			Content: string(content),
			Path:    fullPath,
		}
	}
}

// BuildSkillMessages converts resolved skills to user messages for injection.
// Each skill becomes a user message with metadata XML tags + content.
// Source: runAgent.ts:628-645 — initialMessages.push with formatSkillLoadingMetadata
// Source: processSlashCommand.tsx:786-789 — formatSkillLoadingMetadata
func BuildSkillMessages(skills []SkillInfo) []types.Message {
	if len(skills) == 0 {
		return nil
	}
	messages := make([]types.Message, 0, len(skills))
	for _, skill := range skills {
		// Metadata XML tags — Source: processSlashCommand.tsx:786-789
		metadata := fmt.Sprintf(
			"<command-message>%s</command-message>\n<command-name>%s</command-name>\n<skill-format>true</skill-format>",
			skill.ID, skill.ID,
		)
		messages = append(messages, types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock(metadata + "\n" + skill.Content),
			},
		})
	}
	return messages
}
