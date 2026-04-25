package agent

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Skill preloading for sub-agents
// Source: runAgent.ts:578-646 — skill resolution + loading + injection
// ---------------------------------------------------------------------------

// ResolveSkillNames resolves skill names using 3 strategies from TS.
// Source: runAgent.ts:945-973 — resolveSkillName()
//
// Strategy order:
// 1. Exact match on skill Name
// 2. Plugin prefix match: agentType prefix + ":" + skillName
// 3. Suffix match: skill Name ends with ":" + skillName
//
// Returns resolved SkillCommand for each successfully matched name.
// Names that don't match are silently skipped (TS logs a warning).
func ResolveSkillNames(names []string, allSkills []types.SkillCommand, agentType string) []types.SkillCommand {
	var result []types.SkillCommand
	for _, name := range names {
		if si := resolveOneSkillName(name, allSkills, agentType); si != nil {
			result = append(result, *si)
		}
	}
	return result
}

// resolveOneSkillName tries all 3 strategies for a single skill name.
// Source: runAgent.ts:945-973
func resolveOneSkillName(name string, allSkills []types.SkillCommand, agentType string) *types.SkillCommand {
	// Strategy 1: Exact match on Name
	// Source: runAgent.ts:950-953 — hasCommand(skillName, allSkills)
	for i := range allSkills {
		if allSkills[i].Name == name {
			return &allSkills[i]
		}
	}

	// Strategy 2: Plugin prefix match
	// Source: runAgent.ts:955-963 — prefix from agentType.split(':')[0]
	prefix, _, _ := strings.Cut(agentType, ":")
	prefixed := prefix + ":" + name
	for i := range allSkills {
		if allSkills[i].Name == prefixed {
			return &allSkills[i]
		}
	}

	// Strategy 3: Suffix match — first skill whose Name ends with ":" + name
	// Source: runAgent.ts:965-970 — allSkills.find(cmd => cmd.name.endsWith(":" + skillName))
	suffix := ":" + name
	for i := range allSkills {
		if strings.HasSuffix(allSkills[i].Name, suffix) {
			return &allSkills[i]
		}
	}

	return nil
}

// BuildSkillMessages converts resolved skills to user messages for injection.
// Each skill becomes a user message with metadata XML tags + content.
// Source: runAgent.ts:628-645 — initialMessages.push with formatSkillLoadingMetadata
// Source: processSlashCommand.tsx:786-789 — formatSkillLoadingMetadata
func BuildSkillMessages(skills []types.SkillCommand) []types.Message {
	if len(skills) == 0 {
		return nil
	}
	messages := make([]types.Message, 0, len(skills))
	for _, skill := range skills {
		// Metadata XML tags — Source: processSlashCommand.tsx:786-789
		metadata := fmt.Sprintf(
			"<command-message>%s</command-message>\n<command-name>%s</command-name>\n<skill-format>true</skill-format>",
			skill.Name, skill.Name,
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
