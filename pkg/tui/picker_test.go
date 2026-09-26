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
			SessionItem{SessionID: "abcdefgh-1234", Title: "My Session", UpdatedAt: time.Now()}, // REAL-TIME: relativeTime display
			func() string {
				return fmt.Sprintf("%-20s %s", "My Session", "just now")
			}(),
		},
		{
			SessionItem{SessionID: "abcdefgh-1234", Title: "", UpdatedAt: time.Now()}, // REAL-TIME: relativeTime display
			"abcdefgh             just now",
		},
		{
			SessionItem{SessionID: "ab", Title: "", UpdatedAt: time.Now()}, // REAL-TIME: relativeTime display
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
		{ModelItem{Provider: "openai", Model: "glm-5", Current: false}, "openai / glm-5"},
		{ModelItem{Provider: "openai", Model: "glm-5", Current: true}, "openai / glm-5 *"},
		{ModelItem{Provider: "anthropic", Model: "claude-haiku", Current: false}, "anthropic / claude-haiku"},
	}

	for _, tc := range tests {
		got := tc.item.Label()
		if got != tc.want {
			t.Errorf("Label() = %q, want %q", got, tc.want)
		}
	}
}

func TestDialog_Navigation(t *testing.T) {
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
	p = model.(*Dialog)
	if p.cursor != 1 {
		t.Errorf("cursor after j = %d, want 1", p.cursor)
	}

	// Move down again (using arrow down)
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*Dialog)
	if p.cursor != 2 {
		t.Errorf("cursor after down = %d, want 2", p.cursor)
	}

	// Wrap around at end
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*Dialog)
	if p.cursor != 0 {
		t.Errorf("cursor after 3rd down = %d, want 0 (wrap to top)", p.cursor)
	}

	// Wrap around at top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*Dialog)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// Move up normally
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*Dialog)
	if p.cursor != 1 {
		t.Errorf("cursor after up = %d, want 1", p.cursor)
	}
}

func TestDialog_Select(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	})
	p := NewListPicker("Test", items)

	// Move to second item
	p.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Select
	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*Dialog)

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

func TestDialog_Cancel(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
	})
	p := NewListPicker("Test", items)

	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p = model.(*Dialog)

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

func TestDialog_QKeyCancel(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
	})
	p := NewListPicker("Test", items)

	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	p = model.(*Dialog)

	if !p.Aborted() {
		t.Error("expected Aborted() after q key")
	}
}

func TestDialog_View(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "First", UpdatedAt: time.Now()},  // REAL-TIME: relativeTime display
		{SessionID: "s2", Title: "Second", UpdatedAt: time.Now()}, // REAL-TIME: relativeTime display
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

func TestDialog_EmptyView(t *testing.T) {
	p := NewListPicker("Test", nil)
	view := p.View()
	if !strings.Contains(view, "No items available") {
		t.Errorf("empty picker should say no items, got %q", view)
	}
}

func TestDialog_Init(t *testing.T) {
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
		{"just now", time.Now().Add(-30 * time.Second), "just now"}, // REAL-TIME: relativeTime display
		{"minutes ago", time.Now().Add(-5 * time.Minute), "5m ago"}, // REAL-TIME: relativeTime display
		{"hours ago", time.Now().Add(-3 * time.Hour), "3h ago"},     // REAL-TIME: relativeTime display
		{"yesterday", time.Now().Add(-30 * time.Hour), "yesterday"}, // REAL-TIME: relativeTime display
		{"days ago", time.Now().Add(-72 * time.Hour), "3d ago"},     // REAL-TIME: relativeTime display
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

func TestDialog_WrapAround(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
		{SessionID: "s3", Title: "Session 3"},
	})
	p := NewListPicker("Test", items)

	// At top (cursor=0), press up → should wrap to bottom
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*Dialog)
	if p.cursor != 2 {
		t.Errorf("cursor after up-at-top = %d, want 2 (wrap to bottom)", p.cursor)
	}

	// At bottom (cursor=2), press down → should wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*Dialog)
	if p.cursor != 0 {
		t.Errorf("cursor after down-at-bottom = %d, want 0 (wrap to top)", p.cursor)
	}
}

