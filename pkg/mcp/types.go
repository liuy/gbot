// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
// Source: src/services/mcp/ (22 files, ~12K lines TS)
//
// This file: types.ts (259 lines) — all type definitions.
// No external dependencies — pure Go types and JSON unmarshaling.
package mcp

import (
	"encoding/json"
	"fmt"
	"sync"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// Transport — Source: types.ts:23-26 TransportSchema
// ---------------------------------------------------------------------------

// Transport represents the MCP transport type.
// Source: types.ts:23 — z.enum(['stdio', 'sse', 'sse-ide', 'http', 'ws', 'sdk'])
type Transport string

const (
	TransportStdio        Transport = "stdio"
	TransportSSE          Transport = "sse"
	TransportSSEIDE       Transport = "sse-ide"
	TransportHTTP         Transport = "http"
	TransportWS           Transport = "ws"
	TransportWSIDE        Transport = "ws-ide"
	TransportSDK          Transport = "sdk"
	TransportClaudeAIProxy Transport = "claudeai-proxy" // Source: types.ts:116-122
)

// ---------------------------------------------------------------------------
// ConfigScope — Source: types.ts:10-21 ConfigScopeSchema
// ---------------------------------------------------------------------------

// ConfigScope represents the configuration source scope.
// Source: types.ts:10-21 — 7 values with specific precedence order.
type ConfigScope string

const (
	ScopeLocal      ConfigScope = "local"
	ScopeUser       ConfigScope = "user"
	ScopeProject    ConfigScope = "project"
	ScopeDynamic    ConfigScope = "dynamic"
	ScopeEnterprise ConfigScope = "enterprise"
	ScopeClaudeAI   ConfigScope = "claudeai"
	ScopeManaged    ConfigScope = "managed"
)

// ---------------------------------------------------------------------------
// McpServerConfig — discriminated union via interface
// Source: types.ts:28-135 McpServerConfigSchema (union of 8 schemas)
// ---------------------------------------------------------------------------

// McpServerConfig is the interface for all MCP server configuration types.
// Each concrete type implements Transport() to identify itself.
type McpServerConfig interface {
	GetTransport() Transport
}

// StdioConfig — Source: types.ts:28-35 McpStdioServerConfigSchema
// Note: TS has `type` field as optional for stdio (backwards compatibility).
type StdioConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func (c *StdioConfig) GetTransport() Transport { return TransportStdio }

// SSEConfig — Source: types.ts:58-66 McpSSEServerConfigSchema
type SSEConfig struct {
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	HeadersHelper string            `json:"headersHelper,omitempty"`
	OAuth         *OAuthConfig      `json:"oauth,omitempty"`
}

func (c *SSEConfig) GetTransport() Transport { return TransportSSE }

// SSEIDEConfig — Source: types.ts:69-76 McpSSEIDEServerConfigSchema
// Internal-only server type for IDE extensions.
type SSEIDEConfig struct {
	URL              string `json:"url"`
	IDEName          string `json:"ideName"`
	IDERunningInWindows bool  `json:"ideRunningInWindows,omitempty"`
}

func (c *SSEIDEConfig) GetTransport() Transport { return TransportSSEIDE }

// WSIDEConfig — Source: types.ts:79-87 McpWebSocketIDEServerConfigSchema
// Internal-only server type for IDE extensions.
type WSIDEConfig struct {
	URL              string `json:"url"`
	IDEName          string `json:"ideName"`
	AuthToken        string `json:"authToken,omitempty"`
	IDERunningInWindows bool  `json:"ideRunningInWindows,omitempty"`
}

func (c *WSIDEConfig) GetTransport() Transport { return TransportWSIDE }

// HTTPConfig — Source: types.ts:89-97 McpHTTPServerConfigSchema
type HTTPConfig struct {
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	HeadersHelper string            `json:"headersHelper,omitempty"`
	OAuth         *OAuthConfig      `json:"oauth,omitempty"`
}

func (c *HTTPConfig) GetTransport() Transport { return TransportHTTP }

// WSConfig — Source: types.ts:99-106 McpWebSocketServerConfigSchema
type WSConfig struct {
	URL           string            `json:"url"`
	Headers       map[string]string `json:"headers,omitempty"`
	HeadersHelper string            `json:"headersHelper,omitempty"`
}

func (c *WSConfig) GetTransport() Transport { return TransportWS }

// SDKConfig — Source: types.ts:108-113 McpSdkServerConfigSchema
type SDKConfig struct {
	Name string `json:"name"`
}

func (c *SDKConfig) GetTransport() Transport { return TransportSDK }

// ClaudeAIProxyConfig — Source: types.ts:116-122 McpClaudeAIProxyServerConfigSchema
// Config type for Claude.ai proxy servers. Out of scope for transport, but
// defined so .mcp.json files containing this type parse without error.
type ClaudeAIProxyConfig struct {
	URL string `json:"url"`
	ID  string `json:"id"`
}

func (c *ClaudeAIProxyConfig) GetTransport() Transport { return TransportClaudeAIProxy }

// ---------------------------------------------------------------------------
// OAuthConfig — Source: types.ts:43-56 McpOAuthConfigSchema
// ---------------------------------------------------------------------------

// OAuthConfig represents per-server OAuth configuration.
// Source: types.ts:43-56
type OAuthConfig struct {
	ClientID             string `json:"clientId,omitempty"`
	CallbackPort         int    `json:"callbackPort,omitempty"`
	AuthServerMetadataURL string `json:"authServerMetadataUrl,omitempty"`
	XAA                  bool   `json:"xaa,omitempty"` // Source: types.ts:41 — Cross-App Access flag
}

// ---------------------------------------------------------------------------
// ScopedMcpServerConfig — Source: types.ts:163-169
// ---------------------------------------------------------------------------

// ScopedMcpServerConfig wraps an MCP server config with scope provenance.
// Source: types.ts:163-169 — McpServerConfig & { scope, pluginSource? }
type ScopedMcpServerConfig struct {
	Config       McpServerConfig
	Scope        ConfigScope
	PluginSource string // Source: types.ts:167 — "e.g. 'slack@anthropic'"
}

// MarshalJSON implements json.Marshaler for ScopedMcpServerConfig.
// Merges the inner config JSON with scope and pluginSource fields.
func (s ScopedMcpServerConfig) MarshalJSON() ([]byte, error) {
	configBytes, err := json.Marshal(s.Config)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(configBytes, &raw); err != nil {
		return nil, err
	}
	raw["scope"] = json.RawMessage(`"` + string(s.Scope) + `"`)
	if s.PluginSource != "" {
		raw["pluginSource"] = json.RawMessage(`"` + s.PluginSource + `"`)
	}
	return json.Marshal(raw)
}

// ---------------------------------------------------------------------------
// McpJsonConfig — Source: types.ts:171-177
// ---------------------------------------------------------------------------

// McpJsonConfig represents the top-level .mcp.json file structure.
// Source: types.ts:171-175 — { mcpServers: Record<string, McpServerConfig> }
type McpJsonConfig struct {
	McpServers map[string]json.RawMessage `json:"mcpServers"`
}

// ---------------------------------------------------------------------------
// ServerConnection — discriminated union via interface
// Source: types.ts:180-226 MCPServerConnection (5 types)
// ---------------------------------------------------------------------------

// ServerConnection is the interface for all MCP server connection states.
// Source: types.ts:221-226 — union type with type discriminant.
type ServerConnection interface {
	ConnType() string
}

// ConnectedServer represents a successfully connected MCP server.
// Source: types.ts:180-192 ConnectedMCPServer
type ConnectedServer struct {
	Name         string
	Config       ScopedMcpServerConfig
	Session      *mcp.ClientSession
	Capabilities *mcp.ServerCapabilities
	ServerInfo   *ServerInfo
	Instructions string
	Cleanup      func() error
	closeOnce    sync.Once
}

// ServerInfo holds server identity from the initialize handshake.
// Source: types.ts:185-188 serverInfo?: { name, version }
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Close idempotently calls Cleanup via sync.Once.
// If Cleanup is nil, Close is a no-op.
func (s *ConnectedServer) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.Cleanup != nil {
			err = s.Cleanup()
		}
	})
	return err
}

