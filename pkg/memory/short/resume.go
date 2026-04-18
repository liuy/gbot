package short

import (
	"fmt"
	"log/slog"
)

// ResumeSession orchestrates the full session resume pipeline.
// 1. Loads messages (from compact boundary if present)
// 2. Filters unresolved tool uses, orphaned thinking, whitespace-only assistants
// 3. Detects interrupted turns
// 4. Restores agent/skill/todo/attribution state
//
// TS: sessionRestore.ts:409-534 (ProcessResumedConversation)
// TS: conversationRecovery.ts (full recovery flow)
func (s *Store) ResumeSession(sessionID string) (*ResumedState, []*Message, error) {
	// Phase 1: Load messages from last compact boundary (or all)
	messages, err := s.LoadPostCompactMessages(sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("load messages: %w", err)
	}

	if len(messages) == 0 {
		return &ResumedState{}, []*Message{}, nil
	}

	// Phase 2: Apply filters for API compatibility
	messages = FilterForResume(messages)

	// Phase 3: Detect interrupted turn and truncate if needed
	interrupted := DetectInterruptedTurn(messages)
	if interrupted {
		messages = TruncateInterruptedTurn(messages)
		slog.Info("resume: detected interrupted turn, truncated messages", "count", len(messages))
	}

	// Phase 4: Restore state from filtered messages
	state, _ := s.ProcessResumedConversation(sessionID, messages)

	return state, messages, nil
}

// FilterForResume applies all message filters needed for a clean resume.
// Order matters: unresolved tool uses → orphaned thinking → whitespace-only.
// TS: messages.ts:4920-4960 (filterMessagesForResume)
func FilterForResume(messages []*Message) []*Message {
	messages = FilterUnresolvedToolUses(messages)
	messages = FilterOrphanedThinking(messages)
	messages = FilterWhitespaceOnlyAssistant(messages)
	return messages
}

// DetectInterruptedTurn checks if the conversation was interrupted mid-turn.
// An interrupted turn is detected when:
// - The last message is an assistant with tool_use blocks but no corresponding tool_result
// - The last message is an assistant with only thinking blocks
// - The last message is an assistant with empty content
//
// TS: conversationRecovery.ts:250-290 (detectInterruptedTurn)
func DetectInterruptedTurn(messages []*Message) bool {
	if len(messages) == 0 {
		return false
	}

	last := messages[len(messages)-1]

	// Only assistant messages can indicate interruption
	if last.Type != "assistant" {
		return false
	}

	blocks := ParseContentBlocks(last.Content)

	// Empty content → interrupted
	if len(blocks) == 0 {
		return true
	}

	// Check for unresolved tool uses (tool_use without tool_result)
	hasToolUse := false
	for _, block := range blocks {
		if block.Type == "tool_use" {
			hasToolUse = true
			break
		}
	}

	if hasToolUse {
		// Check if there's a tool_result after this assistant
		// Since this is the LAST message, there can't be a tool_result
		return true
	}

	// Check if only thinking blocks (no text output)
	allThinking := true
	for _, block := range blocks {
		if block.Type != "thinking" && block.Type != "redacted_thinking" {
			allThinking = false
			break
		}
	}

	return allThinking
}

// TruncateInterruptedTurn removes the interrupted last turn.
// Removes the last assistant message and any preceding partial messages
// that belong to the incomplete turn.
// TS: conversationRecovery.ts:295-320 (truncateInterruptedTurn)
func TruncateInterruptedTurn(messages []*Message) []*Message {
	if len(messages) == 0 {
		return messages
	}

	// Remove trailing assistant messages (the interrupted turn)
	i := len(messages) - 1
	for i >= 0 && messages[i].Type == "assistant" {
		i--
	}

	// Keep messages up to and including the last non-assistant message
	return messages[:i+1]
}

// IsSessionResumable checks if a session has enough content to resume.
// Returns false if session has no messages or only system messages.
func (s *Store) IsSessionResumable(sessionID string) (bool, error) {
	count, err := s.CountVisibleMessages(sessionID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
