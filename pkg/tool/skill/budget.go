package skill

// ---------------------------------------------------------------------------
// Budget system for skill listings
// Source: src/tools/SkillTool/prompt.ts
// ---------------------------------------------------------------------------

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/types"
)

const (
	// SkillBudgetContextPercent is the fraction of context window allocated to skill listings.
	// TS: prompt.ts:21 — SKILL_BUDGET_CONTEXT_PERCENT = 0.01
	SkillBudgetContextPercent = 0.01

	// CharsPerToken converts tokens to approximate character count.
	// TS: prompt.ts:22 — CHARS_PER_TOKEN = 4
	CharsPerToken = 4

	// DefaultCharBudget is the fallback budget when context window is unknown.
	// TS: prompt.ts:23 — DEFAULT_CHAR_BUDGET = 8_000 (1% of 200k × 4)
	DefaultCharBudget = 8000

	// MaxListingDescChars is the per-entry hard cap on description length.
	// TS: prompt.ts:29 — MAX_LISTING_DESC_CHARS = 250
	MaxListingDescChars = 250

	// MinDescLength is the minimum description length before going names-only.
	// TS: prompt.ts:68 — MIN_DESC_LENGTH = 20
	MinDescLength = 20
)

// getCharBudget calculates character budget from context window tokens.
// Source: prompt.ts:31-41 — getCharBudget
func getCharBudget(contextWindowTokens int) int {
	if envBudget, _ := strconv.Atoi(os.Getenv("SLASH_COMMAND_TOOL_CHAR_BUDGET")); envBudget > 0 {
		return envBudget
	}
	if contextWindowTokens > 0 {
		return int(float64(contextWindowTokens) * CharsPerToken * SkillBudgetContextPercent)
	}
	return DefaultCharBudget
}

// getCommandDescription builds the description string for a skill.
// Source: prompt.ts:43-50 — getCommandDescription
func getCommandDescription(cmd types.SkillCommand) string {
	var desc string
	if cmd.WhenToUse != "" {
		desc = cmd.Description + " - " + cmd.WhenToUse
	} else {
		desc = cmd.Description
	}
	if utf8.RuneCountInString(desc) > MaxListingDescChars {
		runes := []rune(desc)
		truncated := string(runes[:MaxListingDescChars-1]) + "\u2026"
		return truncated
	}
	return desc
}

// formatCommandDescription formats a single skill line.
// Source: prompt.ts:52-66 — formatCommandDescription
func formatCommandDescription(cmd types.SkillCommand) string {
	return "- " + cmd.Name + ": " + getCommandDescription(cmd)
}

// stringWidth returns the display width of a string.
// Simplified: just returns rune count (no East Asian wide char handling).
func stringWidth(s string) int {
	return utf8.RuneCountInString(s)
}

// BuildSkillListing formats skills within context window budget.
// Source: prompt.ts:70-171 — formatCommandsWithinBudget
//
// Algorithm:
//  1. Try full descriptions — if total fits within budget, return all
//  2. Partition into bundled (never truncated) and rest
//  3. Calculate remaining budget after bundled skills
//  4. Fair allocation of remaining budget across non-bundled
//  5. If maxDescLen >= MIN_DESC_LENGTH: truncate descriptions
//  6. If maxDescLen < MIN_DESC_LENGTH: non-bundled go names-only
func BuildSkillListing(skills []types.SkillCommand, contextWindowTokens int) string {
	if len(skills) == 0 {
		return ""
	}

	budget := getCharBudget(contextWindowTokens)

	// Build full entries
	type entry struct {
		cmd    types.SkillCommand
		full   string
		isBundled bool
	}

	entries := make([]entry, len(skills))
	for i, cmd := range skills {
		isBundled := cmd.Source == types.SkillSourceBundled
		entries[i] = entry{cmd: cmd, full: formatCommandDescription(cmd), isBundled: isBundled}
	}

	// Calculate total with full descriptions
	totalWidth := 0
	for i, e := range entries {
		totalWidth += stringWidth(e.full)
		if i > 0 {
			totalWidth++ // newline
		}
	}

	// Phase 1: If everything fits, return all
	// Source: prompt.ts:88-90
	if totalWidth <= budget {
		lines := make([]string, len(entries))
		for i, e := range entries {
			lines[i] = e.full
		}
		return strings.Join(lines, "\n")
	}

	// Phase 2: Separate bundled (never truncated) from rest
	// Source: prompt.ts:92-102
	var bundledChars int
	var restIndices []int
	for i, e := range entries {
		if e.isBundled {
			bundledChars += stringWidth(e.full) + 1 // +1 for newline
		} else {
			restIndices = append(restIndices, i)
		}
	}

	remainingBudget := budget - bundledChars

	if len(restIndices) == 0 {
		// All bundled — return as-is
		lines := make([]string, len(entries))
		for i, e := range entries {
			lines[i] = e.full
		}
		return strings.Join(lines, "\n")
	}

	// Calculate overhead from names ("- name: " = name + 4 chars)
	// Source: prompt.ts:117-119
	restNameOverhead := 0
	for _, idx := range restIndices {
		restNameOverhead += stringWidth(entries[idx].cmd.Name) + 4 // "- : " = 4
	}
	restNameOverhead += len(restIndices) - 1 // newlines between rest entries

	availableForDescs := remainingBudget - restNameOverhead
	maxDescLen := availableForDescs / len(restIndices)

	// Source: prompt.ts:123-141 — extreme case: names only for non-bundled
	if maxDescLen < MinDescLength {
		lines := make([]string, len(entries))
		for i, e := range entries {
			if e.isBundled {
				lines[i] = e.full
			} else {
				lines[i] = "- " + e.cmd.Name
			}
		}
		return strings.Join(lines, "\n")
	}

	// Source: prompt.ts:163-170 — truncate non-bundled descriptions
	lines := make([]string, len(entries))
	for i, e := range entries {
		if e.isBundled {
			lines[i] = e.full
		} else {
			desc := getCommandDescription(e.cmd)
			if utf8.RuneCountInString(desc) > maxDescLen {
				runes := []rune(desc)
				desc = string(runes[:maxDescLen-1]) + "\u2026"
			}
			lines[i] = "- " + e.cmd.Name + ": " + desc
		}
	}
	return strings.Join(lines, "\n")
}