func TestDialog_WithInitialCursor(t *testing.T) {
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

func TestDialog_WithInitialCursor_OutOfBounds(t *testing.T) {
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

func TestDialog_WithInitialCursor_EmptyList(t *testing.T) {
	p := NewListPicker("Test", nil, WithInitialCursor(5))
	if p.cursor != 5 {
		// Empty list: no clamping since len=0, cursor stays as-is
		t.Errorf("cursor = %d, want 5 (no clamping on empty)", p.cursor)
	}
}

func TestDialog_EmptyNavigation(t *testing.T) {
	p := NewListPicker("Test", nil)
	// Should not panic
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
}

func TestDialog_EmptySelect(t *testing.T) {
	p := NewListPicker("Test", nil)
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*Dialog)
	if !p.Aborted() {
		t.Error("empty picker Enter should abort")
	}
}

func TestDialog_VimKeys(t *testing.T) {
	items := helperSessionItems([]SessionItem{
		{SessionID: "s1", Title: "Session 1"},
		{SessionID: "s2", Title: "Session 2"},
	})
	p := NewListPicker("Test", items)

	// k (up) at top → wrap to bottom
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	p = model.(*Dialog)
	if p.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1 (wrap)", p.cursor)
	}

	// j (down) at bottom → wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	p = model.(*Dialog)
	if p.cursor != 0 {
		t.Errorf("cursor after j = %d, want 0 (wrap)", p.cursor)
	}
}

// ---------------------------------------------------------------------------
// Viewport scrolling tests
// ---------------------------------------------------------------------------

// helperManyItems creates n PickerItems for scroll testing.
func helperManyItems(n int) []PickerItem {
	items := make([]SessionItem, n)
	for i := range n {
		items[i] = SessionItem{
			SessionID: fmt.Sprintf("s%d", i),
			Title:     fmt.Sprintf("Item %02d", i),
		}
	}
	return helperSessionItems(items)
}

func TestDialog_MaxVisible_NoHeight(t *testing.T) {
	// When height is 0 (default), all options are visible
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	if d.maxVisible() != 20 {
		t.Errorf("maxVisible() = %d, want 20 (no height set)", d.maxVisible())
	}
}

func TestDialog_MaxVisible_WithHeight(t *testing.T) {
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15
	mv := d.maxVisible()
	// height=15, overhead=8 (no details), so max = 15-8 = 7
	if mv != 7 {
		t.Errorf("maxVisible() = %d, want 7", mv)
	}
}

func TestDialog_MaxVisible_ClampedToOptionCount(t *testing.T) {
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(3)))
	d.height = 50
	mv := d.maxVisible()
	if mv != 3 {
		t.Errorf("maxVisible() = %d, want 3 (clamped to option count)", mv)
	}
}

func TestDialog_VisibleCount_NoScroll(t *testing.T) {
	// 3 options, height large enough → no scrolling
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(3)))
	d.height = 50
	if d.visibleCount() != 3 {
		t.Errorf("visibleCount() = %d, want 3 (no scroll needed)", d.visibleCount())
	}
}

func TestDialog_VisibleCount_WithScroll(t *testing.T) {
	// 20 options, height=15 → maxVisible=7, visibleCount=7-2=5
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15
	if d.visibleCount() != 5 {
		t.Errorf("visibleCount() = %d, want 5", d.visibleCount())
	}
}

