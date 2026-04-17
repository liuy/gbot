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

// formatAgentList returns the agent definitions filtered by allowedAgentTypes.
// Source: prompt.ts:71-74 — effectiveAgents = filter(agentDefinitions, allowedAgentTypes)
func formatAgentList(defs []*types.AgentDefinition, allowedAgentTypes []string) []*types.AgentDefinition {
	if len(allowedAgentTypes) == 0 {
		return defs
	}
	allowed := make(map[string]bool)
	for _, t := range allowedAgentTypes {
		allowed[t] = true
	}
	var filtered []*types.AgentDefinition
	for _, def := range defs {
		if allowed[def.AgentType] {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

// AgentPrompt returns the full system prompt with optional agent type filtering.
// Source: prompt.ts — getPrompt() with allowedAgentTypes
// TODO: allowedAgentTypes always nil until engine supports Agent(x,y) tool spec.
func AgentPrompt(allowedAgentTypes []string) string {
	defs := ListAgentDefinitions()
	filtered := formatAgentList(defs, allowedAgentTypes)
	var lines []string
	for _, def := range filtered {
		lines = append(lines, formatAgentLine(def))
	}
	return fmt.Sprintf(agentToolPrompt, strings.Join(lines, "\n"))
}

// agentPrompt returns the full system prompt contribution for the Agent tool.
// Source: prompt.ts — getPrompt()
func agentPrompt() string {
	return AgentPrompt(nil)
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

// Source: prompt.ts:202-287 — getPrompt() fork path
// Adapted for gbot: no SendMessage, no isolation, no worktree, no coordinator mode.
// Fork content always included (fork agents are always available in gbot).
const agentToolPrompt = `Launch a new agent to handle complex, multi-step tasks autonomously.

The Agent tool launches specialized agents (subprocesses) that autonomously handle complex tasks. Each agent type has specific capabilities and tools available to it.

Available agent types and the tools they have access to:
%s

When using the Agent tool, specify a subagent_type parameter to select which agent type to use. If omitted, the general-purpose agent is used.

## When to fork

Fork yourself (omit subagent_type) when the intermediate tool output isn't worth keeping in your context. The criterion is qualitative — "will I need this output again" — not task size.
- **Research**: fork open-ended questions. If research can be broken into independent questions, launch parallel forks in one message. A fork beats a fresh subagent for this — it inherits context and shares your cache.
- **Implementation**: prefer to fork implementation work that requires more than a couple of edits. Do research before jumping into implementation.

Forks are cheap because they share your prompt cache. Don't set model on a fork — a different model can't reuse the parent's cache. Pass a short name so the user can see the fork and steer it mid-run.

**Don't peek.** Do not Read or tail the output file unless the user explicitly asks for a progress check. You get a completion notification; trust it. Reading the transcript mid-flight pulls the fork's tool noise into your context, which defeats the point of forking.

**Don't race.** After launching, you know nothing about what the fork found. Never fabricate or predict fork results in any format — not as prose, summary, or structured output. The notification arrives as a user-role message in a later turn; it is never something you write yourself. If the user asks a follow-up before the notification lands, tell them the fork is still running — give status, not a guess.

**Writing a fork prompt.** Since the fork inherits your context, the prompt is a directive — what to do, not what the situation is. Be specific about scope: what's in, what's out, what another agent is handling. Don't re-explain background.

## When NOT to use the Agent tool:
- If you want to read a specific file path, use the Read tool or the Glob tool instead of the Agent tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the Glob tool instead, to find the match more quickly
- If you are searching for code within a specific file or set of 2-3 files, use the Read tool instead of the Agent tool, to find the match more quickly
- Other tasks that are not related to the agent descriptions above

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do
- When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
- When spawning a fresh agent (with a subagent_type), it starts with zero context — provide a complete task description.
- The agent's outputs should generally be trusted
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, etc.), since it is not aware of the user's intent
- If the agent description mentions that it should be used proactively, then you should try your best to use it without the user having to ask for it first.
- If you specify "run_in_background": true, the agent will be launched as a background agent that runs independently. Background agents inherit your full conversation context and tools. Results are delivered as a notification when the agent completes. Use for long-running tasks like verification, testing, or extensive codebase analysis.
- You can optionally specify a model for the agent: "sonnet" (default), "opus" (complex analysis), or "haiku" (quick lookups). If omitted, the agent inherits your current model.
- If you specify "name": "foo", the agent can be addressed by that name while running.

Writing the prompt:
Brief the agent like a smart colleague who just walked into the room — it hasn't seen this conversation, it doesn't know what you've tried, it doesn't understand why this task matters.
- Explain what you're trying to accomplish and why.
- Describe what you've already learned or ruled out.
- Give enough context about the surrounding problem that the agent can make judgment calls rather than just following a narrow instruction.
- If you need a short response, say so ("report in under 200 words").
- Lookups: hand over the exact command. Investigations: hand over the question — prescribed steps become dead weight when the premise is wrong.

Terse command-style prompts produce shallow, generic work.

**Never delegate understanding.** Don't write "based on your findings, fix the bug" or "based on the research, implement it." Those phrases push synthesis onto the agent instead of doing it yourself. Write prompts that prove you understood: include file paths, line numbers, what specifically to change.

Example usage:

<example>
user: "What's left on this branch before we can ship?"
assistant: <thinking>Forking this — it's a survey question. I want the punch list, not the git output in my context.</thinking>
Agent({
  name: "ship-audit",
  description: "Branch ship-readiness audit",
  prompt: "Audit what's left before this branch can ship. Check: uncommitted changes, commits ahead of main, whether tests exist, whether the GrowthBook gate is wired up, whether CI-relevant files changed. Report a punch list — done vs. missing. Under 200 words."
})
assistant: Ship-readiness audit running.
<commentary>
Turn ends here. The coordinator knows nothing about the findings yet. What follows is a SEPARATE turn — the notification arrives from outside, as a user-role message. It is not something the coordinator writes.
</commentary>
[later turn — notification arrives as user message]
assistant: Audit's back. Three blockers: no tests for the new prompt path, GrowthBook gate wired but not in build_flags.yaml, and one uncommitted file.
</example>

<example>
user: "so is the gate wired up or not"
<commentary>
User asks mid-wait. The audit fork was launched to answer exactly this, and it hasn't returned. The coordinator does not have this answer. Give status, not a fabricated result.
</commentary>
assistant: Still waiting on the audit — that's one of the things it's checking. Should land shortly.
</example>

<example>
user: "Can you get a second opinion on whether this migration is safe?"
assistant: <thinking>I'll ask the code-reviewer agent — it won't see my analysis, so it can give an independent read.</thinking>
<commentary>
A subagent_type is specified, so the agent starts fresh. It needs full context in the prompt. The briefing explains what to assess and why.
</commentary>
Agent({
  name: "migration-review",
  description: "Independent migration review",
  subagent_type: "general-purpose",
  prompt: "Review migration 0042_user_schema.sql for safety. Context: we're adding a NOT NULL column to a 50M-row table. Existing rows get a backfill default. I want a second opinion on whether the backfill approach is safe under concurrent writes — I've checked locking behavior but want independent verification. Report: is this safe, and if not, what specifically breaks?"
})
</example>`
