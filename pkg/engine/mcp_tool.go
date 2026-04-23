// Package engine implements the core agentic loop.
//
// This file: MCP tool adapter — wraps MCP DiscoveredTool as tool.Tool
// so the engine can route mcp__-prefixed tool calls through MCP protocol.
package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ---------------------------------------------------------------------------
// MCPTool — adapts an MCP DiscoveredTool to the tool.Tool interface
// ---------------------------------------------------------------------------

// MCPTool wraps an MCP DiscoveredTool as a tool.Tool.
// When Call is invoked, it routes the call through the MCP protocol
// using the registry's connected server.
type MCPTool struct {
	info     mcp.DiscoveredTool
	registry *mcp.Registry
}

// NewMCPTool creates a tool.Tool adapter for an MCP DiscoveredTool.
func NewMCPTool(info mcp.DiscoveredTool, registry *mcp.Registry) *MCPTool {
	return &MCPTool{info: info, registry: registry}
}

func (t *MCPTool) Name() string                 { return t.info.Name }
func (t *MCPTool) Aliases() []string            { return nil }
func (t *MCPTool) InputSchema() json.RawMessage { return t.info.InputSchema }
func (t *MCPTool) IsEnabled() bool              { return true }
func (t *MCPTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *MCPTool) MaxResultSize() int           { return 50000 }
func (t *MCPTool) Prompt() string               { return "" }

func (t *MCPTool) Description(_ json.RawMessage) (string, error) {
	if t.info.SearchHint != "" {
		return t.info.SearchHint, nil
	}
	return t.info.Description, nil
}

func (t *MCPTool) RenderResult(data any) string {
	if data == nil {
		return ""
	}
	switch v := data.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// Call routes the tool invocation through MCP.
// Source: client.ts:3029-3245 — callMCPTool
func (t *MCPTool) Call(ctx context.Context, input json.RawMessage, _ *types.ToolUseContext) (*tool.ToolResult, error) {
	// Get connection from registry
	conn, ok := t.registry.GetConnection(t.info.ServerName)
	if !ok {
		return nil, fmt.Errorf("mcp: server %q not found", t.info.ServerName)
	}
	cs, ok := conn.(*mcp.ConnectedServer)
	if !ok {
		return nil, fmt.Errorf("mcp: server %q not connected (state: %s)", t.info.ServerName, conn.ConnType())
	}

	// Parse input args
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("mcp: invalid input for %q: %w", t.info.Name, err)
		}
	}

	// Call through MCP protocol
	result, err := mcp.CallMCPTool(ctx, mcp.CallMCPToolParams{
		Server:   cs,
		ToolName: t.info.OriginalName,
		Args:     args,
	})
	if err != nil {
		return nil, err
	}

	// Extract text from content blocks
	text := extractMCPText(result)

	return &tool.ToolResult{
		Data: text,
		MCPMeta: &tool.MCPMeta{
			Meta: result.Meta,
		},
	}, nil
}

// extractMCPText concatenates text content from MCP result blocks.
func extractMCPText(result *mcp.MCPToolCallResult) string {
	var text string
	for _, block := range result.Content {
		if tc, ok := block.(*mcpsdk.TextContent); ok {
			if text != "" {
				text += "\n"
			}
			text += tc.Text
		}
	}
	return text
}

// CheckPermissions implements permission gating for MCP tools.
// Source: TS getToolNameForPermissionCheck + destructiveHint prompt
func (t *MCPTool) CheckPermissions(_ json.RawMessage, _ *types.ToolUseContext) types.PermissionResult {
	// MCP tools are allowed by default; destructive tools require confirmation
	// handled at a higher level via the destructiveHint annotation.
	return types.PermissionAllowDecision{}
}

func (t *MCPTool) IsReadOnly(_ json.RawMessage) bool {
	return t.info.IsReadOnly()
}

func (t *MCPTool) IsDestructive(_ json.RawMessage) bool {
	return t.info.IsDestructive()
}

func (t *MCPTool) IsConcurrencySafe(_ json.RawMessage) bool {
	// Remote MCP tools are concurrency-safe (they don't share local state).
	return true
}
