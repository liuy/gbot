// Package mcp provides tool.Tool adapters for MCP resource operations
// (ListMcpResourcesTool and ReadMcpResourceTool).
//
// Source: src/tools/ListMcpResourcesTool/ListMcpResourcesTool.ts (123 lines)
// Source: src/tools/ReadMcpResourceTool/ReadMcpResourceTool.ts (158 lines)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ListMcpResourcesTool — Source: ListMcpResourcesTool.ts:40-123
// ---------------------------------------------------------------------------

const listMcpResourcesDescription = "List available resources from configured MCP servers"

var listMcpResourcesInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {
      "type": "string",
      "description": "Optional server name to filter resources by"
    }
  }
}`)

// NewListMcpResourcesTool creates the ListMcpResourcesTool adapter.
func NewListMcpResourcesTool(reg *gbotmcp.Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "ListMcpResourcesTool",
		Aliases_:     []string{},
		InputSchema_: func() json.RawMessage { return listMcpResourcesInputSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var args struct {
				Server string `json:"server"`
			}
			if len(input) > 0 && json.Unmarshal(input, &args) != nil {
				return listMcpResourcesDescription, nil
			}
			if args.Server != "" {
				return args.Server, nil
			}
			return listMcpResourcesDescription, nil
		},
		Prompt_: listMcpResourcesPrompt(),
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			var args struct {
				Server string `json:"server"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &args); err != nil {
					return nil, fmt.Errorf("mcp: invalid input: %w", err)
				}
			}

			resources, err := gbotmcp.ListMcpResources(ctx, reg, args.Server)
			if err != nil {
				return nil, err
			}
			return &tool.ToolResult{Data: resources}, nil
		},
		CheckPermissions_:  func(json.RawMessage, *tool.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} },
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		IsEnabled_:         func() bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100_000,
		RenderResult_: func(data any) string {
			return renderResourceResult(data, true)
		},
		FormatWireResult_: func(data any) string {
			return renderResourceResult(data, true)
		},
		ShouldDefer_: true,
		SearchHint_:  "list resources from connected MCP servers",
	})
}

// ---------------------------------------------------------------------------
// ReadMcpResourceTool — Source: ReadMcpResourceTool.ts:49-158
// ---------------------------------------------------------------------------

const readMcpResourceDescription = "Read a resource from an MCP server"

var readMcpResourceInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "server": {
      "type": "string",
      "description": "The MCP server name"
    },
    "uri": {
      "type": "string",
      "description": "The resource URI to read"
    }
  },
  "required": ["server", "uri"]
}`)

// NewReadMcpResourceTool creates the ReadMcpResourceTool adapter.
func NewReadMcpResourceTool(reg *gbotmcp.Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "ReadMcpResourceTool",
		Aliases_:     []string{},
		InputSchema_: func() json.RawMessage { return readMcpResourceInputSchema },
		Description_: func(input json.RawMessage) (string, error) {
			var args struct {
				Server string `json:"server"`
				URI    string `json:"uri"`
			}
			if len(input) > 0 && json.Unmarshal(input, &args) != nil {
				return readMcpResourceDescription, nil
			}
			if args.Server != "" && args.URI != "" {
				return args.Server + " " + args.URI, nil
			}
			if args.Server != "" {
				return args.Server, nil
			}
			return readMcpResourceDescription, nil
		},
		Prompt_: readMcpResourcePrompt(),
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			var args struct {
				Server string `json:"server"`
				URI    string `json:"uri"`
			}
			if len(input) > 0 {
				if err := json.Unmarshal(input, &args); err != nil {
					return nil, fmt.Errorf("mcp: invalid input: %w", err)
				}
			}
			if args.Server == "" {
				return nil, fmt.Errorf("mcp: server is required")
			}
			if args.URI == "" {
				return nil, fmt.Errorf("mcp: uri is required")
			}

			contents, err := gbotmcp.ReadMcpResource(ctx, reg, args.Server, args.URI)
			if err != nil {
				return nil, err
			}
			return &tool.ToolResult{Data: contents}, nil
		},
		CheckPermissions_:  func(json.RawMessage, *tool.ToolUseContext) types.PermissionResult { return types.PermissionAllowDecision{} },
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		IsEnabled_:         func() bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100_000,
		RenderResult_: func(data any) string {
			return renderResourceResult(data, false)
		},
		FormatWireResult_: func(data any) string {
			return renderResourceResult(data, false)
		},
		ShouldDefer_: true,
		SearchHint_:  "read a specific MCP resource by URI",
	})
}

// ---------------------------------------------------------------------------
// Shared rendering helpers
// ---------------------------------------------------------------------------

// emptyResourcesMessage matches TS mapToolResultToToolResultBlockParam for empty results.
// Source: ListMcpResourcesTool.ts:114
const emptyResourcesMessage = "No resources found. MCP servers may still provide tools even if they have no resources."

// renderResourceResult renders tool result data as JSON (pretty-printed).
// For ListMcpResourcesTool with empty results, returns a friendly message instead.
func renderResourceResult(data any, isEmptyFriendly bool) string {
	if data == nil {
		if isEmptyFriendly {
			return emptyResourcesMessage
		}
		return ""
	}

	// Check for empty slice
	if slice, ok := data.([]gbotmcp.ServerResource); ok && len(slice) == 0 && isEmptyFriendly {
		return emptyResourcesMessage
	}
	if slice, ok := data.([]gbotmcp.ResourceContent); ok && len(slice) == 0 {
		return "[]"
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		// Fallback to compact JSON
		b, _ = json.Marshal(data)
	}
	return string(b)
}
