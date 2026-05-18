package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/liuy/gbot/pkg/filehistory"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// App — bubbletea root Model
// ---------------------------------------------------------------------------

const (
	modalPeekRows  = 2 // fixed peek rows when content is abundant
	minModalHeight = 5 // minimum modal height to keep dialog usable
)

// TaskSummary is a lightweight task representation for TUI rendering.
// Decoupled from the tasks package to avoid import cycles.
type TaskSummary struct {
	ID        string
	Subject   string
	Status    string // "pending", "in_progress", "completed"
	Owner     string
	BlockedBy []string // uncompleted blocker subjects
}

// taskListFn reads tasks for display. Set via SetTaskListFn from main.go.
type taskListFn func() []TaskSummary

// pendingQueueItem tracks a user message queued during streaming.
// Matched by UUID when the engine drains the attachment and emits EventAttachment.
type pendingQueueItem struct {
	ID   string // UUID matching QueuedItem.UUID
	Text string // original user input text
}

type stashedPrompt struct {
	text        string
	cursor      int
	pasteStore  map[int]string
	nextPasteID int
}

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
	sessionID   string
	projectDir  string // working directory for .gbot/meta.json
	fileHistory *filehistory.Tracker

	// Active dialog overlay (unified for list picking and permission asking)
	activeDialog *Dialog
	onDialogDone func(*Dialog) (tea.Model, tea.Cmd)

	// Active input dialog overlay (interactive PTY input with countdown)
	activeInput *InputDialog

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
	completions *Completions

	// Paste reference state
	pasteStore  map[int]string
	nextPasteID int

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

	// Retry state — source: TS SystemAPIErrorMessage.tsx
	retryActive    bool
	retryAttempt   int
	retryMax       int
	retryRemaining time.Duration
	retryStart     time.Time
	retryErrorType string

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

	// Task list panel (auto-shows when tasks exist)
	taskListFn    taskListFn  // set from main.go to read tasks for display
	autoCleanupFn func() bool // checked every render; cleans tasks and jobs, returns true if reset happened
	taskListCache string      // rendered task list, rebuilt when dirty
	taskListDirty bool
	killAllFn     func() // set from main.go to kill all background tasks

	pendingQueue []pendingQueueItem // user messages queued during streaming

	stashed *stashedPrompt // Ctrl+S stashed input (survives /clear and Ctrl+C)

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
		completions:      NewCompletions(),
		pasteStore:       make(map[int]string),
		nextPasteID:      1,
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
	if ct := a.engine.GetContextTokens(); ct > 0 {
		usedTokens = ct
	}
	a.status.SetContext(usedTokens, contextWindow)
}

// SetTaskListFn sets the function used to read tasks for the task list panel.
// Called from main.go after task tools are registered.
func (a *App) SetTaskListFn(fn taskListFn) {
	a.taskListFn = fn
	a.taskListDirty = true
}

// SetAutoCleanupFn sets a function checked every render cycle.
// Cleans tasks and jobs; returns true if task list was reset (forces cache rebuild).
func (a *App) SetAutoCleanupFn(fn func() bool) {
	a.autoCleanupFn = fn
}

