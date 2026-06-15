package lsptool

import (
	"context"
	"fmt"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// hover queries textDocument/hover for the symbol at uri:pos.
// Mirrors omp action="hover" (index.ts:2272-2289).
func hover(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position) (*tool.ToolResult, error) {
	h, err := lsp.HoverAt(ctx, c, uri, pos)
	if err != nil {
		return nil, fmt.Errorf("hover: %w", err)
	}
	if h == nil || h.Contents == nil {
		return &tool.ToolResult{Data: "No hover information"}, nil
	}

	text := extractHoverText(h.Contents)
	return &tool.ToolResult{Data: text}, nil
}
