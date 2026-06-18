package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// Pre-cached styles for completion rendering (consistent with components.go pattern).
var (
	styleCompletionSelected = lipgloss.NewStyle().Reverse(true)
	styleCompletionNormal   = lipgloss.NewStyle()
)

// maxVisibleCompletions is the maximum number of rows shown in the dropdown.
// Scrolling reveals additional items beyond this window.
const maxVisibleCompletions = 5

// Completion represents a single suggestion item.
type Completion struct {
	Name        string
	Description string
	HasArgs     bool
}

// Completions holds the active completion state.
type Completions struct {
	items   []Completion
	index   int // selected index, 0-based
	visible bool
}

// NewCompletions creates an empty Completions state.
func NewCompletions() *Completions {
	return &Completions{}
}

// Update regenerates completions for the given input text.
// Filtering uses commandMatchPriority: exact > full prefix > part prefix.
// The registry supplies the per-App command tables.
func (c *Completions) Update(text string, cursorAtEnd bool, registry *CommandRegistry) {
	// Guard: must start with "/" and cursor at end
	if !cursorAtEnd || !strings.HasPrefix(text, "/") {
		c.dismiss()
		return
	}

	query := text[1:]

	// Guard: space means user is typing args → dismiss
	if strings.Contains(query, " ") {
		c.dismiss()
		return
	}

	// Guard: non-ASCII (IME input) → dismiss
	for _, ch := range query {
		if ch > unicode.MaxASCII {
			c.dismiss()
			return
		}
	}

	// Filter commands by match priority.
	type matchEntry struct {
		completion Completion
		priority   int
	}
	var matched []matchEntry
	for _, name := range registry.sortedCommands {
		def, ok := registry.getCommandDef(name)
		if !ok {
			continue
		}
		prio := commandMatchPriority(name, query)
		if query == "" {
			prio = 3 // show all when query is empty
		}
		if prio >= 0 {
			matched = append(matched, matchEntry{
				completion: Completion{
					Name:        name,
					Description: def.Description,
					HasArgs:     def.HasArgs,
				},
				priority: prio,
			})
		}
	}

	if len(matched) == 0 {
		c.dismiss()
		return
	}

	// Stable sort by priority (preserves alphabetical order within same priority)
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].priority < matched[j].priority
	})

	// Extract completions
	completions := make([]Completion, len(matched))
	for i, m := range matched {
		completions[i] = m.completion
	}

	c.items = completions
	c.index = 0
	c.visible = true
}

// Accept returns the fill text and whether it should execute immediately.
//
// TS: formatCommand() always returns "/<name> " with trailing space.
// Tab: uses fillText only, never executes.
// Enter: uses fillText AND executes if !HasArgs.
func (c *Completions) Accept() (fillText string, shouldExecute bool) {
	if len(c.items) == 0 {
		return "", false
	}
	item := c.items[c.index]
	fillText = "/" + item.Name + " "
	shouldExecute = !item.HasArgs
	return fillText, shouldExecute
}

// SelectNext moves the selection cursor down (wraps around).
func (c *Completions) SelectNext() {
	if len(c.items) == 0 {
		return
	}
	c.index = (c.index + 1) % len(c.items)
}

// SelectPrev moves the selection cursor up (wraps around).
func (c *Completions) SelectPrev() {
	if len(c.items) == 0 {
		return
	}
	c.index = (c.index - 1 + len(c.items)) % len(c.items)
}

// Dismiss hides the completion list.
func (c *Completions) Dismiss() {
	c.dismiss()
}

// Visible returns whether completions are shown.
func (c *Completions) Visible() bool {
	return c.visible
}

// Items returns current items (for rendering).
func (c *Completions) Items() []Completion {
	return c.items
}

// SelectedIndex returns the current selection (-1 if none).
func (c *Completions) SelectedIndex() int {
	if !c.visible {
		return -1
	}
	return c.index
}

// Render builds the dropdown view string.
// maxHeight limits the number of visible rows (for small terminals).
// The viewport scrolls so that the selected item is always visible.
func (c *Completions) Render(width int, maxHeight int) string {
	if !c.visible || len(c.items) == 0 {
		return ""
	}

	// Determine viewport size: min(total, maxVisibleCompletions, maxHeight)
	total := len(c.items)
	viewport := min(total, maxVisibleCompletions)
	if maxHeight > 0 && viewport > maxHeight {
		viewport = maxHeight
	}

	// Compute viewport start so that c.index is visible
	start := 0
	if total > viewport {
		// Scroll to keep selected item in view
		start = max(c.index-viewport/2, 0)
		if start+viewport > total {
			start = total - viewport
		}
	}

	var b strings.Builder
	for i := range viewport {
		idx := start + i
		item := c.items[idx]
		// Format: "  /name - Description"
		label := fmt.Sprintf("  /%s - %s", item.Name, item.Description)
		if len(label) > width {
			label = label[:width]
		}

		if idx == c.index {
			b.WriteString(styleCompletionSelected.Render(label))
		} else {
			b.WriteString(styleCompletionNormal.Render(label))
		}
		if i < viewport-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// dismiss resets the completion state.
func (c *Completions) dismiss() {
	c.items = nil
	c.index = 0
	c.visible = false
}
