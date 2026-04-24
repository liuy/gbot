package agent

import (
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ExtractPartialResult walks messages backward to find the last assistant
// message with non-empty text content. Returns joined text or empty string.
// Only called on cancellation (context.Canceled / user kill), not general errors.
//
// Source: agentToolUtils.ts:488-500 — extractPartialResult.
// Called on user_cancel_background (AgentTool.tsx:1006) and user_kill_async
// (agentToolUtils.ts:658) to preserve what the agent accomplished before being killed.
func ExtractPartialResult(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != types.RoleAssistant {
			continue
		}
		// Extract text content blocks, joining with newline.
		// Source: agentToolUtils.ts:494 — extractTextContent(content, '\n')
		var textParts []string
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeText && blk.Text != "" {
				textParts = append(textParts, blk.Text)
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n")
		}
		// This assistant message has no text (pure tool_use) — continue backward.
	}
	return ""
}
