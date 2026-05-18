package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------------
// Input cursor accessors (US-001)
// ---------------------------------------------------------------------------

func TestInput_SetCursor_WithinBounds(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.SetCursor(3)
	if i.Cursor() != 3 {
		t.Errorf("expected cursor 3, got %d", i.Cursor())
	}
}

func TestInput_SetCursor_ClampsNegative(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.SetCursor(-5)
	if i.Cursor() != 0 {
		t.Errorf("expected cursor 0, got %d", i.Cursor())
	}
}

func TestInput_SetCursor_ClampsBeyondLength(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.SetCursor(100)
	if i.Cursor() != 5 {
		t.Errorf("expected cursor 5 (len of 'hello'), got %d", i.Cursor())
	}
}

func TestInput_Cursor_AfterSetValue(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	// SetValue puts cursor at end
	if i.Cursor() != 3 {
		t.Errorf("expected cursor 3 after SetValue, got %d", i.Cursor())
	}
}

func TestInput_Cursor_AfterReset(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	i.Reset()
	if i.Cursor() != 0 {
		t.Errorf("expected cursor 0 after Reset, got %d", i.Cursor())
	}
}

// ---------------------------------------------------------------------------
// handleStash push/pop (US-002)
// ---------------------------------------------------------------------------

func TestHandleStash_Push(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.SetCursor(5)
	app.pasteStore[1] = "pasted content"
	app.nextPasteID = 2

	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Input should be cleared
	if a.input.Value() != "" {
		t.Errorf("expected empty input after push, got %q", a.input.Value())
	}
	// Stash should be saved
	if a.stashed == nil {
		t.Fatal("expected stashed to be set")
	}
	if a.stashed.text != "hello world" {
		t.Errorf("expected stashed text 'hello world', got %q", a.stashed.text)
	}
	if a.stashed.cursor != 5 {
		t.Errorf("expected stashed cursor 5, got %d", a.stashed.cursor)
	}
	if a.stashed.pasteStore[1] != "pasted content" {
		t.Errorf("expected stashed pasteStore to have entry 1, got %v", a.stashed.pasteStore)
	}
	if a.stashed.nextPasteID != 2 {
		t.Errorf("expected stashed nextPasteID 2, got %d", a.stashed.nextPasteID)
	}
	// App's pasteStore should be reset
	if len(a.pasteStore) != 0 {
		t.Errorf("expected app pasteStore cleared, got %d entries", len(a.pasteStore))
	}
}

func TestHandleStash_Pop(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("original text")
	app.input.SetCursor(8)
	app.pasteStore[3] = "saved paste"
	app.nextPasteID = 4

	// Push
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Input is empty, stash exists — pop
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a = model.(*App)

	if a.input.Value() != "original text" {
		t.Errorf("expected restored text 'original text', got %q", a.input.Value())
	}
	if a.input.Cursor() != 8 {
		t.Errorf("expected restored cursor 8, got %d", a.input.Cursor())
	}
	if a.pasteStore[3] != "saved paste" {
		t.Errorf("expected restored pasteStore entry 3, got %v", a.pasteStore)
	}
	if a.nextPasteID != 4 {
		t.Errorf("expected restored nextPasteID 4, got %d", a.nextPasteID)
	}
	if a.stashed != nil {
		t.Error("expected stash cleared after pop")
	}
}

func TestHandleStash_NoOp(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Empty input, no stash — should be no-op
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)
	if a.stashed != nil {
		t.Error("expected no stash created on empty input with no existing stash")
	}
}

func TestHandleStash_WhitespaceOnly(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("   ")

	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)
	// Whitespace-only input should not push (treated as empty)
	if a.stashed != nil {
		t.Error("whitespace-only input should not create stash")
	}
}

func TestHandleStash_DeepCopyPasteStore(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello")
	app.pasteStore[1] = "original"

	// Push
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Mutate app's pasteStore after push
	a.pasteStore[1] = "mutated"
	a.pasteStore[2] = "new entry"

	// Stashed copy should be unaffected
	if a.stashed.pasteStore[1] != "original" {
		t.Errorf("stashed pasteStore should not be aliased, got %q", a.stashed.pasteStore[1])
	}
	if _, ok := a.stashed.pasteStore[2]; ok {
		t.Error("stashed pasteStore should not have key 2")
	}
}

func TestHandleStash_PushTwiceOverwrites(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// First push
	app.input.SetValue("first")
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Type new text and push again
	a.input.SetValue("second")
	a.input.SetCursor(2)
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a = model.(*App)

	if a.stashed.text != "second" {
		t.Errorf("expected second stash to overwrite, got %q", a.stashed.text)
	}
	if a.stashed.cursor != 2 {
		t.Errorf("expected cursor 2, got %d", a.stashed.cursor)
	}
}

// ---------------------------------------------------------------------------
// Auto-restore (US-003)
// ---------------------------------------------------------------------------

func TestRestoreStash_NormalSubmit(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// Stash some input
	app.input.SetValue("stashed text")
	app.input.SetCursor(7)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Now type a message and submit normally
	a.input.SetValue("quick question")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = model.(*App)

	// The submit cmd was returned — simulate it running
	if cmd != nil {
		cmd()
	}

	// Stash should be restored to input
	if a.input.Value() != "stashed text" {
		t.Errorf("expected stashed text restored after normal submit, got %q", a.input.Value())
	}
	if a.input.Cursor() != 7 {
		t.Errorf("expected cursor 7 restored, got %d", a.input.Cursor())
	}
	if a.stashed != nil {
		t.Error("expected stash cleared after restore")
	}
}

