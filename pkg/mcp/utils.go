// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/utils.ts (576 lines)
//
// This file: utility functions — filtering, hashing, type guards, and status helpers.
// Source: utils.ts
package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Filter/Exclude by server — Source: utils.ts:39-149
// ---------------------------------------------------------------------------

// FilterToolsByServer returns tools belonging to the specified MCP server.
// Source: utils.ts:39-42 — uses mcp__normalizedServer__ prefix.
func FilterToolsByServer(tools []SerializedTool, serverName string) []SerializedTool {
	prefix := GetMcpPrefix(serverName)
	var result []SerializedTool
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, prefix) {
			result = append(result, tool)
		}
	}
	return result
}

// CommandBelongsToServer returns true if a command belongs to the given MCP server.
// Source: utils.ts:52-62 — MCP prompts use mcp__server__ prefix, skills use server: prefix.
func CommandBelongsToServer(commandName, serverName string) bool {
	normalized := NormalizeNameForMCP(serverName)
	if commandName == "" {
		return false
	}
	return strings.HasPrefix(commandName, "mcp__"+normalized+"__") ||
		strings.HasPrefix(commandName, normalized+":")
}

// FilterCommandsByServer returns commands belonging to the specified MCP server.
// Source: utils.ts:70-75
func FilterCommandsByServer(commands []MCPCommand, serverName string) []MCPCommand {
	var result []MCPCommand
	for _, c := range commands {
		if CommandBelongsToServer(c.Name, serverName) {
			result = append(result, c)
		}
	}
	return result
}

// FilterResourcesByServer returns resources belonging to the specified MCP server.
// Source: utils.ts:102-107 — matches resource.Server field.
func FilterResourcesByServer(resources []ServerResource, serverName string) []ServerResource {
	var result []ServerResource
	for _, r := range resources {
		if r.Server == serverName {
			result = append(result, r)
		}
	}
	return result
}

// ExcludeToolsByServer removes tools belonging to the specified MCP server.
// Source: utils.ts:115-121
func ExcludeToolsByServer(tools []SerializedTool, serverName string) []SerializedTool {
	prefix := GetMcpPrefix(serverName)
	var result []SerializedTool
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, prefix) {
			result = append(result, tool)
		}
	}
	return result
}

// ExcludeCommandsByServer removes commands belonging to the specified MCP server.
// Source: utils.ts:129-134
func ExcludeCommandsByServer(commands []MCPCommand, serverName string) []MCPCommand {
	var result []MCPCommand
	for _, c := range commands {
		if !CommandBelongsToServer(c.Name, serverName) {
			result = append(result, c)
		}
	}
	return result
}

