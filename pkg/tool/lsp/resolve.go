package lsptool

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
)

func resolveSymbolPosition(ctx context.Context, reg *lsp.Registry, symbol, wd string) (string, lsp.Position, error) {
	if symbol == "" {
		return "", lsp.Position{}, fmt.Errorf("symbol parameter required")
	}

	sym, occurrence := parseSymbolOccurrence(symbol)
	return resolveInWorkspace(ctx, reg, sym, occurrence, wd)
}

func resolveInWorkspace(ctx context.Context, reg *lsp.Registry, symbol string, occurrence int, wd string) (string, lsp.Position, error) {
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return "", lsp.Position{}, fmt.Errorf("no language server configured")
	}

	var matches []symbolMatch
	for _, spec := range specs {
		c, err := reg.ForSpec(ctx, spec)
		if err != nil {
			continue
		}
		symbols, err := lsp.WorkspaceSymbol(ctx, c, symbol)
		if err != nil {
			continue
		}
		for _, s := range symbols {
			if s.Name == symbol {
				matches = append(matches, symbolMatch{
					uri: s.Location.URI,
					pos: s.Location.Range.Start,
				})
			}
		}
	}

	if len(matches) == 0 {
		return "", lsp.Position{}, fmt.Errorf("symbol %q not found in workspace", symbol)
	}
	if occurrence > len(matches) {
		return "", lsp.Position{}, fmt.Errorf("symbol %q occurrence %d not found (found %d)", symbol, occurrence, len(matches))
	}

	m := matches[occurrence-1]
	return m.uri, m.pos, nil
}

type symbolMatch struct {
	uri string
	pos lsp.Position
}

// parseSymbolOccurrence splits "name#N" into name and 1-indexed occurrence.
func parseSymbolOccurrence(symbol string) (name string, occurrence int) {
	if idx := strings.LastIndex(symbol, "#"); idx > 0 {
		if n, err := strconv.Atoi(symbol[idx+1:]); err == nil && n > 0 {
			return symbol[:idx], n
		}
	}
	return symbol, 1
}
