// Package agent tool prompt — source: tools/AgentTool/prompt.ts
//
// Generates the system prompt contribution that tells the LLM how and when
// to use the Agent tool. Dynamically includes the list of available agent
// types from the agent definition registry.
package agent

import (
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// agentPrompt returns the full system prompt contribution for the Agent tool.
// Source: prompt.ts — getPrompt()
func agentPrompt() string {
	defs := ListAgentDefinitions()

	var agentLines []string
	for _, def := range defs {
		agentLines = append(agentLines, formatAgentLine(def))
	}

	return fmt.Sprintf(agentToolPrompt, strings.Join(agentLines, "\n"))
}

// formatAgentLine formats one agent definition as a prompt line.
// Source: prompt.ts:43-46 — formatAgentLine
func formatAgentLine(def *types.AgentDefinition) string {
	toolsDesc := getToolsDescription(def)
	return fmt.Sprintf("- %s: %s (Tools: %s)", def.AgentType, def.WhenToUse, toolsDesc)
}

// getToolsDescription returns a human-readable description of an agent's tool access.
// Source: prompt.ts:15-37 — getToolsDescription
func getToolsDescription(def *types.AgentDefinition) string {
	hasAllowlist := len(def.Tools) > 0
	hasDenylist := len(def.DisallowedTools) > 0

	switch {
	case hasAllowlist && hasDenylist:
		denySet := make(map[string]bool)
		for _, t := range def.DisallowedTools {
			denySet[t] = true
		}
		var effective []string
		for _, t := range def.Tools {
			if !denySet[t] {
				effective = append(effective, t)
			}
		}
		if len(effective) == 0 {
			return "None"
		}
		return strings.Join(effective, ", ")
	case hasAllowlist:
		return strings.Join(def.Tools, ", ")
	case hasDenylist:
		return "All tools except " + strings.Join(def.DisallowedTools, ", ")
	default:
		return "All tools"
	}
}

// Source: prompt.ts:202-286 — getPrompt() non-coordinator path
// Adapted for gbot: no fork, no background, no SendMessage, no worktree.
const agentToolPrompt = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches specialized agents (subprocesses) that autonomously handle complex tasks. Each agent type has specific capabilities and tools available to it.

Available agent types and the tools they have access to:
%s

When using the Agent tool, specify a subagent_type parameter to select which agent type to use. If omitted, the general-purpose agent is used.

When NOT to use the Agent tool:
- If you want to read a specific file path, use the Read tool or the Glob tool instead of the Agent tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the Glob tool instead, to find the match more quickly
- If you are searching for code within a specific file or set of 2-3 files, use the Read tool instead of the Agent tool, to find the match more quickly
- Other tasks that are not related to the agent descriptions above

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do
- When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
- Each Agent invocation starts fresh — provide a complete task description.
- The agent's outputs should generally be trusted
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, etc.), since it is not aware of the user's intent
- If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first.

Writing the prompt:
Brief the agent like a smart colleague who just walked into the room — it hasn't seen this conversation, doesn't know what you've tried, doesn't understand why this task matters.
- Explain what you're trying to accomplish and why.
- Describe what you've already learned or ruled out.
- Give enough context about the surrounding problem that the agent can make judgment calls rather than just following a narrow instruction.
- If you need a short response, say so ("report in under 200 words").
- Lookups: hand over the exact command. Investigations: hand over the question — prescribed steps become dead weight when the premise is wrong.

Terse command-style prompts produce shallow, generic work.

**Never delegate understanding.** Don't write "based on your findings, fix the bug" or "based on the research, implement it." Those phrases push synthesis onto the agent instead of doing it yourself. Write prompts that prove you understood: include file paths, line numbers, what specifically to change.

Example usage:

<example>
user: "Can you get a second opinion on whether this migration is safe?"
assistant: A subagent_type is specified, so the agent starts fresh. It needs full context in the prompt. The briefing explains what to assess and why.
Agent({
  description: "Independent migration review",
  subagent_type: "general-purpose",
  prompt: "Review migration 0042_user_schema.sql for safety. Context: we're adding a NOT NULL column to a 50M-row table. Existing rows get a backfill default. I want a second opinion on whether the backfill approach is safe under concurrent writes — I've checked locking behavior but want independent verification. Report: is this safe, and if not, what specifically breaks?"
})
</example>`
