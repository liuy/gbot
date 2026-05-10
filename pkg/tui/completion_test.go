package tui

import (
	"strings"
	"testing"
)

func TestNewCompletions(t *testing.T) {
	c := NewCompletions()
	if c.Visible() {
		t.Error("new completions should not be visible")
	}
	if c.SelectedIndex() != -1 {
		t.Error("new completions should have SelectedIndex -1")
	}
	if len(c.Items()) != 0 {
		t.Error("new completions should have no items")
	}
}

// --- Update tests ---

func TestCompletions_Update_SlashShowsAll(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	if !c.Visible() {
		t.Fatal("expected completions visible after typing '/'")
	}
	items := c.Items()
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}
	// Must be alphabetical: clear, model, rewind, session
	if items[0].Name != "clear" {
		t.Errorf("first item = %q, want %q", items[0].Name, "clear")
	}
	if items[1].Name != "model" {
		t.Errorf("second item = %q, want %q", items[1].Name, "model")
	}
	if items[2].Name != "rewind" {
		t.Errorf("third item = %q, want %q", items[2].Name, "rewind")
	}
	if items[3].Name != "session" {
		t.Errorf("fourth item = %q, want %q", items[3].Name, "session")
	}
}

func TestCompletions_Update_PrefixFilter(t *testing.T) {
	c := NewCompletions()
	c.Update("/s", true)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "session" {
		t.Errorf("item = %q, want %q", items[0].Name, "session")
	}
}

func TestCompletions_Update_PrefixClear(t *testing.T) {
	c := NewCompletions()
	c.Update("/cl", true)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "clear" {
		t.Errorf("item = %q, want %q", items[0].Name, "clear")
	}
}

func TestCompletions_Update_PrefixModel(t *testing.T) {
	c := NewCompletions()
	c.Update("/m", true)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "model" {
		t.Errorf("item = %q, want %q", items[0].Name, "model")
	}
}

func TestCompletions_Update_NoMatch(t *testing.T) {
	c := NewCompletions()
	// First make it visible
	c.Update("/", true)
	if !c.Visible() {
		t.Fatal("should be visible after /")
	}

	// Then type non-matching prefix
	c.Update("/xyz", true)
	if c.Visible() {
		t.Error("should not be visible with no matches")
	}
}

func TestCompletions_Update_IMEGuard(t *testing.T) {
	c := NewCompletions()
	c.Update("/你好", true)
	if c.Visible() {
		t.Error("should not trigger for non-ASCII input")
	}
}

func TestCompletions_Update_SpaceDismisses(t *testing.T) {
	c := NewCompletions()
	c.Update("/session ", true)
	if c.Visible() {
		t.Error("should dismiss when input contains space")
	}
}

func TestCompletions_Update_NotCursorAtEnd(t *testing.T) {
	c := NewCompletions()
	c.Update("/s", false)
	if c.Visible() {
		t.Error("should not trigger when cursor not at end")
	}
}

func TestCompletions_Update_NotSlashCommand(t *testing.T) {
	c := NewCompletions()
	c.Update("hello", true)
	if c.Visible() {
		t.Error("should not trigger for non-slash input")
	}
}

func TestCompletions_Update_EmptyString(t *testing.T) {
	c := NewCompletions()
	c.Update("", true)
	if c.Visible() {
		t.Error("should not trigger for empty input")
	}
}

func TestCompletions_Update_TransitionVisibleToDismissed(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	if !c.Visible() {
		t.Fatal("should be visible after /")
	}

	c.Update("/xyz", true)
	if c.Visible() {
		t.Error("should dismiss after no match")
	}
	if len(c.Items()) != 0 {
		t.Error("items should be cleared after dismiss")
	}
}

// --- Accept tests ---

func TestCompletions_Accept_HasArgs(t *testing.T) {
	c := NewCompletions()
	c.Update("/s", true)

	fillText, shouldExec := c.Accept()
	if fillText != "/session " {
		t.Errorf("fillText = %q, want %q", fillText, "/session ")
	}
	if shouldExec {
		t.Error("session has args, should not auto-execute")
	}
}

