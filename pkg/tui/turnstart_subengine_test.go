package tui

import (
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// TestSubEngineTurnStartDoesNotCorruptStreaming verifies the bug where a
// sub-engine's turnStartMsg arriving while the TUI is idle would trigger
// StartQuery(), leaving the TUI stuck in streaming=true with no matching
// FinishStream(). This caused subsequent LLM text to be appended to a user
// message instead of an assistant message, rendering with ❯ prefix.
//
// Root cause chain:
//  1. Sub-engine processAttachments → runTurns emits turnStartMsg (no Agent)
//  2. TUI idle → turnStartMsg handler calls StartQuery() → streaming=true
//  3. Sub-engine queryEndMsg{Agent!=nil} returns early, no FinishStream()
//  4. TUI stuck streaming=true → user input becomes attachment → {Role:"user"}
//  5. Main engine turnStartMsg → streaming=true → no StartQuery() → AppendTextItem() → lastMsg() is user msg
//  6. AppendChunk() appends LLM text to user msg → rendered with ❯
func TestSubEngineTurnStartDoesNotCorruptStreaming(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Simulate: main query was active and ended normally
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.FinishStream(nil)
	msgCountBefore := len(app.repl.messages)

	if app.repl.IsStreaming() {
		t.Fatal("after FinishStream, streaming should be false")
	}

	// Simulate: sub-engine's turnStartMsg arrives (processAttachments → runTurns)
	subAgent := &types.AgentMeta{
		ParentToolUseID: "call_sub_123",
		AgentType:       "background",
		Depth:           1,
	}
	_, _ = app.updateRepl(turnStartMsg{Agent: subAgent})

	// Sub-engine turnStart must not start streaming
	if app.repl.IsStreaming() {
		t.Fatal("sub-engine turnStartMsg should NOT trigger StartQuery — streaming must stay false")
	}

	// Verify no phantom assistant message was created by the sub-engine turnStart
	if len(app.repl.messages) != msgCountBefore {
		t.Fatalf("sub-engine turnStartMsg should not add messages: before=%d after=%d",
			msgCountBefore, len(app.repl.messages))
	}
}

// TestMainEngineTurnStartStartsQuery verifies that main engine's turnStartMsg
// (Agent=nil) still correctly starts streaming when TUI is idle.
func TestMainEngineTurnStartStartsQuery(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// TUI is idle
	if app.repl.IsStreaming() {
		t.Fatal("should start idle")
	}

	// Main engine turnStartMsg (no Agent metadata)
	_, _ = app.updateRepl(turnStartMsg{Agent: nil})

	if !app.repl.IsStreaming() {
		t.Fatal("main engine turnStartMsg should start streaming when idle")
	}

	// Verify assistant message was created
	msgs := app.repl.messages
	if len(msgs) == 0 {
		t.Fatal("expected an assistant message to be created")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		t.Fatalf("expected assistant message, got role=%q", last.Role)
	}
}

// TestSubEngineTurnStartIgnoredWhileStreaming verifies that a sub-engine
// turnStartMsg arriving during an active main query is a no-op.
func TestSubEngineTurnStartIgnoredWhileStreaming(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Main query is active
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	msgCount := len(app.repl.messages)

	subAgent := &types.AgentMeta{
		ParentToolUseID: "call_sub_456",
		AgentType:       "fork",
		Depth:           1,
	}
	_, _ = app.updateRepl(turnStartMsg{Agent: subAgent})

	// Should not add any new messages
	if len(app.repl.messages) != msgCount {
		t.Fatalf("sub-engine turnStartMsg during main streaming should not add messages: before=%d after=%d",
			msgCount, len(app.repl.messages))
	}
}
