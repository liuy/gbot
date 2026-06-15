package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// rename issues textDocument/rename for a symbol and either applies or previews
// the result. Mirrors omp action="rename" (index.ts:2409-2441).
func rename(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, newName, wd string, apply bool, _ lsp.ServerSpec) (*tool.ToolResult, error) {
	if newName == "" {
		return nil, fmt.Errorf("new_name parameter required for rename")
	}

	edit, err := lsp.Rename(ctx, c, uri, pos, newName)
	if err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	if edit == nil {
		return &tool.ToolResult{Data: "Rename returned no edits"}, nil
	}

	if apply {
		changed, err := lsp.ApplyWorkspaceEdit(edit)
		if err != nil {
			return nil, fmt.Errorf("apply rename: %w", err)
		}
		var b strings.Builder
		b.WriteString("Applied rename:\n")
		for _, line := range changed {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		return &tool.ToolResult{Data: b.String()}, nil
	}

	preview := lsp.FormatWorkspaceEdit(edit, wd)
	var b strings.Builder
	b.WriteString("Rename preview:\n")
	for _, p := range preview {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	if len(preview) == 0 {
		b.WriteString("  (no edits)\n")
	}
	return &tool.ToolResult{Data: b.String()}, nil
}
