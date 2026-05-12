package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/types"
)

var (
	testFutureDeadline = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	testPastDeadline   = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// newTestInputDialog creates an InputDialog with a far-future deadline for testing.
func newTestInputDialog(masked bool) *InputDialog {
	ch := make(chan types.AskResponse, 1)
	return NewInputDialog("Enter password:", masked, testFutureDeadline, ch)
}

// --- NewInputDialog ---

func TestNewInputDialog_Fields(t *testing.T) {
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("prompt:", true, testFutureDeadline, ch)

	if d.prompt != "prompt:" {
		t.Errorf("prompt = %q, want %q", d.prompt, "prompt:")
	}
	if !d.masked {
		t.Error("masked should be true")
	}
	if d.cursor != 0 {
		t.Errorf("cursor = %d, want 0", d.cursor)
	}
	if d.done {
		t.Error("done should be false initially")
	}
	if d.result != ch {
		t.Error("result channel mismatch")
	}
	if d.value.Len() != 0 {
		t.Error("value should be empty initially")
	}
}

// --- Init ---

func TestInputDialog_Init(t *testing.T) {
	d := newTestInputDialog(false)
	cmd := d.Init()
	if cmd == nil {
		t.Error("Init should return a tick command")
	}
	// Execute the returned cmd to cover the tickMsg closure.
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	select {
	case msg := <-done:
		if _, ok := msg.(tickMsg); !ok {
			t.Errorf("expected tickMsg, got %T", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Init cmd timed out")
	}
}

// --- Update: KeyEnter submits ---

func TestInputDialog_Enter_Submits(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("hello")

	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter should return nil cmd")
	}
	if !d.done {
		t.Error("should be done after Enter")
	}

	select {
	case resp := <-d.result:
		if resp.Text != "hello" {
			t.Errorf("text = %q, want %q", resp.Text, "hello")
		}
		if resp.Aborted {
			t.Error("should not be aborted")
		}
	default:
		t.Fatal("expected response on result channel")
	}
}

// --- Update: KeyEsc aborts ---

func TestInputDialog_Esc_Aborts(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("typed")

	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Error("Esc should return nil cmd")
	}
	if !d.done {
		t.Error("should be done after Esc")
	}

	select {
	case resp := <-d.result:
		if !resp.Aborted {
			t.Error("should be aborted")
		}
		if resp.Text != "" {
			t.Errorf("text should be empty on abort, got %q", resp.Text)
		}
	default:
		t.Fatal("expected response on result channel")
	}
}

// --- Update: KeyCtrlC aborts and quits ---

func TestInputDialog_CtrlC_AbortsAndQuits(t *testing.T) {
	d := newTestInputDialog(false)

	model, cmd := d.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("Ctrl+C should return tea.Quit cmd")
	}

	d2 := model.(*InputDialog)
	if !d2.done {
		t.Error("should be done after Ctrl+C")
	}

	select {
	case resp := <-d2.result:
		if !resp.Aborted {
			t.Error("should be aborted")
		}
	default:
		t.Fatal("expected response on result channel")
	}
}

// --- Update: KeyBackspace ---

func TestInputDialog_Backspace(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")

	// Move cursor back one position, then backspace
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if d.value.String() != "ac" {
		t.Errorf("after backspace, value = %q, want %q", d.value.String(), "ac")
	}
}

func TestInputDialog_Backspace_AtStart(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")

	// Move cursor to start, backspace should do nothing
	for range 3 {
		d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	}
	d.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if d.value.String() != "abc" {
		t.Errorf("backspace at start should not change value, got %q", d.value.String())
	}
}

// --- Update: KeyDelete ---

func TestInputDialog_DeleteForward(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc") // cursor=3

	// Move cursor back two → position 1, delete forward (deletes 'b')
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyDelete})

	if d.value.String() != "ac" {
		t.Errorf("after delete, value = %q, want %q", d.value.String(), "ac")
	}
}

func TestInputDialog_DeleteForward_AtEnd(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("ab")

	// Cursor at end, delete should do nothing
	d.Update(tea.KeyMsg{Type: tea.KeyDelete})

	if d.value.String() != "ab" {
		t.Errorf("delete at end should not change value, got %q", d.value.String())
	}
}

// --- Update: KeyLeft/KeyRight ---

func TestInputDialog_CursorLeft(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc") // cursor at 3

	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if d.cursor != 2 {
		t.Errorf("cursor = %d, want 2", d.cursor)
	}
}

func TestInputDialog_CursorLeft_AtStart(t *testing.T) {
	d := newTestInputDialog(false)

	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if d.cursor != 0 {
		t.Errorf("cursor at start should stay 0, got %d", d.cursor)
	}
}

