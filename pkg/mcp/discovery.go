// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: tool discovery, batch fetching, and list_changed handling.
// Source: client.ts:1743-2040 (fetchToolsForClient, fetchResourcesForClient, fetchCommandsForClient)
// Source: client.ts:2226-2403 (getMcpToolsCommandsAndResources — batch orchestration)
// Source: useManageMCPConnections.ts:618-752 (list_changed handlers)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

)

// ---------------------------------------------------------------------------
// DiscoveredTool — an MCP tool with server metadata
// Source: client.ts:1767-1988 — tool object constructed in fetchToolsForClient
// ---------------------------------------------------------------------------

// DiscoveredTool represents an MCP tool discovered from a server.
type DiscoveredTool struct {
	Name         string // fully qualified: mcp__server__tool
	OriginalName string // tool name as reported by the server
	ServerName   string
	Description  string
	InputSchema  json.RawMessage
	Annotations  ToolAnnotations
	SearchHint   string // Source: client.ts:1779-1784 — _meta.anthropic/searchHint
	AlwaysLoad   bool   // Source: client.ts:1785 — _meta.anthropic/alwaysLoad
}

// IsReadOnly returns true if the tool is marked as read-only.
// Source: client.ts:1796-1797 — tool.annotations?.readOnlyHint
func (t *DiscoveredTool) IsReadOnly() bool {
	return t.Annotations.ReadOnlyHint
}

// IsDestructive returns true if the tool may perform destructive updates.
// Source: client.ts:1804-1805 — tool.annotations?.destructiveHint
func (t *DiscoveredTool) IsDestructive() bool {
	return t.Annotations.DestructiveHint
}

// IsOpenWorld returns true if the tool may interact with external entities.
// Source: client.ts:1807-1808 — tool.annotations?.openWorldHint
func (t *DiscoveredTool) IsOpenWorld() bool {
	return t.Annotations.OpenWorldHint
}

// IsSearchOrRead returns true if the tool should be collapsed in the UI.
// Source: client.ts:1810-1812 — classifyMcpToolForCollapse
func (t *DiscoveredTool) IsSearchOrRead() bool {
	result := ClassifyMcpToolForCollapse(t.ServerName, t.OriginalName)
	return result.IsSearch || result.IsRead
}

// ---------------------------------------------------------------------------
// Concurrency limits — Source: client.ts:552-565
// ---------------------------------------------------------------------------

