package tui

import (
	"testing"

	"github.com/liuy/gbot/pkg/engine"
)

// TestBackgroundDrain_TextDeltaDoesNotAddUserMessage verifies that a textDelta
// (assistant streaming) in background does NOT add a user message. Guards
// against a regression where assistant deltas get conflated with user input.
func TestBackgroundDrain_TextDeltaDoesNotAddUserMessage(t *testing.T) {
	repl := NewReplState()
	vs := &engine.EngineViewState{
		Engine: nil,
		Repl:   newReplAdapter(repl),
		ID:     "bg", Name: "background",
	}
	a := &App{}
	drain := a.buildBackgroundDrainFn(vs)

	drain(textDeltaMsg{Text: "assistant reply"})
	msgs := repl.Messages()
	if len(msgs) != 0 {
		t.Fatalf("textDelta should not add a user message, got %d messages", len(msgs))
	}
}
