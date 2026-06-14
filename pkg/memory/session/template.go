package session

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// DefaultTemplate is the 10-section session memory template.
// TS source: prompts.ts:11-41 — DEFAULT_SESSION_MEMORY_TEMPLATE.
var DefaultTemplate = `# Session Notes

## Session Title
[Brief, descriptive title]

## Current State
[What are we working on right now? What's the status?]

## Task specification
[What is the user trying to accomplish? What are the requirements and constraints?]

## Files and Functions
[Key files, functions, and their roles in the current task]

## Workflow
[Step-by-step process being followed, current step, and next steps]

## Errors & Corrections
[Any errors encountered and how they were resolved]

## Codebase and System Documentation
[Relevant documentation, architecture, and design patterns discovered during work]

## Learnings
[Key insights and lessons learned during this session]

## Key results
[Important results, decisions, and their rationale]

## Worklog
- [Timestamp] [Brief description of what was done]
`

// DefaultUpdatePrompt is the prompt template for session memory extraction.
// TS source: prompts.ts — loadSessionMemoryPrompt (custom or default).
var DefaultUpdatePrompt = `You are a session memory manager. Your task is to update the session notes file based on the conversation so far.

## Instructions

1. Read the current session notes below.
2. Update the notes to reflect the current state of the conversation.
3. Preserve the section structure — keep all ## headers.
4. Be concise but comprehensive — capture key information that would be needed to continue this work.
5. Do NOT remove important context — only condense or update existing information.
6. Use the Edit tool to update the file at the specified path.

## Section Guidelines

- **Session Title**: One-line description of what this session is about.
- **Current State**: What's happening right now? What's the status of the current task?
- **Task specification**: What is the user trying to accomplish? Requirements and constraints.
- **Files and Functions**: Key files, functions, and their roles. Keep this focused on what's relevant.
- **Workflow**: What steps have been completed, what's the current step, and what's next.
- **Errors & Corrections**: Any errors encountered and their resolutions.
- **Codebase and System Documentation**: Relevant documentation and architecture discovered.
- **Learnings**: Key insights that would be useful for future reference.
- **Key results**: Important outcomes, decisions, and their rationale.
- **Worklog**: Brief chronological entries of what was done.

{{sectionReminders}}

## Current Notes

{{currentNotes}}

## File Path

Update the file at: {{notesPath}}`

// SectionSizes holds per-section token estimates.
type SectionSizes struct {
	Header string
	Tokens int
}

// analyzeSectionSizes splits content by ## headers and estimates tokens per section.
// TS source: prompts.ts:134-159 — analyzeSectionSizes.
func analyzeSectionSizes(content string) []SectionSizes {
	var sizes []SectionSizes
	// Split by ## headers
	lines := strings.Split(content, "\n")
	var currentHeader string
	var currentLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if currentHeader != "" || len(currentLines) > 0 {
				text := strings.Join(currentLines, "\n")
				sizes = append(sizes, SectionSizes{
					Header: currentHeader,
					Tokens: types.EstimateTokens(text),
				})
			}
			currentHeader = strings.TrimPrefix(line, "## ")
			currentLines = nil
		} else {
			currentLines = append(currentLines, line)
		}
	}
	// Last section
	if currentHeader != "" || len(currentLines) > 0 {
		text := strings.Join(currentLines, "\n")
		sizes = append(sizes, SectionSizes{
			Header: currentHeader,
			Tokens: types.EstimateTokens(text),
		})
	}

	return sizes
}

// generateSectionReminders produces warnings for oversized sections or total token count.
// TS source: prompts.ts:164-196 — generateSectionReminders.
func generateSectionReminders(sizes []SectionSizes, maxSection int, maxTotal int) string {
	totalTokens := 0
	var oversized []SectionSizes
	for _, s := range sizes {
		totalTokens += s.Tokens
		if s.Tokens > maxSection {
			oversized = append(oversized, s)
		}
	}

	if totalTokens <= maxTotal && len(oversized) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "## Size Warnings")
	lines = append(lines, "")

	if totalTokens > maxTotal {
		lines = append(lines, fmt.Sprintf(
			"CRITICAL: Total session memory is ~%d tokens, exceeding the %d token budget. "+
				"You MUST condense the notes to fit within the budget. Prioritize recent and actionable information.",
			totalTokens, maxTotal))
		lines = append(lines, "")
	}

	if len(oversized) > 0 {
		lines = append(lines, "The following sections exceed the per-section token budget and should be condensed:")
		for _, s := range oversized {
			lines = append(lines, fmt.Sprintf("- **%s**: ~%d tokens (budget: %d)", s.Header, s.Tokens, maxSection))
		}
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// variablePattern matches {{variable}} placeholders in prompt templates.
// TS source: prompts.ts — substituteVariables.
var variablePattern = regexp.MustCompile(`\{\{(\w+)\}\}`)

// substituteVariables replaces {{key}} placeholders with values.
// TS source: prompts.ts:201-213 — substituteVariables.
func substituteVariables(template string, vars map[string]string) string {
	return variablePattern.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.Trim(match, "{}")
		if val, ok := vars[key]; ok {
			return val
		}
		return match
	})
}

// BuildUpdatePrompt builds the session memory extraction prompt.
// TS source: prompts.ts:226-247 — buildSessionMemoryUpdatePrompt.
func BuildUpdatePrompt(currentNotes, notesPath string, cfg Config) string {
	sizes := analyzeSectionSizes(currentNotes)
	reminders := generateSectionReminders(sizes, cfg.MaxSectionTokens, cfg.MaxTotalTokens)

	vars := map[string]string{
		"currentNotes":     currentNotes,
		"notesPath":        notesPath,
		"sectionReminders": reminders,
	}

	return substituteVariables(DefaultUpdatePrompt, vars)
}

// IsSessionMemoryEmpty checks if the content matches the template (no real data).
// TS source: prompts.ts:220 — isSessionMemoryEmpty.
// Uses strict string equality against the template (after trimming), matching TS behavior.
func IsSessionMemoryEmpty(content string) bool {
	if content == "" {
		return true
	}
	return strings.TrimSpace(content) == strings.TrimSpace(DefaultTemplate)
}

// TruncateForCompact truncates oversized sections for use in compact messages.
// TS source: prompts.ts:256-296 — truncateSessionMemoryForCompact.
func TruncateForCompact(content string, maxSectionTokens int) string {
	lines := strings.Split(content, "\n")

	// Split by ## headers into sections
	type section struct {
		header string
		lines  []string
	}
	var sections []section
	var current *section

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if current != nil {
				sections = append(sections, *current)
			}
			current = &section{header: line}
		} else if current != nil {
			current.lines = append(current.lines, line)
		} else {
			// Content before any header — create unnamed section
			current = &section{}
			current.lines = append(current.lines, line)
		}
	}
	if current != nil {
		sections = append(sections, *current)
	}

	// Truncate each section at maxChars boundary (tokens * ~4 chars)
	maxChars := maxSectionTokens * 4
	truncationMarker := "\n[... section truncated for length ...]"

	var result []string
	for _, sec := range sections {
		if sec.header != "" {
			result = append(result, sec.header)
		}
		secText := strings.Join(sec.lines, "\n")
		if len(secText) <= maxChars {
			result = append(result, sec.lines...)
		} else {
			// Truncate at last newline before maxChars
			cutAt := strings.LastIndex(secText[:maxChars], "\n")
			if cutAt <= 0 {
				cutAt = maxChars
			}
			result = append(result, secText[:cutAt]+truncationMarker)
		}
	}

	return strings.Join(result, "\n")
}
