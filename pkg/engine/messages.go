package engine

import (
	"time"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/types"
)

// CreateUserMessage creates a user message from a text string.
// Source: utils/messages.ts — createUserMessage()
func CreateUserMessage(text string) types.Message {
	return types.Message{
		ID:   uuid.New().String(),
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
		ID:        uuid.New().String(),
		Role:      types.RoleUser,
		Content:   blocks,
		Timestamp: time.Now(),
	}
}

// CreateToolErrorBlock creates a tool_result content block for an error.
// Source: StreamingToolExecutor.ts:86-99 — createSyntheticErrorMessage().
// Unknown tools get: "Error: No such tool available: <name>"
func CreateToolErrorBlock(toolUseID string, errMsg string) types.ContentBlock {
	return types.NewToolResultBlock(toolUseID, marshalBlocks([]types.ContentBlock{types.NewTextBlock(errMsg)}), true)
}

// Abort reason constants for synthetic error blocks.
// Source: StreamingToolExecutor.ts — abort reasons from abortController.abort(reason).
const (
	AbortReasonUserInterrupted   = "user_interrupted"
	AbortReasonStreamingFallback = "streaming_fallback"
	AbortReasonSiblingError      = "sibling_error"
)

// CreateSyntheticErrorBlock creates a tool_result block for abort scenarios.
// Source: StreamingToolExecutor.ts:153-205 — createSyntheticErrorMessage().
// Reasons: AbortReasonSiblingError, AbortReasonUserInterrupted, AbortReasonStreamingFallback.
func CreateSyntheticErrorBlock(toolUseID, reason string) types.ContentBlock {
	var msg string
	switch reason {
	case AbortReasonUserInterrupted:
		msg = userRejectMessage
	case AbortReasonStreamingFallback:
		msg = "Error: Streaming fallback - tool execution discarded"
	default:
		msg = "Cancelled: parallel tool call errored"
	}
	return types.NewToolResultBlock(toolUseID, marshalBlocks([]types.ContentBlock{types.NewTextBlock(msg)}), true)
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

// syntheticToolResultPlaceholder is the content injected by EnsureToolResultPairing
// when a tool_use block has no matching tool_result.
// Source: TS utils/messages.ts — SYNTHETIC_TOOL_RESULT_PLACEHOLDER.
const syntheticToolResultPlaceholder = "[Tool result missing due to internal error]"

// EnsureToolResultPairing repairs tool_use/tool_result pairing mismatches
// before sending messages to the API.
//
// Handles both directions:
//   - Forward: inserts synthetic error tool_result blocks for tool_use blocks
//     missing results (e.g. after stream interruption / model switch)
//   - Reverse: strips orphaned tool_result blocks referencing non-existent
//     tool_use blocks (e.g. after compact drops the assistant)
//   - Dedup: removes duplicate tool_use IDs across assistant messages and
//     duplicate tool_result IDs within user messages
//
// Source: TS utils/messages.ts — ensureToolResultPairing().
func EnsureToolResultPairing(messages []types.Message) []types.Message {
	result := make([]types.Message, 0, len(messages))
	allSeenToolUseIDs := make(map[string]bool)

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role != types.RoleAssistant {
			// A user message with tool_result blocks but NO preceding
			// assistant message in the output has orphaned tool_results.
			if msg.Role == types.RoleUser &&
				hasToolResult(msg) &&
				(len(result) == 0 || lastRole(result) != types.RoleAssistant) {
				stripped := stripToolResults(msg)
				if len(stripped) != len(msg.Content) {
					var content []types.ContentBlock
					if len(stripped) > 0 {
						content = stripped
					} else if len(result) == 0 {
						content = []types.ContentBlock{
							types.NewTextBlock("[Orphaned tool result removed due to conversation resume]"),
						}
					}
					if content != nil {
						msg = types.Message{
							Role:    msg.Role,
							Content: content,
							Flags:   msg.Flags,
						}
						result = append(result, msg)
					}
					continue
				}
			}
			result = append(result, msg)
			continue
		}

		// --- Assistant message ---

		// Dedupe tool_use IDs tracked globally (cross-message).
		// A duplicate in a later assistant (different message.id) is stripped.
		seen := make(map[string]bool)
		var finalContent []types.ContentBlock
		for _, c := range msg.Content {
			if c.Type == types.ContentTypeToolUse {
				if allSeenToolUseIDs[c.ID] {
					continue
				}
				allSeenToolUseIDs[c.ID] = true
				seen[c.ID] = true
			}
			finalContent = append(finalContent, c)
		}

		if len(finalContent) == 0 {
			finalContent = []types.ContentBlock{types.NewTextBlock("[Tool use interrupted]")}
		}

		result = append(result, types.Message{
			Role:    msg.Role,
			Content: finalContent,
			Flags:   msg.Flags,
		})

		// Collect tool_use IDs from this assistant for pairing check.
		toolUseIDs := make([]string, 0, len(seen))
		for id := range seen {
			toolUseIDs = append(toolUseIDs, id)
		}

		// Check next user message for matching tool_results.
		existingIDs := make(map[string]bool)
		hasDupTr := false
		nextIdx := i + 1
		if nextIdx < len(messages) && messages[nextIdx].Role == types.RoleUser {
			for _, c := range messages[nextIdx].Content {
				if c.Type == types.ContentTypeToolResult {
					if existingIDs[c.ToolUseID] {
						hasDupTr = true
					}
					existingIDs[c.ToolUseID] = true
				}
			}
		}

		// Missing tool_results (forward: tool_use without tool_result).
		toolUseSet := make(map[string]bool, len(toolUseIDs))
		for _, id := range toolUseIDs {
			toolUseSet[id] = true
		}
		var missingIDs []string
		for _, id := range toolUseIDs {
			if !existingIDs[id] {
				missingIDs = append(missingIDs, id)
			}
		}

		// Orphaned tool_results (reverse: tool_result without tool_use).
		var orphanedIDs []string
		for id := range existingIDs {
			if !toolUseSet[id] {
				orphanedIDs = append(orphanedIDs, id)
			}
		}

		if len(missingIDs) == 0 && len(orphanedIDs) == 0 && !hasDupTr {
			continue
		}

		// Build synthetic error tool_result blocks for missing IDs.
		syntheticBlocks := make([]types.ContentBlock, 0, len(missingIDs))
		for _, id := range missingIDs {
			placeholder := marshalBlocks([]types.ContentBlock{types.NewTextBlock(syntheticToolResultPlaceholder)})
			syntheticBlocks = append(syntheticBlocks,
				types.NewToolResultBlock(id, placeholder, true))
		}

		if nextIdx < len(messages) && messages[nextIdx].Role == types.RoleUser {
			// Patch next user message: strip orphans + dedup, prepend synthetic.
			orphanedSet := make(map[string]bool, len(orphanedIDs))
			for _, id := range orphanedIDs {
				orphanedSet[id] = true
			}
			seenTr := make(map[string]bool)
			var nextContent []types.ContentBlock
			for _, c := range messages[nextIdx].Content {
				if c.Type == types.ContentTypeToolResult {
					if orphanedSet[c.ToolUseID] {
						continue
					}
					if seenTr[c.ToolUseID] {
						continue
					}
					seenTr[c.ToolUseID] = true
				}
				nextContent = append(nextContent, c)
			}

			patched := make([]types.ContentBlock, 0, len(syntheticBlocks)+len(nextContent))
			patched = append(patched, syntheticBlocks...)
			patched = append(patched, nextContent...)

			if len(patched) > 0 {
				result = append(result, types.Message{
					Role:    types.RoleUser,
					Content: patched,
					Flags:   messages[nextIdx].Flags,
				})
			} else {
				result = append(result, types.Message{
					Role:    types.RoleUser,
					Content: []types.ContentBlock{types.NewTextBlock("[No content]")},
					Flags:   types.FlagMeta,
				})
			}
			i++ // consumed next message
		} else if len(syntheticBlocks) > 0 {
			// No next user message — insert synthetic user message.
			result = append(result, types.Message{
				Role:    types.RoleUser,
				Content: syntheticBlocks,
				Flags:   types.FlagMeta,
			})
		}
	}

	return result
}

func hasToolResult(msg types.Message) bool {
	for _, c := range msg.Content {
		if c.Type == types.ContentTypeToolResult {
			return true
		}
	}
	return false
}

func stripToolResults(msg types.Message) []types.ContentBlock {
	var out []types.ContentBlock
	for _, c := range msg.Content {
		if c.Type != types.ContentTypeToolResult {
			out = append(out, c)
		}
	}
	return out
}

func lastRole(msgs []types.Message) types.Role {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1].Role
}