// SetKillAllFn sets the callback to kill all background tasks on double-press Escape.
func (a *App) SetKillAllFn(fn func()) {
	a.killAllFn = fn
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
func (a *App) SetStore(store *short.Store, sessionID, projectDir string) {
	a.sessionID = sessionID
	a.projectDir = projectDir

	// Propagate store to engine for persistence delegation
	a.engine.SetStore(store, projectDir)
	if sessionID != "" {
		a.engine.SetSessionID(sessionID)
	}

	// Sync repl.messages from engine on resume.
	// Without this, the TUI shows an empty conversation after restart.
	if len(a.engine.Messages()) > 0 {
		a.repl.messages = engineMessagesToViews(a.engine.Messages())
		a.committedCount = len(a.repl.messages)
	}

	// Cleanup old backup session directories (older than 30 days).
	// Source: TS cleanup.ts:305-348 — cleanupOldFileHistoryBackups.
	{
		fhDir := filepath.Join(filepath.Dir(store.DBPath()), "..", "file-history")
		if cleaned, err := filehistory.CleanupOldBackups(fhDir, filehistory.DefaultCleanupAge); err != nil {
			slog.Warn("tui:file_history:cleanup_failed", "err", err)
		} else if cleaned > 0 {
			slog.Info("tui:file_history:cleaned", "sessions", cleaned)
		}

		// Background session cleanup: delete sessions older than 30 days.
		// Goroutine is cheap; no activity check needed (unlike TS single-thread).
		go func() {
			time.Sleep(5 * time.Second)
			for {
				if cleaned, err := store.CleanupOldSessions(30 * 24 * time.Hour); err != nil {
					slog.Warn("cleanup:old_sessions_failed", "err", err)
				} else if cleaned > 0 {
					slog.Info("cleanup:old_sessions", "count", cleaned)
				}
				time.Sleep(24 * time.Hour)
			}
		}()
	}

	// Create file history tracker for rewind/restore.
	// Source: TS fileHistory.ts — per-session backup directory.
	if sessionID != "" {
		trackerDir := filepath.Join(filepath.Dir(store.DBPath()), "..", "file-history", sessionID)
		tracker := filehistory.NewTracker(trackerDir)
		// Load persisted state (crash recovery / session resume).
		if state, err := store.LoadFileHistoryState(sessionID); err == nil && state != nil {
			tracker.LoadState(*state)
			slog.Info("tui:file_history:loaded", "snapshots", len(state.Snapshots), "dir", trackerDir)
		}
		a.fileHistory = tracker
		a.engine.SetFileHistory(tracker)
		// Wire persistence: save state after each MakeSnapshot.
		a.engine.SetFileHistoryWriter(func(state filehistory.FileHistoryState) {
			if err := store.SaveFileHistoryState(sessionID, state); err != nil {
				slog.Warn("tui:file_history:persist_failed", "err", err)
			}
		})
		slog.Info("tui:file_history", "dir", trackerDir)
	}
	// Wire record writer: persist ContentReplacementRecords to transcript.
	a.engine.SetRecordWriter(func(records []toolresult.ContentReplacementRecord) {
		if err := store.SaveContentReplacementRecords(sessionID, records); err != nil {
			slog.Warn("failed to save content replacement records", "error", err)
		}
	})
}

// engineMessagesToViews converts engine messages to TUI MessageViews for display.
// Used on session resume to populate repl.messages from persisted state.
func engineMessagesToViews(msgs []types.Message) []MessageView {
	views := make([]MessageView, 0, len(msgs))
	for _, msg := range msgs {
		mv := MessageView{}
		switch msg.Role {
		case types.RoleUser:
			mv.Role = "user"
		case types.RoleAssistant:
			mv.Role = "assistant"
		default:
			continue
		}

		for _, block := range msg.Content {
			switch block.Type {
			case types.ContentTypeText:
				if strings.TrimSpace(block.Text) != "" {
					mv.Blocks = append(mv.Blocks, ContentBlock{Type: BlockText, Text: block.Text})
				}
			case types.ContentTypeToolUse:
				mv.Blocks = append(mv.Blocks, ContentBlock{
					Type: BlockTool,
					ToolCall: ToolCallView{
						ID:    block.ID,
						Name:  block.Name,
						Done:  true,
						Input: string(block.Input),
					},
				})
			case types.ContentTypeThinking:
				mv.Blocks = append(mv.Blocks, ContentBlock{
					Type:     BlockThinking,
					Thinking: ThinkingView{Text: block.Text},
				})
			}
		}

		// Skip messages with no renderable blocks
		if len(mv.Blocks) > 0 {
			views = append(views, mv)
		}
	}
	return views
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
	a.pasteStore = make(map[int]string)
	a.nextPasteID = 1
	a.cacheReadTokens = 0
	a.cacheCreationTokens = 0
	a.toolBlink = false
	a.toolBlinkTick = 0
	a.retryActive = false
	a.retryAttempt = 0
	a.retryMax = 0
	a.retryRemaining = 0
	a.retryStart = time.Time{}
	a.retryErrorType = ""
	a.pendingQueue = nil
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
// isInternalTeaMsg reports whether msg is a Bubble Tea framework-internal
// message (e.g. printLineMessage from tea.Println). These are handled by the
// renderer and don't need application-level processing.
func isInternalTeaMsg(msg tea.Msg) bool {
	t := reflect.TypeOf(msg)
	return t != nil && t.PkgPath() == "github.com/charmbracelet/bubbletea"
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Route to active InputDialog overlay when active.
	// InputDialog intercepts ALL keys (including Ctrl+C) to prevent unwanted actions.
	if a.activeInput != nil {
		// New inputAskMsg aborts existing dialog and falls through to updateRepl
		if iam, ok := msg.(inputAskMsg); ok && iam.event != nil {
			sendDecision(a.activeInput.result, types.AskResponse{Aborted: true})
			a.activeInput = nil
		} else {
			model, cmd := a.activeInput.Update(msg)
			if d, ok := model.(*InputDialog); ok {
				a.activeInput = d
			}
			if a.activeInput.done {
				a.activeInput = nil
				return a, a.readEvents()
			}
			return a, cmd
		}
	}

	// Route to active dialog overlay when active.
	// Dialog intercepts ALL keys (including Ctrl+C) to prevent unwanted actions.
	if a.activeDialog != nil {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			a.width = msg.Width
			a.height = msg.Height
			a.activeDialog.width = msg.Width
			// height recalculated dynamically in renderModalView()
			return a, nil
		case tea.KeyMsg:
			a.activeDialog.HandleKey(msg)
			if a.activeDialog.Done() {
				handler := a.onDialogDone
				dialog := a.activeDialog
				a.onDialogDone = nil
				a.activeDialog = nil
				if handler == nil {
					return a, a.readEvents()
				}
				return handler(dialog)
			}
			return a, nil
		default:
			return a, nil
		}
	}

	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		a.input.SetWidth(a.width - 4 - renderedPromptWidth)
		a.status.SetWidth(a.width)
		a.contentDirty = true
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(m)

	case tea.MouseMsg:
		return a, a.handleMouse(m)

	// All REPL messages are handled by repl.go
	case textStartMsg, textDeltaMsg, textEndMsg, toolRunMsg, toolStartMsg, toolParamDeltaMsg, toolOutputDeltaMsg, toolEndMsg,
		queryEndMsg, turnStartMsg, streamMessageMsg, usageMsg,
		thinkingStartMsg, thinkingDeltaMsg, thinkingEndMsg,
		attachmentMsg, idleAbortedMsg,
		infoMsg, errMsg, submitMsg, spinnerTickMsg,
		permissionAskMsg, inputAskMsg, retryAttemptMsg:
		handled, cmd := a.updateRepl(msg)
		if handled {
			return a, cmd
		}
	default:
		// Bubble Tea internal messages (e.g. printLineMessage from
		// tea.Println) are handled by the renderer; suppress WARN for them.
		if !isInternalTeaMsg(msg) {
			slog.Warn("tui:update:unhandled_msg", "msgType", fmt.Sprintf("%T", msg))
		}
	}

	return a, nil
}

