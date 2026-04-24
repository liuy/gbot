package agent

import (
	"github.com/liuy/gbot/pkg/types"
)

// CountToolUses counts tool_use blocks across all assistant messages.
//
// Source: agentToolUtils.ts:262-274 — countToolUses.
// Iterates forward through all messages, counting each tool_use block
// in assistant messages. Used to report tool use count in FinalizeResult.
func CountToolUses(messages []types.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, blk := range msg.Content {
			if blk.Type == types.ContentTypeToolUse {
				count++
			}
		}
	}
	return count
}

// GetLastToolUseName returns the name of the last tool_use block in a single
// assistant message. Returns empty string for non-assistant or no tool_use.
//
// Source: agentToolUtils.ts:363-367 — getLastToolUseName.
// Takes a SINGLE message (not array). Called per-message during streaming
// to emit task progress (AgentTool.tsx:946,1070).
func GetLastToolUseName(msg types.Message) string {
	if msg.Role != types.RoleAssistant {
		return ""
	}
	// Walk backward to find the last tool_use block.
	// Source: TS uses Array.findLast() — Go equivalent is reverse iteration.
	for i := len(msg.Content) - 1; i >= 0; i-- {
		if msg.Content[i].Type == types.ContentTypeToolUse {
			return msg.Content[i].Name
		}
	}
	return ""
}