func TestCompletions_Accept_NoArgs(t *testing.T) {
	c := NewCompletions()
	c.Update("/cl", true)

	fillText, shouldExec := c.Accept()
	if fillText != "/clear " {
		t.Errorf("fillText = %q, want %q", fillText, "/clear ")
	}
	if !shouldExec {
		t.Error("clear has no args, should auto-execute")
	}
}

func TestCompletions_Accept_Empty(t *testing.T) {
	c := NewCompletions()
	fillText, shouldExec := c.Accept()
	if fillText != "" {
		t.Errorf("fillText = %q, want empty", fillText)
	}
	if shouldExec {
		t.Error("empty completions should not execute")
	}
}

// --- Navigation tests ---

func TestCompletions_SelectNext(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true) // clear, model, session

	if c.SelectedIndex() != 0 {
		t.Fatalf("initial index = %d, want 0", c.SelectedIndex())
	}

	c.SelectNext()
	if c.SelectedIndex() != 1 {
		t.Errorf("after SelectNext = %d, want 1", c.SelectedIndex())
	}

	c.SelectNext()
	if c.SelectedIndex() != 2 {
		t.Errorf("after SelectNext = %d, want 2", c.SelectedIndex())
	}

	c.SelectNext()
	if c.SelectedIndex() != 3 {
		t.Errorf("after SelectNext = %d, want 3", c.SelectedIndex())
	}

	// Wrap around
	c.SelectNext()
	if c.SelectedIndex() != 0 {
		t.Errorf("after wrap = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_SelectPrev(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true) // clear, model, rewind, session

	// Wrap around backwards
	c.SelectPrev()
	if c.SelectedIndex() != 3 {
		t.Errorf("after SelectPrev wrap = %d, want 3", c.SelectedIndex())
	}

	c.SelectPrev()
	if c.SelectedIndex() != 2 {
		t.Errorf("after SelectPrev = %d, want 2", c.SelectedIndex())
	}
}

func TestCompletions_SelectAfterFilter(t *testing.T) {
	c := NewCompletions()
	c.Update("/s", true) // only session

	c.SelectNext()
	if c.SelectedIndex() != 0 {
		t.Errorf("single item wrap = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_Dismiss(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	if !c.Visible() {
		t.Fatal("should be visible")
	}

	c.Dismiss()
	if c.Visible() {
		t.Error("should not be visible after dismiss")
	}
	if c.SelectedIndex() != -1 {
		t.Errorf("SelectedIndex after dismiss = %d, want -1", c.SelectedIndex())
	}
}

// --- Render tests ---

func TestCompletions_Render_Basic(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	view := c.Render(80, 5)
	if view == "" {
		t.Fatal("render should not be empty")
	}
	// Should contain all command names
	if !strings.Contains(view, "clear") {
		t.Error("render should contain 'clear'")
	}
	if !strings.Contains(view, "model") {
		t.Error("render should contain 'model'")
	}
	if !strings.Contains(view, "session") {
		t.Error("render should contain 'session'")
	}
	// First item selected → should be rendered (reverse style applied via ANSI)
	// Check that clear appears in the output (it's the first alphabetical item)
	lines := strings.Split(view, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(lines))
	}
}

func TestCompletions_Render_SelectedHighlight(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	// Select second item (model)
	c.SelectNext()

	view := c.Render(80, 5)
	lines := strings.Split(view, "\n")

	// Second line should contain "model"
	if !strings.Contains(lines[1], "model") {
		t.Errorf("second line should contain 'model', got %q", lines[1])
	}
	// Selected line should differ from unstyled plain text
	// (lipgloss may or may not emit ANSI in test environments)
	plainLine := "  /model - Switch model"
	if stripANSI(lines[1]) != plainLine {
		t.Errorf("second line content = %q, want %q", stripANSI(lines[1]), plainLine)
	}
}

func TestCompletions_Render_MaxHeight(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	// Limit to 2 lines
	view := c.Render(80, 2)
	lines := strings.Split(view, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines with maxHeight=2, got %d", len(lines))
	}
}

func TestCompletions_Render_NotVisible(t *testing.T) {
	c := NewCompletions()
	view := c.Render(80, 5)
	if view != "" {
		t.Errorf("render when not visible should be empty, got %q", view)
	}
}

func TestCompletions_Render_Truncation(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	// Very narrow width
	view := c.Render(10, 5)
	for line := range strings.SplitSeq(view, "\n") {
		// Strip ANSI codes for length check
		stripped := stripANSI(line)
		if len(stripped) > 10 {
			t.Errorf("line too long (%d chars): %q", len(stripped), stripped)
		}
	}
}

// --- Integration: Update changes selection properly ---

func TestCompletions_Update_ResetsSelection(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	c.SelectNext() // index = 1
	c.SelectNext() // index = 2

	// Re-update should reset to index 0
	c.Update("/m", true)
	if c.SelectedIndex() != 0 {
		t.Errorf("after re-update index = %d, want 0", c.SelectedIndex())
	}
}

// --- Command metadata tests ---

func TestCompletions_ItemMetadata(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	items := c.Items()

	// clear: HasArgs=false
	clearItem := items[0]
	if clearItem.Name != "clear" {
		t.Fatalf("first item = %q, want clear", clearItem.Name)
	}
	if clearItem.HasArgs {
		t.Error("clear should not have args")
	}
	if clearItem.Description == "" {
		t.Error("clear should have description")
	}

	// rewind: HasArgs=false
	rewindItem := items[2]
	if rewindItem.Name != "rewind" {
		t.Fatalf("third item = %q, want rewind", rewindItem.Name)
	}
	if rewindItem.HasArgs {
		t.Error("rewind should not have args")
	}

	// session: HasArgs=true
	sessionItem := items[3]
	if sessionItem.Name != "session" {
		t.Fatalf("fourth item = %q, want session", sessionItem.Name)
	}
	if !sessionItem.HasArgs {
		t.Error("session should have args")
	}
}

// --- P1 edge case tests (from code review) ---

func TestCompletions_SelectNext_EmptyItems(t *testing.T) {
	c := NewCompletions()
	c.SelectNext() // no panic on empty
	if c.SelectedIndex() != -1 {
		t.Errorf("empty SelectNext: index = %d, want -1", c.SelectedIndex())
	}
}

func TestCompletions_SelectPrev_EmptyItems(t *testing.T) {
	c := NewCompletions()
	c.SelectPrev() // no panic on empty
	if c.SelectedIndex() != -1 {
		t.Errorf("empty SelectPrev: index = %d, want -1", c.SelectedIndex())
	}
}

func TestCompletions_Render_NegativeMaxHeight(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)

	view := c.Render(80, -1)
	// Negative maxHeight should show all items (guard: maxHeight > 0)
	if !strings.Contains(view, "clear") || !strings.Contains(view, "session") {
		t.Errorf("negative maxHeight should show all items, got: %q", view)
	}
}

