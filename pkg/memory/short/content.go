package short

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// ParseContentBlocks parses the JSON content string into a slice of ContentBlock.
// Returns empty slice for empty or invalid JSON (logs warning for invalid JSON).
// TS: Various content[].type parsing logic throughout messages.ts.
func ParseContentBlocks(contentJSON string) []ContentBlock {
	if contentJSON == "" {
		return []ContentBlock{}
	}

	var blocks []ContentBlock
	if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
		// Invalid JSON - return empty slice (matches TS behavior of graceful handling)
		slog.Warn("ParseContentBlocks: invalid JSON", "error", err)
		return []ContentBlock{}
	}

	return blocks
}

// ExtractTextFromJSON extracts only text block content from the JSON,
// concatenating with newlines. Used for FTS5 indexing.
// Ignores tool_use, tool_result, thinking, and other non-text blocks.
// TS: No direct equivalent - gbot FTS5-specific.
func ExtractTextFromJSON(contentJSON string) string {
	blocks := ParseContentBlocks(contentJSON)
	if len(blocks) == 0 {
		return ""
	}

	var textParts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			textParts = append(textParts, block.Text)
		}
	}

	return strings.Join(textParts, "\n")
}

// FilterUnresolvedToolUses removes assistant messages that contain tool_use blocks
// without corresponding tool_result blocks. Truncates to the last complete user/assistant pair.
// TS: messages.ts:2795-2841 filterUnresolvedToolUses()
func FilterUnresolvedToolUses(messages []*TranscriptMessage) []*TranscriptMessage {
	// Collect all tool_use IDs and tool_result tool_use_ids
	toolUseIDs := make(map[string]bool)
	toolResultIDs := make(map[string]bool)

	for _, msg := range messages {
		if msg.Type != "user" && msg.Type != "assistant" {
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		for _, block := range blocks {
			if block.Type == "tool_use" && block.ID != "" {
				toolUseIDs[block.ID] = true
			}
			if block.Type == "tool_result" && block.ToolUseID != "" {
				toolResultIDs[block.ToolUseID] = true
			}
		}
	}

	// Find unresolved tool_use IDs (no matching tool_result)
	unresolvedIDs := make(map[string]bool)
	for id := range toolUseIDs {
		if !toolResultIDs[id] {
			unresolvedIDs[id] = true
		}
	}

	if len(unresolvedIDs) == 0 {
		return messages
	}

	// Filter out assistant messages where ALL tool_use blocks are unresolved
	filtered := make([]*TranscriptMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Type != "assistant" {
			filtered = append(filtered, msg)
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		var msgToolUseIDs []string
		for _, block := range blocks {
			if block.Type == "tool_use" && block.ID != "" {
				msgToolUseIDs = append(msgToolUseIDs, block.ID)
			}
		}

		if len(msgToolUseIDs) == 0 {
			// Assistant message with no tool_use blocks - keep it
			filtered = append(filtered, msg)
			continue
		}

		// Check if ALL tool_use blocks in this message are unresolved
		allUnresolved := true
		for _, id := range msgToolUseIDs {
			if !unresolvedIDs[id] {
				allUnresolved = false
				break
			}
		}

		// Keep message only if NOT all tool_uses are unresolved
		if !allUnresolved {
			filtered = append(filtered, msg)
		}
	}

	return filtered
}

// FilterOrphanedThinking removes assistant messages that contain only thinking blocks
// (no text, tool_use, or other content blocks) and have no sibling message with the same
// UUID containing non-thinking content. These cause "thinking blocks cannot be modified"
// API errors.
// TS: messages.ts:4991-5058 filterOrphanedThinkingOnlyMessages()
func FilterOrphanedThinking(messages []*TranscriptMessage) []*TranscriptMessage {
	// First pass: collect message UUIDs that have non-thinking content
	// Note: Go messages don't have a separate message.id field like TS, so we use UUID
	uuidsWithNonThinking := make(map[string]bool)
	for _, msg := range messages {
		if msg.Type != "assistant" {
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		hasNonThinking := false
		for _, block := range blocks {
			if block.Type != "thinking" && block.Type != "redacted_thinking" {
				hasNonThinking = true
				break
			}
		}

		if hasNonThinking && msg.UUID != "" {
			uuidsWithNonThinking[msg.UUID] = true
		}
	}

	// Second pass: filter out thinking-only messages that are truly orphaned
	filtered := make([]*TranscriptMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Type != "assistant" {
			filtered = append(filtered, msg)
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		if len(blocks) == 0 {
			filtered = append(filtered, msg)
			continue
		}

		// Check if ALL content blocks are thinking blocks
		allThinking := true
		for _, block := range blocks {
			if block.Type != "thinking" && block.Type != "redacted_thinking" {
				allThinking = false
				break
			}
		}

		if !allThinking {
			// Has non-thinking content, keep it
			filtered = append(filtered, msg)
			continue
		}

		// It's thinking-only. Keep if there's another message with same UUID
		// that has non-thinking content
		if msg.UUID != "" && uuidsWithNonThinking[msg.UUID] {
			filtered = append(filtered, msg)
			continue
		}

		// Truly orphaned - filter it out
		// In TS this logs analytics event; we skip that for Go
	}

	return filtered
}

// HasOnlyWhitespaceTextContent checks if a message's content blocks are all
// text blocks containing only whitespace characters.
// Returns false for empty content, non-text blocks, or text with actual content.
// TS: messages.ts:4835-4855 hasOnlyWhitespaceTextContent()
func HasOnlyWhitespaceTextContent(msg *TranscriptMessage) bool {
	blocks := ParseContentBlocks(msg.Content)
	if len(blocks) == 0 {
		return false
	}

	for _, block := range blocks {
		// If there's any non-text block (tool_use, thinking, etc.), the message is valid
		if block.Type != "text" {
			return false
		}
		// If there's a text block with non-whitespace content, the message is valid
		if block.Text != "" && strings.TrimSpace(block.Text) != "" {
			return false
		}
	}

	// All blocks are text blocks with only whitespace
	return true
}

// FilterWhitespaceOnlyAssistant removes assistant messages that contain only
// whitespace-only text content blocks. These cause API errors requiring
// "text content blocks must contain non-whitespace text".
// Merges adjacent user messages that result from filtering.
// TS: messages.ts:4869-4919 filterWhitespaceOnlyAssistantMessages()
func FilterWhitespaceOnlyAssistant(messages []*TranscriptMessage) []*TranscriptMessage {
	// First pass: filter out whitespace-only assistant messages
	filtered := make([]*TranscriptMessage, 0, len(messages))
	hasChanges := false

	for _, msg := range messages {
		if msg.Type != "assistant" {
			filtered = append(filtered, msg)
			continue
		}

		blocks := ParseContentBlocks(msg.Content)
		// Keep messages with empty content arrays (handled elsewhere)
		if len(blocks) == 0 {
			filtered = append(filtered, msg)
			continue
		}

		if HasOnlyWhitespaceTextContent(msg) {
			hasChanges = true
			// Skip this message (filter it out)
			continue
		}

		filtered = append(filtered, msg)
	}

	if !hasChanges {
		return messages
	}

	// Second pass: merge adjacent user messages
	// (removing assistant messages may leave adjacent user messages needing merge)
	merged := make([]*TranscriptMessage, 0, len(filtered))
	for _, msg := range filtered {
		prev := lastMessage(merged)
		if msg.Type == "user" && prev != nil && prev.Type == "user" {
			// Merge adjacent user messages
			merged[len(merged)-1] = mergeUserMessages(prev, msg)
		} else {
			merged = append(merged, msg)
		}
	}

	return merged
}

// isChainParticipant returns true if the message is part of the main conversation chain.
// Progress messages are excluded from chain building and content processing.
// TS: Implicit in various chain-building functions (progress messages are skipped).
func isChainParticipant(msg *TranscriptMessage) bool {
	return msg.Type != "progress"
}

// lastMessage returns the last message in a slice, or nil if empty.
func lastMessage(messages []*TranscriptMessage) *TranscriptMessage {
	if len(messages) == 0 {
		return nil
	}
	return messages[len(messages)-1]
}

// mergeUserMessages merges two adjacent user messages into one.
// Concatenates their content blocks. Preserves metadata from the first message.
// TS: messages.ts:2411-2449 mergeUserMessages()
func mergeUserMessages(a, b *TranscriptMessage) *TranscriptMessage {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	// Parse both contents
	blocksA := ParseContentBlocks(a.Content)
	blocksB := ParseContentBlocks(b.Content)

	// Concatenate content blocks
	mergedBlocks := append(blocksA, blocksB...)
	mergedContent, _ := json.Marshal(mergedBlocks)

	// Create merged message - preserve metadata from first message
	return &TranscriptMessage{
		Seq:              a.Seq,
		SessionID:        a.SessionID,
		UUID:             a.UUID,
		ParentUUID:       b.ParentUUID, // Use second message's parent for chain integrity
		LogicalParentUUID: b.LogicalParentUUID,
		IsSidechain:      a.IsSidechain,
		Type:             "user",
		Subtype:          a.Subtype,
		Content:          string(mergedContent),
		CreatedAt:        b.CreatedAt, // Use later timestamp
	}
}
