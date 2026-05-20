package tui

import (
	"strings"
	"testing"
)

// TestBlockUserStreamingAppendsToAssistant verifies that mid-turn attachment drain
// appends a BlockUser to the current assistant message (not a new user message).
func TestBlockUserStreamingAppendsToAssistant(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetStreaming(true)

	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "queued msg",
		SourceUUID: "uuid-1",
	})

	// No user message created
	msgs := app.repl.messages
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		t.Fatalf("expected assistant message, got role=%q", last.Role)
	}

	// BlockUser appended to assistant message
	found := false
	for _, b := range last.Blocks {
		if b.Type == BlockUser {
			found = true
			if b.Text != "queued msg" {
				t.Errorf("BlockUser text = %q, want %q", b.Text, "queued msg")
			}
		}
	}
	if !found {
		t.Fatal("BlockUser not found in assistant message blocks")
	}
}

// TestBlockUserRendering_SingleLine verifies single-line rendering with ❯ prefix.
func TestBlockUserRendering_SingleLine(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("LLM text")

	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "user input",
		SourceUUID: "uuid-2",
	})

	v := app.View()
	if !strings.Contains(v, "❯ user input") {
		t.Errorf("View should contain '❯ user input', got:\n%s", v)
	}
}

// TestBlockUserRendering_MultiLine verifies multi-line rendering:
// first line gets ❯, continuation lines get indent alignment.
func TestBlockUserRendering_MultiLine(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("LLM text")

	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "line1\nline2\nline3",
		SourceUUID: "uuid-3",
	})

	v := app.View()
	if !strings.Contains(v, "❯ line1") {
		t.Errorf("View should contain '❯ line1', got:\n%s", v)
	}
	// Continuation lines should be indented, not have ❯ prefix
	indent := strings.Repeat(" ", renderedPromptWidth)
	if !strings.Contains(v, indent+"line2") {
		t.Errorf("View should contain indented 'line2', got:\n%s", v)
	}
	if strings.Contains(v, "❯ line2") {
		t.Error("continuation line should NOT have ❯ prefix")
	}
}

// TestBlockUserNotCreatedAfterStreaming verifies processAttachments path
// still creates a proper user message (not BlockUser).
func TestBlockUserNotCreatedAfterStreaming(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.FinishStream(nil)

	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "post-query msg",
		SourceUUID: "uuid-4",
	})

	last := app.repl.messages[len(app.repl.messages)-1]
	if last.Role != "user" {
		t.Fatalf("expected user message, got role=%q", last.Role)
	}
	// Should be BlockText, not BlockUser
	for _, b := range last.Blocks {
		if b.Type == BlockUser {
			t.Error("processAttachments path should NOT create BlockUser")
		}
	}
}
