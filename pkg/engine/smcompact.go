package engine

// smcompact.go implements session-memory-based compact (SM-compact).
// Instead of calling the LLM for summarization, it uses the already-extracted
// session memory file as the summary — faster (no API call) and often higher quality.
//
// TS source: services/compact/sessionMemoryCompact.ts (~631 lines)
// Gracefully falls back to LLM compact when session memory is unavailable.

import (
	"os"
	"strings"

	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/types"
)

// TrySMCompact attempts compact using session memory content instead of LLM summarization.
// Returns nil if SM-compact cannot be used (no session memory, empty, or error),
// which causes the caller to fall back to LLM summarization.
// TS source: sessionMemoryCompact.ts:514-630 — trySessionMemoryCompaction.
func (c *AutoCompactor) TrySMCompact(messages []types.Message, sm *session.SessionMemory) (*CompactResult, error) {
	if sm == nil {
		return nil, nil
	}

	// Wait for any in-progress extraction to complete
	if err := sm.WaitForExtraction(); err != nil {
		c.logger.Warn("sm-compact: extraction wait failed, falling back", "error", err)
		return nil, nil
	}

	// Read session memory content directly from the notes file
	notesPath := sm.NotesPath()
	data, err := os.ReadFile(notesPath)
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	content := strings.TrimSpace(string(data))

	// Gate: check if session memory has real content (not just template)
	if session.IsSessionMemoryEmpty(content) {
		return nil, nil
	}

	if len(messages) == 0 {
		return nil, nil
	}

	beforeTokens := TokenCountWithEstimation(messages)

	// Determine how many recent messages to keep (reuse existing logic)
	shortMsgs := engineToShort(messages)
	keepFrom := c.findKeepFrom(shortMsgs)
	if keepFrom >= len(shortMsgs) || keepFrom <= 1 {
		return nil, nil
	}

	// PartialCompact is a pure in-memory function that only errors on invalid keepFrom.
	// keepFrom is already validated above (keepFrom > 1 && keepFrom < len), so this cannot fail.
	pcr, _ := c.store.PartialCompact(c.sessionID, shortMsgs, keepFrom)

	// Truncate session memory for compact (per-section caps)
	truncated := session.TruncateForCompact(content, session.DefaultConfig().MaxSectionTokens)

	// Build summary from session memory content
	summaryText := formatSMSummary(truncated)

	built := c.buildResultMessages(pcr, summaryText)
	afterTokens := TokenCountWithEstimation(built)

	c.logger.Info("sm-compact: using session memory for compact",
		"before_tokens", beforeTokens,
		"after_tokens", afterTokens,
		"before_messages", len(messages),
		"after_messages", len(built))

	return &CompactResult{
		Summary:        summaryText,
		BeforeTokens:   beforeTokens,
		BeforeMessages: len(messages),
		AfterTokens:    afterTokens,
		Messages:       built,
	}, nil
}

// formatSMSummary formats session memory content as a compact summary.
// TS source: sessionMemoryCompact.ts — createCompactionResultFromSessionMemory.
func formatSMSummary(sessionMemoryContent string) string {
	var sb strings.Builder
	sb.WriteString("The following is a summary of the conversation from session memory:\n\n")
	sb.WriteString(sessionMemoryContent)
	return sb.String()
}

