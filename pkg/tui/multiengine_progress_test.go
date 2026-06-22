package tui

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestMultiEngine_QueryEnded_Background_StopsProgressLine: when an engine
// finishes its query while in background, switching to it must show NO
// progress line — the engine is idle, not streaming.
func TestMultiEngine_QueryEnded_Background_StopsProgressLine(t *testing.T) {
	t.Parallel()
	a, _, _ := newMultiEngineIntegrationApp(t)

	e2VS := a.engineMgr.Get("e2")
	if e2VS == nil {
		t.Fatal("e2 view state not found")
	}
	e2Repl := e2VS.Repl.(replSnapshotAdapter).r
	drain := a.buildBackgroundDrainFn(e2VS)

	// Engine-2 runs a query in background.
	drain(turnStartMsg{})
	drain(textDeltaMsg{Text: "thinking..."})
	drain(queryEndMsg{TotalUsage: types.Usage{InputTokens: 100, OutputTokens: 50}})

	// After FinishStream, e2's ReplState reports not streaming.
	if e2Repl.IsStreaming() {
		t.Fatal("e2 should not be streaming after queryEnd in background")
	}

	// Switch to e2.
	_, _ = a.switchEngine("e2")

	// Render the View and verify NO live progress line is shown.
	// The progress line has a spinner frame (braille pattern like ⠋) that
	// distinguishes it from the stats line (which is static message content).
	view := a.View()
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, "tokens") && hasSpinnerFrame(line) {
			t.Errorf("progress line should not render for idle engine; got snippet: %s", line)
		}
	}
}

// hasSpinnerFrame returns true if the line starts with a braille spinner
// character (⠋, ⠙, ⠹, etc.).
func hasSpinnerFrame(line string) bool {
	for _, r := range line {
		return r >= '\u2800' && r <= '\u28FF'
	}
	return false
}