func TestInputDialog_CursorRight(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")                     // cursor at 3
	d.Update(tea.KeyMsg{Type: tea.KeyLeft}) // cursor at 2
	d.Update(tea.KeyMsg{Type: tea.KeyRight})

	if d.cursor != 3 {
		t.Errorf("cursor = %d, want 3", d.cursor)
	}
}

func TestInputDialog_CursorRight_AtEnd(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc") // cursor at 3

	d.Update(tea.KeyMsg{Type: tea.KeyRight})
	if d.cursor != 3 {
		t.Errorf("cursor at end should stay 3, got %d", d.cursor)
	}
}

// --- Update: KeyRunes inserts text ---

func TestInputDialog_InsertText(t *testing.T) {
	d := newTestInputDialog(false)

	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}})

	if d.value.String() != "hi" {
		t.Errorf("value = %q, want %q", d.value.String(), "hi")
	}
	if d.cursor != 2 {
		t.Errorf("cursor = %d, want 2", d.cursor)
	}
}

// --- Update: tickMsg countdown ---

func TestInputDialog_Tick_Countdown(t *testing.T) {
	d := newTestInputDialog(false)

	// Tick with time remaining should return another tick command
	_, cmd := d.Update(tickMsg(testFutureDeadline))
	if cmd == nil {
		t.Error("tick with time remaining should return tick command")
	}
}

func TestInputDialog_Tick_CountdownExpired(t *testing.T) {
	// Deadline in the past → countdown zero → auto-abort
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("prompt:", false, testPastDeadline, ch)

	_, cmd := d.Update(tickMsg(testFutureDeadline))
	if cmd != nil {
		t.Error("expired countdown should return nil cmd")
	}
	if !d.done {
		t.Error("should be done after countdown expired")
	}

	select {
	case resp := <-ch:
		if !resp.Aborted {
			t.Error("should be aborted on countdown expiry")
		}
	default:
		t.Fatal("expected response on result channel")
	}
}

// --- Update: done state ignores further input ---

func TestInputDialog_Done_IgnoresKeys(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("first")
	d.submit() // sets done=true

	// Should ignore all subsequent input
	_, cmd := d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd != nil {
		t.Error("done dialog should return nil cmd")
	}
	if d.value.String() != "first" {
		t.Errorf("value should not change after done, got %q", d.value.String())
	}
}

func TestInputDialog_Done_IgnoresTick(t *testing.T) {
	d := newTestInputDialog(false)
	d.submit()

	_, cmd := d.Update(tickMsg(testFutureDeadline))
	if cmd != nil {
		t.Error("done dialog should return nil cmd on tick")
	}
}

// --- Update: unknown message type ---

func TestInputDialog_UnknownMsg(t *testing.T) {
	d := newTestInputDialog(false)
	_, cmd := d.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Error("unknown msg should return nil cmd")
	}
}

// --- View ---

func TestInputDialog_View_ContainsPrompt(t *testing.T) {
	d := newTestInputDialog(false)
	view := d.View()

	if !strings.Contains(view, "Enter password:") {
		t.Error("view should contain prompt text")
	}
	if !strings.Contains(view, "Input Required") {
		t.Error("view should contain title")
	}
	if !strings.Contains(view, "Timeout in") {
		t.Error("view should contain countdown")
	}
	if !strings.Contains(view, "Enter to submit") {
		t.Error("view should contain hint")
	}
}

func TestInputDialog_View_MaskedHint(t *testing.T) {
	d := newTestInputDialog(true)
	view := d.View()

	if !strings.Contains(view, "Esc to abort") {
		t.Error("dialog should show 'Esc to abort' hint")
	}
}

func TestInputDialog_View_UnmaskedHint(t *testing.T) {
	d := newTestInputDialog(false)
	view := d.View()

	if !strings.Contains(view, "Esc to abort") {
		t.Error("dialog should show 'Esc to abort' hint")
	}
}

// --- renderInput ---

func TestInputDialog_RenderInput_PlainText(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")
	rendered := d.renderInput()

	if !strings.Contains(rendered, "ab") {
		t.Error("rendered input should contain text before cursor")
	}
}

func TestInputDialog_RenderInput_Masked(t *testing.T) {
	d := newTestInputDialog(true)
	d.insertText("abc")
	rendered := d.renderInput()

	// Should show bullet chars, not actual text
	if strings.Contains(rendered, "abc") {
		t.Error("masked input should not show actual text")
	}
	// renderInput shows cursor as styled block; at end, all 3 bullets visible.
	// Cursor at position 3 (end): before="•••", at=" " (styled space).
	bulletCount := strings.Count(rendered, inputBullet)
	if bulletCount != 3 {
		t.Errorf("expected 3 bullets for 3-char masked input, got %d", bulletCount)
	}
}

