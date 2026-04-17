// Package agent provides built-in agent definitions and tool filtering for the Agent tool.
//
// Source reference: tools/AgentTool/builtInAgents.ts, tools/AgentTool/built-in/*.ts
package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Built-in agent definitions
// Source: tools/AgentTool/builtInAgents.ts — getBuiltInAgents()
// ---------------------------------------------------------------------------

// builtInAgents is the registry of all built-in agent types.
// Source: tools/AgentTool/builtInAgents.ts — getBuiltInAgents()
var builtInAgents = map[string]*types.AgentDefinition{
	"General": {
		AgentType: "General",
		WhenToUse: "General-purpose agent for researching complex questions, searching for code, and executing multi-step tasks. When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries use this agent to perform the search for you.",
		SystemPrompt: func() string {
			return generalPurposeSystemPrompt
		},
		Tools:           []string{"*"},
		DisallowedTools: nil,
		Model:           "inherit",
		OmitClaudeMd:    false,
		MaxTurns:        0,
		Source:          types.AgentSourceBuiltIn,
		BaseDir:         "built-in",
	},
	"Explore": {
		AgentType: "Explore",
		WhenToUse: "Use this agent when you need to quickly find files by patterns, search code for keywords, or answer questions about the codebase.",
		SystemPrompt: func() string {
			return exploreSystemPrompt
		},
		// No whitelist — uses default set minus DisallowedTools
		Tools:           nil,
		DisallowedTools: []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"},
		Model:           "inherit",
		OmitClaudeMd:    true,
		MaxTurns:        0,
		Source:          types.AgentSourceBuiltIn,
		BaseDir:         "built-in",
	},
	"Plan": {
		AgentType: "Plan",
		WhenToUse: "Use this agent when you need to explore the codebase and design an implementation plan before writing code.",
		SystemPrompt: func() string {
			return planSystemPrompt
		},
		// No whitelist — uses default set minus DisallowedTools (same as Explore)
		Tools:           nil,
		DisallowedTools: []string{"ExitPlanMode", "Edit", "Write", "NotebookEdit"},
		Model:           "inherit",
		OmitClaudeMd:    true,
		MaxTurns:        0,
		Source:          types.AgentSourceBuiltIn,
		BaseDir:         "built-in",
	},
}

// GetAgentDefinition returns the agent definition for the given type.
// If a global loader is initialized, uses it (includes custom agents).
// Otherwise falls back to built-in agents only.
// Returns an error if the type is not found.
// Source: tools/AgentTool/builtInAgents.ts — getBuiltInAgents() lookup
func GetAgentDefinition(agentType string) (*types.AgentDefinition, error) {
	if agentType == "" {
		agentType = "General"
	}

	// Use global loader if initialized (includes custom agents + override resolution)
	if globalLoader != nil {
		if def := globalLoader.Get(agentType); def != nil {
			return def, nil
		}
		return nil, fmt.Errorf("unknown agent type %q", agentType)
	}

	// Fallback: built-in agents only
	// Exact match first
	if def, ok := builtInAgents[agentType]; ok {
		return def, nil
	}
	// Case-insensitive fallback — LLM may send "explore" instead of "Explore"
	lower := strings.ToLower(agentType)
	for key, def := range builtInAgents {
		if strings.ToLower(key) == lower {
			return def, nil
		}
	}
	return nil, fmt.Errorf("unknown agent type %q: not found in built-in agents", agentType)
}

// ListAgentDefinitions returns all active agent definitions sorted by name.
// If a global loader is initialized, includes custom agents.
// Otherwise returns built-in agents only.
func ListAgentDefinitions() []*types.AgentDefinition {
	if globalLoader != nil {
		return globalLoader.ListAll()
	}

	defs := make([]*types.AgentDefinition, 0, len(builtInAgents))
	for _, def := range builtInAgents {
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].AgentType < defs[j].AgentType
	})
	return defs
}

