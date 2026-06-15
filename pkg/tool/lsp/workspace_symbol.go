package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// workspaceSymbol queries symbols matching the query string across the project.
// Mirrors omp action="symbols" with file="*" (index.ts:1992-2057): queries each
// server, applies filterWorkspaceSymbols + dedupeWorkspaceSymbols, truncates.
func workspaceSymbol(ctx context.Context, reg *lsp.Registry, in Input, wd string) (*tool.ToolResult, error) {
	if in.Query == "" {
		return nil, fmt.Errorf("query parameter required for workspace_symbol")
	}

	specs := reg.Snapshot()
	if len(specs) == 0 {
		return nil, fmt.Errorf("no language server configured")
	}

	var allSymbols []lsp.SymbolInformation
	for _, spec := range specs {
		c, err := reg.ForSpec(ctx, spec)
		if err != nil {
			continue
		}
		symbols, err := lsp.WorkspaceSymbol(ctx, c, in.Query)
		if err != nil {
			continue
		}
		allSymbols = append(allSymbols, symbols...)
	}

	// Filter by query (case-insensitive substring on name/container/path).
	allSymbols = filterWorkspaceSymbols(allSymbols, in.Query, wd)
	// Dedupe across servers that return the same symbol.
	allSymbols = dedupeWorkspaceSymbols(allSymbols)

	if len(allSymbols) == 0 {
		return &tool.ToolResult{Data: fmt.Sprintf("No symbols matching %q", in.Query)}, nil
	}

	limit := 200
	total := len(allSymbols)
	truncated := false
	if total > limit {
		allSymbols = allSymbols[:limit]
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d symbol(s) matching %q:\n\n", len(allSymbols), in.Query)
	for _, s := range allSymbols {
		formatSymbolInformation(&b, s, wd)
	}
	if truncated {
		fmt.Fprintf(&b, "... %d more (truncated)\n", total-limit)
	}
	return &tool.ToolResult{Data: b.String()}, nil
}

// filterWorkspaceSymbols keeps only symbols whose name, container, or file path
// contains the query (case-insensitive). Mirrors omp filterWorkspaceSymbols
// (utils.ts:460-466).
func filterWorkspaceSymbols(symbols []lsp.SymbolInformation, query, wd string) []lsp.SymbolInformation {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return symbols
	}
	out := symbols[:0]
	for _, s := range symbols {
		filePath := lsp.URItoPath(s.Location.URI)
		if strings.Contains(strings.ToLower(s.Name), needle) ||
			strings.Contains(strings.ToLower(s.ContainerName), needle) ||
			strings.Contains(strings.ToLower(filePath), needle) {
			out = append(out, s)
		}
	}
	return out
}

// dedupeWorkspaceSymbols removes duplicates by (name, container, kind, uri, line, col).
// Mirrors omp dedupeWorkspaceSymbols (utils.ts:469-481).
func dedupeWorkspaceSymbols(symbols []lsp.SymbolInformation) []lsp.SymbolInformation {
	seen := make(map[string]bool)
	out := make([]lsp.SymbolInformation, 0, len(symbols))
	for _, s := range symbols {
		key := fmt.Sprintf("%s:%s:%d:%s:%d:%d",
			s.Name, s.ContainerName, s.Kind,
			s.Location.URI,
			s.Location.Range.Start.Line, s.Location.Range.Start.Character)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}
