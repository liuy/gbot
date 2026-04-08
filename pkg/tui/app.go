package tui

import (
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
)

// ---------------------------------------------------------------------------
// App — bubbletea root Model
// ---------------------------------------------------------------------------

// App is the root bubbletea Model.
// Source: App.tsx → bubbletea root Model
type App struct {
	width  int
	height int

	// Components
	input   *Input
	status  StatusBar
	spinner Spinner

	// REPL session state (delegated to repl.go)
	repl *ReplState

	// Engine
	engine       *engine.Engine
	systemPrompt json.RawMessage
}

// NewApp creates a new App model.
func NewApp(eng *engine.Engine, systemPrompt json.RawMessage) *App {
	return &App{
		input:        NewInput(),
		status:       NewStatusBar(),
		spinner:      NewSpinner(),
		repl:         NewReplState(),
		engine:       eng,
		systemPrompt: systemPrompt,
	}
}

// ---------------------------------------------------------------------------
// tea.Model interface
// ---------------------------------------------------------------------------

// Init initializes the TUI.
func (a *App) Init() tea.Cmd {
	return tea.EnterAltScreen
}

// Update handles bubbletea messages.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		a.input.SetWidth(a.width - 4)
		a.status.SetWidth(a.width)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(m)

	// All REPL messages are handled by repl.go
	case streamChunkMsg, streamToolUseMsg, streamToolResultMsg,
		streamCompleteMsg, errMsg, submitMsg, spinnerTickMsg:
		handled, cmd := a.updateRepl(msg)
		if handled {
			return a, cmd
		}
	}

	return a, nil
}

// View renders the entire TUI.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	var sb strings.Builder

	// Render messages (scroll region)
	availHeight := a.height - 4 // status bar + input + borders
	if availHeight < 3 {
		availHeight = 3
	}

	rendered := renderMessages(a.repl.Messages(), a.width, availHeight)
	sb.WriteString(rendered)

	// Streaming assistant output and pending tools (from repl.go)
	a.replView(&sb)

	// Status bar
	sb.WriteString(a.status.View())
	sb.WriteString("\n")

	// Input
	sb.WriteString(a.input.View())

	return sb.String()
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {

	case tea.KeyCtrlC:
		if a.repl.IsStreaming() {
			// Cancel current query
			if a.repl.cancelFunc != nil {
				a.repl.cancelFunc()
				a.repl.cancelFunc = nil
			}
			a.repl.FinishStream(nil)
			return a, nil
		}
		// Not streaming: quit
		return a, tea.Quit

	case tea.KeyEnter:
		text := a.input.Value()
		if strings.TrimSpace(text) == "" {
			return a, nil
		}
		return a, a.handleSubmitRepl(text)

	case tea.KeyBackspace:
		a.input.Backspace()
		return a, nil

	case tea.KeyDelete:
		a.input.Backspace() // simplified
		return a, nil

	case tea.KeyLeft:
		a.input.CursorLeft()
		return a, nil

	case tea.KeyRight:
		a.input.CursorRight()
		return a, nil

	case tea.KeyHome:
		a.input.Home()
		return a, nil

	case tea.KeyEnd:
		a.input.End()
		return a, nil

	case tea.KeySpace:
		a.input.InsertChar(' ')
		return a, nil

	case tea.KeyRunes:
		for _, ch := range msg.Runes {
			a.input.InsertChar(ch)
		}
		return a, nil

	case tea.KeyCtrlU:
		// Clear input line
		a.input.Reset()
		return a, nil

	case tea.KeyCtrlW:
		// Delete word
		a.input.DeleteWord()
		return a, nil
	}

	return a, nil
}
