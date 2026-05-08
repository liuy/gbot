package tui

import (
	"fmt"
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
//
// Algorithm (aligned with TS generateCommandSuggestions):
//  1. text must start with "/" and cursorAtEnd must be true
//  2. Query = text after "/" up to first space (exclusive)
//  3. If text contains space → dismiss (user is typing args)
//  4. Non-ASCII in query → dismiss (IME guard)
//  5. If query == "": return all commands, alphabetical, max 5
//  6. If query != "": filter by HasPrefix, alphabetical, max 5
//  7. No matches → dismiss
func (c *Completions) Update(text string, cursorAtEnd bool) {
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

	// Filter commands by prefix
	var matched []Completion
	for _, name := range sortedCommands {
		def, ok := getCommandDef(name)
		if !ok {
			continue
		}
		if query == "" || strings.HasPrefix(name, query) {
			matched = append(matched, Completion{
				Name:        name,
				Description: def.Description,
				HasArgs:     def.HasArgs,
			})
		}
	}

	if len(matched) == 0 {
		c.dismiss()
		return
	}

	c.items = matched
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