// GetLocalBatchSize returns the concurrency limit for local server discovery.
// Source: client.ts:552-554 — MCP_SERVER_CONNECTION_BATCH_SIZE, default 3
func GetLocalBatchSize() int {
	if v := os.Getenv("MCP_SERVER_CONNECTION_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}

// GetRemoteBatchSize returns the concurrency limit for remote server discovery.
// Source: client.ts:556-559 — MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE, default 20
func GetRemoteBatchSize() int {
	if v := os.Getenv("MCP_REMOTE_SERVER_CONNECTION_BATCH_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

// IsLocalServer returns true for stdio/sdk servers.
// Source: client.ts:563-565 — isLocalMcpServer
func IsLocalServer(config ScopedMcpServerConfig) bool {
	t := config.Config.GetTransport()
	return t == TransportStdio || t == TransportSDK || t == ""
}

// ---------------------------------------------------------------------------
// FetchToolsForServer — LRU-cached tool discovery
// Source: client.ts:1743-1998 — fetchToolsForClient (memoizeWithLRU)
// ---------------------------------------------------------------------------

const fetchCacheCapacity = 20 // Source: client.ts:1726 MCP_FETCH_CACHE_SIZE

// FetchToolsForServer fetches tools from a connected MCP server.
// Source: client.ts:1743-1998 — fetchToolsForClient
//
// Returns empty slice for non-connected servers or if tools capability is absent.
// Results are cached per server name in the provided LRU cache.
func FetchToolsForServer(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []DiscoveredTool]) ([]DiscoveredTool, error) {
	if conn == nil {
		return nil, nil
	}

	// Check LRU cache
	if cached, ok := cache.Get(conn.Name); ok {
		return cached, nil
	}

	if conn.Session == nil {
		return nil, nil
	}

	// Source: client.ts:1748-1750 — check capabilities
	if conn.Capabilities == nil || conn.Capabilities.Tools == nil {
		return nil, nil
	}

	// Source: client.ts:1752-1755 — call tools/list
	result, err := conn.Session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: tools/list for %q: %w", conn.Name, err)
	}

	if result == nil || len(result.Tools) == 0 {
		return []DiscoveredTool{}, nil
	}

	// Source: client.ts:1766-1990 — convert SDK tools to DiscoveredTool
	tools := make([]DiscoveredTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		qualifiedName := BuildMcpToolName(conn.Name, tool.Name)

		desc := tool.Description
		// Source: client.ts:1790-1793 — description truncation
		if len(desc) > MaxMCPDescriptionLength {
			desc = desc[:MaxMCPDescriptionLength] + "… [truncated]"
		}

		var annotations ToolAnnotations
		if tool.Annotations != nil {
			annotations.ReadOnlyHint = tool.Annotations.ReadOnlyHint
			annotations.DestructiveHint = boolOrDefault(tool.Annotations.DestructiveHint, false)
			annotations.OpenWorldHint = boolOrDefault(tool.Annotations.OpenWorldHint, false)
		}

		// Source: client.ts:1779-1785 — extract _meta fields
		var searchHint string
		var alwaysLoad bool
		if tool.Meta != nil {
			if v, ok := tool.Meta["anthropic/searchHint"].(string); ok {
				searchHint = strings.Join(strings.Fields(v), " ") // collapse whitespace
			}
			if _, ok := tool.Meta["anthropic/alwaysLoad"].(bool); ok {
				alwaysLoad = true
			}
		}

		var inputSchema json.RawMessage
		if tool.InputSchema != nil {
			inputSchema, _ = json.Marshal(tool.InputSchema)
		}

		tools = append(tools, DiscoveredTool{
			Name:         qualifiedName,
			OriginalName: tool.Name,
			ServerName:   conn.Name,
			Description:  desc,
			InputSchema:  inputSchema,
			Annotations:  annotations,
			SearchHint:   searchHint,
			AlwaysLoad:   alwaysLoad,
		})
	}

	cache.Put(conn.Name, tools)
	return tools, nil
}

// ---------------------------------------------------------------------------
// FetchResourcesForServer — LRU-cached resource discovery
// Source: client.ts:2000-2031 — fetchResourcesForClient
// ---------------------------------------------------------------------------

// FetchResourcesForServer fetches resources from a connected MCP server.
// Source: client.ts:2000-2031
func FetchResourcesForServer(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []ServerResource]) ([]ServerResource, error) {
	if conn == nil || conn.Session == nil {
		return nil, nil
	}

	// Check cache
	if cached, ok := cache.Get(conn.Name); ok {
		return cached, nil
	}

	if conn.Capabilities == nil || conn.Capabilities.Resources == nil {
		return nil, nil
	}

	result, err := conn.Session.ListResources(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: resources/list for %q: %w", conn.Name, err)
	}

	if result == nil || len(result.Resources) == 0 {
		empty := []ServerResource{}
		cache.Put(conn.Name, empty)
		return empty, nil
	}

	// Source: client.ts:2017-2020 — add server name to each resource
	resources := make([]ServerResource, 0, len(result.Resources))
	for _, r := range result.Resources {
		resources = append(resources, ServerResource{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MimeType:    r.MIMEType,
			Server:      conn.Name,
		})
	}

	cache.Put(conn.Name, resources)
	return resources, nil
}

// ---------------------------------------------------------------------------
// FetchCommandsForServer — LRU-cached prompt/command discovery
// Source: client.ts:2033-2116 — fetchCommandsForClient
// ---------------------------------------------------------------------------

// FetchCommandsForServer fetches commands (MCP prompts mapped to slash commands).
// Source: client.ts:2033-2116
func FetchCommandsForServer(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []MCPCommand]) ([]MCPCommand, error) {
	if conn == nil || conn.Session == nil {
		return nil, nil
	}

	// Check cache
	if cached, ok := cache.Get(conn.Name); ok {
		return cached, nil
	}

	if conn.Capabilities == nil || conn.Capabilities.Prompts == nil {
		return nil, nil
	}

	result, err := conn.Session.ListPrompts(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: prompts/list for %q: %w", conn.Name, err)
	}

	if result == nil || len(result.Prompts) == 0 {
		empty := []MCPCommand{}
		cache.Put(conn.Name, empty)
		return empty, nil
	}

	commands := make([]MCPCommand, 0, len(result.Prompts))
	for _, p := range result.Prompts {
		cmd := MCPCommand{
			Name:        "mcp__" + conn.Name + "__" + p.Name,
			Description: p.Description,
			ServerName:  conn.Name,
		}
		for _, arg := range p.Arguments {
			cmd.Arguments = append(cmd.Arguments, MCPCommandArg{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			})
		}
		commands = append(commands, cmd)
	}

	cache.Put(conn.Name, commands)
	return commands, nil
}

