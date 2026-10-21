package tui

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestMultiEngine_BackgroundQueryEnd_AppendsStatsBlock verifies that when a
// background engine finishes its query, its ReplState records a BlockStats
// entry (tokens, tools, elapsed) so the user sees the same stats line after
// switching to that engine as they would have seen live.
func TestMultiEngine_BackgroundQueryEnd_AppendsStatsBlock(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	e2VS := a.engineMgr.Get("e2")
	if e2VS == nil {
		t.Fatal("e2 view state not found")
	}
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	drain := a.buildBackgroundDrainFn(e2VS)

	// Simulate one full query lifecycle on the background engine.
	drain(turnStartMsg{})
	drain(textDeltaMsg{Text: "hi"})
	drain(queryEndMsg{TotalUsage: types.Usage{
		InputTokens:          100,
		OutputTokens:         50,
		CacheReadInputTokens: 80,
	}})

	msgs := e2Repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages after background query end")
	}
	last := msgs[len(msgs)-1]
	var statsText string
	for _, blk := range last.Blocks {
		if blk.Type == BlockStats {
			statsText = blk.Text
			break
		}
	}
	if statsText == "" {
		t.Fatalf("last message has no BlockStats block; blocks=%+v", last.Blocks)
	}
	// Must include tokens (↑↓), otherwise we'd be appending an empty line.
	if !strings.Contains(statsText, "↑") || !strings.Contains(statsText, "↓") {
		t.Errorf("stats line missing token counters: %q", statsText)
	}
}

// TestMultiEngine_BackgroundQueryEnd_StatsReflectsUsage verifies the stats
// line reflects the usage passed via queryEndMsg, not zeros.
func TestMultiEngine_BackgroundQueryEnd_StatsReflectsUsage(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	drain := a.buildBackgroundDrainFn(e2VS)

	drain(turnStartMsg{})
	drain(queryEndMsg{TotalUsage: types.Usage{
		InputTokens:  5000,
		OutputTokens: 250,
	}})

	last := e2Repl.Messages()[len(e2Repl.Messages())-1]
	var statsText string
	for _, blk := range last.Blocks {
		if blk.Type == BlockStats {
			statsText = blk.Text
		}
	}
	if statsText == "" {
		t.Fatal("BlockStats missing — see TestMultiEngine_BackgroundQueryEnd_AppendsStatsBlock")
	}
	// FormatTokenCount(5000) = "4.9k" (divides by 1024), so accept either form.
	if !strings.Contains(statsText, "4.9k") && !strings.Contains(statsText, "5000") {
		t.Errorf("stats line should show ~5000 input tokens, got: %q", statsText)
	}
	if !strings.Contains(statsText, "250") {
		t.Errorf("stats line should show 250 output tokens, got: %q", statsText)
	}
}

// TestMultiEngine_Drain_UsageMsgDoesNotFeedStats pins the drain contract:
// usageMsg is not a drain case at all — a background engine's stats come
// solely from queryEndMsg.TotalUsage. If someone adds a usageMsg case to the
// drain, the background-agent guard must travel with it.
func TestMultiEngine_Drain_UsageMsgDoesNotFeedStats(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	e2VS := a.engineMgr.Get("e2")
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	drain := a.buildBackgroundDrainFn(e2VS)

	jobMeta := &types.AgentMeta{ParentToolUseID: "tu_bg", JobID: "agent-9"}
	drain(turnStartMsg{})
	drain(usageMsg{InputTokens: 999, Agent: jobMeta})
	drain(queryEndMsg{TotalUsage: types.Usage{InputTokens: 100}})

	last := e2Repl.Messages()[len(e2Repl.Messages())-1]
	var statsText string
	for _, blk := range last.Blocks {
		if blk.Type == BlockStats {
			statsText = blk.Text
		}
	}
	if statsText == "" {
		t.Fatal("BlockStats missing — see TestMultiEngine_BackgroundQueryEnd_AppendsStatsBlock")
	}
	if !strings.Contains(statsText, "100") {
		t.Errorf("stats line should show 100 input tokens, got: %q", statsText)
	}
	if strings.Contains(statsText, "999") {
		t.Errorf("stats line must not contain the drained usageMsg tokens (999), got: %q", statsText)
	}
}
