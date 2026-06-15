package lsptool

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
)

// resolveSymbolPosition resolves a symbol name to a file URI + LSP position.
// It supports two strategies:
//
//  1. File-scoped: when file is provided, queries documentSymbol for that file
//     and finds the symbol in the tree. Faster and disambiguates same-name
//     symbols across files.
//  2. Workspace-scoped: when no file is provided, queries workspace/symbol
//     across all servers and takes the first match.
//
// Both strategies support the `symbol#N` syntax to select the Nth occurrence
// when multiple symbols share the same name.
//
// When file is provided, reg must also be provided so the LSP client for that
// file can be obtained. When file is empty, only reg is used.
func resolveSymbolPosition(ctx context.Context, reg *lsp.Registry, symbol, file, wd string) (string, lsp.Position, error) {
	if symbol == "" {
		return "", lsp.Position{}, fmt.Errorf("symbol parameter required")
	}

	sym, occurrence := parseSymbolOccurrence(symbol)

	if file != "" {
		targetFile := resolvePath(file, wd)
		c, err := reg.ForFile(ctx, targetFile)
		if err != nil {
			return "", lsp.Position{}, fmt.Errorf("lsp for file: %w", err)
		}
		return resolveInFile(ctx, c, sym, occurrence, file, wd)
	}
	return resolveInWorkspace(ctx, reg, sym, occurrence, wd)
}

func resolveInFile(ctx context.Context, c *lsp.Client, symbol string, occurrence int, file, wd string) (string, lsp.Position, error) {
	uri := lsp.FileToURI(resolvePath(file, wd))

	syms, err := lsp.DocumentSymbols(ctx, c, uri)
	if err != nil {
		return "", lsp.Position{}, fmt.Errorf("documentSymbol for %s: %w", file, err)
	}

	var matches []symbolMatch
	collectDocumentSymbolMatches(syms, symbol, &matches, uri)
	if len(matches) == 0 {
		return "", lsp.Position{}, fmt.Errorf("symbol %q not found in %s", symbol, file)
	}
	if occurrence > len(matches) {
		return "", lsp.Position{}, fmt.Errorf("symbol %q occurrence %d not found in %s (found %d)", symbol, occurrence, file, len(matches))
	}

	m := matches[occurrence-1]
	return m.uri, m.pos, nil
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

// collectDocumentSymbolMatches walks the DocumentSymbol tree depth-first and
// collects entries whose name exactly matches the search symbol.
func collectDocumentSymbolMatches(syms []lsp.DocumentSymbol, symbol string, out *[]symbolMatch, uri string) {
	for i := range syms {
		s := &syms[i]
		if s.Name == symbol {
			*out = append(*out, symbolMatch{
				uri: uri,
				pos: s.SelectionRange.Start,
			})
		}
		if len(s.Children) > 0 {
			collectDocumentSymbolMatches(s.Children, symbol, out, uri)
		}
	}
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