// View renders the active (uncommitted) content + progress + input.
// renderQueueBox renders dim ❯-prefixed queued messages between progress and input.
func (a *App) renderQueueBox() string {
	if len(a.pendingQueue) == 0 {
		return ""
	}
	maxWidth := max(a.width-renderedPromptWidth, 10)
	var sb strings.Builder
	prefix := styleDim.Render("❯ ")
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	for _, item := range a.pendingQueue {
		wrapped := wordWrap(item.Text, maxWidth)
		lines := strings.Split(wrapped, "\n")
		for li, line := range lines {
			if li == 0 {
				sb.WriteString(prefix + line)
			} else {
				sb.WriteString("\n" + indent + line)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// Committed messages are in terminal scrollback via tea.Println — never re-rendered.
// When uncommitted content exceeds terminal height, a scroll window is applied so
// only the visible portion is rendered, preventing Bubble Tea's inline renderer from
// corrupting terminal scrollback.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	// Dialog overlay (unified for list picking and permission asking)
	if a.activeDialog != nil {
		return a.renderModalView()
	}

	// Input dialog overlay (interactive PTY input with countdown)
	if a.activeInput != nil {
		return a.renderInputOverlay()
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
			a.contentCache = renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, toolDot, a.repl.IsStreaming(), false, 0)
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

	// Build task list panel if visible (cache rebuild only when dirty).
	var taskPanel string
	var taskPanelLines int
	if a.taskListFn != nil {
		// Auto-cleanup: checked every render cycle, bypasses cache.
		if a.autoCleanupFn != nil && a.autoCleanupFn() {
			a.taskListDirty = true
		}
		if a.taskListDirty {
			a.taskListCache = a.renderTaskList()
			a.taskListDirty = false
		}
		taskPanel = a.taskListCache
		if taskPanel != "" {
			taskPanelLines = strings.Count(taskPanel, "\n") + 1
			taskPanel = taskPanel + "\n"
			maxContentLines = max(maxContentLines-taskPanelLines, 1)
		}
	}

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

	// Task list panel (auto-shows when tasks exist)
	if taskPanel != "" {
		sb.WriteString(taskPanel)
	}

	// Progress line: spinner + elapsed + tokens + thinking when streaming
	if a.repl.IsStreaming() && !a.progressStart.IsZero() {
		// Retry display: show user-friendly error + countdown for attempts >= 4
		// Source: TS SystemAPIErrorMessage.tsx — hidden for attempts < 4
		if a.retryActive && a.retryAttempt >= 4 && a.responseCharCount == 0 && !a.thinkingActive {
			secs := max(int((a.retryRemaining-time.Since(a.retryStart)).Seconds())+1, 0)
			errLine := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(formatRetryError(a.retryErrorType))
			countdownLine := lipgloss.NewStyle().Faint(true).Render(
				fmt.Sprintf("Retrying in %ds… (attempt %d/%d)", secs, a.retryAttempt, a.retryMax))
			sb.WriteString(errLine + "\n" + countdownLine + "\n")
		} else {
			spinnerFrame := a.spinner.View()
			elapsedStr := formatElapsed(a.progressStart)
			tokensStr := fmt.Sprintf("↑%s ↓%s tokens", types.FormatTokenCount(a.displayedInputTokens), types.FormatTokenCount(a.displayedOutputTokens))
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
	}

	// Queue box: dim preview of messages queued during streaming
	if queueStr := a.renderQueueBox(); queueStr != "" {
		sb.WriteString(queueStr)
	}

	// Stash notice: dim indicator when input is stashed
	if stashStr := a.renderStashNotice(); stashStr != "" {
		sb.WriteString(stashStr)
	}

	// Input: View() returns pure text, prepend prompt/indent here.
	inputView := a.input.View()
	inputLines := strings.Split(inputView, "\n")
	indent := strings.Repeat(" ", renderedPromptWidth)
	for li, line := range inputLines {
		if li == 0 {
			sb.WriteString(renderedPrompt + line)
		} else {
			sb.WriteString("\n" + indent + line)
		}
	}

	// Completion dropdown (below input, above separator — TS: PromptInputFooter)
	if a.completions.Visible() {
		maxRows := max(
			// reserve: separator + status + input + at least 1 content line
			a.height-4, 1)
		sb.WriteString("\n")
		sb.WriteString(a.completions.Render(a.width, maxRows))
	}

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
		if a.completions.Visible() {
			a.completions.SelectPrev()
			return a, nil
		}
		if a.input.CursorUp() {
			return a, nil
		}
		return a.handleHistoryUp(), nil

	case tea.KeyCtrlN, tea.KeyDown:
		if a.completions.Visible() {
			a.completions.SelectNext()
			return a, nil
		}
		if a.input.CursorDown() {
			return a, nil
		}
		return a.handleHistoryDown(), nil

	case tea.KeyCtrlH:
		a.input.BackspaceToken()
		return a, nil

	case tea.KeyCtrlD:
		a.input.DeleteForward()
		return a, nil

	case tea.KeyEscape:
		if a.completions.Visible() {
			a.completions.Dismiss()
			return a, nil
		}
		// Double-press check FIRST — works regardless of streaming state.
		// If first press was while streaming and queryEndMsg arrived between
		// the two presses, IsStreaming() would be false but doublePress
		// still has the pending state from the first press.
		if a.doublePress.Press("escape") {
			slog.Info("tui:double_escape_kill_all")
			if a.killAllFn != nil {
				a.killAllFn()
			}
			return a, nil
		}
		// Source: TS onCancel — Escape during streaming cancels the query.
		// Do NOT call FinishStream — let queryEndMsg handle it naturally.
		if a.repl.IsStreaming() {
			slog.Info("tui:escape_cancel", "hasCancelFunc", a.repl.cancelFunc != nil)
			if a.repl.cancelFunc != nil {
				a.repl.cancelFunc()
				a.repl.cancelFunc = nil
			}
			return a, nil
		}
		return a, nil

	case tea.KeyCtrlL, tea.KeyCtrlG:
		if a.completions.Visible() {
			a.completions.Dismiss()
			return a, nil
		}
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

	case tea.KeyCtrlS:
		return a.handleStash()

	case tea.KeyTab:
		if a.completions.Visible() && a.input.cursor == len(a.input.value) {
			fillText, _ := a.completions.Accept()
			a.input.SetValue(fillText)
			a.completions.Dismiss()
			return a, nil
		}
		return a, nil

	case tea.KeyEnter:
		// Alt+Enter (also VSCode Shift+Enter via \x1b\r): insert newline.
		if msg.Alt {
			a.input.InsertNewline()
			return a, nil
		}
		// Backslash+Enter: remove trailing \ and insert newline.
		if a.input.cursor > 0 && a.input.value[a.input.cursor-1] == '\\' {
			a.input.Backspace() // remove the backslash
			a.input.InsertNewline()
			return a, nil
		}
		text := a.input.Value()
		if strings.TrimSpace(text) == "" {
			return a, nil
		}
		text = a.expandPasteRefs(text)
		if a.completions.Visible() && a.input.cursor == len(a.input.value) {
			fillText, shouldExec := a.completions.Accept()
			a.completions.Dismiss()
			if shouldExec {
				return a, a.handleSubmitRepl(a.expandPasteRefs(fillText))
			}
			a.input.SetValue(fillText)
			return a, nil
		}
		return a, a.handleSubmitRepl(text)

	case tea.KeyRunes:
		return a.handleRunes(msg)

	case tea.KeyBackspace:
		a.resetNavAndAccum()
		a.input.BackspaceToken()
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value))
		return a, nil

	case tea.KeyDelete:
		a.resetNavAndAccum()
		a.input.DeleteForward()
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value))
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
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value))
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
		slog.Info("tui:ctrlc_cancel", "hasCancelFunc", a.repl.cancelFunc != nil)
		if a.repl.cancelFunc != nil {
			a.repl.cancelFunc()
			a.repl.cancelFunc = nil
		}
		a.repl.FinishStream(nil)
		// Commit uncommitted messages to scrollback
		var cmd tea.Cmd
		uncommitted := a.repl.messages[a.committedCount:]
		if len(uncommitted) > 0 {
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, false, 0)
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