func (s *ConnectedServer) ConnType() string { return "connected" }

// FailedServer represents a server that failed to connect.
// Source: types.ts:194-199 FailedMCPServer
type FailedServer struct {
	Name   string
	Config ScopedMcpServerConfig
	Error  string // Source: types.ts:198 — error? (optional in TS)
}

func (s *FailedServer) ConnType() string { return "failed" }

// NeedsAuthServer represents a server requiring authentication.
// Source: types.ts:201-205 NeedsAuthMCPServer
type NeedsAuthServer struct {
	Name   string
	Config ScopedMcpServerConfig
}

func (s *NeedsAuthServer) ConnType() string { return "needs-auth" }

// PendingServer represents a server pending approval/reconnection.
// Source: types.ts:207-213 PendingMCPServer
type PendingServer struct {
	Name                string
	Config              ScopedMcpServerConfig
	ReconnectAttempt    int // Source: types.ts:211 — optional
	MaxReconnectAttempts int // Source: types.ts:212 — optional
}

func (s *PendingServer) ConnType() string { return "pending" }

// DisabledServer represents a server that has been disabled.
// Source: types.ts:215-219 DisabledMCPServer
type DisabledServer struct {
	Name   string
	Config ScopedMcpServerConfig
}

func (s *DisabledServer) ConnType() string { return "disabled" }