func TestDialog_ClampScroll_Down(t *testing.T) {
	// 20 items, height=15 → visibleCount=5
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15

	// Move cursor down past first page
	for range 5 {
		d.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	// cursor=5, scrollOffset should have moved
	if d.scrollOffset == 0 {
		t.Error("scrollOffset should have moved past 0")
	}
	if d.cursor != 5 {
		t.Errorf("cursor = %d, want 5", d.cursor)
	}
}

func TestDialog_ClampScroll_Up(t *testing.T) {
	// 20 items, height=15 → visibleCount=5
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15
	d.cursor = 10
	d.clampScroll()

	// Now cursor=10, scrollOffset should be around 6 (10-5+1)
	if d.scrollOffset != 6 {
		t.Errorf("scrollOffset = %d, want 6", d.scrollOffset)
	}

	// Move up several times
	for range 5 {
		d.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	}
	// cursor=5, scrollOffset should have moved up
	if d.scrollOffset > 5 {
		t.Errorf("scrollOffset = %d, should be <= 5 after moving up", d.scrollOffset)
	}
}

func TestDialog_ClampScroll_WrapToBottom(t *testing.T) {
	// 20 items, height=15 → visibleCount=5
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15

	// At cursor=0, press up → wrap to last item
	d.HandleKey(tea.KeyMsg{Type: tea.KeyUp})
	if d.cursor != 19 {
		t.Errorf("cursor = %d, want 19 (wrap)", d.cursor)
	}
	// scrollOffset should show last page: 19-5+1=15
	if d.scrollOffset != 15 {
		t.Errorf("scrollOffset = %d, want 15", d.scrollOffset)
	}
}

func TestDialog_ClampScroll_WrapToTop(t *testing.T) {
	// 20 items, height=15 → visibleCount=5
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15
	d.cursor = 19
	d.clampScroll()

	// At cursor=19, press down → wrap to first item
	d.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	if d.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (wrap)", d.cursor)
	}
	if d.scrollOffset != 0 {
		t.Errorf("scrollOffset = %d, want 0", d.scrollOffset)
	}
}

func TestDialog_View_ScrollIndicators(t *testing.T) {
	// 10 items, height=12 → overhead=8, maxVisible=4, visibleCount=2
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(10)))
	d.height = 12

	view := d.View()
	// Should show "↓ 8 more" at bottom initially (no items above)
	if !strings.Contains(view, "↓ 8 more") {
		t.Errorf("view should show '↓ 8 more', got:\n%s", view)
	}
	// At top: scroll indicator should NOT appear (hint "↑/k" is fine)
	if strings.Contains(view, "↑ 0 more") || strings.Contains(view, "↑ 1 more") || strings.Contains(view, "↑ 2 more") {
		t.Errorf("view should not show '↑ N more' at top, got:\n%s", view)
	}

	// Move down past first page
	for range 2 {
		d.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	view = d.View()
	// Now in the middle: should show "↑ N more" indicator
	if !strings.Contains(view, "↑ 1 more") {
		t.Errorf("view should show '↑ 1 more' in middle, got:\n%s", view)
	}
}

func TestDialog_View_NoScrollIndicatorsWhenAllFit(t *testing.T) {
	// 3 items, large height → all fit, no indicators
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(3)))
	d.height = 50

	view := d.View()
	if strings.Contains(view, "more") {
		t.Errorf("view should not show scroll indicators when all fit, got:\n%s", view)
	}
}

func TestDialog_WithInitialCursor_Scroll(t *testing.T) {
	// 20 items, height=15 → visibleCount=5
	items := helperManyItems(20)
	p := NewListPicker("Test", items)
	p.height = 15
	p.cursor = 15
	p.clampScroll()

	// Cursor at 15, scrollOffset should show it
	if p.cursor != 15 {
		t.Errorf("cursor = %d, want 15", p.cursor)
	}
	if p.scrollOffset > 15 {
		t.Errorf("scrollOffset = %d, should be <= 15", p.scrollOffset)
	}
	if p.scrollOffset < 11 {
		t.Errorf("scrollOffset = %d, should be >= 11 (15-5+1)", p.scrollOffset)
	}
}

func TestDialog_FixedHeightWhileScrolling(t *testing.T) {
	// Verify dialog height stays constant while scrolling
	d := NewDialog("Test", pickerItemsToOptions(helperManyItems(20)))
	d.height = 15

	view0 := d.View()
	// Move down several times
	for range 10 {
		d.HandleKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	view1 := d.View()

	// Count lines (approximate: split by \n)
	lines0 := strings.Count(view0, "\n")
	lines1 := strings.Count(view1, "\n")
	if lines0 != lines1 {
		t.Errorf("line count changed: initial=%d, after scroll=%d", lines0, lines1)
	}
}
