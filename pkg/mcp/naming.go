// Package mcp — naming: MCP name normalization and parsing utilities.
//
// Source: src/services/mcp/normalization.ts (23 lines)
// Source: src/services/mcp/mcpStringUtils.ts (107 lines)
package mcp

import (
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// Constants — Source: normalization.ts:7
// ---------------------------------------------------------------------------

// claudeAIServerPrefix is the prefix for Claude.ai server names.
// Source: normalization.ts:7 — const CLAUDEAI_SERVER_PREFIX = 'claude.ai '
const claudeAIServerPrefix = "claude.ai "

// nonAlphaNum matches characters that are NOT [a-zA-Z0-9_-].
// Source: normalization.ts:18 — /[^a-zA-Z0-9_-]/g
var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// consecutiveUnderscore matches two or more consecutive underscores.
// Source: normalization.ts:20 — /_+/g
var consecutiveUnderscore = regexp.MustCompile(`_+`)

// leadingTrailingUnderscore matches leading or trailing underscores.
// Source: normalization.ts:20 — /^_|_$/g
var leadingTrailingUnderscore = regexp.MustCompile(`^_|_$`)

// mcpSuffixPattern matches the " (MCP)" suffix in user-facing tool names.
// Source: mcpStringUtils.ts:91 — /\s*\(MCP\)\s*$/
var mcpSuffixPattern = regexp.MustCompile(`\s*\(MCP\)\s*$`)

// ---------------------------------------------------------------------------
// normalizeNameForMCP — Source: normalization.ts:17-23
// ---------------------------------------------------------------------------

// NormalizeNameForMCP normalizes server/tool names for API compatibility.
// Replaces invalid characters with underscores. For claude.ai prefix names,
// also collapses consecutive underscores and strips leading/trailing underscores.
// Source: normalization.ts:17-23
func NormalizeNameForMCP(name string) string {
	normalized := nonAlphaNum.ReplaceAllString(name, "_")
	if strings.HasPrefix(name, claudeAIServerPrefix) {
		// Source: normalization.ts:20 — collapse consecutive _ then strip leading/trailing _
		normalized = consecutiveUnderscore.ReplaceAllString(normalized, "_")
		normalized = leadingTrailingUnderscore.ReplaceAllString(normalized, "")
	}
	return normalized
}

// ---------------------------------------------------------------------------
// mcpInfoFromString — Source: mcpStringUtils.ts:19-32
// ---------------------------------------------------------------------------

// McpInfoFromString extracts MCP server and tool names from a tool string.
// Expected format: "mcp__serverName__toolName"
// Known limitation: server names containing "__" will be parsed incorrectly.
// Source: mcpStringUtils.ts:19-32
func McpInfoFromString(toolString string) *MCPToolInfo {
	parts := strings.Split(toolString, "__")
	// Source: mcpStringUtils.ts:24 — [mcpPart, serverName, ...toolNameParts]
	if len(parts) < 2 || parts[0] != "mcp" || parts[1] == "" {
		return nil
	}
	serverName := parts[1]
	// Source: mcpStringUtils.ts:29 — join remaining parts to preserve __ in tool names
	var toolName string
	if len(parts) > 2 {
		toolName = strings.Join(parts[2:], "__")
	}
	return &MCPToolInfo{ServerName: serverName, ToolName: toolName}
}

// ---------------------------------------------------------------------------
// getMcpPrefix — Source: mcpStringUtils.ts:39-41
// ---------------------------------------------------------------------------

// GetMcpPrefix returns the MCP tool/command name prefix for a server.
// Source: mcpStringUtils.ts:39-41
func GetMcpPrefix(serverName string) string {
	return "mcp__" + NormalizeNameForMCP(serverName) + "__"
}

// ---------------------------------------------------------------------------
// buildMcpToolName — Source: mcpStringUtils.ts:50-52
// ---------------------------------------------------------------------------

// BuildMcpToolName builds a fully qualified MCP tool name from server and tool names.
// Inverse of McpInfoFromString(). Source: mcpStringUtils.ts:50-52
func BuildMcpToolName(serverName, toolName string) string {
	return GetMcpPrefix(serverName) + NormalizeNameForMCP(toolName)
}

// ---------------------------------------------------------------------------
// getToolNameForPermissionCheck — Source: mcpStringUtils.ts:60-67
// ---------------------------------------------------------------------------

// GetToolNameForPermissionCheck returns the name to use for permission rule matching.
// For MCP tools, uses the fully qualified mcp__server__tool name so that deny rules
// targeting builtins don't match unprefixed MCP replacements.
// Source: mcpStringUtils.ts:60-67
func GetToolNameForPermissionCheck(toolName string, mcpInfo *MCPToolInfo) string {
	if mcpInfo != nil && mcpInfo.ToolName != "" {
		return BuildMcpToolName(mcpInfo.ServerName, mcpInfo.ToolName)
	}
	return toolName
}

// ---------------------------------------------------------------------------
// getMcpDisplayName — Source: mcpStringUtils.ts:75-81
// ---------------------------------------------------------------------------

// GetMcpDisplayName extracts the display name from an MCP tool name.
// Source: mcpStringUtils.ts:75-81
func GetMcpDisplayName(fullName, serverName string) string {
	prefix := "mcp__" + NormalizeNameForMCP(serverName) + "__"
	return strings.Replace(fullName, prefix, "", 1)
}

// ---------------------------------------------------------------------------
// extractMcpToolDisplayName — Source: mcpStringUtils.ts:88-106
// ---------------------------------------------------------------------------

// ExtractMcpToolDisplayName extracts the tool/command display name from a userFacingName.
// Removes the (MCP) suffix, then removes the server prefix (everything before " - ").
// Source: mcpStringUtils.ts:88-106
func ExtractMcpToolDisplayName(userFacingName string) string {
	// Source: mcpStringUtils.ts:91 — remove the (MCP) suffix if present
	withoutSuffix := mcpSuffixPattern.ReplaceAllString(userFacingName, "")
	withoutSuffix = strings.TrimSpace(withoutSuffix)

	// Source: mcpStringUtils.ts:98 — remove server prefix (everything before " - ")
	before, after, found := strings.Cut(withoutSuffix, " - ")
	_ = before
	if found {
		displayName := strings.TrimSpace(after)
		return displayName
	}

	// Source: mcpStringUtils.ts:104 — if no dash found, return string without (MCP)
	return withoutSuffix
}

// ---------------------------------------------------------------------------
// IsMcpTool — checks if a tool name has the MCP prefix
// ---------------------------------------------------------------------------

// IsMcpTool returns true if the tool name has the "mcp__" prefix.
func IsMcpTool(toolName string) bool {
	return strings.HasPrefix(toolName, "mcp__")
}
