package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/liuy/gbot/pkg/memory/short"
)

// TestApp_WindowSizeMsg_DuringStream_NoCommitRegress reproduces the blank
// render bug. Root cause: WindowSizeMsg handler at app.go:843 uses
// `committedCount == 0 && len(messages) > 0` to detect "first resize after
// session resume", but the same condition is true during the first query
// after /clear (or any fresh session) once AddUserMessage + StartQuery have
// appended messages.
//
// Symptom: any SIGWINCH during stream silently sets
// committedCount = len(messages), making uncommitted empty → View() blank.
//
// Production repro: /clear → /goal → (resize window mid-stream) → TUI blank.
// Deterministic, not a race.
func TestApp_WindowSizeMsg_DuringStream_NoCommitRegress(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	app.SetStore(store, "session-resume", "/project")

	// Simulate /clear: empty messages, committedCount=0.
	if cmd := app.handleSlashCommand(SlashCommand{Name: "clear"}, nil); cmd == nil {
		t.Fatal("/clear returned nil cmd")
	}
	if len(app.repl.messages) != 0 {
		t.Fatalf("after /clear, messages should be empty; got %d", len(app.repl.messages))
	}

	// Simulate the state right after user submits a fresh query:
	// AddUserMessage + StartQuery. This is exactly what handleSubmitRepl's
	// regular branch does (repl.go:1071-1073).
	app.repl.AddUserMessage("fresh query")
	app.repl.StartQuery() // appends empty assistant message, sets streaming=true

	if !app.repl.IsStreaming() {
		t.Fatal("streaming must be true after StartQuery")
	}
	if got := len(app.repl.messages); got != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", got)
	}
	if app.committedCount != 0 {
		t.Fatalf("committedCount must be 0 after fresh submit, got %d", app.committedCount)
	}

	// Terminal resize arrives mid-stream (tmux split, window drag, etc).
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Regression assertion: WindowSizeMsg must NOT commit streaming messages.
	// If committedCount advanced to len(messages), the next View() will
	// render an empty uncommitted slice — the blank render bug.
	if app.committedCount >= len(app.repl.messages) {
		t.Errorf("WindowSizeMsg during stream committed messages: "+
			"committedCount=%d, totalMessages=%d. View() would render blank. "+
			"This is the /clear → /goal → resize → blank bug.",
			app.committedCount, len(app.repl.messages))
	}

	// Sanity: streaming flag must remain set; the resize is informational.
	if !app.repl.IsStreaming() {
		t.Error("WindowSizeMsg must not stop streaming")
	}
}
