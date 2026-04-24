package agent

import (
	"slices"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Tool filtering for sub-agents
// Source: tools/AgentTool/agentToolUtils.ts:70-225
// ---------------------------------------------------------------------------

// AllAgentDisallowedTools are tools that NO sub-agent is allowed to use.
// Source: agentToolUtils.ts:70-84 — TS blocks Agent for non-ant users;
// gbot has no user tiers, so Agent is allowed (nested agents supported).
var AllAgentDisallowedTools = map[string]bool{}

// FilterToolsForAgent filters the tool set for a given agent definition.
// Removes tools in AllAgentDisallowedTools and agent-specific DisallowedTools.
// Source: agentToolUtils.ts:70-116 — filterToolsForAgent()
func FilterToolsForAgent(allTools map[string]tool.Tool, agentDef *types.AgentDefinition) map[string]tool.Tool {
	filtered := make(map[string]tool.Tool, len(allTools))
	for name, t := range allTools {
		// Skip globally disallowed tools
		if AllAgentDisallowedTools[name] {
			continue
		}
		// Skip agent-specific disallowed tools
		if isDisallowed(name, agentDef.DisallowedTools) {
			continue
		}
		filtered[name] = t
	}
	return filtered
}

// ResolveAgentTools resolves the final tool set for an agent based on its definition.
// Applies whitelist/blacklist in the correct order:
// 1. Remove AllAgentDisallowedTools
// 2. Remove agent-specific DisallowedTools
// 3. If Tools is nil or ["*"], return remaining; otherwise filter to whitelist
// Source: agentToolUtils.ts:122-225 — resolveAgentTools()
func ResolveAgentTools(allTools map[string]tool.Tool, agentDef *types.AgentDefinition) map[string]tool.Tool {
	// Step 1+2: Filter out disallowed tools
	filtered := FilterToolsForAgent(allTools, agentDef)

	// Step 3: Apply whitelist
	if len(agentDef.Tools) == 0 || isWildcard(agentDef.Tools) {
		// No whitelist or wildcard — return all filtered tools
		return filtered
	}

	// Whitelist mode — only keep explicitly listed tools
	result := make(map[string]tool.Tool, len(agentDef.Tools))
	for _, name := range agentDef.Tools {
		if t, ok := filtered[name]; ok {
			result[name] = t
		}
	}
	return result
}

// isDisallowed checks if a tool name is in the disallowed list.
func isDisallowed(name string, disallowed []string) bool {
	return slices.Contains(disallowed, name)
}

// isWildcard checks if the tools list is ["*"].
func isWildcard(tools []string) bool {
	return len(tools) == 1 && tools[0] == "*"
}

// FilterMCPToolsForAgent filters MCP tools by RequiredMcpServers.
// If requiredServers is empty, returns all tools unchanged (inherit parent).
// If specified, only MCP tools from the listed servers are kept.
// Non-MCP tools (no "mcp__" prefix) always pass through.
//
// Source: runAgent.ts:95-218 — initializeAgentMcpServers (simplified version).
// TS actually connects new MCP servers; gbot only filters existing tools.
func FilterMCPToolsForAgent(tools map[string]tool.Tool, requiredServers []string) map[string]tool.Tool {
	if len(requiredServers) == 0 {
		return tools
	}
	// Build set for O(1) lookup instead of linear scan per tool.
	serverSet := make(map[string]bool, len(requiredServers))
	for _, s := range requiredServers {
		serverSet[s] = true
	}
	filtered := make(map[string]tool.Tool, len(tools))
	for name, t := range tools {
		serverName := extractMCPServerName(name)
		if serverName == "" {
			// Non-MCP tool — always pass through
			filtered[name] = t
			continue
		}
		// MCP tool — only keep if server is in required set
		if serverSet[serverName] {
			filtered[name] = t
		}
	}
	return filtered
}

// extractMCPServerName extracts server name from "mcp__server__tool" format.
// Returns empty string for non-MCP tool names.
// Edge case: "mcp__server__sub__tool" → "server" (first segment after "mcp").
func extractMCPServerName(toolName string) string {
	if !strings.HasPrefix(toolName, "mcp__") {
		return ""
	}
	// Remove "mcp__" prefix, then take the first segment before the next "__"
	rest := toolName[5:]
	before, _, found := strings.Cut(rest, "__")
	if !found {
		return rest // "mcp__server" → "server"
	}
	return before // "mcp__server__tool" → "server"
}