// handleHistoryUp navigates to the previous history entry or moves within draft.
func (a *App) handleHistoryUp() tea.Model {
	res := a.history.Up(a.input.Value())
	if res.Cursor == CursorNone {
		return a
	}
	a.input.SetValue(res.Text)
	if res.Cursor == CursorHome {
		a.input.Home()
	} else {
		a.input.End()
	}
	return a
}

// handleHistoryDown navigates to the next history entry or enters draft.
func (a *App) handleHistoryDown() tea.Model {
	res := a.history.Down()
	if res.Cursor == CursorNone {
		return a
	}
	a.input.SetValue(res.Text)
	if res.Cursor == CursorHome {
		a.input.Home()
	} else {
		a.input.End()
	}
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
		slog.Info("tui:paste_input", "runes", len(msg.Runes))
		a.resetNavAndAccum()
		runes := make([]rune, len(msg.Runes))
		for i, ch := range msg.Runes {
			if ch == '\r' {
				ch = '\n'
			}
			runes[i] = ch
		}
		newlineCount := 0
		for _, ch := range runes {
			if ch == '\n' {
				newlineCount++
			}
		}
		if len(runes) > 800 || newlineCount > 2 {
			id := a.nextPasteID
			a.nextPasteID++
			a.pasteStore[id] = string(runes)
			a.input.InsertString(formatPasteRef(id, newlineCount))
			slog.Info("tui:paste_ref", "id", id, "runes", len(runes), "newlines", newlineCount)
		} else {
			for _, ch := range runes {
				a.input.InsertChar(ch)
			}
		}
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value))
		return a, nil
	}
	a.resetNavAndAccum()
	for _, ch := range msg.Runes {
		if ch == '\r' {
			ch = '\n'
		}
		a.input.InsertChar(ch)
	}
	a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value))
	return a, nil
}

