package engine

import (
	"encoding/json"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// CreateUserMessage creates a user message from a text string.
// Source: utils/messages.ts — createUserMessage()
func CreateUserMessage(text string) types.Message {
	return types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock(text),
		},
		Timestamp: time.Now(),
	}
}

// CreateAssistantMessage creates an assistant message with text content.
// Source: utils/messages.ts — used in synthetic message construction.
func CreateAssistantMessage(text string) types.Message {
	return types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock(text),
		},
		Timestamp: time.Now(),
	}
}

// CreateToolResultMessage creates a user message containing tool result blocks.
// Source: StreamingToolExecutor.ts — createUserMessage with tool_result content.
// After tools execute, results are appended as a user message with tool_result blocks.
func CreateToolResultMessage(blocks []types.ContentBlock) types.Message {
	return types.Message{
		Role:      types.RoleUser,
		Content:   blocks,
		Timestamp: time.Now(),
	}
}

// CreateToolErrorBlock creates a tool_result content block for an error.
// Source: StreamingToolExecutor.ts:86-99 — createSyntheticErrorMessage().
// Unknown tools get: "Error: No such tool available: <name>"
func CreateToolErrorBlock(toolUseID string, errMsg string) types.ContentBlock {
	errJSON, _ := json.Marshal(map[string]string{"error": errMsg})
	return types.NewToolResultBlock(toolUseID, errJSON, true)
}

// Abort reason constants for synthetic error blocks.
// Source: StreamingToolExecutor.ts — abort reasons from abortController.abort(reason).
const (
	AbortReasonUserInterrupted  = "user_interrupted"
	AbortReasonStreamingFallback = "streaming_fallback"
	AbortReasonSiblingError     = "sibling_error"
)

// CreateSyntheticErrorBlock creates a tool_result block for abort scenarios.
// Source: StreamingToolExecutor.ts:153-205 — createSyntheticErrorMessage().
// Reasons: AbortReasonSiblingError, AbortReasonUserInterrupted, AbortReasonStreamingFallback.
func CreateSyntheticErrorBlock(toolUseID, reason string) types.ContentBlock {
	var msg string
	switch reason {
	case AbortReasonUserInterrupted:
		msg = "User rejected tool use"
	case AbortReasonStreamingFallback:
		msg = "Error: Streaming fallback - tool execution discarded"
	default:
		msg = "Cancelled: parallel tool call errored"
	}
	errJSON, _ := json.Marshal(map[string]string{"error": msg})
	return types.NewToolResultBlock(toolUseID, errJSON, true)
}

// SyntheticToolResultsForBlocks generates synthetic tool_result error blocks
// for tool_use blocks whose IDs are NOT in startedIDs.
// Used during post-streaming and mid-streaming aborts to prevent orphaned
// tool_use blocks (which would cause API 400 errors on the next turn).
// Source: query.ts — yieldMissingToolResultBlocks.
func SyntheticToolResultsForBlocks(blocks []types.ContentBlock, startedIDs map[string]bool, reason string) []types.ContentBlock {
	var result []types.ContentBlock
	for _, cb := range blocks {
		if cb.Type == types.ContentTypeToolUse {
			if _, started := startedIDs[cb.ID]; !started {
				result = append(result, CreateSyntheticErrorBlock(cb.ID, reason))
			}
		}
	}
	return result
}

// ExtractTextBlocks returns all text content from a message.
// Source: query.ts — text extraction for result reporting.
func ExtractTextBlocks(msg types.Message) []string {
	var texts []string
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeText && cb.Text != "" {
			texts = append(texts, cb.Text)
		}
	}
	return texts
}

// HasToolUseBlocks checks if a message contains tool_use content blocks.
// Source: query.ts — Stage 20 check: hasToolUse detection.
func HasToolUseBlocks(msg types.Message) bool {
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolUse {
			return true
		}
	}
	return false
}

// ExtractToolUseBlocks returns all tool_use blocks from a message.
// Source: query.ts — collecting toolUseBlocks for Stage 21 execution.
func ExtractToolUseBlocks(msg types.Message) []types.ContentBlock {
	var blocks []types.ContentBlock
	for _, cb := range msg.Content {
		if cb.Type == types.ContentTypeToolUse {
			blocks = append(blocks, cb)
		}
	}
	return blocks
}