// Compile-time interface checks.
var (
	_ ServerConnection = (*ConnectedServer)(nil)
	_ ServerConnection = (*FailedServer)(nil)
	_ ServerConnection = (*NeedsAuthServer)(nil)
	_ ServerConnection = (*PendingServer)(nil)
	_ ServerConnection = (*DisabledServer)(nil)
	_ McpServerConfig  = (*StdioConfig)(nil)
	_ McpServerConfig  = (*SSEConfig)(nil)
	_ McpServerConfig  = (*SSEIDEConfig)(nil)
	_ McpServerConfig  = (*WSIDEConfig)(nil)
	_ McpServerConfig  = (*HTTPConfig)(nil)
	_ McpServerConfig  = (*WSConfig)(nil)
	_ McpServerConfig  = (*SDKConfig)(nil)
	_ McpServerConfig  = (*ClaudeAIProxyConfig)(nil)
)

// ---------------------------------------------------------------------------
// Resource types — Source: types.ts:229
// ---------------------------------------------------------------------------

// ServerResource represents an MCP server resource.
// Source: types.ts:229 — Resource & { server: string }
// Resource from SDK has: uri, name, description?, mimeType?
type ServerResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Server      string `json:"server"` // appended by gbot, not in SDK Resource
}

// ---------------------------------------------------------------------------
// MCPToolInfo — tool name parsing result
// ---------------------------------------------------------------------------

// MCPToolInfo holds parsed server/tool names from an MCP tool string.
// Source: mcpStringUtils.ts:19-31 — { serverName, toolName? }
type MCPToolInfo struct {
	ServerName string
	ToolName   string // empty string if not present (TS uses undefined)
}

// ---------------------------------------------------------------------------
// MCPCommand — MCP prompt mapped to slash command
// Source: client.ts:2033-2116 (fetchCommandsForClient)
// ---------------------------------------------------------------------------

// MCPCommand represents an MCP prompt mapped to a slash command.
type MCPCommand struct {
	Name        string
	Description string
	Arguments   []MCPCommandArg
	ServerName  string
}