func TestInputDialog_RenderInput_Empty(t *testing.T) {
	d := newTestInputDialog(false)
	rendered := d.renderInput()

	// Empty input should render a styled cursor block
	if rendered == "" {
		t.Error("empty input should still render cursor")
	}
}

func TestInputDialog_RenderInput_CursorAtMiddle(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")
	// Move cursor to position 1 (between 'a' and 'b')
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})

	rendered := d.renderInput()
	if !strings.Contains(rendered, "a") {
		t.Error("should contain 'a' before cursor")
	}
}

// --- submit sends response ---

func TestInputDialog_Submit_SendsText(t *testing.T) {
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("p:", false, testFutureDeadline, ch)
	d.insertText("mysecret")
	d.submit()

	select {
	case resp := <-ch:
		if resp.Text != "mysecret" {
			t.Errorf("text = %q, want %q", resp.Text, "mysecret")
		}
		if resp.Aborted {
			t.Error("submit should not set aborted")
		}
	default:
		t.Fatal("expected response after submit")
	}
}

func TestInputDialog_Submit_DoesNotBlock(t *testing.T) {
	// sendDecision is non-blocking; submit on full channel should not deadlock
	ch := make(chan types.AskResponse, 1)
	ch <- types.AskResponse{Text: "stale"} // fill the buffer

	d := NewInputDialog("p:", false, testFutureDeadline, ch)
	d.insertText("new")
	d.submit() // should not block

	// Channel still has the stale value (sendDecision dropped the new one)
	select {
	case resp := <-ch:
		if resp.Text != "stale" {
			t.Errorf("stale value should remain, got %q", resp.Text)
		}
	default:
		t.Fatal("expected stale value on channel")
	}
}

// --- abort sends aborted response ---

func TestInputDialog_Abort_SendsAborted(t *testing.T) {
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("p:", false, testFutureDeadline, ch)
	d.abort()

	select {
	case resp := <-ch:
		if !resp.Aborted {
			t.Error("abort should set Aborted=true")
		}
	default:
		t.Fatal("expected response after abort")
	}
}

// --- insertText at cursor position ---

func TestInputDialog_InsertText_Middle(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("ac")                      // "ac", cursor=2
	d.Update(tea.KeyMsg{Type: tea.KeyLeft}) // cursor=1
	d.insertText("b")                       // insert "b" at position 1

	if d.value.String() != "abc" {
		t.Errorf("value = %q, want %q", d.value.String(), "abc")
	}
	if d.cursor != 2 {
		t.Errorf("cursor = %d, want 2", d.cursor)
	}
}

// --- deleteBackward ---

func TestInputDialog_DeleteBackward_Middle(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")
	// cursor at 3, move left twice to position 1
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.deleteBackward()

	if d.value.String() != "bc" {
		t.Errorf("value = %q, want %q", d.value.String(), "bc")
	}
	if d.cursor != 0 {
		t.Errorf("cursor = %d, want 0", d.cursor)
	}
}

// --- deleteForward ---

func TestInputDialog_DeleteForward_Middle(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("abc")
	// cursor at 3, move to position 1
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.Update(tea.KeyMsg{Type: tea.KeyLeft})
	d.deleteForward()

	if d.value.String() != "ac" {
		t.Errorf("value = %q, want %q", d.value.String(), "ac")
	}
}

func TestInputDialog_DeleteForward_AtOrBeyondEnd(t *testing.T) {
	d := newTestInputDialog(false)
	d.insertText("a")
	d.deleteForward() // cursor at end, should do nothing

	if d.value.String() != "a" {
		t.Errorf("delete at end should not change value, got %q", d.value.String())
	}
}

// --- View countdown display ---

func TestInputDialog_View_CountdownFuture(t *testing.T) {
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("p:", false, testFutureDeadline, ch)
	view := d.View()

	if !strings.Contains(view, "Timeout in") {
		t.Errorf("view should show countdown text, got:\n%s", view)
	}
}

func TestInputDialog_View_CountdownZero(t *testing.T) {
	ch := make(chan types.AskResponse, 1)
	d := NewInputDialog("p:", false, testPastDeadline, ch)
	view := d.View()

	if !strings.Contains(view, "Timeout in 0s") {
		t.Errorf("expired deadline should show 0s, got:\n%s", view)
	}
}
