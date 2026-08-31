package tui

import (
	"strings"
	"testing"
)

func TestNewCompletions(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible after typing '/'")
	}
	items := c.Items()
	if len(items) != 8 {
		t.Fatalf("expected 8 items, got %d", len(items))
	}
	// Must be alphabetical: agent, clear, compact, context, model, rewind, session, think
	if items[0].Name != "agent" {
		t.Errorf("first item = %q, want %q", items[0].Name, "agent")
	}
	if items[1].Name != "clear" {
		t.Errorf("second item = %q, want %q", items[1].Name, "clear")
	}
	if items[2].Name != "compact" {
		t.Errorf("third item = %q, want %q", items[2].Name, "compact")
	}
	if items[3].Name != "context" {
		t.Errorf("fourth item = %q, want %q", items[3].Name, "context")
	}
	if items[4].Name != "model" {
		t.Errorf("fifth item = %q, want %q", items[4].Name, "model")
	}
	if items[5].Name != "rewind" {
		t.Errorf("sixth item = %q, want %q", items[5].Name, "rewind")
	}
	if items[6].Name != "session" {
		t.Errorf("seventh item = %q, want %q", items[6].Name, "session")
	}
	if items[7].Name != "think" {
		t.Errorf("eighth item = %q, want %q", items[7].Name, "think")
	}
}

func TestCompletions_Update_PrefixFilter(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/s", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %v", len(items), items)
	}
	if items[0].Name != "session" {
		t.Errorf("item = %q, want %q", items[0].Name, "session")
	}
}

func TestCompletions_Update_PrefixClear(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/c", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	// /c matches "clear", "compact", and "context"
	if len(items) != 3 {
		t.Fatalf("expected 3 items (clear, compact, context), got %d: %v", len(items), items)
	}
	want := map[string]bool{"clear": true, "compact": true, "context": true}
	for _, item := range items {
		if !want[item.Name] {
			t.Errorf("unexpected item %q", item.Name)
		}
	}
}

func TestCompletions_Update_PrefixModel(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/m", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item (model), got %d: %v", len(items), items)
	}
	if items[0].Name != "model" {
		t.Errorf("item = %q, want %q", items[0].Name, "model")
	}
}

func TestCompletions_Update_NoMatch(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/xyz", true, r)

	if c.Visible() {
		t.Error("should not be visible with no matches")
	}
}

func TestCompletions_Update_IMEGuard(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/你好", true, r)

	if c.Visible() {
		t.Error("should not be visible for non-ASCII (IME) input")
	}
}

func TestCompletions_Update_SpaceDismisses(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	// First make it visible
	c.Update("/s", true, r)
	if !c.Visible() {
		t.Fatal("expected visible after /s")
	}
	// Then type space — should dismiss
	c.Update("/s ", true, r)
	if c.Visible() {
		t.Error("should be dismissed after space")
	}
}

func TestCompletions_Update_NotCursorAtEnd(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/s", false, r)
	if c.Visible() {
		t.Error("should not be visible when cursor not at end")
	}
}

func TestCompletions_Update_NotSlashCommand(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("hello", true, r)
	if c.Visible() {
		t.Error("should not be visible for non-slash input")
	}
}

func TestCompletions_Update_EmptyString(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("", true, r)
	if c.Visible() {
		t.Error("should not be visible for empty input")
	}
}

func TestCompletions_Update_TransitionVisibleToDismissed(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/s", true, r)
	if !c.Visible() {
		t.Fatal("expected visible after /s")
	}
	c.Update("/xyz-nomatch", true, r)
	if c.Visible() {
		t.Error("should be dismissed after no-match transition")
	}
}

// --- Accept tests ---

func TestCompletions_Accept_HasArgs(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/session", true, r)

	fillText, shouldExecute := c.Accept()
	if fillText != "/session " {
		t.Errorf("fillText = %q, want %q", fillText, "/session ")
	}
	if shouldExecute {
		t.Error("session has args, should not auto-execute")
	}
}

func TestCompletions_Accept_NoArgs(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/clear", true, r)

	fillText, shouldExecute := c.Accept()
	if fillText != "/clear " {
		t.Errorf("fillText = %q, want %q", fillText, "/clear ")
	}
	if !shouldExecute {
		t.Error("clear has no args, should auto-execute")
	}
}

func TestCompletions_Accept_Empty(t *testing.T) {
	t.Parallel()
	c := NewCompletions()
	fillText, shouldExecute := c.Accept()
	if fillText != "" {
		t.Errorf("fillText = %q, want empty", fillText)
	}
	if shouldExecute {
		t.Error("should not execute with no items")
	}
}

// --- Select tests ---

