package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Cached dialog styles — allocated once, not per View() call.
var (
	dialogBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("63")).
				Padding(1, 2)
	dialogTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("212"))
	dialogLabelStyle = lipgloss.NewStyle().Faint(true)
	dialogHighlight  = lipgloss.NewStyle().
				Background(lipgloss.Color("63")).
				Foreground(lipgloss.Color("230")).
				Padding(0, 1)
	dialogNormal = lipgloss.NewStyle().Padding(0, 1)
	dialogHint   = lipgloss.NewStyle().Faint(true)
)

// DialogOption is one selectable choice in a Dialog.
type DialogOption struct {
	Label    string // display text
	Shortcut string // optional single-key shortcut (e.g., "y", "n")
}

// DialogDetail is a key-value line displayed between the title and options.
type DialogDetail struct {
	Label string // "Tool:", "Command:", "Reason:", etc.
	Value string // content
}

// Dialog is a unified modal overlay for both list picking and permission asking.
// It renders with a rounded border, optional detail section, and selectable options.
type Dialog struct {
	title   string
	details []DialogDetail
	options []DialogOption
	cursor  int
	done    bool  // user made a selection
	aborted bool  // user cancelled (esc/q)
	width   int
	height  int
}

// NewDialog creates a dialog with the given title, options, and optional detail lines.
func NewDialog(title string, options []DialogOption, details ...DialogDetail) *Dialog {
	return &Dialog{
		title:   title,
		details: details,
		options: options,
		cursor:  0,
	}
}

// Done returns true if the user has selected an option or cancelled.
func (d *Dialog) Done() bool {
	return d.done || d.aborted
}

// Aborted returns true if the user cancelled the dialog.
func (d *Dialog) Aborted() bool {
	return d.aborted
}

// SelectedIndex returns the index of the selected option, or -1 if aborted.
func (d *Dialog) SelectedIndex() int {
	if d.aborted {
		return -1
	}
	if d.done && d.cursor >= 0 && d.cursor < len(d.options) {
		return d.cursor
	}
	return -1
}

// HandleKey processes a key event. Returns true if the key was consumed.
func (d *Dialog) HandleKey(key tea.KeyMsg) bool {
	if d.Done() {
		return true
	}

	switch key.String() {
	case "up", "k":
		if len(d.options) > 0 {
			if d.cursor > 0 {
				d.cursor--
			} else {
				d.cursor = len(d.options) - 1
			}
		}
		return true
	case "down", "j":
		if len(d.options) > 0 {
			if d.cursor < len(d.options)-1 {
				d.cursor++
			} else {
				d.cursor = 0
			}
		}
		return true
	case "enter":
		if len(d.options) == 0 {
			d.aborted = true
			return true
		}
		d.done = true
		return true
	case "esc", "q":
		d.aborted = true
		return true
	default:
		// Check shortcut keys
		pressed := key.String()
		for _, opt := range d.options {
			if opt.Shortcut == pressed {
				d.cursor = indexOfOption(d.options, opt)
				d.done = true
				return true
			}
		}
		// Intercept all other keys to prevent them reaching normal handlers
		return true
	}
}

// indexOfOption finds the index of a specific option in the slice.
func indexOfOption(options []DialogOption, target DialogOption) int {
	for i, opt := range options {
		if opt == target {
			return i
		}
	}
	return 0
}

// View renders the dialog with border, details, options, and hints.
func (d *Dialog) View() string {
	if len(d.options) == 0 {
		return dialogBorderStyle.Render("No items available.\n\nPress Esc to cancel.")
	}

	var b strings.Builder

	// Title
	b.WriteString(dialogTitleStyle.Render(d.title))
	b.WriteString("\n\n")

	// Detail lines
	for _, det := range d.details {
		b.WriteString(dialogLabelStyle.Render(det.Label + " "))
		b.WriteString(det.Value)
		b.WriteString("\n")
	}
	if len(d.details) > 0 {
		b.WriteString("\n")
	}

	// Options
	for i, opt := range d.options {
		row := "  " + opt.Label
		if i == d.cursor {
			b.WriteString(dialogHighlight.Render(row))
		} else {
			b.WriteString(dialogNormal.Render(row))
		}
		b.WriteString("\n")
	}

	// Hint line
	b.WriteString("\n")
	hint := d.buildHint()
	b.WriteString(dialogHint.Render(hint))

	return dialogBorderStyle.Render(b.String())
}

// buildHint constructs the keybinding hint line based on available shortcuts.
func (d *Dialog) buildHint() string {
	var parts []string

	// Add shortcut hints
	for _, opt := range d.options {
		if opt.Shortcut != "" {
			parts = append(parts, opt.Shortcut+" "+opt.Label)
		}
	}

	// Always show navigation hints
	parts = append(parts, "↑/k up · ↓/j down · Enter select · Esc cancel")

	// Find the separator: first navigation hint index
	navStart := 0
	for i, p := range parts {
		if p == "↑/k up · ↓/j down · Enter select · Esc cancel" {
			navStart = i
			break
		}
	}

	if navStart > 0 {
		return strings.Join(parts[:navStart], " · ") + " · " + parts[navStart]
	}
	return parts[0]
}
