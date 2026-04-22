package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PickerItem is the interface for items displayed in a ListPicker.
type PickerItem interface {
	Label() string
}

// SessionItem represents a session in the picker list.
type SessionItem struct {
	SessionID string
	Title     string
	UpdatedAt time.Time
}

// Label returns a display line for the session item (name + relative time).
func (s *SessionItem) Label() string {
	name := s.Title
	if name == "" && len(s.SessionID) >= 8 {
		name = s.SessionID[:8]
	} else if name == "" {
		name = s.SessionID
	}
	return fmt.Sprintf("%-20s %s", name, relativeTime(s.UpdatedAt))
}

// ListPickerOption is a functional option for ListPicker.
type ListPickerOption func(*ListPicker)

// WithInitialCursor sets the initial cursor position.
// Out-of-range values are clamped to valid bounds.
func WithInitialCursor(idx int) ListPickerOption {
	return func(p *ListPicker) {
		if idx < 0 {
			p.cursor = 0
		} else if len(p.items) > 0 && idx >= len(p.items) {
			p.cursor = len(p.items) - 1
		} else {
			p.cursor = idx
		}
	}
}

// ListPicker is a generic Bubble Tea model for selecting from a list of items.
type ListPicker struct {
	title    string
	items    []PickerItem
	cursor   int
	selected int   // -1 = none
	aborted  bool
	width    int
	height   int
}

// NewListPicker creates a picker with the given title and items.
func NewListPicker(title string, items []PickerItem, opts ...ListPickerOption) *ListPicker {
	p := &ListPicker{
		title:    title,
		items:    items,
		selected: -1,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Init satisfies tea.Model.
func (p *ListPicker) Init() tea.Cmd { return nil }

// Update handles key events for the picker.
func (p *ListPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if len(p.items) > 0 {
				if p.cursor > 0 {
					p.cursor--
				} else {
					p.cursor = len(p.items) - 1
				}
			}
		case "down", "j":
			if len(p.items) > 0 {
				if p.cursor < len(p.items)-1 {
					p.cursor++
				} else {
					p.cursor = 0
				}
			}
		case "enter":
			if len(p.items) == 0 {
				p.aborted = true
				return p, nil
			}
			if p.cursor < len(p.items) {
				p.selected = p.cursor
			}
			return p, nil
		case "esc", "q":
			p.aborted = true
			return p, nil
		}
	}

	return p, nil
}

// View renders the picker.
func (p *ListPicker) View() string {
	if len(p.items) == 0 {
		return "No items available.\n\nPress Esc to cancel."
	}

	highlight := lipgloss.NewStyle().
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	normal := lipgloss.NewStyle().
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		MarginBottom(1)

	var s strings.Builder
	s.WriteString(titleStyle.Render(p.title) + "\n")

	for i, item := range p.items {
		row := "  " + item.Label()

		if i == p.cursor {
			s.WriteString(highlight.Render(row) + "\n")
		} else {
			s.WriteString(normal.Render(row) + "\n")
		}
	}

	s.WriteString("\n" + lipgloss.NewStyle().Faint(true).Render("↑/k up · ↓/j down · Enter select · Esc/q cancel"))
	return s.String()
}

// Done returns true if the picker has finished (selected or aborted).
func (p *ListPicker) Done() bool {
	return p.aborted || p.selected >= 0
}

// Aborted returns true if the user cancelled the picker.
func (p *ListPicker) Aborted() bool {
	return p.aborted
}

// SelectedIndex returns the index of the selected item, or -1 if none/aborted.
func (p *ListPicker) SelectedIndex() int {
	return p.selected
}

// relativeTime returns a human-friendly relative time string.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// openPicker loads sessions and opens the session picker overlay.
func (a *App) openPicker(commitCmd tea.Cmd) tea.Cmd {
	sessions, err := a.store.ListSessions(a.projectDir, 100)
	if err != nil {
		return a.showInfo(fmt.Sprintf("Failed to list sessions: %v", err))
	}

	items := make([]SessionItem, len(sessions))
	for i, s := range sessions {
		items[i] = SessionItem{
			SessionID: s.SessionID,
			Title:     s.Title,
			UpdatedAt: s.UpdatedAt,
		}
	}

	if a.listPicker != nil {
		return a.showInfo("A picker is already open")
	}

	pickerItems := make([]PickerItem, len(items))
	for i := range items {
		pickerItems[i] = &items[i]
	}
	a.listPicker = NewListPicker("Switch Session", pickerItems)

	captured := items
	a.onPickerDone = func(p *ListPicker) (tea.Model, tea.Cmd) {
		return a.handleSessionPickerDone(p, captured)
	}
	return commitCmd
}

// handleSessionPickerDone processes the session picker selection or cancellation.
func (a *App) handleSessionPickerDone(p *ListPicker, items []SessionItem) (tea.Model, tea.Cmd) {
	a.listPicker = nil
	a.onPickerDone = nil

	if p.Aborted() {
		return a, nil
	}

	idx := p.SelectedIndex()
	if idx < 0 || idx >= len(items) {
		return a, nil
	}

	selected := items[idx]

	// Same session — no-op
	if selected.SessionID == a.sessionID {
		return a, a.showInfo("Already on this session")
	}

	// Resume the selected session
	engineMsgs, err := loadAndConvertMessages(a.store, selected.SessionID)
	if err != nil {
		return a, a.showInfo(fmt.Sprintf("Failed to load session: %v", err))
	}

	a.engine.SetMessages(engineMsgs)
	a.engine.SetSessionID(selected.SessionID)
	a.sessionID = selected.SessionID
	a.lastPersistedIdx = len(engineMsgs)

	*a.repl = *NewReplState()
	a.committedCount = 0

	title := selected.Title
	if title == "" {
		title = selected.SessionID[:8]
	}
	return a, a.showInfo(fmt.Sprintf("Switched to session: %s", title))
}
