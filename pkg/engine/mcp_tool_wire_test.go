package engine

import (
	"testing"

	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// mcpToolWireBlocks routes *MCPTool through the tool.Tool interface the
// engine uses and returns its ToolWithWireBlocks view.
func mcpToolWireBlocks(t *testing.T) tool.ToolWithWireBlocks {
	t.Helper()
	var asTool tool.Tool = NewMCPTool(mcp.DiscoveredTool{}, nil)
	wb, ok := asTool.(tool.ToolWithWireBlocks)
	if !ok {
		t.Fatal("MCPTool must implement ToolWithWireBlocks")
	}
	return wb
}

// Source: MCPTool.ts:70-76 — wire content is the result string verbatim;
// extractMCPText has already joined multi-block text into one string.
func TestMCPTool_FormatWireBlocks_StringPassesThrough(t *testing.T) {
	t.Parallel()
	blocks := mcpToolWireBlocks(t).FormatWireBlocks("result text\nsecond line")
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks[0].Type = %q, want %q", blocks[0].Type, types.ContentTypeText)
	}
	if blocks[0].Text != "result text\nsecond line" {
		t.Errorf("block text = %q, want %q", blocks[0].Text, "result text\nsecond line")
	}
}

// Non-string data keeps the JSON fallback so anything DecodeResult
// reconstructs still serializes instead of panicking.
func TestMCPTool_FormatWireBlocks_NonStringFallsBackToJSON(t *testing.T) {
	t.Parallel()
	blocks := mcpToolWireBlocks(t).FormatWireBlocks(42)
	if len(blocks) != 1 || blocks[0].Type != types.ContentTypeText {
		t.Fatalf("blocks = %+v, want one text block", blocks)
	}
	if blocks[0].Text != "42" {
		t.Errorf("block text = %q, want %q", blocks[0].Text, "42")
	}
}