// MCPCommandArg represents an argument for an MCP command.
type MCPCommandArg struct {
	Name        string
	Description string
	Required    bool
}

// ---------------------------------------------------------------------------
// ToolAnnotations — Source: client.ts:1800-1830
// ---------------------------------------------------------------------------

// ToolAnnotations holds tool behavior hints from _meta.annotations.
type ToolAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// ---------------------------------------------------------------------------
// CLI State types — Source: types.ts:232-258
// ---------------------------------------------------------------------------

// SerializedTool represents a tool for CLI state serialization.
// Source: types.ts:232-244 SerializedTool
type SerializedTool struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	InputJSONSchema  json.RawMessage `json:"inputJSONSchema,omitempty"`
	IsMcp            bool            `json:"isMcp,omitempty"`
	OriginalToolName string          `json:"originalToolName,omitempty"`
}

// SerializedClient represents a client for CLI state serialization.
// Source: types.ts:246-250 SerializedClient
type SerializedClient struct {
	Name         string `json:"name"`
	Type         string `json:"type"` // "connected" | "failed" | "needs-auth" | "pending" | "disabled"
	Capabilities any    `json:"capabilities,omitempty"` // ServerCapabilities — typed in Step 9
}

// MCPCliState represents the full MCP state for CLI serialization.
// Source: types.ts:252-258 MCPCliState
type MCPCliState struct {
	Clients        []SerializedClient              `json:"clients"`
	Configs        map[string]ScopedMcpServerConfig `json:"configs"`
	Tools          []SerializedTool                `json:"tools"`
	Resources      map[string][]ServerResource     `json:"resources"`
	NormalizedNames map[string]string              `json:"normalizedNames,omitempty"`
}

// ---------------------------------------------------------------------------
// Connection lifecycle constants
// Source: client.ts:162-175, useManageMCPConnections.ts:367-464
// ---------------------------------------------------------------------------

const (
	// MaxReconnectAttempts is the maximum number of reconnection attempts.
	// Source: client.ts:162
	MaxReconnectAttempts = 5

	// InitialBackoffMs is the initial backoff duration in milliseconds.
	InitialBackoffMs = 1000

	// MaxBackoffMs is the maximum backoff duration in milliseconds.
	MaxBackoffMs = 30000

	// MaxErrorsBeforeReconnect is the consecutive error count triggering reconnect.
	// Source: client.ts:162
	MaxErrorsBeforeReconnect = 3

	// DefaultConnectionTimeoutMs is the default connection timeout in milliseconds.
	// Source: client.ts getConnectionTimeoutMs
	DefaultConnectionTimeoutMs = 30000

	// DefaultToolCallTimeoutMs is the default tool call timeout in nanoseconds (~27.8hr).
	// Source: client.ts getMcpToolTimeoutMs
	DefaultToolCallTimeoutMs = 100000000

	// MaxMCPDescriptionLength is the maximum tool description length before truncation.
	MaxMCPDescriptionLength = 2048
)

// ---------------------------------------------------------------------------
// Error types — Source: client.ts:311-315, client.ts:2813-2830
// ---------------------------------------------------------------------------

// McpAuthError represents an MCP authentication failure.
// Source: client.ts:311-315 — used to distinguish auth errors from connection failures.
type McpAuthError struct {
	ServerName string
	Message    string
}

func (e *McpAuthError) Error() string {
	return fmt.Sprintf("MCP auth error for %s: %s", e.ServerName, e.Message)
}

// McpToolCallError represents an MCP tool call failure with structured info.
// Source: client.ts:2813-2830 — carries tool name, server name, and original error.
type McpToolCallError struct {
	ServerName string
	ToolName   string
	Err        error
}

func (e *McpToolCallError) Error() string {
	return fmt.Sprintf("MCP tool call %s/%s failed: %v", e.ServerName, e.ToolName, e.Err)
}

