package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// helperSessionItems creates PickerItem slice from SessionItem slice.
func helperSessionItems(items []SessionItem) []PickerItem {
	pi := make([]PickerItem, len(items))
	for i := range items {
		pi[i] = &items[i]
	}
	return pi
}

func TestSessionItem_Label(t *testing.T) {
	tests := []struct {
		item SessionItem
		want string
	}{
		{
			SessionItem{SessionID: "abcdefgh-1234", Title: "My Session", UpdatedAt: time.Now()},
			func() string {
				return fmt.Sprintf("%-20s %s", "My Session", "just now")
			}(),
		},
		{
			SessionItem{SessionID: "abcdefgh-1234", Title: "", UpdatedAt: time.Now()},
			"abcdefgh             just now",
		},
		{
			SessionItem{SessionID: "ab", Title: "", UpdatedAt: time.Now()},
			fmt.Sprintf("%-20s %s", "ab", "just now"),
		},
	}

	for _, tc := range tests {
		got := tc.item.Label()
		if got != tc.want {
			t.Errorf("Label() = %q, want %q", got, tc.want)
		}
	}
}

func TestModelItem_Label(t *testing.T) {
	tests := []struct {
		item ModelItem
		want string
	}{
		{ModelItem{Provider: "openai", Tier: "pro", Model: "glm-5", Current: false}, "openai / pro  glm-5"},
		{ModelItem{Provider: "openai", Tier: "pro", Model: "glm-5", Current: true}, "openai / pro  glm-5 *"},
		{ModelItem{Provider: "anthropic", Tier: "lite", Model: "claude-haiku", Current: false}, "anthropic / lite claude-haiku"},
	}

	for _, tc := range tests {
		got := tc.item.Label()
		if got != tc.want {
			t.Errorf("Label() = %q, want %q", got, tc.want)
		}
	}
}

func TestListPicker_Navigation(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	})
	p := NewListPicker("Test", items)

	if p.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", p.cursor)
	}

	// Move down (using "j" key)
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	p = model.(*ListPicker)
	if p.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", p.cursor)
	}

	// Move down again (using arrow down)
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*ListPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after down = %d, want 2", p.cursor)
	}

	// Wrap around at end
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*ListPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after 3rd down = %d, want 0 (wrap to top)", p.cursor)
	}

	// Wrap around at top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*ListPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// Move up normally
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*ListPicker)
	if p.cursor != 1 {
		t.Errorf("cursor after up = %d, want 1", p.cursor)
	}
}

func TestListPicker_Select(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	})
	p := NewListPicker("Test", items)

	// Move to second item
	p.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Select
	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*ListPicker)

	if p.SelectedIndex() != 1 {
		t.Errorf("SelectedIndex() = %d, want 1", p.SelectedIndex())
	}
	if !p.Done() {
		t.Error("expected Done() after Enter")
	}
	if cmd != nil {
		t.Errorf("expected nil cmd (no tea.Quit), got %v", cmd)
	}
}

func TestListPicker_Cancel(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
	})
	p := NewListPicker("Test", items)

	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p = model.(*ListPicker)

	if !p.Aborted() {
		t.Error("expected Aborted() after Esc")
	}
	if !p.Done() {
		t.Error("expected Done() after Esc")
	}
	if cmd != nil {
		t.Errorf("expected nil cmd (no tea.Quit), got %v", cmd)
	}
}

func TestListPicker_QKeyCancel(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
	})
	p := NewListPicker("Test", items)

	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	p = model.(*ListPicker)

	if !p.Aborted() {
		t.Error("expected Aborted() after q key")
	}
}

func TestListPicker_View(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "First", UpdatedAt: time.Now()},
		{SessionID: "s2", Title: "Second", UpdatedAt: time.Now()},
	})
	p := NewListPicker("Test Title", items)
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
	if !strings.Contains(view, "Test Title") {
		t.Error("view should contain title")
	}
	if !strings.Contains(view, "Esc") {
		t.Error("view should contain key hints")
	}
}

func TestListPicker_EmptyView(t *testing.T) {
	p := NewListPicker("Test", nil)
	view := p.View()
	if !strings.Contains(view, "No items available") {
		t.Errorf("empty picker should say no items, got %q", view)
	}
}

func TestListPicker_Init(t *testing.T) {
	items := helperSessionItems([]SessionItem{{SessionID: "s1", Title: "Test"}})
	p := NewListPicker("Test", items)
	cmd := p.Init()
	if cmd != nil {
		t.Errorf("Init() should return nil, got %v", cmd)
	}
}

func TestRelativeTime(t *testing.T) {
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"just now", time.Now().Add(-30 * time.Second), "just now"},
		{"minutes ago", time.Now().Add(-5 * time.Minute), "5m ago"},
		{"hours ago", time.Now().Add(-3 * time.Hour), "3h ago"},
		{"yesterday", time.Now().Add(-30 * time.Hour), "yesterday"},
		{"days ago", time.Now().Add(-72 * time.Hour), "3d ago"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relativeTime(tc.t)
			if got != tc.want {
				t.Errorf("relativeTime() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListPicker_WrapAround(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	})
	p := NewListPicker("Test", items)

	// At top (cursor=0), press up → should wrap to bottom
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*ListPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// At bottom (cursor=2), press down → should wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*ListPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after down-at-bottom = %d, want 0 (wrap to top)", p.cursor)
	}
}

func TestListPicker_WithInitialCursor(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	})
	p := NewListPicker("Test", items, WithInitialCursor(2))
	if p.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (WithInitialCursor)", p.cursor)
	}
}

func TestListPicker_WithInitialCursor_OutOfBounds(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	})
	t.Run("negative clamps to 0", func(t *testing.T) {
		p := NewListPicker("Test", items, WithInitialCursor(-1))
		if p.cursor != 0 {
			t.Errorf("cursor = %d, want 0 (clamped from -1)", p.cursor)
		}
	})
	t.Run("overflow clamps to last", func(t *testing.T) {
		p := NewListPicker("Test", items, WithInitialCursor(10))
		if p.cursor != 1 {
			t.Errorf("cursor = %d, want 1 (clamped from 10)", p.cursor)
		}
	})
}

func TestListPicker_WithInitialCursor_EmptyList(t *testing.T) {
	p := NewListPicker("Test", nil, WithInitialCursor(5))
	if p.cursor != 5 {
		// Empty list: no clamping since len=0, cursor stays as-is
		t.Errorf("cursor = %d, want 5 (no clamping on empty)", p.cursor)
	}
}

func TestListPicker_EmptyNavigation(t *testing.T) {
	p := NewListPicker("Test", nil)
	// Should not panic
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
}

func TestListPicker_EmptySelect(t *testing.T) {
	p := NewListPicker("Test", nil)
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*ListPicker)
	if !p.Aborted() {
		t.Error("empty picker Enter should abort")
	}
}

func TestListPicker_VimKeys(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	})
	p := NewListPicker("Test", items)

	// k (up) at top → wrap to bottom
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	p = model.(*ListPicker)
	if p.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1 (wrap)", p.cursor)
	}

	// j (down) at bottom → wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	p = model.(*ListPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after j = %d, want 0 (wrap)", p.cursor)
	}
}
