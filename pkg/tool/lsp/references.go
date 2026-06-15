package lsptool

import (
	"context"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/lsp"
	"github.com/liuy/gbot/pkg/tool"
)

// references queries textDocument/references, with a project-aware retry
// loop, and formats results with context for the first N and plain listings
// for the rest. Mirrors omp action="references" (index.ts:2224-2270).
func references(ctx context.Context, c *lsp.Client, uri string, pos lsp.Position, wd string, spec lsp.ServerSpec) (*tool.ToolResult, error) {
	const contextLines = 1

	var locs []lsp.Location
	var lastErr error
	projectAware := isProjectAwareLspServer(spec)
	for attempt := 0; attempt <= referencesRetryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := lsp.References(ctx, c, uri, pos)
		if err != nil {
			lastErr = err
			break
		}
		locs = result
		if !projectAware || attempt == referencesRetryCount {
			break
		}
		// Project-aware servers may return only the declaration on the first
		// call before indexing completes. Retry if we got nothing meaningful.
		if len(locs) > 0 && !isOnlyQueriedDeclaration(locs, uri, pos) {
			break
		}
		if err := waitForProjectLoaded(ctx); err != nil {
			return nil, fmt.Errorf("references: %w", err)
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("references: %w", lastErr)
	}
	if len(locs) == 0 {
		return &tool.ToolResult{Data: "No references found"}, nil
	}

	locs = filterGitIgnored(ctx, locs, wd)
	if len(locs) == 0 {
		return &tool.ToolResult{Data: "No references found (all gitignored)"}, nil
	}

	totalCount := len(locs)
	contextual := locs
	plain := []lsp.Location{}
	if totalCount > referenceContextLimit {
		contextual = locs[:referenceContextLimit]
		plain = locs[referenceContextLimit:]
	}

	contextualLines := lsp.FormatLocationsWithContext(contextual, wd, contextLines)
	plainLines := make([]string, 0, len(plain))
	for _, l := range plain {
		plainLines = append(plainLines, "  "+lsp.FormatLocation(l, wd))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d reference(s):\n\n", totalCount)
	for _, f := range contextualLines {
		fmt.Fprintf(&b, "  %s\n\n", f)
	}
	if len(plainLines) > 0 {
		fmt.Fprintf(&b, "  ... %d additional reference(s) shown without context\n", len(plainLines))
		for _, p := range plainLines {
			fmt.Fprintf(&b, "%s\n", p)
		}
	}
	return &tool.ToolResult{Data: b.String()}, nil
}
