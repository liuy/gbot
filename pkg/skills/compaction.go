package skills

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Compaction protection for invoked skills
// Source: src/services/compact/compact.ts:129-130 — constants
// Source: src/services/compact/compact.ts:1494-1534 — createSkillAttachmentIfNeeded
// ---------------------------------------------------------------------------

const (
	// PostCompactMaxTokensPerSkill limits each skill's content in the post-compact attachment.
	// TS: compact.ts:129 — POST_COMPACT_MAX_TOKENS_PER_SKILL = 5_000
	PostCompactMaxTokensPerSkill = 5000

	// PostCompactSkillsTokenBudget is the total token budget for all skills in the attachment.
	// TS: compact.ts:130 — POST_COMPACT_SKILLS_TOKEN_BUDGET = 25_000
	PostCompactSkillsTokenBudget = 25000
)

// AddInvokedSkill records a skill invocation for compaction protection.
// Key format: "${agentID}:${skillName}" — scoped per agent.
// Source: state.ts:1510
func (r *Registry) AddInvokedSkill(skillName, skillPath, content, agentID string) {
	key := agentID + ":" + skillName
	info := types.InvokedSkillInfo{
		SkillName: skillName,
		SkillPath: skillPath,
		Content:   content,
		InvokedAt: time.Now(),
		AgentID:   agentID,
	}
	r.mu.Lock()
	r.invokedSkills[key] = info
	r.mu.Unlock()
	slog.Debug("skills: recorded invoked skill", "name", skillName, "agent", agentID)
}

// GetInvokedSkillsForAgent returns all invoked skills for an agent.
// Source: compact.ts:1494-1534 — used during compaction to create skill attachment.
func (r *Registry) GetInvokedSkillsForAgent(agentID string) []types.InvokedSkillInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []types.InvokedSkillInfo
	for _, info := range r.invokedSkills {
		if info.AgentID == agentID {
			result = append(result, info)
		}
	}
	// Sort by invocation time for deterministic output
	sort.Slice(result, func(i, j int) bool {
		return result[i].InvokedAt.Before(result[j].InvokedAt)
	})
	return result
}

// ClearInvokedSkillsForAgent removes invoked skills for a completed fork agent.
// Source: executeForkedSkill finally block
func (r *Registry) ClearInvokedSkillsForAgent(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, info := range r.invokedSkills {
		if info.AgentID == agentID {
			delete(r.invokedSkills, key)
		}
	}
	slog.Debug("skills: cleared invoked skills for agent", "agent", agentID)
}

// CreateSkillAttachment builds a compaction-safe attachment from invoked skills.
// Applies token budget per skill and total budget.
// Source: compact.ts:1494-1534
func (r *Registry) CreateSkillAttachment(agentID string, maxTokensPerSkill, totalBudget int) string {
	skills := r.GetInvokedSkillsForAgent(agentID)
	if len(skills) == 0 {
		return ""
	}

	charsPerToken := 4
	maxCharsPerSkill := maxTokensPerSkill * charsPerToken
	totalCharsBudget := totalBudget * charsPerToken

	var sections []string
	usedChars := 0

	for _, info := range skills {
		content := info.Content
		if len(content) > maxCharsPerSkill {
			content = content[:maxCharsPerSkill] + "\n[...truncated]"
		}

		section := fmt.Sprintf("### Skill: %s\n%s", info.SkillName, content)

		if usedChars+len(section) > totalCharsBudget {
			// Budget exceeded — stop adding
			break
		}

		sections = append(sections, section)
		usedChars += len(section)
	}

	if len(sections) == 0 {
		return ""
	}

	return fmt.Sprintf("## Previously invoked skills\n\n%s",
		joinSections(sections))
}

// CleanupActivatedSkills removes expired activation records.
// Prevent unbounded growth in long sessions.
func (r *Registry) CleanupActivatedSkills(maxAge time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear expired invoked skills
	for key, info := range r.invokedSkills {
		if time.Since(info.InvokedAt) > maxAge {
			delete(r.invokedSkills, key)
		}
	}

	// Clear activatedNames — they'll be re-activated on next file access.
	// Without this, activatedNames grows unboundedly in long sessions.
	r.activatedNames = make(map[string]bool)
}

// joinSections joins skill sections with double newlines.
func joinSections(sections []string) string {
	var buf strings.Builder
	for i, s := range sections {
		if i > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(s)
	}
	return buf.String()
}
