package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
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

	// Thinking state
	thinkingActive bool
	thinkingStart  time.Time
	thinkingDuration time.Duration // set after thinking ends

	// Completed query stats (shown after streaming ends)
	lastInputTokens  int
	lastOutputTokens int
	lastElapsed      time.Duration
	lastThinking     time.Duration
	showStats        bool

	// Dynamic token estimation (source: TS uses responseLength / 4)
	responseCharCount int

	// Tool execution blink state
	toolBlink     bool
	toolBlinkTick int

	// Content cache — avoids rebuilding rendered messages every frame.
	// TS writes content to terminal; terminal handles scrollback natively.
	// We follow the same pattern: render all messages, let terminal scroll.
	contentDirty bool
	contentCache string

	// Smoothly animated token counters for spinner display
	displayedInputTokens  int
	displayedOutputTokens int
	outputTokenTarget    int
	inputTokenTarget      int // estimate set at submit; replaced by actual on first usage event
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
// No EnterAltScreen — terminal native scrollback handles scrolling,
// matching TS behavior where Ink writes content and the terminal scrolls.
func (a *App) Init() tea.Cmd {
	return nil
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
	case streamChunkMsg, streamToolUseMsg, streamToolDeltaMsg, streamToolOutputMsg, streamToolResultMsg,
		streamCompleteMsg, streamStartMsg, streamMessageMsg, streamUsageMsg,
		streamThinkingStartMsg, streamThinkingEndMsg,
		errMsg, submitMsg, spinnerTickMsg:
		handled, cmd := a.updateRepl(msg)
		if handled {
			return a, cmd
		}
	}

	return a, nil
}

// View renders the entire TUI.
// Renders all messages (no truncation) — terminal native scrollback handles scrolling.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Rebuild content cache only when dirty
	if a.contentDirty || a.contentCache == "" {
		var toolDot string
		if a.repl.IsStreaming() && a.toolBlink {
			toolDot = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
		}
		a.contentCache = renderMessagesFull(a.repl.Messages(), a.width, a.allToolsExpanded, toolDot)
		a.contentDirty = false
	}

	var sb strings.Builder

	// Message content — terminal scrolls natively
	sb.WriteString(a.contentCache)
	sb.WriteString("\n")

	// Progress line: spinner + elapsed + tokens + thinking when streaming
	if a.repl.IsStreaming() && !a.progressStart.IsZero() {
		spinnerFrame := a.spinner.View()
		elapsedStr := formatElapsed(a.progressStart)
		tokensStr := fmt.Sprintf("↑%s ↓%s tokens", formatTokenCount(a.displayedInputTokens), formatTokenCount(a.displayedOutputTokens))
		var thinkingStr string
		if a.thinkingActive {
			thinkingStr = " · thinking"
		} else if a.thinkingDuration > 0 {
			thinkingStr = fmt.Sprintf(" · thought for %.1fs", a.thinkingDuration.Seconds())
		}
		progressLine := spinnerFrame + " (" + elapsedStr + " · " + tokensStr + thinkingStr + ")"
		sb.WriteString(progressLine)
		sb.WriteString("\n")
	} else if a.showStats && !a.repl.IsStreaming() {
		elapsedStr := fmt.Sprintf("%.1fs", a.lastElapsed.Seconds())
		tokensStr := fmt.Sprintf("↑%s ↓%s tokens", formatTokenCount(a.lastInputTokens), formatTokenCount(a.lastOutputTokens))
		summaryLine := styleDim.Render(tokensStr + " · " + elapsedStr)
		sb.WriteString(summaryLine)
		sb.WriteString("\n")
	}

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
		return a.handleCtrlC()

	case tea.KeyCtrlO:
		a.allToolsExpanded = !a.allToolsExpanded
		a.contentDirty = true
		return a, nil

	case tea.KeyCtrlB:
		a.input.CursorLeft()
		return a, nil

	case tea.KeyCtrlF:
		a.input.CursorRight()
		return a, nil

	case tea.KeyCtrlP, tea.KeyUp:
		if a.input.CursorUp() {
			return a, nil
		}
		return a.handleHistoryUp(), nil

	case tea.KeyCtrlN, tea.KeyDown:
		if a.input.CursorDown() {
			return a, nil
		}
		return a.handleHistoryDown(), nil

	case tea.KeyCtrlH:
		a.input.Backspace()
		return a, nil

	case tea.KeyCtrlD:
		a.input.DeleteForward()
		return a, nil

	case tea.KeyCtrlL, tea.KeyCtrlG, tea.KeyEscape:
		return a, nil

	case tea.KeyCtrlLeft:
		a.input.PrevWord()
		return a, nil

	case tea.KeyCtrlRight:
		a.input.NextWord()
		return a, nil

	case tea.KeyCtrlA:
		a.input.Home()
		return a, nil

	case tea.KeyCtrlE:
		a.input.End()
		return a, nil

	case tea.KeyCtrlK:
		a.killRing.Push(string(a.input.value[a.input.cursor:]), "append")
		a.input.value = a.input.value[:a.input.cursor]
		return a, nil

	case tea.KeyCtrlY:
		if yanked := a.killRing.Top(); yanked != "" {
			for _, ch := range yanked {
				a.input.InsertChar(ch)
			}
		}
		return a, nil

	case tea.KeyCtrlU:
		a.killRing.Push(string(a.input.value[:a.input.cursor]), "prepend")
		a.input.value = a.input.value[a.input.cursor:]
		a.input.cursor = 0
		return a, nil

	case tea.KeyCtrlW:
		a.handleKillWord()
		return a, nil

	case tea.KeyEnter:
		text := a.input.Value()
		if strings.TrimSpace(text) == "" {
			return a, nil
		}
		return a, a.handleSubmitRepl(text)

	case tea.KeyRunes:
		return a.handleRunes(msg)

	case tea.KeyBackspace:
		a.resetNavAndAccum()
		a.input.Backspace()
		return a, nil

	case tea.KeyDelete:
		a.resetNavAndAccum()
		a.input.DeleteForward()
		return a, nil

	case tea.KeyHome:
		a.resetNavAndAccum()
		a.input.Home()
		return a, nil

	case tea.KeyEnd:
		a.resetNavAndAccum()
		a.input.End()
		return a, nil

	case tea.KeySpace:
		a.resetNavAndAccum()
		a.input.InsertChar(' ')
		return a, nil

	case tea.KeyLeft:
		a.resetNavAndAccum()
		a.input.CursorLeft()
		return a, nil

	case tea.KeyRight:
		a.resetNavAndAccum()
		a.input.CursorRight()
		return a, nil
	}

	return a, nil
}

