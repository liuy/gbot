package engine

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// The sessionMemory extractor spawns a sub-engine from a background goroutine
// while the main turn is mid-applyBudget; both touch the parent's
// contentReplacementState maps (clone iterates, enforce writes). Without
// serialization this is concurrent map iteration+write — Go throws
// "concurrent map iteration and map write", killing the process.
//
// Shape notes (measured, not guessed): a small map clones in microseconds and
// escapes detection in ~90% of runs, so the state is pre-seeded to widen the
// iteration window; pairing is 1:1 per round because concurrent enforces
// never happen in production (single call site, one turn loop per engine) —
// an enforce-vs-enforce fan-out would test a race that cannot occur.
func TestBudgetCloneRace(t *testing.T) {
	t.Parallel()
	eng := New(&Params{
		Provider:   &testProvider{},
		Model:      "test",
		WorkingDir: t.TempDir(),
	})
	t.Cleanup(func() { eng.Close() })

	for i := range 2000 {
		eng.contentReplacementState.SeenIDs[fmt.Sprintf("seed-%04d", i)] = true
	}

	mkMsgs := func(round int) []types.Message {
		var blocks []types.ContentBlock
		for i := range 20 {
			id := fmt.Sprintf("tu-%d-%d", round, i)
			blocks = append(blocks, types.ContentBlock{
				Type:      "tool_result",
				ToolUseID: id,
				Content:   json.RawMessage(`"payload ` + id + `"`),
			})
		}
		return []types.Message{{Role: "user", Content: blocks}}
	}

	for round := range 100 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			eng.applyBudget(mkMsgs(round))
		}()
		go func() {
			defer wg.Done()
			eng.NewSubEngine(SubEngineOptions{})
		}()
		wg.Wait()
	}
}