// --- P2 edge case tests ---

func TestCompletions_Update_SameQuery_ResetsSelection(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	c.SelectNext() // index = 1

	c.Update("/", true) // same query again
	if c.SelectedIndex() != 0 {
		t.Errorf("re-update should reset selection, got %d", c.SelectedIndex())
	}
}

func TestCompletions_Accept_NonZeroIndex(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true)
	c.SelectNext() // index = 1 → model

	fillText, shouldExec := c.Accept()
	if fillText != "/model " {
		t.Errorf("non-zero index Accept = %q, want %q", fillText, "/model ")
	}
	if shouldExec {
		t.Error("model has args, should not auto-execute")
	}
}

func TestCompletions_Dismiss_Idempotent(t *testing.T) {
	c := NewCompletions()
	c.Dismiss() // already empty
	c.Dismiss() // double dismiss
	if c.Visible() {
		t.Error("double dismiss should still be invisible")
	}
}

// TestCompletions_Update_MaxItemsCap verifies that all matching items are stored
// (no hard cap). Display limiting is handled by Render's maxHeight, not by
// truncating the item list.
func TestCompletions_Update_NoCapOnItems(t *testing.T) {
	// Save and restore sortedCommands + commandDefs + skillDefs
	origCommands := sortedCommands
	origDefs := commandDefs
	origSkills := skillDefs
	t.Cleanup(func() {
		sortedCommands = origCommands
		commandDefs = origDefs
		skillDefs = origSkills
	})

	// Inject 10 commands — all must be available for scrolling
	injectCommands := map[string]CommandDef{
		"aaa": {Description: "A", HasArgs: false},
		"bbb": {Description: "B", HasArgs: false},
		"ccc": {Description: "C", HasArgs: false},
		"ddd": {Description: "D", HasArgs: false},
		"eee": {Description: "E", HasArgs: false},
		"fff": {Description: "F", HasArgs: false},
		"ggg": {Description: "G", HasArgs: false},
		"hhh": {Description: "H", HasArgs: false},
		"iii": {Description: "I", HasArgs: false},
		"jjj": {Description: "J", HasArgs: false},
	}
	commandDefs = injectCommands
	skillDefs = nil
	sortedCommands = AllCommands()

	c := NewCompletions()
	c.Update("/", true)

	if !c.Visible() {
		t.Fatal("expected visible")
	}
	items := c.Items()
	if len(items) != 10 {
		t.Fatalf("expected all 10 items (no cap), got %d", len(items))
	}
}

