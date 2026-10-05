package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

// The mid-turn queued message echo renders persistently gray (the model sees it
// as a special reminder forever, so the UI matches): the BlockUser text must
// carry styleDim's ANSI color (256-color 246), not plain terminal text.
// lipgloss strips colors under the test env's Ascii profile, so the profile
// is forced for this test.
func TestBlockUserRendering_DimGray(t *testing.T) {
	forceColorProfile(t)

	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.status.SetStreaming(true)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("LLM text")

	_, _ = app.updateRepl(attachmentMsg{
		UserText:   "gray forever",
		SourceUUID: "uuid-gray",
	})

	v := app.View()
	if !strings.Contains(v, "gray forever") {
		t.Fatalf("View should contain the queued message text, got:\n%s", v)
	}
	if !strings.Contains(v, "\x1b[38;5;246mgray forever") {
		t.Errorf("queued message text must be wrapped in styleDim (gray), got:\n%q", v)
	}
}

// forceColorProfile makes lipgloss emit ANSI colors under `go test` (no TTY),
// restored via t.Cleanup.
func forceColorProfile(t *testing.T) {
	t.Helper()
	backup := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(backup) })
}
