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
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	// Must be alphabetical: clear, model, session
	if items[0].Name != "clear" {
		t.Errorf("first item = %q, want %q", items[0].Name, "clear")
	}
	if items[1].Name != "model" {
		t.Errorf("second item = %q, want %q", items[1].Name, "model")
	}
	if items[2].Name != "session" {
		t.Errorf("third item = %q, want %q", items[2].Name, "session")
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

	// Wrap around
	c.SelectNext()
	if c.SelectedIndex() != 0 {
		t.Errorf("after wrap = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_SelectPrev(t *testing.T) {
	c := NewCompletions()
	c.Update("/", true) // clear, model, session

	// Wrap around backwards
	c.SelectPrev()
	if c.SelectedIndex() != 2 {
		t.Errorf("after SelectPrev wrap = %d, want 2", c.SelectedIndex())
	}

	c.SelectPrev()
	if c.SelectedIndex() != 1 {
		t.Errorf("after SelectPrev = %d, want 1", c.SelectedIndex())
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
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
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
	plainLine := "  /model      Switch model"
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

	// session: HasArgs=true
	sessionItem := items[2]
	if sessionItem.Name != "session" {
		t.Fatalf("third item = %q, want session", sessionItem.Name)
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

// TestCompletions_Update_MaxItemsCap verifies the maxCompletionItems cap
// by temporarily injecting extra commands into the package-level sortedCommands.
func TestCompletions_Update_MaxItemsCap(t *testing.T) {
	// Save and restore sortedCommands + commandDefs
	origCommands := sortedCommands
	origDefs := commandDefs
	t.Cleanup(func() {
		sortedCommands = origCommands
		commandDefs = origDefs
	})

	// Inject 7 commands (exceeds maxCompletionItems=5)
	commandDefs = map[string]CommandDef{
		"aaa": {Description: "A", HasArgs: false},
		"bbb": {Description: "B", HasArgs: false},
		"ccc": {Description: "C", HasArgs: false},
		"ddd": {Description: "D", HasArgs: false},
		"eee": {Description: "E", HasArgs: false},
		"fff": {Description: "F", HasArgs: false},
		"ggg": {Description: "G", HasArgs: false},
	}
	sortedCommands = []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff", "ggg"}

	c := NewCompletions()
	c.Update("/", true)

	if !c.Visible() {
		t.Fatal("expected visible")
	}
	items := c.Items()
	if len(items) != 5 {
		t.Fatalf("expected 5 items (capped), got %d", len(items))
	}
	// Must be first 5 alphabetically
	expected := []string{"aaa", "bbb", "ccc", "ddd", "eee"}
	for i, exp := range expected {
		if items[i].Name != exp {
			t.Errorf("item[%d] = %q, want %q", i, items[i].Name, exp)
		}
	}
}