// TestCompletions_Render_ViewportScrolling verifies that Render shows a viewport
// window around the selected item, not always from index 0.
func TestCompletions_Render_ViewportScrolling(t *testing.T) {
	origCommands := sortedCommands
	origDefs := commandDefs
	origSkills := skillDefs
	t.Cleanup(func() {
		sortedCommands = origCommands
		commandDefs = origDefs
		skillDefs = origSkills
	})

	// 10 commands, render viewport of 3
	commandDefs = map[string]CommandDef{
		"aaa": {Description: "A", HasArgs: false},
		"bbb": {Description: "B", HasArgs: false},
		"ccc": {Description: "C", HasArgs: false},
		"ddd": {Description: "D", HasArgs: false},
		"eee": {Description: "E", HasArgs: false},
		"fff": {Description: "F", HasArgs: false},
		"ggg": {Description: "G", HasArgs: false},
		"hhh": {Description: "H", HasArgs: false},
		"iii": {Description: "I", HasArgs: false},
		"jjj": {Description: "J", HasArgs: false},
	}
	skillDefs = nil
	sortedCommands = AllCommands()

	c := NewCompletions()
	c.Update("/", true)

	// Scroll to index 5 (fff)
	for range 5 {
		c.SelectNext()
	}
	if c.SelectedIndex() != 5 {
		t.Fatalf("index = %d, want 5", c.SelectedIndex())
	}

	// Render with maxHeight=3 — viewport should show items around index 5
	view := c.Render(80, 3)
	lines := strings.Split(view, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 rendered lines, got %d", len(lines))
	}

	// The viewport should contain "fff" (the selected item), NOT "aaa"
	viewText := stripANSI(view)
	if !strings.Contains(viewText, "fff") {
		t.Errorf("viewport should contain selected item 'fff', got: %s", viewText)
	}
	if strings.Contains(viewText, "aaa") {
		t.Errorf("viewport should not contain item 'aaa' (scrolled past), got: %s", viewText)
	}
}

// TestCompletions_Render_MaxVisibleRows verifies that Render caps display to
// maxVisibleCompletions rows even when terminal has room for more.
func TestCompletions_Render_MaxVisibleRows(t *testing.T) {
	origCommands := sortedCommands
	origDefs := commandDefs
	origSkills := skillDefs
	t.Cleanup(func() {
		sortedCommands = origCommands
		commandDefs = origDefs
		skillDefs = origSkills
	})

	// 10 commands, but render should show at most maxVisibleCompletions (5)
	commandDefs = map[string]CommandDef{
		"aaa": {Description: "A", HasArgs: false},
		"bbb": {Description: "B", HasArgs: false},
		"ccc": {Description: "C", HasArgs: false},
		"ddd": {Description: "D", HasArgs: false},
		"eee": {Description: "E", HasArgs: false},
		"fff": {Description: "F", HasArgs: false},
		"ggg": {Description: "G", HasArgs: false},
		"hhh": {Description: "H", HasArgs: false},
		"iii": {Description: "I", HasArgs: false},
		"jjj": {Description: "J", HasArgs: false},
	}
	skillDefs = nil
	sortedCommands = AllCommands()

	c := NewCompletions()
	c.Update("/", true)

	// Render with large maxHeight (simulating big terminal)
	view := c.Render(80, 30)
	lines := strings.Split(view, "\n")
	if len(lines) != maxVisibleCompletions {
		t.Fatalf("expected %d visible rows, got %d", maxVisibleCompletions, len(lines))
	}
}
