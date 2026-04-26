package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
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

	// Commit-on-complete: messages[:committedCount] are committed to terminal
	// scrollback via tea.Println and never re-rendered by Bubble Tea.
	// Only messages[committedCount:] are managed by View().
	committedCount int

	// Engine
	engine       *engine.Engine
	systemPrompt json.RawMessage

	// Persistence (short-term memory store)
	store            *short.Store
	sessionID        string
	lastPersistedIdx int    // tracks how many engine messages have been persisted
	projectDir       string // working directory for .gbot/meta.json

	// Picker overlay (generic ListPicker for /session, /model, etc.)
	listPicker   *ListPicker
	onPickerDone func(*ListPicker) (tea.Model, tea.Cmd)

	// Permission dialog overlay (修正 5: intercepts all keys including Ctrl+C)
	permissionDialog *PermissionDialog

	// Multi-provider model switching
	providers       map[string]llm.Provider
	cfg             *config.Config
	currentProvider string
	currentTier     config.Tier
	providerConfigs map[string]*config.Provider

	// Hub — callback-based event routing
	hub        *hub.Hub
	tuiHandler *TUIHandler

	// Idle listener stop channel — closed when user submits to abort
	// an idle readEvents goroutine. Prevents goroutine leak.
	idleStop chan struct{}

	// Feature modules
	history     *History
	killRing    *KillRing
	doublePress *DoublePress

	// Spinner progress state
	progressStart    time.Time
	allToolsExpanded bool

	// Thinking state
	thinkingActive   bool
	thinkingStart    time.Time
	thinkingDuration time.Duration // set after thinking ends

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

	// Internal scroll buffer for uncommitted content.
	// When content exceeds terminal height, only a window is rendered.
	scrollOffset int  // first visible line index (0 = top)
	scrollTotal  int  // total lines in rendered content
	userScrolled bool // true when user manually scrolled up; reset on new content

	// Smoothly animated token counters for spinner display
	displayedInputTokens  int
	displayedOutputTokens int
	outputTokenTarget     int
	inputTokenTarget      int // estimate set at submit; replaced by actual on first usage event
	// Cache token tracking for spinner display
	cacheReadTokens     int
	cacheCreationTokens int
}

// NewApp creates a new App model.
func NewApp(eng *engine.Engine, systemPrompt json.RawMessage, h *hub.Hub) *App {
	// Resolve history file path: ~/.gbot/history.jsonl
	var historyPath string
	if configDir, err := config.ConfigDir(); err == nil {
		historyPath = filepath.Join(configDir, "history.jsonl")
	}

	a := &App{
		input:            NewInput(),
		status:           NewStatusBar(),
		spinner:          NewSpinner(),
		repl:             NewReplState(),
		engine:           eng,
		systemPrompt:     systemPrompt,
		hub:              h,
		history:          NewHistory(historyPath),
		killRing:         NewKillRing(),
		doublePress:      NewDoublePress(),
		allToolsExpanded: false,
		idleStop:         make(chan struct{}),
	}
	if h != nil {
		a.tuiHandler = NewTUIHandler()
		h.Subscribe(a.tuiHandler)
	}
	if eng != nil {
		a.status.SetToolCount(len(eng.AllTools()))
		a.status.SetContext(0, eng.ContextWindow())
		a.status.SetModel(eng.Model())
	}
	return a
}

// SetProviders configures multi-provider model switching.
// Called from main.go after createAllProviders().
func (a *App) SetProviders(providers map[string]llm.Provider, cfg *config.Config) {
	a.providers = providers
	a.cfg = cfg
	a.providerConfigs = make(map[string]*config.Provider, len(cfg.Providers))
	for i := range cfg.Providers {
		a.providerConfigs[cfg.Providers[i].Name] = &cfg.Providers[i]
	}
	providerName, tier, err := cfg.ParseModel()
	if err != nil {
		slog.Warn("config: invalid model, falling back to pro", "model", cfg.Model, "error", err)
		tier = config.TierPro
	}
	if providerName != "" {
		a.currentProvider = providerName
	} else if len(cfg.Providers) > 0 {
		a.currentProvider = cfg.Providers[0].Name
	}
	a.currentTier = tier
}

// SetInitialContext sets the initial context usage estimate on the StatusBar.
// Called from main.go after system prompt and tools are loaded.
// The estimate is a heuristic (len/4) and will be corrected after the first API response.
func (a *App) SetInitialContext(usedTokens, contextWindow int) {
	a.status.SetContext(usedTokens, contextWindow)
}

// persistModelSelection writes the current provider/tier back to settings.json.
func (a *App) persistModelSelection() {
	if a.cfg == nil {
		return
	}
	a.cfg.Model = a.currentProvider + "/" + string(a.currentTier)
	if err := a.cfg.Save(); err != nil {
		slog.Warn("model: failed to persist selection", "error", err)
	}
}

