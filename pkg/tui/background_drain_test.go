package tui

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/engine"
)

// TestBackgroundDrain_UserMessage verifies that when an engine is in the
// background (not active), a userMessageMsg routed through its drain fn
// renders the user message in that engine's background ReplState.
// Connector-originated user messages must render even when the engine isn't
// active — otherwise the conversation is invisible until the user switches to
// the engine.
func TestBackgroundDrain_UserMessage(t *testing.T) {
	// Build a view state with a fresh ReplState for the background engine.
	repl := NewReplState()
	vs := &engine.EngineViewState{
		Engine: nil,
		Repl:   newReplAdapter(repl),
		ID:     "bg", Name: "background",
	}
	a := &App{}
	drain := a.buildBackgroundDrainFn(vs)

	// A connector user message arrives while this engine is backgrounded.
	drain(userMessageMsg{Text: "hello from wechat"})

	// The message must have been appended to the background ReplState.
	msgs := repl.Messages()
	if len(msgs) != 1 {
		t.Fatalf("background ReplState messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("message role = %q, want %q", msgs[0].Role, "user")
	}
	if len(msgs[0].Blocks) != 1 || msgs[0].Blocks[0].Text != "hello from wechat" {
		t.Errorf("message = %+v, want 1 block with text %q", msgs[0].Blocks, "hello from wechat")
	}
}

// TestBackgroundDrain_UserMessage_NilRepl verifies the nil-Repl guard: a view
// state with no Repl returns a no-op drain that doesn't panic.
func TestBackgroundDrain_UserMessage_NilRepl(t *testing.T) {
	vs := &engine.EngineViewState{
		Engine: nil,
		Repl:   nil, // no adapter
		ID:     "lazy", Name: "lazy",
	}
	a := &App{}
	drain := a.buildBackgroundDrainFn(vs)

	// Must not panic.
	drain(userMessageMsg{Text: "ignored"})
}

// TestBackgroundDrain_TextDeltaDoesNotStartQuery verifies that a textDelta
// (assistant streaming) in background does NOT add a user message — only
// userMessageMsg should. Guards against a regression where connector user
// messages and assistant deltas get conflated.
func TestBackgroundDrain_TextDeltaDoesNotStartQuery(t *testing.T) {
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

// TestActiveRepl_UserMessage verifies that on the ACTIVE engine, a
// userMessageMsg adds the user message to the active ReplState (mirroring the
// background behavior). This is the Step 5 path.
func TestActiveRepl_UserMessage(t *testing.T) {
	a := &App{
		repl: NewReplState(),
	}
	handled, _ := a.updateRepl(userMessageMsg{Text: "via hub"})
	if !handled {
		t.Fatal("updateRepl should handle userMessageMsg")
	}
	msgs := a.repl.Messages()
	if len(msgs) != 1 {
		t.Fatalf("active ReplState messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || len(msgs[0].Blocks) != 1 || msgs[0].Blocks[0].Text != "via hub" {
		t.Errorf("message = role=%q blocks=%+v, want role=user 1 block text=%q", msgs[0].Role, msgs[0].Blocks, "via hub")
	}
}

// TestActiveRepl_UserMessage_NoDoubleOnStreamMessage verifies that
// streamMessageMsg (EventQueryStart) does NOT itself add a user message —
// the connector's userMessageMsg already did, so double-render must be
// avoided.
func TestActiveRepl_UserMessage_NoDoubleOnStreamMessage(t *testing.T) {
	a := &App{
		repl: NewReplState(),
	}
	// Connector user message first.
	a.updateRepl(userMessageMsg{Text: "from wechat"})
	// Then the engine's QueryStart arrives (streamMessageMsg).
	a.updateRepl(streamMessageMsg{Role: "user"})

	msgs := a.repl.Messages()
	// Only ONE user message: the userMessageMsg. streamMessageMsg must not
	// add another (it only sets up the assistant message / streaming state).
	userCount := 0
	for _, m := range msgs {
		if m.Role == "user" && len(m.Blocks) > 0 && strings.Contains(m.Blocks[0].Text, "from wechat") {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected exactly 1 user message from wechat, got %d (double-render?)", userCount)
	}
}