func TestCompletions_SelectNext(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)

	if c.SelectedIndex() != 0 {
		t.Fatalf("initial SelectedIndex = %d, want 0", c.SelectedIndex())
	}
	c.SelectNext()
	if c.SelectedIndex() != 1 {
		t.Errorf("after SelectNext, SelectedIndex = %d, want 1", c.SelectedIndex())
	}
	// Wrap around
	for range 10 {
		c.SelectNext()
	}
	// Should have wrapped; index should be valid
	if c.SelectedIndex() < 0 || c.SelectedIndex() >= len(c.Items()) {
		t.Errorf("SelectNext wrapped to invalid index %d", c.SelectedIndex())
	}
}

func TestCompletions_SelectPrev(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)

	// Start at 0, go prev → should wrap to last
	c.SelectPrev()
	if c.SelectedIndex() != len(c.Items())-1 {
		t.Errorf("after SelectPrev from 0, SelectedIndex = %d, want %d", c.SelectedIndex(), len(c.Items())-1)
	}
}

func TestCompletions_SelectAfterFilter(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	c.SelectNext()
	c.SelectNext()
	// Now filter — selection should reset to 0
	c.Update("/s", true, r)
	if c.SelectedIndex() != 0 {
		t.Errorf("after filter, SelectedIndex = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_Dismiss(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/s", true, r)
	c.Dismiss()
	if c.Visible() {
		t.Error("should not be visible after Dismiss")
	}
}

// --- Render tests ---

func TestCompletions_Render_Basic(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/c", true, r)

	view := c.Render(80, 10)
	if view == "" {
		t.Fatal("expected non-empty render")
	}
	if !strings.Contains(view, "clear") {
		t.Errorf("render should contain 'clear', got: %s", view)
	}
	if !strings.Contains(view, "context") {
		t.Errorf("render should contain 'context', got: %s", view)
	}
}

func TestCompletions_Render_SelectedHighlight(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	// Select second item (clear)
	c.SelectNext()
	view := c.Render(80, 5)
	lines := strings.Split(view, "\n")
	// Second line should contain "clear"
	if !strings.Contains(lines[1], "clear") {
		t.Errorf("second line should contain 'clear', got %q", lines[1])
	}
}

func TestCompletions_Render_MaxHeight(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)

	// maxHeight=2 should cap visible rows
	view := c.Render(80, 2)
	lines := strings.Split(view, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines with maxHeight=2, got %d", len(lines))
	}
}

func TestCompletions_Render_NotVisible(t *testing.T) {
	t.Parallel()
	c := NewCompletions()
	// Don't trigger Update — not visible
	view := c.Render(80, 10)
	if view != "" {
		t.Errorf("expected empty render when not visible, got %q", view)
	}
}

func TestCompletions_Render_Truncation(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/c", true, r)

	// width=10 should truncate long labels
	view := c.Render(10, 10)
	for line := range strings.SplitSeq(view, "\n") {
		stripped := stripANSI(line)
		if len(stripped) > 10 {
			t.Errorf("line not truncated to width 10: %q (len %d)", stripped, len(stripped))
		}
	}
}

func TestCompletions_Update_ResetsSelection(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	c.SelectNext()
	c.SelectNext()
	if c.SelectedIndex() == 0 {
		t.Fatal("test setup: expected non-zero index")
	}
	// Trigger another Update — index should reset
	c.Update("/", true, r)
	if c.SelectedIndex() != 0 {
		t.Errorf("after Update, SelectedIndex = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_ItemMetadata(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/session", true, r)

	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "session" {
		t.Errorf("Name = %q, want %q", items[0].Name, "session")
	}
	if items[0].Description == "" {
		t.Error("Description should not be empty")
	}
	if !items[0].HasArgs {
		t.Error("session should have args")
	}
}

func TestCompletions_SelectNext_EmptyItems(t *testing.T) {
	t.Parallel()
	c := NewCompletions()
	c.SelectNext() // should not panic
}

func TestCompletions_SelectPrev_EmptyItems(t *testing.T) {
	t.Parallel()
	c := NewCompletions()
	c.SelectPrev() // should not panic
}

func TestCompletions_Render_NegativeMaxHeight(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	view := c.Render(80, -1)
	// Negative maxHeight should show all items (guard: maxHeight > 0)
	if !strings.Contains(view, "clear") || !strings.Contains(view, "session") {
		t.Errorf("negative maxHeight should show all items, got: %q", view)
	}
}

func TestCompletions_Update_SameQuery_ResetsSelection(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	c.SelectNext()
	c.SelectNext()
	// Same query — should still reset selection
	c.Update("/", true, r)
	if c.SelectedIndex() != 0 {
		t.Errorf("after Update with same query, SelectedIndex = %d, want 0", c.SelectedIndex())
	}
}

func TestCompletions_Accept_NonZeroIndex(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	c := NewCompletions()
	c.Update("/", true, r)
	// Move to index 1 (clear)
	c.SelectNext()
	fillText, _ := c.Accept()
	if fillText != "/clear " {
		t.Errorf("fillText at index 1 = %q, want %q", fillText, "/clear ")
	}
}

func TestCompletions_Dismiss_Idempotent(t *testing.T) {
	t.Parallel()
	c := NewCompletions()
	c.Dismiss()
	c.Dismiss() // should not panic
	if c.Visible() {
		t.Error("should not be visible")
	}
}

// TestCompletions_Update_NoCapOnItems verifies that all matching items are stored
// (no hard cap). Display limiting is handled by Render's maxHeight, not by
// truncating the item list.
func TestCompletions_Update_NoCapOnItems(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
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
	})

	c := NewCompletions()
	c.Update("/", true, r)

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
	t.Parallel()
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
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
	})

	c := NewCompletions()
	c.Update("/", true, r)

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
	t.Parallel()
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
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
	})

	c := NewCompletions()
	c.Update("/", true, r)

	view := c.Render(80, 30)
	lines := strings.Split(view, "\n")
	if len(lines) != maxVisibleCompletions {
		t.Fatalf("expected %d visible rows, got %d", maxVisibleCompletions, len(lines))
	}
}

