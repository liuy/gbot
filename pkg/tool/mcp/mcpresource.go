// Package mcp provides tool.Tool adapters for MCP resource operations
// (ListMcpResources and ReadMcpResource).
//
// Source: src/tools/ListMcpResources/ListMcpResources.ts (123 lines)
// Source: src/tools/ReadMcpResource/ReadMcpResource.ts (158 lines)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gbotmcp "github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ListMcpResources — Source: ListMcpResources.ts:40-123
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

// NewListMcpResources creates the ListMcpResources adapter.
func NewListMcpResources(reg *gbotmcp.Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "ListMcpResources",
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
		CheckPermissions_: func(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
			return types.PermissionAllowDecision{}
		},
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		IsEnabled_:         func() bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100_000,
		RenderResult_: func(data any) string {
			return renderListResourcesTUI(data)
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			return []types.ContentBlock{types.NewTextBlock(renderResourceResultJSON(data))}
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var resources []gbotmcp.ServerResource
			if err := json.Unmarshal([]byte(text), &resources); err != nil {
				return nil, err
			}
			return resources, nil
		},
		ShouldDefer_: true,
		SearchHint_:  "list resources from connected MCP servers",
	})
}

// ---------------------------------------------------------------------------
// ReadMcpResource — Source: ReadMcpResource.ts:49-158
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

// NewReadMcpResource creates the ReadMcpResource adapter.
func NewReadMcpResource(reg *gbotmcp.Registry) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "ReadMcpResource",
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
		CheckPermissions_: func(json.RawMessage, *tool.ToolUseContext) types.PermissionResult {
			return types.PermissionAllowDecision{}
		},
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		IsEnabled_:         func() bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100_000,
		RenderResult_: func(data any) string {
			return renderReadResourceTUI(data)
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			return []types.ContentBlock{types.NewTextBlock(renderResourceResultJSON(data))}
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var contents []gbotmcp.ResourceContent
			if err := json.Unmarshal([]byte(text), &contents); err != nil {
				return nil, err
			}
			return contents, nil
		},
		ShouldDefer_: true,
		SearchHint_:  "read a specific MCP resource by URI",
	})
}

// ---------------------------------------------------------------------------
// Shared rendering helpers
// ---------------------------------------------------------------------------

// emptyResourcesMessage matches TS mapToolResultToToolResultBlockParam for empty results.
// Source: ListMcpResources.ts:114
const emptyResourcesMessage = "No resources found. MCP servers may still provide tools even if they have no resources."

// renderResourceResultJSON renders tool result data as JSON (pretty-printed).
// Used by FormatWireBlocks_ for LLM consumption.
// For empty results, returns a friendly message instead.
func renderResourceResultJSON(data any) string {
	if data == nil {
		return emptyResourcesMessage
	}

	// Check for empty slice
	if slice, ok := data.([]gbotmcp.ServerResource); ok && len(slice) == 0 {
		return emptyResourcesMessage
	}
	if slice, ok := data.([]gbotmcp.ResourceContent); ok && len(slice) == 0 {
		return "[]"
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		b, _ = json.Marshal(data)
	}
	return string(b)
}

// renderListResourcesTUI renders resource list for TUI display.
// Groups resources by server, shows URI and MIME type per line.
func renderListResourcesTUI(data any) string {
	switch v := data.(type) {
	case []gbotmcp.ServerResource:
		return formatResourceList(v)
	default:
		return emptyResourcesMessage
	}
}

func formatResourceList(resources []gbotmcp.ServerResource) string {
	if len(resources) == 0 {
		return emptyResourcesMessage
	}

	// Group by server
	groups := make(map[string][]gbotmcp.ServerResource)
	servers := make([]string, 0, 4)
	for _, r := range resources {
		if _, exists := groups[r.Server]; !exists {
			servers = append(servers, r.Server)
		}
		groups[r.Server] = append(groups[r.Server], r)
	}
	sort.Strings(servers)

	var sb strings.Builder
	totalCount := 0
	for _, srv := range servers {
		res := groups[srv]
		totalCount += len(res)
		fmt.Fprintf(&sb, "%s (%d resources):\n", srv, len(res))
		for _, r := range res {
			mime := ""
			if r.MimeType != "" {
				mime = " (" + r.MimeType + ")"
			}
			fmt.Fprintf(&sb, "  %s%s\n", r.URI, mime)
		}
	}
	fmt.Fprintf(&sb, "(%d resources from %d servers)", totalCount, len(servers))
	return sb.String()
}

// renderReadResourceTUI renders resource content for TUI display.
// Shows header with URI and MIME type, then content text.
func renderReadResourceTUI(data any) string {
	switch v := data.(type) {
	case []gbotmcp.ResourceContent:
		return formatResourceContent(v)
	default:
		return ""
	}
}

func formatResourceContent(contents []gbotmcp.ResourceContent) string {
	if len(contents) == 0 {
		return "[]"
	}

	var sb strings.Builder
	for _, c := range contents {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		prefix := ""
		if c.MimeType != "" {
			prefix = " (" + c.MimeType + ")"
		}
		fmt.Fprintf(&sb, "[%s]%s\n", c.URI, prefix)
		if c.Text != "" {
			sb.WriteString(c.Text)
		} else if c.BlobSavedTo != "" {
			fmt.Fprintf(&sb, "(binary content saved to %s)", c.BlobSavedTo)
		}
	}
	return sb.String()
}
