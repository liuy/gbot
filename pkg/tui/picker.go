package tui

import (
	"fmt"
	"log/slog"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// PickerItem is the interface for items displayed in a list dialog.
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

// pickerItemsToOptions converts PickerItem slice to DialogOption slice.
func pickerItemsToOptions(items []PickerItem) []DialogOption {
	opts := make([]DialogOption, len(items))
	for i, item := range items {
		opts[i] = DialogOption{Label: item.Label()}
	}
	return opts
}

// openPicker loads sessions and opens the session picker dialog.
func (a *App) openPicker(commitCmd tea.Cmd) tea.Cmd {
	sessions, err := a.engine.ListSessions(100)
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

	if a.activeDialog != nil {
		return a.showInfo("A dialog is already open")
	}

	pickerItems := make([]PickerItem, len(items))
	for i := range items {
		pickerItems[i] = &items[i]
	}

	a.activeDialog = NewDialog("Switch Session", pickerItemsToOptions(pickerItems))
	a.activeDialog.width = a.width

	captured := items
	a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
		return a.handleSessionPickerDone(d, captured)
	}
	return commitCmd
}

// handleSessionPickerDone processes the session picker selection or cancellation.
func (a *App) handleSessionPickerDone(d *Dialog, items []SessionItem) (tea.Model, tea.Cmd) {
	if d.Aborted() {
		return a, nil
	}

	idx := d.SelectedIndex()
	if idx < 0 || idx >= len(items) {
		return a, nil
	}

	selected := items[idx]

	// Same session — no-op
	if selected.SessionID == a.sessionID {
		return a, a.showInfo("Already on this session")
	}

	// Resume the selected session via engine
	engineMsgs, err := a.engine.SwitchSession(selected.SessionID)
	if err != nil {
		return a, a.showInfo(fmt.Sprintf("Failed to load session: %v", err))
	}

	a.sessionID = selected.SessionID

	*a.repl = *NewReplState()
	a.repl.messages = engineMessagesToViews(engineMsgs)
	a.committedCount = len(a.repl.messages)

	slog.Info("session: switched via picker", "sessionID", selected.SessionID, "messages", len(engineMsgs))

	// Update workspace meta so restart resumes the correct session
	if err := WriteWorkspaceMeta(a.projectDir, a.sessionID); err != nil {
		slog.Warn("session picker: write workspace meta failed", "error", err)
	}

	title := selected.Title
	if title == "" {
		title = selected.SessionID[:8]
	}
	return a, a.showInfo(fmt.Sprintf("Switched to session: %s", title))
}

// NewListPicker creates a Dialog from PickerItem slice with optional functional options.
// Convenience wrapper for test callers.
func NewListPicker(title string, items []PickerItem, opts ...func(*Dialog)) *Dialog {
	d := NewDialog(title, pickerItemsToOptions(items))
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

// WithInitialCursor returns a function that sets the initial cursor on a Dialog.
func WithInitialCursor(idx int) func(*Dialog) {
	return func(d *Dialog) {
		if idx < 0 {
			d.cursor = 0
		} else if len(d.options) > 0 && idx >= len(d.options) {
			d.cursor = len(d.options) - 1
		} else {
			d.cursor = idx
		}
		d.clampScroll()
	}
}

// ApplyDialogOption applies a functional option to a Dialog.
func applyDialogOption(d *Dialog, opt func(*Dialog)) {
	if opt != nil {
		opt(d)
	}
}

// Ensure Dialog satisfies tea.Model.
var _ tea.Model = (*Dialog)(nil)

// Init satisfies tea.Model.
func (d *Dialog) Init() tea.Cmd { return nil }

// Update satisfies tea.Model — delegates to HandleKey for KeyMsg.
func (d *Dialog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
	case tea.KeyMsg:
		d.HandleKey(msg)
	}
	return d, nil
}