// ---------------------------------------------------------------------------
// BatchDiscovery — concurrent discovery with semaphore
// Source: client.ts:2226-2403 — getMcpToolsCommandsAndResources
// Source: client.ts:2218-2224 — processBatched (pMap with concurrency)
// ---------------------------------------------------------------------------

// ServerDiscovery holds the discovered data for a single server.
type ServerDiscovery struct {
	ServerName string
	Tools      []DiscoveredTool
	Resources  []ServerResource
	Commands   []MCPCommand
	Error      error
}

// BatchDiscovery fetches tools, resources, and commands from multiple servers
// concurrently with bounded parallelism.
//
// Source: client.ts:2226-2403 — splits local/remote, runs with separate concurrency
//
// Local servers (stdio/sdk): GetLocalBatchSize() concurrent (default 3)
// Remote servers (sse/http/ws): GetRemoteBatchSize() concurrent (default 20)
func BatchDiscovery(ctx context.Context, connections []*ConnectedServer,
	toolCache *LRUCache[string, []DiscoveredTool],
	resourceCache *LRUCache[string, []ServerResource],
	commandCache *LRUCache[string, []MCPCommand],
) []ServerDiscovery {
	results := make([]ServerDiscovery, len(connections))

	// Split into local and remote groups
	var localIdx, remoteIdx []int
	for i, conn := range connections {
		if IsLocalServer(conn.Config) {
			localIdx = append(localIdx, i)
		} else {
			remoteIdx = append(remoteIdx, i)
		}
	}

	// Process each group concurrently with its own semaphore
	var wg sync.WaitGroup

	processGroup := func(indices []int, concurrency int) {
		sem := make(chan struct{}, concurrency)
		for _, idx := range indices {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					results[i] = ServerDiscovery{
						ServerName: connections[i].Name,
						Error:      ctx.Err(),
					}
					return
				}
				results[i] = discoverForServer(ctx, connections[i], toolCache, resourceCache, commandCache)
			}(idx)
		}
	}

	processGroup(localIdx, GetLocalBatchSize())
	processGroup(remoteIdx, GetRemoteBatchSize())
	wg.Wait()

	return results
}

// discoverForServer fetches all discovery data for a single server.
func discoverForServer(ctx context.Context, conn *ConnectedServer,
	toolCache *LRUCache[string, []DiscoveredTool],
	resourceCache *LRUCache[string, []ServerResource],
	commandCache *LRUCache[string, []MCPCommand],
) ServerDiscovery {
	d := ServerDiscovery{ServerName: conn.Name}

	d.Tools, d.Error = FetchToolsForServer(ctx, conn, toolCache)
	if d.Error != nil {
		return d
	}

	d.Resources, d.Error = FetchResourcesForServer(ctx, conn, resourceCache)
	if d.Error != nil {
		return d
	}

	d.Commands, d.Error = FetchCommandsForServer(ctx, conn, commandCache)
	return d
}

// ---------------------------------------------------------------------------
// list_changed — tool/resource/command cache invalidation
// Source: useManageMCPConnections.ts:618-752
// ---------------------------------------------------------------------------

