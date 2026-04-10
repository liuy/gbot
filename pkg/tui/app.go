package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
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

	// Hub — callback-based event routing
	hub        *hub.Hub
	tuiHandler *TUIHandler

	// Feature modules
	history      *History
	killRing    *KillRing
	doublePress *DoublePress

	// Spinner progress state
	progressStart      time.Time
	allToolsExpanded bool
}

// NewApp creates a new App model.
func NewApp(eng *engine.Engine, systemPrompt json.RawMessage, h *hub.Hub) *App {
	// Resolve history file path: ~/.gbot/history.jsonl
	var historyPath string
	if configDir, err := config.ConfigDir(); err == nil {
		historyPath = filepath.Join(configDir, "history.jsonl")
	}

	a := &App{
		input:              NewInput(),
		status:             NewStatusBar(),
		spinner:           NewSpinner(),
		repl:              NewReplState(),
		engine:            eng,
		systemPrompt:      systemPrompt,
		hub:               h,
		history:           NewHistory(historyPath),
		killRing:         NewKillRing(),
		doublePress:      NewDoublePress(),
		allToolsExpanded: false,
	}
	if h != nil {
		a.tuiHandler = NewTUIHandler()
		h.Subscribe(a.tuiHandler)
	}
	return a
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
		streamCompleteMsg, streamStartMsg, streamMessageMsg, errMsg, submitMsg, spinnerTickMsg:
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
	availHeight := a.height - 5 // status bar + progress + input + borders
	if availHeight < 3 {
		availHeight = 3
	}

	rendered := renderMessages(a.repl.Messages(), a.width, availHeight, a.allToolsExpanded)
	sb.WriteString(rendered)

	// Progress line: spinner + elapsed + tokens when streaming
	if a.repl.IsStreaming() && !a.progressStart.IsZero() {
		spinnerFrame := a.spinner.View()
		elapsedStr := formatElapsed(a.progressStart)
		tokensStr := fmt.Sprintf("in:%d out:%d", a.status.inputTokens, a.status.outTokens)
		progressLine := spinnerFrame + " " + elapsedStr + "  " + tokensStr
		sb.WriteString(progressLine)
		sb.WriteString("\n")
	}

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
		// Double-press Ctrl+C to quit (within 800ms window)
		if a.doublePress.Press("ctrl-c") {
			return a, tea.Quit
		}
		// First press: reset and wait
		return a, nil

	case tea.KeyCtrlO:
		a.allToolsExpanded = !a.allToolsExpanded
		return a, nil

	case tea.KeyCtrlA:
		a.input.Home()
		return a, nil

	case tea.KeyCtrlE:
		a.input.End()
		return a, nil

	case tea.KeyCtrlK:
		after := string(a.input.value[a.input.cursor:])
		a.killRing.Push(after, "append")
		a.input.value = a.input.value[:a.input.cursor]
		return a, nil

	case tea.KeyCtrlY:
		yanked := a.killRing.Top()
		if yanked != "" {
			for _, ch := range yanked {
				a.input.InsertChar(ch)
			}
		}
		return a, nil

	case tea.KeyUp:
		text, _ := a.history.Up(a.input.Value())
		a.input.SetValue(text)
		a.input.End()
		return a, nil

	case tea.KeyDown:
		text, _ := a.history.Down()
		a.input.SetValue(text)
		a.input.End()
		return a, nil

	case tea.KeyEnter:
		text := a.input.Value()
		if strings.TrimSpace(text) == "" {
			return a, nil
		}
		return a, a.handleSubmitRepl(text)

	case tea.KeyRunes:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		for _, ch := range msg.Runes {
			a.input.InsertChar(ch)
		}
		return a, nil

	case tea.KeyBackspace:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.Backspace()
		return a, nil

	case tea.KeyDelete:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.Backspace()
		return a, nil

	case tea.KeyHome:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.Home()
		return a, nil

	case tea.KeyEnd:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.End()
		return a, nil

	case tea.KeySpace:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.InsertChar(' ')
		return a, nil

	case tea.KeyLeft:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.CursorLeft()
		return a, nil

	case tea.KeyRight:
		a.history.ResetNav()
		a.killRing.ResetAccumulation()
		a.input.CursorRight()
		return a, nil

	case tea.KeyCtrlU:
		before := string(a.input.value[:a.input.cursor])
		a.killRing.Push(before, "prepend")
		a.input.value = a.input.value[a.input.cursor:]
		a.input.cursor = 0
		return a, nil

	case tea.KeyCtrlW:
		pos := a.input.cursor - 1
		for pos > 0 && a.input.value[pos] == ' ' {
			pos--
		}
		for pos > 0 && a.input.value[pos-1] != ' ' {
			pos--
		}
		word := string(a.input.value[pos:a.input.cursor])
		a.killRing.Push(word, "prepend")
		a.input.value = append(a.input.value[:pos], a.input.value[a.input.cursor:]...)
		a.input.cursor = pos
		return a, nil
	}

	return a, nil
}
