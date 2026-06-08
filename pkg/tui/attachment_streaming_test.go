package tui

import (
	"strings"
	"testing"
)

// TestAttachmentMsgDuringStreamingDoesNotCreateUserMessage reproduces the bug
// where mid-turn attachment drain (engine.go:1114) arrives while streaming=true,
// causing attachmentMsg handler to create a user MessageView. This makes
// lastMsg() non-assistant, so subsequent AppendChunk/AppendTextItem write LLM
// text into a user message — rendered with ❯ prefix.
//
// Root cause: attachmentMsg handler unconditionally creates user message.
// Correct behavior: only create user message when !streaming (processAttachments path).
func TestAttachmentMsgDuringStreamingDoesNotCreateUserMessage(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Simulate main query active
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	msgCountBefore := len(app.repl.messages)

	// Mid-turn drain sends attachmentMsg while streaming
	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "queued user input",
		SourceUUID: "uuid-1",
	})

	// attachmentMsg during streaming must NOT create a user message
	if len(app.repl.messages) != msgCountBefore {
		t.Fatalf("attachmentMsg during streaming should not add messages: before=%d after=%d",
			msgCountBefore, len(app.repl.messages))
	}

	// Verify streaming state is unchanged
	if !app.repl.IsStreaming() {
		t.Fatal("streaming should still be true after mid-turn attachment")
	}
}

// TestAttachmentMsgAfterStreamingCreatesUserMessage verifies that
// processAttachments path (streaming=false) correctly creates a user message.
func TestAttachmentMsgAfterStreamingCreatesUserMessage(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Simulate query finished
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.FinishStream(nil)

	if app.repl.IsStreaming() {
		t.Fatal("streaming should be false after FinishStream")
	}

	msgCountBefore := len(app.repl.messages)

	// processAttachments sends attachmentMsg after streaming ended
	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "post-query user input",
		SourceUUID: "uuid-2",
	})

	// Should create user message
	if len(app.repl.messages) != msgCountBefore+1 {
		t.Fatalf("attachmentMsg after streaming should add one user message: before=%d after=%d",
			msgCountBefore, len(app.repl.messages))
	}

	last := app.repl.messages[len(app.repl.messages)-1]
	if last.Role != "user" {
		t.Fatalf("expected user message, got role=%q", last.Role)
	}
	if len(last.Blocks) == 0 || last.Blocks[0].Type != BlockText {
		t.Fatal("expected text block in user message")
	}
	if last.Blocks[0].Text != "post-query user input" {
		t.Fatalf("expected text %q, got %q", "post-query user input", last.Blocks[0].Text)
	}
}

// Regression: ESC + FinishStream left streaming=false, so turnStartMsg from
// processAttachments had to bootstrap the full streaming setup + spinner tick.
func TestProcessAttachmentsAfterEsc_RestartsStreaming(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Simulate normal query start
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.cancelFunc = app.engine.Abort

	// ESC: calls engine.Abort(), then queryEndMsg calls FinishStream
	app.repl.cancelFunc()
	app.repl.FinishStream(nil)

	if app.repl.IsStreaming() {
		t.Fatal("streaming should be false after ESC + FinishStream")
	}

	// processAttachments sends attachmentMsg, then turnStartMsg
	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "auto-processed attachment",
		SourceUUID: "uuid-att-1",
	})
	_, _ = app.updateRepl(turnStartMsg{})

	if !app.repl.IsStreaming() {
		t.Fatal("streaming should be true after processAttachments turnStart — timer freezes")
	}
}

// Regression: ESC used to nil-out cancelFunc, breaking subsequent processAttachments cancellation.
func TestProcessAttachmentsAfterEsc_HasCancelFunction(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Simulate normal query start
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.cancelFunc = app.engine.Abort

	// ESC: calls engine.Abort(), then queryEndMsg calls FinishStream
	app.repl.cancelFunc()
	app.repl.FinishStream(nil)

	// cancelFunc is engine.Abort — never nil'd, always callable
	if app.repl.cancelFunc == nil {
		t.Fatal("cancelFunc should be non-nil — engine.Abort is always available")
	}

	// processAttachments starts, cancelFunc still valid
	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "auto-processed attachment",
		SourceUUID: "uuid-att-2",
	})

	// Defensive: attachmentMsg handler must not clear cancelFunc
	if app.repl.cancelFunc == nil {
		t.Fatal("cancelFunc should survive attachmentMsg handling unchanged")
	}
}

// TestMidTurnAttachmentThenLLMTextDoesNotCorrupt verifies the full bug chain:
// mid-turn attachment → user message created → LLM text appended to wrong message.
// Expected behavior: mid-turn attachment is ignored, LLM text goes to assistant message.
func TestMidTurnAttachmentThenLLMTextDoesNotCorrupt(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})

	// Main query streaming
	app.repl.StartQuery()
	app.status.SetStreaming(true)

	// Mid-turn attachment drain arrives
	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "queued msg",
		SourceUUID: "uuid-3",
	})

	// LLM continues streaming text
	app.repl.AppendChunk("LLM response text")

	// Verify: LLM text must be in assistant message, not user
	last := app.repl.messages[len(app.repl.messages)-1]
	if last.Role != "assistant" {
		t.Fatalf("LLM text appended to non-assistant message (role=%q). "+
			"This means attachmentMsg during streaming created a user message, "+
			"making lastMsg() non-assistant.", last.Role)
	}

	hasLLMText := false
	for _, b := range last.Blocks {
		if b.Type == BlockText && strings.Contains(b.Text, "LLM response text") {
			hasLLMText = true
			break
		}
	}
	if !hasLLMText {
		t.Fatal("LLM text not found in assistant message blocks")
	}
}