// SetStore configures persistence on the App after creation.
// Called from main.go after auto-resume logic determines the session state.
func (a *App) SetStore(store *short.Store, sessionID, projectDir string, lastPersistedIdx int) {
	a.store = store
	a.sessionID = sessionID
	a.projectDir = projectDir
	a.lastPersistedIdx = lastPersistedIdx
}

// resetDisplayState zeros all App-level display fields for a clean session.
// Called by createNewSession so both /clear and /session -n benefit.
func (a *App) resetDisplayState() {
	a.scrollOffset = 0
	a.scrollTotal = 0
	a.userScrolled = false
	a.contentCache = ""
	a.contentDirty = false
	a.allToolsExpanded = false
	a.thinkingActive = false
	a.thinkingStart = time.Time{}
	a.thinkingDuration = 0
	a.progressStart = time.Time{}
	a.responseCharCount = 0
	a.displayedInputTokens = 0
	a.displayedOutputTokens = 0
	a.outputTokenTarget = 0
	a.inputTokenTarget = 0
	a.cacheReadTokens = 0
	a.cacheCreationTokens = 0
	a.toolBlink = false
	a.toolBlinkTick = 0
	a.status.SetUsage(types.Usage{})
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
	// Route to picker overlay when active
	if a.listPicker != nil {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			a.listPicker.width = msg.Width
			a.listPicker.height = msg.Height
			return a, nil
		case tea.KeyMsg:
			model, cmd := a.listPicker.Update(msg)
			if p, ok := model.(*ListPicker); ok {
				a.listPicker = p
			}
			if a.listPicker.Done() {
				handler := a.onPickerDone
				a.onPickerDone = nil
				if handler == nil {
					a.listPicker = nil
					return a, nil
				}
				return handler(a.listPicker)
			}
			return a, cmd
		}
		return a, nil
	}

	// 修正 5: Permission dialog intercepts ALL keys (including Ctrl+C) before handleKey.
	if a.permissionDialog != nil {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if a.permissionDialog.HandleKey(msg) && a.permissionDialog.Done() {
				a.permissionDialog = nil
			}
			return a, a.readEvents()
		case tea.WindowSizeMsg:
			a.width = msg.Width
			a.height = msg.Height
			return a, nil
		default:
			return a, nil
		}
	}

	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		a.input.SetWidth(a.width - 4)
		a.status.SetWidth(a.width)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(m)

	case tea.MouseMsg:
		return a, a.handleMouse(m)

	// All REPL messages are handled by repl.go
	case textStartMsg, textDeltaMsg, textEndMsg, toolRunMsg, toolStartMsg, toolParamDeltaMsg, toolOutputDeltaMsg, toolEndMsg,
		queryEndMsg, turnStartMsg, streamMessageMsg, usageMsg,
		thinkingStartMsg, thinkingDeltaMsg, thinkingEndMsg,
		agentToolMsg, agentUsageMsg,
		notificationPendingMsg, idleAbortedMsg,
		infoMsg, errMsg, submitMsg, spinnerTickMsg,
		permissionAskMsg:
		handled, cmd := a.updateRepl(msg)
		if handled {
			return a, cmd
		}
	default:
		slog.Warn("tui:update:unhandled_msg", "msgType", fmt.Sprintf("%T", msg))
	}

	return a, nil
}