// ---------------------------------------------------------------------------
// Part-based matching — source: TS commandSuggestions.ts
// ---------------------------------------------------------------------------

func TestCompletions_PartMatch_PluginSkill(t *testing.T) {
	t.Parallel()
	// /ral should match "oh-my-claudecode:ralph" via part prefix
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
		"clear": {Description: "Clear conversation", HasArgs: false},
	})
	r.RegisterSkillCommands(map[string]CommandDef{
		"oh-my-claudecode:ralph":     {Description: "Persistent agent loop", HasArgs: true},
		"oh-my-claudecode:autopilot": {Description: "Autopilot mode", HasArgs: true},
		"oh-my-claudecode:cancel":    {Description: "Cancel active mode", HasArgs: false},
	})

	c := NewCompletions()
	c.Update("/ral", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible for /ral")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %v", len(items), items)
	}
	if items[0].Name != "oh-my-claudecode:ralph" {
		t.Errorf("item = %q, want %q", items[0].Name, "oh-my-claudecode:ralph")
	}
}

func TestCompletions_PartMatch_AutoPrefix(t *testing.T) {
	t.Parallel()
	// /auto should match "oh-my-claudecode:autopilot" via part prefix
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{})
	r.RegisterSkillCommands(map[string]CommandDef{
		"oh-my-claudecode:ralph":     {Description: "Persistent agent loop", HasArgs: true},
		"oh-my-claudecode:autopilot": {Description: "Autopilot mode", HasArgs: true},
	})

	c := NewCompletions()
	c.Update("/auto", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible for /auto")
	}
	items := c.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %v", len(items), items)
	}
	if items[0].Name != "oh-my-claudecode:autopilot" {
		t.Errorf("item = %q, want %q", items[0].Name, "oh-my-claudecode:autopilot")
	}
}

func TestCompletions_PartMatch_FullPrefixWins(t *testing.T) {
	t.Parallel()
	// If a builtin starts with "r" and a plugin skill has part "ralph",
	// the full prefix match (builtin) should appear first.
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
		"rewind": {Description: "Rewind conversation", HasArgs: false},
	})
	r.RegisterSkillCommands(map[string]CommandDef{
		"oh-my-claudecode:ralph": {Description: "Persistent agent loop", HasArgs: true},
	})

	c := NewCompletions()
	c.Update("/r", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible for /r")
	}
	items := c.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %v", len(items), items)
	}
	if items[0].Name != "rewind" {
		t.Errorf("first item = %q, want %q (full prefix should win)", items[0].Name, "rewind")
	}
	if items[1].Name != "oh-my-claudecode:ralph" {
		t.Errorf("second item = %q, want %q", items[1].Name, "oh-my-claudecode:ralph")
	}
}

func TestCompletions_PartMatch_ClaudecodeMatches(t *testing.T) {
	t.Parallel()
	// /claude should match "oh-my-claudecode:ralph" via part prefix on "claudecode"
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{})
	r.RegisterSkillCommands(map[string]CommandDef{
		"oh-my-claudecode:ralph":  {Description: "Persistent agent loop", HasArgs: true},
		"oh-my-claudecode:cancel": {Description: "Cancel mode", HasArgs: false},
	})

	c := NewCompletions()
	c.Update("/claude", true, r)

	if !c.Visible() {
		t.Fatal("expected completions visible for /claude")
	}
	items := c.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 items (both have claudecode part), got %d: %v", len(items), items)
	}
}

func TestCompletions_PartMatch_NoMatch(t *testing.T) {
	t.Parallel()
	// /xyz should not match any plugin skill
	r := NewCommandRegistryWithBuiltins(map[string]CommandDef{
		"clear": {Description: "Clear conversation", HasArgs: false},
	})
	r.RegisterSkillCommands(map[string]CommandDef{
		"oh-my-claudecode:ralph": {Description: "Persistent agent loop", HasArgs: true},
	})

	c := NewCompletions()
	c.Update("/xyz", true, r)

	if c.Visible() {
		t.Error("should not be visible with no matches")
	}
}
