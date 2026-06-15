package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// definition queries textDocument/definition for the symbol at uri:pos.
// Mirrors omp action="definition" (index.ts:2153-2175).
func definition(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, wd string) (*tool.ToolResult, error) {
	const contextLines = 1
	locs, err := lsp.Definition(ctx, c, uri, pos)
	if err != nil {
		return nil, fmt.Errorf("definition: %w", err)
	}
	locs = filterGitIgnored(ctx, locs, wd)
	if len(locs) == 0 {
		return &tool.ToolResult{Data: "No definition found"}, nil
	}
	formatted := lsp.FormatLocationsWithContext(locs, wd, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d definition(s):\n\n", len(locs))
	for _, f := range formatted {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

// typeDefinition queries textDocument/typeDefinition.
// Mirrors omp action="type_definition" (index.ts:2177-2200).
func typeDefinition(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, wd string) (*tool.ToolResult, error) {
	const contextLines = 1
	raw, err := c.Request(ctx, "textDocument/typeDefinition", map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     pos,
	})
	if err != nil {
		return nil, fmt.Errorf("type definition: %w", err)
	}
	locs, err := decodeLocations(raw)
	if err != nil {
		return nil, err
	}
	locs = filterGitIgnored(ctx, locs, wd)
	if len(locs) == 0 {
		return &tool.ToolResult{Data: "No type definition found"}, nil
	}
	formatted := lsp.FormatLocationsWithContext(locs, wd, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d type definition(s):\n\n", len(locs))
	for _, f := range formatted {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

// implementation queries textDocument/implementation.
// Mirrors omp action="implementation" (index.ts:2201-2223).
func implementation(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, wd string) (*tool.ToolResult, error) {
	const contextLines = 1
	locs, err := lsp.Implementation(ctx, c, uri, pos)
	if err != nil {
		return nil, fmt.Errorf("implementation: %w", err)
	}
	locs = filterGitIgnored(ctx, locs, wd)
	if len(locs) == 0 {
		return &tool.ToolResult{Data: "No implementation found"}, nil
	}
	formatted := lsp.FormatLocationsWithContext(locs, wd, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d implementation(s):\n\n", len(locs))
	for _, f := range formatted {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	return &tool.ToolResult{Data: b.String()}, nil
}