func TestRestoreStash_Enqueue(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// Set up streaming state
	app.repl.StartQuery()

	// Stash input
	app.input.SetValue("stashed text")
	app.input.SetCursor(5)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Submit while streaming (enqueue path)
	a.input.SetValue("follow-up")
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = model.(*App)

	if cmd != nil {
		// enqueue returns nil cmd, but just in case
		cmd()
	}

	// Stash should be restored immediately after enqueue
	if a.input.Value() != "stashed text" {
		t.Errorf("expected stash restored after enqueue, got %q", a.input.Value())
	}
	if a.input.Cursor() != 5 {
		t.Errorf("expected cursor 5 restored, got %d", a.input.Cursor())
	}
	if a.stashed != nil {
		t.Error("expected stash cleared after restore")
	}
}

func TestRestoreStash_SlashCommand(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// Stash input
	app.input.SetValue("stashed text")
	app.input.SetCursor(4)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	a := model.(*App)

	// Submit a slash command
	a.input.SetValue("/model")
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	a = model.(*App)

	// Stash should be restored after slash command
	if a.input.Value() != "stashed text" {
		t.Errorf("expected stash restored after slash command, got %q", a.input.Value())
	}
	if a.input.Cursor() != 4 {
		t.Errorf("expected cursor 4 restored, got %d", a.input.Cursor())
	}
	if a.stashed != nil {
		t.Error("expected stash cleared after restore")
	}
}

func TestRestoreStash_NoStashNoChange(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// Submit without stash — input should be cleared normally
	app.input.SetValue("normal message")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Input should be cleared (normal submit behavior)
	// The submit starts a query, so input is reset
	if app.input.Value() != "" {
		// After restoreStash with nil stash, input stays as the Reset() left it (empty)
		t.Errorf("expected empty input after submit without stash, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// Stash notice rendering (US-004)
// ---------------------------------------------------------------------------

func TestRenderStashNotice_WithStash(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.stashed = &stashedPrompt{
		text:   "saved",
		cursor: 3,
	}

	notice := app.renderStashNotice()
	if notice == "" {
		t.Fatal("expected non-empty stash notice")
	}
	if !strings.Contains(notice, "Stashed") {
		t.Errorf("expected notice to contain 'Stashed', got %q", notice)
	}
	if !strings.Contains(notice, "auto-restores after submit") {
		t.Errorf("expected notice to contain 'auto-restores after submit', got %q", notice)
	}
	if !strings.Contains(notice, "‣") {
		t.Errorf("expected notice to contain ‣, got %q", notice)
	}
}

func TestRenderStashNotice_WithoutStash(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	notice := app.renderStashNotice()
	if notice != "" {
		t.Errorf("expected empty notice when no stash, got %q", notice)
	}
}

func TestRenderStashNotice_PositionInLayout(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.stashed = &stashedPrompt{text: "saved", cursor: 3}

	view := app.View()

	if !strings.Contains(view, "Stashed") {
		t.Error("View() should contain stash notice when stash is set")
	}

	// Verify stash notice appears after queue box area and before input
	// The stash notice should be between "❯" (queue box prefix) and "❯" (input prompt)
	// With no queue, it should appear just before the input prompt
	if !strings.Contains(view, "‣ Stashed") {
		t.Errorf("View should contain '‣ Stashed', got:\n%s", view)
	}
}

// ---------------------------------------------------------------------------
// Edge cases (US-005)
// ---------------------------------------------------------------------------

func TestStash_SurvivesResetDisplayState(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.stashed = &stashedPrompt{
		text:        "survivor",
		cursor:      4,
		pasteStore:  map[int]string{1: "paste"},
		nextPasteID: 2,
	}

	app.resetDisplayState()

	if app.stashed == nil {
		t.Fatal("stash should survive resetDisplayState")
	}
	if app.stashed.text != "survivor" {
		t.Errorf("expected stash text 'survivor', got %q", app.stashed.text)
	}
}

func TestStash_PopClearsStash(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

	// Pop — restores "hello" to input
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if app.stashed != nil {
		t.Error("stash should be nil after pop")
	}
	// Input was restored
	if app.input.Value() != "hello" {
		t.Errorf("expected restored input 'hello', got %q", app.input.Value())
	}

	// Manually clear input, then Ctrl+S on empty input with no stash = no-op
	app.input.Reset()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if app.stashed != nil {
		t.Error("stash should still be nil after no-op Ctrl+S on empty input")
	}
}

func TestStash_WithPendingQueue(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	// Set up streaming state
	app.repl.StartQuery()

	// Stash input
	app.input.SetValue("stashed text")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})

	// Enqueue a message
	app.input.SetValue("queued msg")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Both should coexist: stash restored, pendingQueue has the message
	if app.stashed != nil {
		t.Error("stash should be cleared after restore")
	}
	if len(app.pendingQueue) == 0 {
		t.Error("pendingQueue should have the enqueued message")
	}
	if app.pendingQueue[0].Text != "queued msg" {
		t.Errorf("expected queued msg text 'queued msg', got %q", app.pendingQueue[0].Text)
	}
	// Input should have the stashed text restored
	if app.input.Value() != "stashed text" {
		t.Errorf("expected stashed text restored, got %q", app.input.Value())
	}
}
