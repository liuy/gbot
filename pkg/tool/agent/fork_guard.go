package agent

import (
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// IsInForkChild returns true if the conversation history indicates we're
// already inside a fork agent. Prevents recursive forking.
// Source: forkSubagent.ts:78-89 — isInForkChild()
func IsInForkChild(messages []types.Message) bool {
	marker := "<" + types.ForkBoilerplateTag + ">"
	for _, msg := range messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeText && strings.Contains(block.Text, marker) {
				return true
			}
		}
	}
	return false
}