// formatPasteRef returns the display string for a paste reference.
// Matches TS: formatPastedTextRef(id, numLines).
func formatPasteRef(id, numLines int) string {
	if numLines == 0 {
		return fmt.Sprintf("[Pasted text #%d]", id)
	}
	return fmt.Sprintf("[Pasted text #%d +%d lines]", id, numLines)
}

// pasteRefExpandRe matches [Pasted text #N] or [Pasted text #N +L lines]
// for expanding references back to full content on submit.
var pasteRefExpandRe = regexp.MustCompile(`\[Pasted text #(\d+)(?: \+\d+ lines)?\]`)

// expandPasteRefs replaces all paste references in text with their stored content.
// References without a matching store entry are left as-is.
func (a *App) expandPasteRefs(text string) string {
	return pasteRefExpandRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := pasteRefExpandRe.FindStringSubmatch(match)
		id, _ := strconv.Atoi(sub[1])
		if content, ok := a.pasteStore[id]; ok {
			return content
		}
		return match
	})
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

// handleStash toggles Ctrl+S stash: push (save + clear) or pop (restore).
// Source: TS PromptInput.tsx:1356-1381 — chat:stash handler.
func (a *App) handleStash() (tea.Model, tea.Cmd) {
	input := a.input.Value()
	if strings.TrimSpace(input) == "" && a.stashed != nil {
		// Pop: restore stashed input
		a.input.SetValue(a.stashed.text)
		a.input.SetCursor(a.stashed.cursor)
		a.pasteStore = a.stashed.pasteStore
		a.nextPasteID = a.stashed.nextPasteID
		a.stashed = nil
	} else if strings.TrimSpace(input) != "" {
		// Push: save current input and clear
		pasteCopy := make(map[int]string, len(a.pasteStore))
		maps.Copy(pasteCopy, a.pasteStore)
		a.stashed = &stashedPrompt{
			text:        input,
			cursor:      a.input.Cursor(),
			pasteStore:  pasteCopy,
			nextPasteID: a.nextPasteID,
		}
		a.input.Reset()
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
	}
	return a, nil
}

