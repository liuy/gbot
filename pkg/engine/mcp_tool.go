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

// SearchHint returns a short, curated summary for the tool, or empty string if none.
func (t *MCPTool) SearchHint() string {
	return t.info.SearchHint
}

// IsSearchOrRead classifies MCP tools for TUI collapse behavior.
// Delegates to the existing DiscoveredTool classification.
func (t *MCPTool) IsSearchOrRead(_ json.RawMessage) tool.SearchReadKind {
	if t.info.IsSearchOrRead() {
		return tool.SearchReadKind{IsSearch: true}
	}
	return tool.SearchReadKind{}
}

func (t *MCPTool) Name() string                              { return t.info.Name }
func (t *MCPTool) Aliases() []string                         { return nil }
func (t *MCPTool) InputSchema() json.RawMessage              { return t.info.InputSchema }
func (t *MCPTool) IsEnabled() bool                           { return true }
func (t *MCPTool) InterruptBehavior() tool.InterruptBehavior { return tool.InterruptCancel }
func (t *MCPTool) MaxResultSize() int                        { return 50000 }
func (t *MCPTool) Prompt() string                            { return "" }

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

func (t *MCPTool) DecodeResult(raw json.RawMessage) (any, error) {
	text, err := tool.UnmarshalSingleBlock(raw)
	if err != nil {
		return nil, err
	}
	// MCPTool's result type is string; FormatWireBlocksOrDefault JSON-encodes
	// it, so the wire text is itself a JSON-encoded string. Unwrap once more.
	var s string
	if json.Unmarshal([]byte(text), &s) == nil {
		return s, nil
	}
	return text, nil
}

// Call routes the tool invocation through MCP.
// Source: client.ts:3029-3245 — callMCPTool
func (t *MCPTool) Call(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	// Get connection from registry, with single reconnect attempt on "not found".
	// This handles the case where a config reload or transient disconnect removed
	// the connection — Reconnect re-establishes it from the stored config.
	conn, ok := t.registry.GetConnection(t.info.ServerName)
	if !ok {
		var reconnectErr error
		conn, reconnectErr = t.registry.Reconnect(ctx, t.info.ServerName)
		if reconnectErr != nil {
			return nil, fmt.Errorf("mcp: server %q not found (reconnect failed: %w)", t.info.ServerName, reconnectErr)
		}
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

	// Build OnProgress closure if tctx provides one.
	// Routes MCP progress notifications → engine's EventToolOutputDelta → TUI update.
	var onProgress func(mcp.MCPProgress)
	if tctx != nil && tctx.OnProgress != nil {
		onProgress = func(p mcp.MCPProgress) {
			msg := mcp.FormatMCPProgress(p)
			if msg == "" {
				return // skip empty progress updates
			}
			tctx.OnProgress(tool.ProgressUpdate{Lines: []string{msg}})
		}
	}

	// Call through MCP protocol
	result, err := mcp.CallMCPTool(ctx, mcp.CallMCPToolParams{
		Server:     cs,
		ToolName:   t.info.OriginalName,
		Args:       args,
		OnProgress: onProgress,
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
func (t *MCPTool) CheckPermissions(_ json.RawMessage, _ *tool.ToolUseContext) types.PermissionResult {
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

// IsDeferred implements IsDeferredTool interface.
// MCP tools are deferred by default unless AlwaysLoad=true.
// Source: TS prompt.ts:62-68 — alwaysLoad → false, isMcp → true
func (t *MCPTool) IsDeferred() bool {
	return !t.info.AlwaysLoad
}

func (t *MCPTool) IsConcurrencySafe(_ json.RawMessage) bool {
	// Remote MCP tools are concurrency-safe (they don't share local state).
	return true
}

// Summary implements tool.ToolWithSummary.
// Returns SearchHint (curated short text) or extracts common params from input.
func (t *MCPTool) Summary(input json.RawMessage) string {
	if t.info.SearchHint != "" {
		return t.info.SearchHint
	}
	return extractCommonParams(string(input))
}

// extractCommonParams tries common parameter field names from potentially incomplete JSON.
func extractCommonParams(input string) string {
	for _, field := range []string{"url", "query", "file_path", "pattern", "command", "path"} {
		if v := extractJSONStringField(input, field, "", 60); v != "" {
			return v
		}
	}
	return ""
}
