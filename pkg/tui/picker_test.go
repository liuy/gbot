package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSessionPicker_Label(t *testing.T) {
	tests := []struct {
		item SessionItem
		want string
	}{
		{SessionItem{SessionID: "abcdefgh-1234", Title: "My Session"}, "My Session"},
		{SessionItem{SessionID: "abcdefgh-1234", Title: ""}, "abcdefgh"},
		{SessionItem{SessionID: "ab", Title: ""}, "ab"},
	}

	for _, tc := range tests {
		got := tc.item.Label()
		if got != tc.want {
			t.Errorf("Label() = %q, want %q", got, tc.want)
		}
	}
}

func TestSessionPicker_Navigation(t *testing.T) {
	items := []SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	}
	p := NewSessionPicker(items)

	if p.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", p.cursor)
	}

	// Move down
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*SessionPicker)
	if p.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", p.cursor)
	}

	// Move down again
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*SessionPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after 2nd down = %d, want 2", p.cursor)
	}

	// Wrap around at end
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*SessionPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after 3rd down = %d, want 0 (wrap to top)", p.cursor)
	}

	// Wrap around at top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*SessionPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// Move up normally
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*SessionPicker)
	if p.cursor != 1 {
		t.Errorf("cursor after up = %d, want 1", p.cursor)
	}
}

func TestSessionPicker_Select(t *testing.T) {
	items := []SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	}
	p := NewSessionPicker(items)

	// Move to second item
	p.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Select
	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*SessionPicker)

	if p.selected == nil {
		t.Fatal("expected selection after Enter")
	}
	if p.selected.SessionID != "s2" {
		t.Errorf("selected SessionID = %q, want %q", p.selected.SessionID, "s2")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after selection")
	}
}

func TestSessionPicker_Cancel(t *testing.T) {
	items := []SessionItem{
		{SessionID: "s1", Title: "Session 1"},
	}
	p := NewSessionPicker(items)

	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p = model.(*SessionPicker)

	if !p.aborted {
		t.Error("expected aborted after Esc")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after cancel")
	}
}

func TestSessionPicker_View(t *testing.T) {
	items := []SessionItem{
		{SessionID: "s1", Title: "First"},
		{SessionID: "s2", Title: "Second", UpdatedAt: time.Now()},
	}
	p := NewSessionPicker(items)
	view := p.View()

	if view == "" {
		t.Fatal("expected non-empty view")
	}
	if !strings.Contains(view, "First") {
		t.Error("view should contain 'First'")
	}
	if !strings.Contains(view, "Second") {
		t.Error("view should contain 'Second'")
	}
	if !strings.Contains(view, "Esc") {
		t.Error("view should contain key hints")
	}
}

func TestSessionPicker_EmptyView(t *testing.T) {
	p := NewSessionPicker(nil)
	view := p.View()
	if !strings.Contains(view, "No sessions") {
		t.Errorf("empty picker should say no sessions, got %q", view)
	}
}

func TestSessionPicker_WrapAround(t *testing.T) {
	items := []SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	}
	p := NewSessionPicker(items)

	// At top (cursor=0), press up → should wrap to bottom
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*SessionPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// At bottom (cursor=2), press down → should wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*SessionPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after down-at-bottom = %d, want 0 (wrap to top)", p.cursor)
	}
}