// restoreStash restores stashed input to the input field.
// Called at each submit point after input.Reset()+pasteStore clear.
func (a *App) restoreStash() {
	if a.stashed == nil {
		return
	}
	a.input.SetValue(a.stashed.text)
	a.input.SetCursor(a.stashed.cursor)
	a.pasteStore = a.stashed.pasteStore
	a.nextPasteID = a.stashed.nextPasteID
	a.stashed = nil
}

// renderStashNotice renders the stash indicator above the input.
// Source: TS PromptInputStashNotice.tsx — dim ‣ Stashed notice.
func (a *App) renderStashNotice() string {
	if a.stashed == nil {
		return ""
	}
	return styleDim.Render("  ‣ Stashed (auto-restores after submit)") + "\n"
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

// ---------------------------------------------------------------------------
// Modal rendering — bottom-anchored dialog with transcript peek
// ---------------------------------------------------------------------------

// renderModalView renders the modal overlay: peek content + dialog.
func (a *App) renderModalView() string {
	peek := a.computePeek()
	modalHeight := max(a.height-max(a.countPeekLines(peek), 0), minModalHeight)
	a.activeDialog.height = modalHeight
	a.activeDialog.width = a.width
	dialogView := a.activeDialog.View()
	if peek == "" {
		return dialogView
	}
	return peek + "\n" + dialogView
}

// renderInputOverlay renders the InputDialog overlay with transcript peek.
func (a *App) renderInputOverlay() string {
	peek := a.computePeek()
	inputView := a.activeInput.View()
	if peek == "" {
		return inputView
	}
	return peek + "\n" + inputView
}

// computePeek returns the adaptive peek content for overlay rendering.
// Sparse content expands peek; abundant content caps at modalPeekRows.
func (a *App) computePeek() string {
	content := a.getRenderedContent()
	if content == "" {
		return ""
	}
	contentLines := strings.Count(content, "\n") + 1
	maxPeek := max(a.height-minModalHeight, 0)
	peekRows := contentLines
	if peekRows > maxPeek {
		peekRows = modalPeekRows
	}
	return lastNLines(content, peekRows)
}

// countPeekLines returns the number of lines in s, or 0 if empty.
func (a *App) countPeekLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// getRenderedContent returns cached or freshly built rendered content.
func (a *App) getRenderedContent() string {
	uncommitted := a.repl.messages[a.committedCount:]
	if len(uncommitted) == 0 {
		return ""
	}
	if a.contentCache != "" {
		return a.contentCache
	}
	return renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, false, 0)
}

// lastNLines returns the last n lines of s without allocating a full slice.
// Scans backwards for newline boundaries.
func lastNLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[i+1:]
			}
		}
	}
	return s // fewer than n lines
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