// ---------------------------------------------------------------------------
// handleKey helpers
// ---------------------------------------------------------------------------

// resetNavAndAccum resets history navigation and kill ring accumulation.
func (a *App) resetNavAndAccum() {
	a.history.ResetNav()
	a.killRing.ResetAccumulation()
}

// handleCtrlC handles Ctrl+C: cancel stream or double-press quit.
func (a *App) handleCtrlC() (tea.Model, tea.Cmd) {
	if a.repl.IsStreaming() {
		if a.repl.cancelFunc != nil {
			a.repl.cancelFunc()
			a.repl.cancelFunc = nil
		}
		a.repl.FinishStream(nil)
		return a, nil
	}
	if a.doublePress.Press("ctrl-c") {
		return a, tea.Quit
	}
	return a, nil
}

// handleHistoryUp navigates to the previous history entry.
func (a *App) handleHistoryUp() tea.Model {
	text, _ := a.history.Up(a.input.Value())
	a.input.SetValue(text)
	a.input.End()
	return a
}

// handleHistoryDown navigates to the next history entry.
func (a *App) handleHistoryDown() tea.Model {
	text, _ := a.history.Down()
	a.input.SetValue(text)
	a.input.End()
	return a
}

// handleRunes handles rune input: Alt combos, paste, and normal typing.
func (a *App) handleRunes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Alt {
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'b':
				a.input.PrevWord()
				return a, nil
			case 'f':
				a.input.NextWord()
				return a, nil
			case 'd':
				deleted := a.input.DeleteWordForward()
				a.killRing.Push(deleted, "append")
				return a, nil
			}
		}
		return a, nil
	}
	if msg.Paste {
		for _, ch := range msg.Runes {
			a.input.InsertChar(ch)
		}
		return a, nil
	}
	a.resetNavAndAccum()
	for _, ch := range msg.Runes {
		a.input.InsertChar(ch)
	}
	return a, nil
}

// handleKillWord deletes the word before the cursor and pushes it to the kill ring.
func (a *App) handleKillWord() {
	if a.input.cursor == 0 {
		return
	}
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
}