// ExcludeResourcesByServer removes all resources for the specified server from the map.
// Source: utils.ts:142-149 — returns a new map without the server key.
func ExcludeResourcesByServer(resources map[string][]ServerResource, serverName string) map[string][]ServerResource {
	result := make(map[string][]ServerResource, len(resources))
	for k, v := range resources {
		if k != serverName {
			result[k] = v
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// hashMcpConfig — Source: utils.ts:157-169
// ---------------------------------------------------------------------------

// HashMcpConfig returns a stable SHA-256 hash of the server config for change detection.
// Excludes scope (provenance, not content). Keys sorted at all levels for determinism.
// Source: utils.ts:157-169 — SHA-256, first 16 hex chars.
func HashMcpConfig(config ScopedMcpServerConfig) string {
	// Source: utils.ts:158 — const { scope: _scope, ...rest } = config
	inner, err := json.Marshal(config.Config)
	if err != nil {
		return ""
	}

	// Parse into generic map for deep key sorting
	var raw any
	if json.Unmarshal(inner, &raw) != nil {
		return ""
	}

	// Add pluginSource if present — part of "rest" after scope exclusion
	if m, ok := raw.(map[string]any); ok && config.PluginSource != "" {
		m["pluginSource"] = config.PluginSource
	}

	// Source: utils.ts:159-166 — replacer sorts all object keys
	sorted := sortKeysDeep(raw)
	stable, err := json.Marshal(sorted)
	if err != nil {
		return ""
	}

	// Source: utils.ts:168 — sha256, first 16 hex chars
	hash := sha256.Sum256(stable)
	return fmt.Sprintf("%x", hash)[:16]
}

// sortKeysDeep recursively sorts map keys for deterministic JSON output.
// Mirrors the TS jsonStringify replacer that sorts object keys at every level.
func sortKeysDeep(v any) any {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sorted := make(map[string]any, len(val))
		for _, k := range keys {
			sorted[k] = sortKeysDeep(val[k])
		}
		return sorted
	case []any:
		result := make([]any, len(val))
		for i, elem := range val {
			result[i] = sortKeysDeep(elem)
		}
		return result
	default:
		return v
	}
}

// ---------------------------------------------------------------------------
// excludeStalePluginClients — Source: utils.ts:185-224
// ---------------------------------------------------------------------------

// MCPConnectionState holds the current MCP connection state for stale detection.
// Source: utils.ts:186-191 — { clients, tools, commands, resources }
type MCPConnectionState struct {
	Clients   []ServerConnection
	Tools     []SerializedTool
	Commands  []MCPCommand
	Resources map[string][]ServerResource
}

// MCPExclusionResult holds the result of stale client exclusion.
// Source: utils.ts:193-199 — includes stale clients for caller cleanup.
type MCPExclusionResult struct {
	Clients   []ServerConnection
	Tools     []SerializedTool
	Commands  []MCPCommand
	Resources map[string][]ServerResource
	Stale     []ServerConnection
}

// connName extracts the name from a ServerConnection via type switch.
func connName(conn ServerConnection) string {
	switch c := conn.(type) {
	case *ConnectedServer:
		return c.Name
	case *FailedServer:
		return c.Name
	case *NeedsAuthServer:
		return c.Name
	case *PendingServer:
		return c.Name
	case *DisabledServer:
		return c.Name
	default:
		return ""
	}
}

// connConfig extracts the config from a ServerConnection via type switch.
func connConfig(conn ServerConnection) ScopedMcpServerConfig {
	switch c := conn.(type) {
	case *ConnectedServer:
		return c.Config
	case *FailedServer:
		return c.Config
	case *NeedsAuthServer:
		return c.Config
	case *PendingServer:
		return c.Config
	case *DisabledServer:
		return c.Config
	default:
		return ScopedMcpServerConfig{}
	}
}

// ExcludeStalePluginClients removes stale MCP clients and their associated data.
// Source: utils.ts:185-224
//
// A client is stale if:
//   - No fresh config AND scope is 'dynamic' (plugin disabled), or
//   - Config hash changed (args/url/env edited) — any scope
func ExcludeStalePluginClients(
	state MCPConnectionState,
	configs map[string]ScopedMcpServerConfig,
) MCPExclusionResult {
	// Source: utils.ts:200-204 — detect stale clients
	var stale []ServerConnection
	for _, c := range state.Clients {
		cfg := connConfig(c)
		name := connName(c)
		fresh, exists := configs[name]
		if !exists {
			// Source: utils.ts:202 — no fresh config, stale only if dynamic
			if cfg.Scope == ScopeDynamic {
				stale = append(stale, c)
			}
			continue
		}
		// Source: utils.ts:203 — config changed at any scope
		if HashMcpConfig(cfg) != HashMcpConfig(fresh) {
			stale = append(stale, c)
		}
	}

	if len(stale) == 0 {
		// Source: utils.ts:205-207 — no stale, return copy
		return MCPExclusionResult{
			Clients:   state.Clients,
			Tools:     state.Tools,
			Commands:  state.Commands,
			Resources: state.Resources,
			Stale:     nil,
		}
	}

	// Source: utils.ts:209-214 — remove stale data
	tools := state.Tools
	commands := state.Commands
	resources := state.Resources
	for _, s := range stale {
		name := connName(s)
		tools = ExcludeToolsByServer(tools, name)
		commands = ExcludeCommandsByServer(commands, name)
		resources = ExcludeResourcesByServer(resources, name)
	}

	// Source: utils.ts:215-216 — build stale name set
	staleNames := make(map[string]bool, len(stale))
	for _, s := range stale {
		staleNames[connName(s)] = true
	}

	// Source: utils.ts:218-224 — filter clients
	var remaining []ServerConnection
	for _, c := range state.Clients {
		if !staleNames[connName(c)] {
			remaining = append(remaining, c)
		}
	}

	return MCPExclusionResult{
		Clients:   remaining,
		Tools:     tools,
		Commands:  commands,
		Resources: resources,
		Stale:     stale,
	}
}

// ---------------------------------------------------------------------------
// isToolFromMcpServer — Source: utils.ts:232-238
// ---------------------------------------------------------------------------

// IsToolFromMcpServer checks if a tool name belongs to a specific MCP server.
// Source: utils.ts:232-238 — uses mcpInfoFromString to extract server name.
func IsToolFromMcpServer(toolName, serverName string) bool {
	info := McpInfoFromString(toolName)
	if info == nil {
		return false
	}
	return info.ServerName == serverName
}

// ---------------------------------------------------------------------------
// isMcpCommand — Source: utils.ts:254-256
// ---------------------------------------------------------------------------

// IsMcpCommand returns true if the command name indicates an MCP command.
// Source: utils.ts:254-256
func IsMcpCommand(commandName string) bool {
	return strings.HasPrefix(commandName, "mcp__")
}

// ---------------------------------------------------------------------------
// getScopeLabel — Source: utils.ts:282-299
// ---------------------------------------------------------------------------

// GetScopeLabel returns a human-readable description for a config scope.
// Source: utils.ts:282-299
func GetScopeLabel(scope ConfigScope) string {
	switch scope {
	case ScopeLocal:
		return "Local config (private to you in this project)"
	case ScopeProject:
		return "Project config (shared via .mcp.json)"
	case ScopeUser:
		return "User config (available in all your projects)"
	case ScopeDynamic:
		return "Dynamic config (from command line)"
	case ScopeEnterprise:
		return "Enterprise config (managed by your organization)"
	case ScopeClaudeAI:
		return "claude.ai config"
	case ScopeManaged:
		return "Managed config"
	default:
		return string(scope)
	}
}

// ---------------------------------------------------------------------------
// describeMcpConfigFilePath — Source: utils.ts:263-280
// ---------------------------------------------------------------------------

// DescribeMcpConfigFilePath describes the file path for a given MCP config scope.
// Source: utils.ts:263-280
// globalConfigPath: equivalent of getGlobalClaudeFile()
// cwd: equivalent of getCwd()
// enterpriseConfigPath: equivalent of getEnterpriseMcpFilePath()
func DescribeMcpConfigFilePath(scope ConfigScope, globalConfigPath, cwd, enterpriseConfigPath string) string {
	switch scope {
	case ScopeUser:
		return globalConfigPath
	case ScopeProject:
		return cwd + "/.mcp.json"
	case ScopeLocal:
		return globalConfigPath + " [project: " + cwd + "]"
	case ScopeDynamic:
		return "Dynamically configured"
	case ScopeEnterprise:
		return enterpriseConfigPath
	case ScopeClaudeAI:
		return "claude.ai"
	default:
		return string(scope)
	}
}

// ---------------------------------------------------------------------------
// ensureConfigScope — Source: utils.ts:301-311
// ---------------------------------------------------------------------------

// ValidConfigScopes is the set of valid config scope values.
// Source: types.ts:10-21 — ConfigScopeSchema.options
var ValidConfigScopes = []ConfigScope{
	ScopeLocal, ScopeUser, ScopeProject, ScopeDynamic,
	ScopeEnterprise, ScopeClaudeAI, ScopeManaged,
}

// EnsureConfigScope validates and returns a ConfigScope.
// Returns ScopeLocal for empty input. Returns error for invalid values.
// Source: utils.ts:301-311
func EnsureConfigScope(scope string) (ConfigScope, error) {
	if scope == "" {
		return ScopeLocal, nil
	}
	for _, valid := range ValidConfigScopes {
		if ConfigScope(scope) == valid {
			return valid, nil
		}
	}
	return "", fmt.Errorf("invalid scope: %s. Must be one of: %s",
		scope, strings.Join(scopesToStrings(ValidConfigScopes), ", "))
}

func scopesToStrings(scopes []ConfigScope) []string {
	result := make([]string, len(scopes))
	for i, s := range scopes {
		result[i] = string(s)
	}
	return result
}

// ---------------------------------------------------------------------------
// ensureTransport — Source: utils.ts:313-323
// ---------------------------------------------------------------------------

// EnsureTransport validates and returns a transport type.
// Returns TransportStdio for empty input. Returns error for invalid values.
// Source: utils.ts:313-323
func EnsureTransport(t string) (Transport, error) {
	if t == "" {
		return TransportStdio, nil
	}
	switch Transport(t) {
	case TransportStdio, TransportSSE, TransportHTTP:
		return Transport(t), nil
	default:
		return "", fmt.Errorf("invalid transport type: %s. Must be one of: stdio, sse, http", t)
	}
}

// ---------------------------------------------------------------------------
// parseHeaders — Source: utils.ts:325-349
// ---------------------------------------------------------------------------

// ParseHeaders parses an array of "Header-Name: value" strings into a map.
// Source: utils.ts:325-349
func ParseHeaders(headerArray []string) (map[string]string, error) {
	headers := make(map[string]string, len(headerArray))
	for _, header := range headerArray {
		// Source: utils.ts:329 — find first colon
		before, after, ok := strings.Cut(header, ":")
		if !ok {
			return nil, fmt.Errorf("invalid header format: %q. Expected format: \"Header-Name: value\"", header)
		}
		key := strings.TrimSpace(before)
		value := strings.TrimSpace(after)
		if key == "" {
			return nil, fmt.Errorf("invalid header: %q: Header name cannot be empty", header)
		}
		headers[key] = value
	}
	return headers, nil
}

// ---------------------------------------------------------------------------
// getProjectMcpServerStatus — Source: utils.ts:351-406
// ---------------------------------------------------------------------------

// McpProjectSettings holds settings needed to determine MCP server approval status.
// Source: utils.ts:351-406 — encapsulates settings access from TS.
type McpProjectSettings struct {
	DisabledServers         []string
	EnabledServers          []string
	EnableAll               bool
	SkipDangerousPermission bool
	NonInteractive          bool
	ProjectSettingsEnabled  bool
}

// GetProjectMcpServerStatus determines the approval status of a project MCP server.
// Source: utils.ts:351-406 — returns "approved", "rejected", or "pending".
func GetProjectMcpServerStatus(serverName string, settings McpProjectSettings) string {
	normalizedName := NormalizeNameForMCP(serverName)

	// Source: utils.ts:359-365 — check disabled servers
	for _, name := range settings.DisabledServers {
		if NormalizeNameForMCP(name) == normalizedName {
			return "rejected"
		}
	}

	// Source: utils.ts:367-375 — check enabled servers or enable-all
	if settings.EnableAll {
		return "approved"
	}
	for _, name := range settings.EnabledServers {
		if NormalizeNameForMCP(name) == normalizedName {
			return "approved"
		}
	}

	// Source: utils.ts:386-391 — bypass permissions mode auto-approve
	if settings.SkipDangerousPermission && settings.ProjectSettingsEnabled {
		return "approved"
	}

	// Source: utils.ts:398-403 — non-interactive mode auto-approve
	if settings.NonInteractive && settings.ProjectSettingsEnabled {
		return "approved"
	}

	return "pending"
}

// ---------------------------------------------------------------------------
// Type guards — Source: utils.ts:438-457
// ---------------------------------------------------------------------------

// IsStdioConfig returns true if the config is a StdioConfig.
// Source: utils.ts:439-443 — stdio type is optional (defaults to stdio when empty).
func IsStdioConfig(config McpServerConfig) bool {
	_, ok := config.(*StdioConfig)
	return ok
}

// IsSSEConfig returns true if the config is an SSEConfig.
// Source: utils.ts:445-447
func IsSSEConfig(config McpServerConfig) bool {
	_, ok := config.(*SSEConfig)
	return ok
}

// IsHTTPConfig returns true if the config is an HTTPConfig.
// Source: utils.ts:449-451
func IsHTTPConfig(config McpServerConfig) bool {
	_, ok := config.(*HTTPConfig)
	return ok
}

// IsWSConfig returns true if the config is a WSConfig.
// Source: utils.ts:453-457
func IsWSConfig(config McpServerConfig) bool {
	_, ok := config.(*WSConfig)
	return ok
}

// ---------------------------------------------------------------------------
// extractAgentMcpServers — Source: utils.ts:466-553
// ---------------------------------------------------------------------------

// AgentMcpSpec represents either a string reference or an inline MCP server config
// from an agent definition's mcpServers field.
// Source: utils.ts:466-553 — mcpServers is (string | { [name: string]: McpServerConfig })[]
type AgentMcpSpec struct {
	Ref    string          // String reference to existing server
	Name   string          // Server name for inline definitions
	Config McpServerConfig // Server config for inline definitions
}

// IsRef returns true if this spec is a string reference (skip inline processing).
func (s AgentMcpSpec) IsRef() bool {
	return s.Ref != ""
}

// AgentDefinition represents an agent with optional MCP server definitions.
// Source: tools/AgentTool/loadAgentsDir.ts — AgentDefinition
type AgentDefinition struct {
	AgentType  string
	McpServers []AgentMcpSpec
}

// AgentMcpServerInfo holds extracted MCP server info from agent definitions.
// Source: components/mcp/types.ts — AgentMcpServerInfo
type AgentMcpServerInfo struct {
	Name         string
	SourceAgents []string
	Transport    string // "stdio", "sse", "http", "ws"
	Command      string // for stdio
	URL          string // for sse/http/ws
	NeedsAuth    bool
}

// ExtractAgentMcpServers extracts MCP server definitions from agent frontmatter.
// Source: utils.ts:466-553 — groups by server name, returns sorted by name.
func ExtractAgentMcpServers(agents []AgentDefinition) []AgentMcpServerInfo {
	// Source: utils.ts:470-476 — map: server name -> { config, sourceAgents }
	type entry struct {
		config       McpServerConfig
		sourceAgents []string
	}
	serverMap := make(map[string]*entry)

	for _, agent := range agents {
		// Source: utils.ts:479 — skip agents without mcpServers
		if len(agent.McpServers) == 0 {
			continue
		}

		for _, spec := range agent.McpServers {
			// Source: utils.ts:482-483 — skip string references
			if spec.IsRef() {
				continue
			}

			// Source: utils.ts:486-488 — inline definition with single entry
			if spec.Name == "" {
				continue
			}

			existing, found := serverMap[spec.Name]
			if found {
				// Source: utils.ts:493-495 — add agent as another source
				found := slices.Contains(existing.sourceAgents, agent.AgentType)
				if !found {
					existing.sourceAgents = append(existing.sourceAgents, agent.AgentType)
				}
			} else {
				// Source: utils.ts:498-505 — new server
				serverMap[spec.Name] = &entry{
					config:       spec.Config,
					sourceAgents: []string{agent.AgentType},
				}
			}
		}
	}

	// Source: utils.ts:511-552 — convert map to array, filter by supported transport
	var result []AgentMcpServerInfo
	for name, e := range serverMap {
		switch c := e.config.(type) {
		case *StdioConfig:
			result = append(result, AgentMcpServerInfo{
				Name:         name,
				SourceAgents: e.sourceAgents,
				Transport:    "stdio",
				Command:      c.Command,
				NeedsAuth:    false,
			})
		case *SSEConfig:
			result = append(result, AgentMcpServerInfo{
				Name:         name,
				SourceAgents: e.sourceAgents,
				Transport:    "sse",
				URL:          c.URL,
				NeedsAuth:    true,
			})
		case *HTTPConfig:
			result = append(result, AgentMcpServerInfo{
				Name:         name,
				SourceAgents: e.sourceAgents,
				Transport:    "http",
				URL:          c.URL,
				NeedsAuth:    true,
			})
		case *WSConfig:
			result = append(result, AgentMcpServerInfo{
				Name:         name,
				SourceAgents: e.sourceAgents,
				Transport:    "ws",
				URL:          c.URL,
				NeedsAuth:    false,
			})
		default:
			// Source: utils.ts:548-549 — skip unsupported transport types
		}
	}

	// Source: utils.ts:552 — sort by name
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// ---------------------------------------------------------------------------
// getLoggingSafeMcpBaseUrl — Source: utils.ts:561-575
// ---------------------------------------------------------------------------

// GetLoggingSafeMcpBaseUrl extracts the base URL (without query string) for analytics logging.
// Source: utils.ts:561-575 — strips query params (may contain tokens) and trailing slashes.
func GetLoggingSafeMcpBaseUrl(config McpServerConfig) string {
	switch c := config.(type) {
	case *SSEConfig:
		return stripURLForLogging(c.URL)
	case *HTTPConfig:
		return stripURLForLogging(c.URL)
	case *WSConfig:
		return stripURLForLogging(c.URL)
	default:
		return ""
	}
}

// stripURLForLogging strips query params and trailing slashes from a URL.
// Source: utils.ts:566-574
func stripURLForLogging(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	result := parsed.String()
	return strings.TrimRight(result, "/")
}
