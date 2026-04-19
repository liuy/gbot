package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SessionItem represents a session in the picker list.
type SessionItem struct {
	SessionID string
	Title     string
	UpdatedAt time.Time
}

// SessionPicker is a Bubble Tea model for selecting a session.
type SessionPicker struct {
	items    []SessionItem
	cursor   int
	selected *SessionItem
	aborted  bool
	width    int
	height   int
}

// NewSessionPicker creates a picker with the given session items.
func NewSessionPicker(items []SessionItem) *SessionPicker {
	return &SessionPicker{
		items: items,
	}
}

// Init satisfies tea.Model.
func (p *SessionPicker) Init() tea.Cmd { return nil }

// Update handles key events for the picker.
func (p *SessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "down", "j":
			if p.cursor < len(p.items)-1 {
				p.cursor++
			}
		case "enter":
			if p.cursor < len(p.items) {
				item := p.items[p.cursor]
				p.selected = &item
			}
			return p, tea.Quit
		case "esc", "q":
			p.aborted = true
			return p, tea.Quit
		}
	}

	return p, nil
}

// View renders the picker.
func (p *SessionPicker) View() string {
	if len(p.items) == 0 {
		return "No sessions found.\n\nPress Esc to cancel."
	}

	highlight := lipgloss.NewStyle().
		Background(lipgloss.Color("63")).
		Foreground(lipgloss.Color("230")).
		Padding(0, 1)

	normal := lipgloss.NewStyle().
		Padding(0, 1)

	title := lipgloss.NewStyle().
		Bold(true).
		MarginBottom(1)

	var s string
	s += title.Render("Switch Session") + "\n"

	for i, item := range p.items {
		label := item.Label()
		timeStr := relativeTime(item.UpdatedAt)

		row := fmt.Sprintf("  %s  %s", label, timeStr)

		if i == p.cursor {
			s += highlight.Render(row) + "\n"
		} else {
			s += normal.Render(row) + "\n"
		}
	}

	s += "\n" + lipgloss.NewStyle().Faint(true).Render("↑/k up · ↓/j down · Enter select · Esc cancel")
	return s
}

// Label returns a display name for the session item.
func (s *SessionItem) Label() string {
	if s.Title != "" {
		return s.Title
	}
	if len(s.SessionID) >= 8 {
		return s.SessionID[:8]
	}
	return s.SessionID
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
	sessions, err := a.store.ListSessions("", 100)
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

	a.picker = NewSessionPicker(items)
	a.pickerMode = true
	return commitCmd
}

// handlePickerResult processes the picker selection or cancellation.
func (a *App) handlePickerResult() (tea.Model, tea.Cmd) {
	a.pickerMode = false

	if a.picker.aborted {
		a.picker = nil
		return a, nil
	}

	selected := a.picker.selected
	a.picker = nil

	if selected == nil {
		return a, nil
	}

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