// View renders the active (uncommitted) content + progress + input.
// Committed messages are in terminal scrollback via tea.Println — never re-rendered.
// When uncommitted content exceeds terminal height, a scroll window is applied so
// only the visible portion is rendered, preventing Bubble Tea's inline renderer from
// corrupting terminal scrollback.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Picker overlay
	if a.listPicker != nil {
		return a.listPicker.View()
	}

	// Permission dialog overlay
	if a.permissionDialog != nil {
		return a.permissionDialog.View()
	}

	uncommitted := a.repl.messages[a.committedCount:]

	var contentStr string
	if len(uncommitted) > 0 {
		// Rebuild content cache only when dirty
		if a.contentDirty || a.contentCache == "" {
			var toolDot string
			if a.repl.IsStreaming() && a.toolBlink {
				toolDot = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
			}
			// maxOutputLines=0 means unlimited — terminal scroll handles overflow
			a.contentCache = renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, toolDot, false, 0)
			a.contentDirty = false
		}
		contentStr = a.contentCache
	} else {
		a.contentCache = ""
		a.contentDirty = false
		a.scrollOffset = 0
		a.scrollTotal = 0
		a.userScrolled = false
		if a.committedCount == 0 {
			// Initial state — show welcome
			welcomeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true)
			contentStr = welcomeStyle.Render("Welcome to gbot. Type a message to get started.")
		}
	}

	// Apply scroll window: limit visible content to what fits in terminal.
	// Reserve 5 lines for: progress (1) + input (1) + separator (1) + status bar (1) + margin (1).
	maxContentLines := max(a.height-5, 1)

	var visibleContent string
	var showScrollIndicator bool

	if contentStr != "" {
		lines := strings.Split(contentStr, "\n")
		a.scrollTotal = len(lines)

		if len(lines) <= maxContentLines {
			// Content fits entirely — no scrolling needed
			a.scrollOffset = 0
			visibleContent = contentStr
		} else {
			showScrollIndicator = true
			// Reserve 1 line for scroll indicator
			viewLines := max(maxContentLines-1, 1)

			// Auto-scroll to bottom unless user explicitly scrolled up
			if !a.userScrolled {
				a.scrollOffset = len(lines) - viewLines
			}

			// Clamp scrollOffset to valid range
			maxOff := max(len(lines)-viewLines, 0)
			if a.scrollOffset > maxOff {
				a.scrollOffset = maxOff
			}
			if a.scrollOffset < 0 {
				a.scrollOffset = 0
			}

			end := min(a.scrollOffset+viewLines, len(lines))
			visibleContent = strings.Join(lines[a.scrollOffset:end], "\n")
		}
	} else {
		a.scrollTotal = 0
		a.scrollOffset = 0
	}

	var sb strings.Builder

	// Scroll indicator when content overflows viewport
	if showScrollIndicator {
		viewLines := max(maxContentLines-1, 1)
		totalPages := max((a.scrollTotal+viewLines-1)/viewLines, 1)
		atTop := a.scrollOffset == 0
		atBottom := a.scrollOffset+viewLines >= a.scrollTotal
		// Page number: which page the viewport top falls on.
		// At bottom, force last page to avoid off-by-one from integer division.
		currentPage := a.scrollOffset/viewLines + 1
		if atBottom {
			currentPage = totalPages
		}
		if currentPage > totalPages {
			currentPage = totalPages
		}
		// Directional arrow: ↑=content above, ↓=content below, ↕=both
		var arrow string
		switch {
		case atTop && !atBottom:
			arrow = "↓"
		case atBottom && !atTop:
			arrow = "↑"
		default:
			arrow = "↕"
		}
		sb.WriteString(styleDim.Render(fmt.Sprintf("%s %d/%d · PgUp/PgDown/Mouse", arrow, currentPage, totalPages)))
		sb.WriteString("\n")
	}

	// Active (uncommitted) content (scroll-windowed)
	if visibleContent != "" {
		sb.WriteString(visibleContent)
		sb.WriteString("\n")
	}

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
		var toolsStr string
		if tc := a.repl.toolCount; tc > 0 {
			if tc == 1 {
				toolsStr = " · 1 tool"
			} else {
				toolsStr = fmt.Sprintf(" · %d tools", tc)
			}
		}
		progressLine := spinnerFrame + " (" + elapsedStr + " · " + tokensStr + toolsStr + thinkingStr + ")"
		sb.WriteString(progressLine)
		sb.WriteString("\n")
	}

	// Input
	sb.WriteString(a.input.View())

	// Horizontal line separator + Status bar below input
	sb.WriteString("\n")
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	sb.WriteString(sepStyle.Render(strings.Repeat("─", max(a.width, 1))))
	sb.WriteString("\n")
	sb.WriteString(a.status.View())

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

	case tea.KeyPgUp:
		vl := a.calcViewLines()
		a.scrollUp(max(1, vl/2))
		return a, nil

	case tea.KeyPgDown:
		vl := a.calcViewLines()
		a.scrollDown(max(1, vl/2))
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
		// Commit uncommitted messages to scrollback
		var cmd tea.Cmd
		uncommitted := a.repl.messages[a.committedCount:]
		if len(uncommitted) > 0 {
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, 0)
			a.committedCount = len(a.repl.messages)
			cmd = tea.Println(rendered)
		}
		a.contentCache = ""
		a.contentDirty = false
		return a, cmd
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

// ---------------------------------------------------------------------------
// Scroll handling
// ---------------------------------------------------------------------------

// calcViewLines returns the number of visible content lines when content overflows.
// Matches View()'s viewLines calculation: maxContentLines-1 (reserve 1 for indicator).
func (a *App) calcViewLines() int {
	maxContentLines := max(a.height-5, 1)
	if a.scrollTotal > maxContentLines {
		return max(1, maxContentLines-1) // reserve 1 for scroll indicator
	}
	return maxContentLines
}

// scrollUp moves the scroll viewport up by n lines.
func (a *App) scrollUp(n int) {
	if a.scrollTotal == 0 {
		return
	}
	a.scrollOffset -= n
	if a.scrollOffset < 0 {
		a.scrollOffset = 0
	}
	a.userScrolled = true
}

// scrollDown moves the scroll viewport down by n lines.
func (a *App) scrollDown(n int) {
	if a.scrollTotal == 0 {
		return
	}
	viewLines := a.calcViewLines()
	maxOff := max(a.scrollTotal-viewLines, 0)
	a.scrollOffset += n
	if a.scrollOffset > maxOff {
		a.scrollOffset = maxOff
	}
	// If scrolled to bottom, resume auto-scroll
	a.userScrolled = a.scrollOffset < maxOff
}

// handleMouse handles mouse events for scroll wheel support.
func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		a.scrollUp(3)
	case tea.MouseButtonWheelDown:
		a.scrollDown(3)
	}
	return nil
}
