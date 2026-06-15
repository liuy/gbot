package lsptool

import (
	"context"
	"time"

	"github.com/liuy/gbot/pkg/lsp"
)

// referencesRetryCount mirrors omp REFERENCES_RETRY_COUNT (index.ts:362).
// Total attempts = 1 + retries.
const referencesRetryCount = 2

// referencesRetryDelay mirrors omp REFERENCES_RETRY_DELAY_MS (index.ts:363).
const referencesRetryDelay = 250 * time.Millisecond

// referenceContextLimit mirrors omp REFERENCE_CONTEXT_LIMIT (index.ts:360).
// The first N references get context lines; the rest are listed plain.
const referenceContextLimit = 50

// isProjectAwareLspServer mirrors omp isProjectAwareLspServer (index.ts:300-302).
// In omp this is `!createClient && !isLinter`. gbot has no separate linter
// channel, but ServerSpec carries IsLinter for parity with omp's defaults.json
// (biome, swiftlint, etc. set isLinter:true). Non-linters are treated as
// project-aware: references/definition/rename may need to wait for the project
// to finish indexing.
func isProjectAwareLspServer(s lsp.ServerSpec) bool {
	return !s.IsLinter
}

// waitForProjectLoaded gives a slow server a chance to publish diagnostics
// before the caller retries. gbot does not yet track diagnostics counters, so
// we sleep context-aware as an interim. Unlike time.Sleep, it respects ctx
// cancellation so an interrupt can break out of a retry cycle.
func waitForProjectLoaded(ctx context.Context) error {
	select {
	case <-time.After(referencesRetryDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isOnlyQueriedDeclaration reports whether the reference list is the trivial
// "this is the symbol's own declaration" singleton — which signals that a
// project-aware server has not yet finished indexing. Mirrors omp
// isOnlyQueriedDeclaration (index.ts:373-376).
func isOnlyQueriedDeclaration(locs []lsp.Location, uri string, pos lsp.Position) bool {
	if len(locs) != 1 {
		return false
	}
	if locs[0].URI != uri {
		return false
	}
	return rangeContainsPosition(locs[0].Range, pos)
}

func comparePosition(a, b lsp.Position) int {
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	return a.Character - b.Character
}

func rangeContainsPosition(rng lsp.Range, pos lsp.Position) bool {
	return comparePosition(rng.Start, pos) <= 0 && comparePosition(pos, rng.End) <= 0
}