func (e *McpToolCallError) Unwrap() error { return e.Err }

// ---------------------------------------------------------------------------
// MCPResultType — Source: client.ts:2680-2695
// ---------------------------------------------------------------------------

// MCPResultType enumerates MCP tool result content types.
type MCPResultType string

const (
	MCPResultText     MCPResultType = "text"
	MCPResultImage    MCPResultType = "image"
	MCPResultAudio    MCPResultType = "audio"
	MCPResultResource MCPResultType = "resource"
)

// TransformedMCPResult wraps a content block with its type discriminant.
// Source: client.ts:2697-2710
type TransformedMCPResult struct {
	Type    MCPResultType
	Content any // string text content or structured data

	// Fields for binary content (image, audio, blob)
	RawData  string // base64-encoded binary data
	MIMEType string // MIME type of the content
}

// ---------------------------------------------------------------------------
// UnmarshalServerConfig — discriminated union deserialization
// Source: types.ts:124-135 McpServerConfigSchema (z.union of 8 schemas)
// ---------------------------------------------------------------------------

// serverConfigType is a helper struct to extract the "type" field from JSON.
type serverConfigType struct {
	Type string `json:"type"`
}

// UnmarshalServerConfig detects the type field and unmarshals to the correct struct.
// Source: types.ts:124-135 — z.union discriminated by "type" field.
// For stdio, the type field is optional (defaults to "stdio").
// Source: types.ts:30 — type: z.literal('stdio').optional()
func UnmarshalServerConfig(data json.RawMessage) (McpServerConfig, error) {
	var t serverConfigType
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("mcp: invalid server config: %w", err)
	}

	// Source: types.ts:30 — stdio type is optional, defaults to "stdio"
	configType := t.Type
	if configType == "" {
		configType = "stdio"
	}

	switch configType {
	case "stdio":
		var cfg StdioConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid stdio config: %w", err)
		}
		if cfg.Command == "" {
			return nil, fmt.Errorf("mcp: stdio command cannot be empty")
		}
		return &cfg, nil
	case "sse":
		var cfg SSEConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid sse config: %w", err)
		}
		return &cfg, nil
	case "sse-ide":
		var cfg SSEIDEConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid sse-ide config: %w", err)
		}
		return &cfg, nil
	case "ws-ide":
		var cfg WSIDEConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid ws-ide config: %w", err)
		}
		return &cfg, nil
	case "http":
		var cfg HTTPConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid http config: %w", err)
		}
		return &cfg, nil
	case "ws":
		var cfg WSConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid ws config: %w", err)
		}
		return &cfg, nil
	case "sdk":
		var cfg SDKConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid sdk config: %w", err)
		}
		return &cfg, nil
	case "claudeai-proxy":
		var cfg ClaudeAIProxyConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: invalid claudeai-proxy config: %w", err)
		}
		return &cfg, nil
	default:
		return nil, fmt.Errorf("mcp: unknown server config type: %q", configType)
	}
}

// ---------------------------------------------------------------------------
// LoadMcpJsonConfig — parse a .mcp.json file
// Source: types.ts:171-175 McpJsonConfigSchema
// ---------------------------------------------------------------------------

// LoadMcpJsonConfig parses raw JSON as a .mcp.json config, returning
// a map of server name → McpServerConfig.
func LoadMcpJsonConfig(data []byte) (map[string]McpServerConfig, error) {
	var raw McpJsonConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("mcp: invalid .mcp.json: %w", err)
	}

	configs := make(map[string]McpServerConfig, len(raw.McpServers))
	for name, rawCfg := range raw.McpServers {
		cfg, err := UnmarshalServerConfig(rawCfg)
		if err != nil {
			return nil, fmt.Errorf("mcp: server %q: %w", name, err)
		}
		configs[name] = cfg
	}
	return configs, nil
}