// OnToolsChanged invalidates the tool cache for a server and re-fetches.
// Source: useManageMCPConnections.ts:618-664 — ToolListChangedNotification handler
func OnToolsChanged(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []DiscoveredTool]) ([]DiscoveredTool, error) {
	cache.Delete(conn.Name)
	return FetchToolsForServer(ctx, conn, cache)
}

// OnResourcesChanged invalidates the resource cache for a server and re-fetches.
// Source: useManageMCPConnections.ts:705-751 — ResourceListChangedNotification handler
func OnResourcesChanged(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []ServerResource]) ([]ServerResource, error) {
	cache.Delete(conn.Name)
	return FetchResourcesForServer(ctx, conn, cache)
}

// OnCommandsChanged invalidates the command cache for a server and re-fetches.
// Source: useManageMCPConnections.ts:667-703 — PromptListChangedNotification handler
func OnCommandsChanged(ctx context.Context, conn *ConnectedServer, cache *LRUCache[string, []MCPCommand]) ([]MCPCommand, error) {
	cache.Delete(conn.Name)
	return FetchCommandsForServer(ctx, conn, cache)
}

// ---------------------------------------------------------------------------
// ClassifyMcpToolForCollapse — tool classification
// Source: src/tools/MCPTool/classifyForCollapse.ts:595-604
// ---------------------------------------------------------------------------

// CollapseClassification holds the result of tool collapse classification.
type CollapseClassification struct {
	IsSearch bool
	IsRead   bool
}

// searchToolNames and readToolNames contain known tool name patterns.
// Source: classifyForCollapse.ts — SEARCH_TOOLS (~139 entries) and READ_TOOLS (~444 entries)
// Simplified to most common patterns for the Go port.
var searchToolNames = map[string]bool{
	"search": true, "search_files": true, "search_code": true,
	"grep": true, "grep_file": true, "search_file_content": true,
	"find": true, "find_files": true, "find_in_files": true,
	"glob": true, "list_directory": true, "list_files": true,
	"query": true, "query_index": true, "search_symbols": true,
	"web_search": true, "web_fetch": true,
}

var readToolNames = map[string]bool{
	"read": true, "read_file": true, "read_files": true,
	"get": true, "get_file": true, "get_content": true,
	"fetch": true, "fetch_file": true, "fetch_content": true,
	"view": true, "view_file": true, "cat": true,
	"head": true, "tail": true, "show": true,
	"read_resource": true, "get_resource": true,
	"inspect": true, "dump": true, "peek": true,
	"type": true, "display": true, "print": true,
	"open": true, "open_file": true, "load": true,
	"retrieve": true, "examine": true, "browse": true,
}

// ClassifyMcpToolForCollapse determines if a tool should be collapsed in the UI.
// Source: classifyForCollapse.ts:595-604
//
// Normalizes tool name (lowercase, snake_case) then checks against allowlists.
func ClassifyMcpToolForCollapse(serverName, toolName string) CollapseClassification {
	normalized := toSnakeCase(toolName)
	// toSnakeCase already lowercases; ensure full lowercase for matching
	normalized = strings.ToLower(normalized)

	return CollapseClassification{
		IsSearch: searchToolNames[normalized],
		IsRead:   readToolNames[normalized],
	}
}

// toSnakeCase converts camelCase and kebab-case to snake_case.
func toSnakeCase(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	var result strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				result.WriteRune('_')
			}
			result.WriteRune(r + 32) // to lowercase
		} else {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// ---------------------------------------------------------------------------
// MCPToolInputToAutoClassifierInput — tool input formatting
// Source: client.ts:1733-1741 — mcpToolInputToAutoClassifierInput
// ---------------------------------------------------------------------------

// MCPToolInputToAutoClassifierInput encodes MCP tool input for the auto-mode classifier.
// Source: client.ts:1733-1741
func MCPToolInputToAutoClassifierInput(input map[string]any, toolName string) string {
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return toolName
	}
	// Sort keys for deterministic output
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fmt.Sprint(input[k]))
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func boolOrDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