// ---------------------------------------------------------------------------
// One-shot agent types
// Source: tools/AgentTool/constants.ts:6-12 — ONE_SHOT_BUILTIN_AGENT_TYPES
// ---------------------------------------------------------------------------

// OneShotAgentTypes are agent types that run once and return a report.
// Their wire result omits the agentId hint and usage trailer to save tokens.
// Source: tools/AgentTool/constants.ts:6-12
var OneShotAgentTypes = map[string]bool{
	"Explore": true,
	"Plan":    true,
}

// IsOneShotAgent returns true if the agent type is a one-shot agent.
func IsOneShotAgent(agentType string) bool {
	return OneShotAgentTypes[agentType]
}

// ---------------------------------------------------------------------------
// System prompts — source: tools/AgentTool/built-in/*.ts
// ---------------------------------------------------------------------------

// generalPurposeSystemPrompt is the system prompt for the general-purpose agent.
// Source: tools/AgentTool/built-in/generalPurposeAgent.ts
const generalPurposeSystemPrompt = `You are an agent for Claude Code, Anthropic's official CLI for Claude. Given the user's message, you should use the tools available to complete the task. Complete the task fully—don't gold-plate, but don't leave it half-done.

When you complete the task, respond with a concise report covering what was done and any key findings — the caller will relay this to the user, so it only needs the essentials.

Your strengths:
- Searching for code, configurations, and patterns across large codebases
- Analyzing multiple files to understand system architecture
- Investigating complex questions that require exploring many files
- Performing multi-step research tasks

Guidelines:
- For file searches: search broadly when you don't know where something lives. Use Read when you know the specific file path.
- For analysis: Start broad and narrow down. Use multiple search strategies if the first doesn't yield results.
- Be thorough: Check multiple locations, consider different naming conventions, look for related files.
- NEVER create files unless they're absolutely necessary for achieving your goal. ALWAYS prefer editing an existing file to creating a new one.
- NEVER proactively create documentation files (*.md) or README files. Only create documentation files if explicitly requested.`

// exploreSystemPrompt is the system prompt for the Explore agent.
// Source: tools/AgentTool/built-in/exploreAgent.ts
const exploreSystemPrompt = `You are a file search specialist for Claude Code, Anthropic's official CLI for Claude. You excel at thoroughly navigating and exploring codebases.

=== CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS ===
This is a READ-ONLY exploration task. You are STRICTLY PROHIBITED from:
- Creating new files (no Write, touch, or file creation of any kind)
- Modifying existing files (no Edit operations)
- Deleting files (no rm or deletion)
=== END READ-ONLY CONSTRAINTS ===

Your primary tools are file search, code search, and file reading. Use them effectively to navigate the codebase and find what the user is looking for.

Guidelines:
- Start with broad searches and narrow down based on results.
- Use multiple search strategies if the first doesn't yield results.
- When searching for a specific file, try multiple patterns and naming conventions.
- When searching for code, try both exact matches and fuzzy patterns.
- Report file paths exactly as they appear in the codebase.`

// planSystemPrompt is the system prompt for the Plan agent.
// Source: tools/AgentTool/built-in/planAgent.ts
const planSystemPrompt = `You are a software architect and planning specialist for Claude Code. Your role is to explore the codebase and design implementation plans.

=== CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS ===
This is a READ-ONLY planning task. You are STRICTLY PROHIBITED from:
- Creating new files (no Write, touch, or file creation of any kind)
- Modifying existing files (no Edit operations)
- Deleting files (no rm or deletion)
=== END READ-ONLY CONSTRAINTS ===

Your job is to:
1. Explore the relevant parts of the codebase
2. Understand the existing architecture and patterns
3. Design a detailed implementation plan
4. Identify potential risks and edge cases

## Required Output

End your response with:

### Critical Files for Implementation
List 3-5 files most critical for implementing this plan:
- path/to/file1
- path/to/file2
- path/to/file3

REMEMBER: You can ONLY explore and plan. You CANNOT and MUST NOT write, edit, or modify any files. You do NOT have access to file editing tools.`
