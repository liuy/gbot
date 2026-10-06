package lsptool

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
)

func resolveSymbolPosition(ctx context.Context, reg *lsp.Registry, symbol, wd, root, ext string) (string, lsp.Position, error) {
	if symbol == "" {
		return "", lsp.Position{}, fmt.Errorf("symbol parameter required")
	}

	sym, occurrence := parseSymbolOccurrence(symbol)
	return resolveInWorkspace(ctx, reg, sym, occurrence, wd, root, ext)
}

func resolveInWorkspace(ctx context.Context, reg *lsp.Registry, symbol string, occurrence int, wd, root, ext string) (string, lsp.Position, error) {
	specs := reg.Snapshot()
	if len(specs) == 0 {
		return "", lsp.Position{}, fmt.Errorf("no language server configured")
	}

	// rootScoped: ask servers rooted at `root`. Only safe when we know the
	// extension the caller means — otherwise a foreign root would spawn a full
	// process for every configured server for a file no server claims.
	rootScoped := false
	if ext != "" {
		var filtered []lsp.ServerSpec
		for _, s := range specs {
			if slices.Contains(s.FileExts, ext) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			specs = filtered
			rootScoped = true
		}
	}

	var matches []symbolMatch
	for _, spec := range specs {
		var c *lsp.Client
		var err error
		if rootScoped {
			c, err = reg.ForSpecInRoot(ctx, spec, root)
		} else {
			c, err = reg.ForSpec(ctx, spec)
		}
		if err != nil {
			continue
		}
		symbols, err := lsp.WorkspaceSymbol(ctx, c, symbol)
		if err != nil {
			continue
		}
		for _, s := range symbols {
			if symbolNameMatches(s.Name, symbol) {
				matches = append(matches, symbolMatch{
					uri: s.Location.URI,
					pos: s.Location.Range.Start,
				})
			}
		}
	}

	if len(matches) == 0 {
		// Name the root the search actually used. Without a claimed extension
		// the query ran in the launch workspace, and blaming `root` would send
		// the caller to a project that was never asked.
		queried := root
		if !rootScoped {
			queried = reg.DefaultRoot()
		}
		return "", lsp.Position{}, fmt.Errorf("symbol %q not found in %s (pass file=<a path inside the project you mean> to query a different project)", symbol, queried)
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

// symbolNameMatches checks if the returned symbol name matches the query.
// gopls returns qualified names like "Store.LoadMessagesAfterSeq" or
// "(*mockEngine).Messages". Match the last dot-separated component.
func symbolNameMatches(returned, query string) bool {
	if returned == query {
		return true
	}
	if last := returned[strings.LastIndex(returned, ".")+1:]; last != returned && last == query {
		return true
	}
	return false
}
