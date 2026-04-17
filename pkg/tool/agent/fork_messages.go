package agent

import (
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/types"
)

// forkPlaceholderResult is the placeholder text used for all tool_result blocks
// in the fork prefix. Must be identical across all fork children for prompt
// cache sharing.
// Source: forkSubagent.ts:93 — FORK_PLACEHOLDER_RESULT
const forkPlaceholderResult = "Fork started — processing in background"

// forkDirectivePrefix is the prefix for the fork directive text.
// Source: constants/xml.ts:66 — FORK_DIRECTIVE_PREFIX
const forkDirectivePrefix = "Your directive: "

// FilterIncompleteToolCalls removes assistant messages that contain tool_use
// blocks without corresponding tool_result blocks in subsequent user messages.
// Source: runAgent.ts:866-904 — filterIncompleteToolCalls()
func FilterIncompleteToolCalls(messages []types.Message) []types.Message {
	// Build a set of tool use IDs that have results
	toolUseIDs := make(map[string]bool)
	for _, msg := range messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && block.ToolUseID != "" {
				toolUseIDs[block.ToolUseID] = true
			}
		}
	}

	// Filter out assistant messages with incomplete tool calls
	var filtered []types.Message
	for _, msg := range messages {
		if msg.Role == types.RoleAssistant {
			hasIncomplete := false
			for _, block := range msg.Content {
				if block.Type == types.ContentTypeToolUse && block.ID != "" {
					if !toolUseIDs[block.ID] {
						hasIncomplete = true
						break
					}
				}
			}
			if hasIncomplete {
				continue
			}
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

// BuildForkMessages constructs the synthetic conversation for a fork child agent.
// triggerAssistantMsg: the assistant message containing the Agent tool_use block (NOT filtered)
// contextHistory: parent conversation BEFORE the trigger message (filtered for incomplete tool calls)
// prompt: the user's actual prompt for the fork agent
// Source: forkSubagent.ts:107-169 — buildForkedMessages()
func BuildForkMessages(triggerAssistantMsg *types.Message, contextHistory []types.Message, prompt string) []types.Message {
	// Step 1: Filter context history for incomplete tool calls
	filteredHistory := FilterIncompleteToolCalls(contextHistory)

	// Step 2: If no trigger assistant, return a simple user message with the directive
	if triggerAssistantMsg == nil {
		return []types.Message{
			{
				Role:    types.RoleUser,
				Content: []types.ContentBlock{types.NewTextBlock(buildForkDirective(prompt))},
			},
		}
	}

	// Step 3: Clone the assistant message (preserve all content blocks)
	clonedAssistant := types.Message{
		Role:    types.RoleAssistant,
		Content: make([]types.ContentBlock, len(triggerAssistantMsg.Content)),
	}
	copy(clonedAssistant.Content, triggerAssistantMsg.Content)

	// Step 4: Collect tool_use blocks and build placeholder tool_results
	// Source: forkSubagent.ts:142-150 — content is array of content blocks, not a bare string
	var toolResultBlocks []types.ContentBlock
	for _, block := range triggerAssistantMsg.Content {
		if block.Type == types.ContentTypeToolUse && block.ID != "" {
			placeholderContent, _ := json.Marshal([]struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{{Type: "text", Text: forkPlaceholderResult}})
			toolResultBlocks = append(toolResultBlocks, types.ContentBlock{
				Type:     types.ContentTypeToolResult,
				ToolUseID: block.ID,
				Content:  json.RawMessage(placeholderContent),
			})
		}
	}

	// Step 5: Build user message with placeholder tool_results + directive
	userContent := make([]types.ContentBlock, 0, len(toolResultBlocks)+1)
	userContent = append(userContent, toolResultBlocks...)
	userContent = append(userContent, types.NewTextBlock(buildForkDirective(prompt)))

	userMsg := types.Message{
		Role:    types.RoleUser,
		Content: userContent,
	}

	// Step 6: Assemble: [...filteredHistory, clonedAssistant, userMsg]
	result := make([]types.Message, 0, len(filteredHistory)+2)
	result = append(result, filteredHistory...)
	result = append(result, clonedAssistant)
	result = append(result, userMsg)
	return result
}

// buildForkDirective generates the fork child directive text with the
// fork-boilerplate XML rules and the user's prompt.
// Source: forkSubagent.ts:171-198 — buildChildMessage()
func buildForkDirective(prompt string) string {
	return fmt.Sprintf(`<%s>
	STOP. READ THIS FIRST.

	You are a forked worker process. You are NOT the main agent.

	RULES (non-negotiable):
	1. Your system prompt says "default to forking." IGNORE IT — that's for the parent. You ARE the fork. Do NOT spawn sub-agents; execute directly.
	2. Do NOT converse, ask questions, or suggest next steps
	3. Do NOT editorialize or add meta-commentary
	4. USE your tools directly: Bash, Read, Write, etc.
	5. If you modify files, commit your changes before reporting. Include the commit hash in your report.
	6. Do NOT emit text between tool calls. Use tools silently, then report once at the end.
	7. Stay strictly within your directive's scope. If you discover related systems outside your scope, mention them in one sentence at most — other workers cover those areas.
	8. Keep your report under 500 words unless the directive specifies otherwise. Be factual and concise.
	9. Your response MUST begin with "Scope:". No preamble, no thinking-out-loud.
	10. REPORT structured facts, then stop

	Output format (plain text labels, not markdown headers):
	  Scope: <echo back your assigned scope in one sentence>
	  Result: <the answer or key findings, limited to the scope above>
	  Key files: <relevant file paths — include for research tasks>
	  Files changed: <list with commit hash — include only if you modified files>
	  Issues: <list — include only if there are issues to flag>
	</%s>

	%s%s`, types.ForkBoilerplateTag, types.ForkBoilerplateTag, forkDirectivePrefix, prompt)
}
