package tui

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
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
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
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
	input       *Input
	status      StatusBar
	spinner     Spinner
	bgBlinkTick int // advanced by bgTickMsg, drives background engine dot blink

	// REPL session state (delegated to repl.go)
	// Always points to the active engine's ReplState. Updated by switchEngine.
	// Backed by engineMgr — a.engineMgr.Active().Repl.(replSnapshotAdapter).r
	// is the same pointer, kept here as a cache so the existing call sites
	// (a.repl.<method>) keep working without mass-rename.
	repl *ReplState

	// Engine
	// a.engine is the active engine; a.engineMgr owns the full set.
	// engineMgr is the source of truth for multi-engine state; a.engine is
	// kept as a cached pointer updated by switchEngine so existing call sites
	// don't need mass-rename.
	engine       *engine.Engine
	engineMgr    *engine.EngineManager
	systemPrompt string

	// engineFactory builds a new *engine.Engine for /engine new. Set by
	// main.go via SetEngineFactory. nil = /engine new disabled.
	engineFactory EngineFactoryFn

	// Persistence (short-term memory store)
	// sessionID is the active engine's session ID. Kept as a cached mirror of
	// engineMgr.Active().ActiveSessionID; updated by switchEngine and
	// createNewSession/forkCurrentSession.
	sessionID          string
	projectDir         string // working directory for .gbot/meta.json (shared across engines)
	fileHistory        *filehistory.Tracker
	disableFileHistory bool

	// Active dialog overlay (unified for list picking and permission asking)
	activeDialog *Dialog
	onDialogDone func(*Dialog) (tea.Model, tea.Cmd)
	// modelPickerItems stores the current model list while the picker is
	// open, so async quota fetches can update Quota on the right items.
	modelPickerItems []ModelItem

	// Info overlay (read-only text, dismiss with Esc/q)
	infoOverlay       string
	infoOverlayScroll int

	// Active input dialog overlay (interactive PTY input with countdown)
	activeInput *InputDialog

	// Multi-provider model switching.
	// provider/model authoritative state lives on the engine — query via
	// a.engine.Provider().Name() / a.engine.Model(). No TUI-level cache.
	providers       map[string]llm.Provider
	cfg             *config.Config
	providerConfigs map[string]*config.Provider

	// Quota fetcher for the current provider (nil = no quota display).
	// Built once in SetProviders from cfg.Providers[0].
	quotaFetcher quota.Fetcher
	// quotaFetchSeq increments each time the provider switches, allowing
	// in-flight async quota responses from the old provider to be dropped.
	quotaFetchSeq int

	// Hub — callback-based event routing
	hub        *hub.Hub
	tuiHandler *TUIHandler

	// inputReadOnly mirrors the active engine's EngineViewState.ReadOnly. Set in
	// switchEngine. When true, the input box rejects submits and shows a
	// read-only placeholder (the engine is driven by an external connector).
	inputReadOnly bool

	// Idle listener stop channel — closed when user submits to abort
	// an idle readEvents goroutine. Prevents goroutine leak.
	idleStop chan struct{}

	// Feature modules
	history     *History
	killRing    *KillRing
	doublePress *DoublePress
	completions *Completions

	// commands holds the per-App slash command tables (builtins + skills).
	// Tests rely on this being per-instance so they can run in parallel
	// without polluting each other's command state.
	commands *CommandRegistry

	// Paste reference state
	pasteStore  map[int]string
	nextPasteID int

	// Spinner progress state
	allToolsExpanded bool

	// Real-time token rate cached for 1s to avoid jitter
	rateDisplayVal  float64
	rateDisplayTime time.Time

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

	// Task list panel (auto-shows when tasks exist)
	taskListFn    taskListFn  // set from main.go to read tasks for display
	autoCleanupFn func() bool // checked every render; cleans tasks and jobs, returns true if reset happened
	taskListCache string      // rendered task list, rebuilt when dirty
	taskListDirty bool
	killAllFn     func() // set from main.go to kill all background jobs

	stashed *stashedPrompt // Ctrl+S stashed input (survives /clear and Ctrl+C)
}

// NewApp creates a new App model wrapping a single engine in a default
// EngineManager. Equivalent to NewAppWithManager(manager, systemPrompt, h)
// after building a one-engine manager.
//
// Retained for backward compatibility with the many test callers. Production
// code (main.go) should call NewAppWithManager directly.
func NewApp(eng *engine.Engine, systemPrompt string, h *hub.Hub) *App {
	mgr := engine.NewEngineManager()
	if eng != nil {
		mgr.Add(&engine.EngineViewState{
			Engine:          eng,
			Repl:            newReplAdapter(NewReplState()),
			Handler:         nil, // set by NewAppWithManager from hub
			History:         nil, // set by NewAppWithManager loop below
			ID:              eng.EngineID(),
			Name:            "main",
			ActiveSessionID: eng.SessionID(),
			Model:           eng.Model(),
			CreatedAt:       time.Now(),
			LastActiveAt:    time.Now(),
			ReadOnly:        false,
		})
	}
	return NewAppWithManager(mgr, systemPrompt, h)
}

// NewAppWithManager creates an App bound to an EngineManager that already
// holds one or more engines. The active engine (mgr.Active()) becomes the
// initial a.engine / a.repl / a.sessionID cache.
// historyPathFor returns the per-engine history file path:
// ~/.gbot/history/{engineID}.jsonl. Returns "" if config dir is unavailable.
func historyPathFor(engineID string) string {
	configDir, err := config.ConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "history", engineID+".jsonl")
}

func NewAppWithManager(mgr *engine.EngineManager, systemPrompt string, h *hub.Hub) *App {
	a := &App{
		input:            NewInput(),
		status:           NewStatusBar(),
		spinner:          NewSpinner(),
		engineMgr:        mgr,
		systemPrompt:     systemPrompt,
		hub:              h,
		killRing:         NewKillRing(),
		doublePress:      NewDoublePress(),
		completions:      NewCompletions(),
		commands:         NewCommandRegistry(),
		pasteStore:       make(map[int]string),
		nextPasteID:      1,
		allToolsExpanded: false,
		idleStop:         make(chan struct{}),
	}
	if mgr != nil {
		if vs := mgr.Active(); vs != nil {
			a.engine = vs.Engine
			if r, ok := vs.Repl.(replSnapshotAdapter); ok {
				a.repl = r.r
			} else {
				// Fallback for adapters that don't unwrap cleanly OR
				// view states registered without a Repl (e.g. restoreEngines
				// in main.go, which adds view states with only Engine set).
				// Without a fresh ReplState here, a.repl stays nil and
				// SetStore panics on a.repl.messages dereference.
				// Cache back on vs.Repl so switchEngine reuses this state
				// instead of building another fresh one (which would lose
				// streaming state accumulated while this engine was active).
				fresh := NewReplState()
				if vs.Engine != nil {
					fresh.messages = engineMessagesToViews(vs.Engine.Messages(), vs.Engine.AllTools())
				}
				a.repl = fresh
				vs.Repl = newReplAdapter(fresh)
			}
			a.sessionID = vs.ActiveSessionID
		}
		// Initialize per-engine History for all registered view states.
		for _, vs := range mgr.List() {
			if vs.History == nil {
				vs.History = NewHistory(historyPathFor(vs.ID))
			}
		}
		if vs := mgr.Active(); vs != nil {
			if h, ok := vs.History.(*History); ok {
				a.history = h
			}
		}
	}
	if a.history == nil {
		a.history = NewHistory("")
	}
	// Bind to the active engine's Hub+handler pair. The factory in main.go
	// builds one Hub per engine and stores its subscribed handler on
	// EngineViewState.Handler. Creating a fresh TUIHandler here would bypass
	// that pair — engine events would never reach the App (query appears
	// to hang, token counter stays at 0). Fall back to the legacy Hub+fresh
	// handler only when vs.Handler is nil (e.g. tests constructing App
	// without a per-engine handler).
	if vs := mgr.Active(); vs != nil && vs.Handler != nil {
		if handler, ok := vs.Handler.(*TUIHandler); ok {
			a.tuiHandler = handler
		}
		a.hub = h // only used by tests / legacy Subscribe path
	}
	if a.tuiHandler == nil && h != nil {
		a.tuiHandler = NewTUIHandler()
		h.Subscribe(a.tuiHandler)
	}
	if a.engine != nil {
		a.status.SetToolCount(len(a.engine.AllTools()))
		a.status.SetContext(0, a.engine.ContextWindow())
		a.status.SetModel(a.engine.Model())
	}

	// Set all non-active engines to background drain mode. Without this,
	// their handlers stay in active mode (drainFn=nil) — events pile up in
	// each handler's appCh with no reader, eventually filling the buffer
	// (cap=1024) and blocking the engine goroutine on appCh <- msg.
	activeID := ""
	if mgr != nil {
		activeID = mgr.ActiveID()
		for _, vs := range mgr.List() {
			if vs.ID == activeID {
				continue
			}
			if h, ok := vs.Handler.(*TUIHandler); ok && h != nil {
				h.SetDrainFn(a.buildBackgroundDrainFn(vs))
			}
		}
	}

	return a
}

// RegisterSkillCommands replaces the per-App skill command registrations.
// Forwards to a.commands; see CommandRegistry.RegisterSkillCommands.
func (a *App) RegisterSkillCommands(cmds map[string]CommandDef) {
	a.commands.RegisterSkillCommands(cmds)
}

// SetDisableFileHistory disables the file history tracker (rewind/restore).
// Used in daemon mode where TakeSnapshot would walk ~/.gbot/ and OOM.
func (a *App) SetDisableFileHistory(v bool) {
	a.disableFileHistory = v
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

	// Sync the active engine's provider/model to the config's resolved
	// default. This is only meaningful for the initial engine created in
	// main.go before any per-engine restore — the engine's own state is
	// authoritative thereafter.
	providerName, modelName, err := cfg.ParseModel()
	if err != nil {
		slog.Warn("config: invalid model, using first", "model", cfg.Model, "error", err)
	}
	if providerName == "" && len(cfg.Providers) > 0 {
		providerName = cfg.Providers[0].Name
	}
	if modelName == "" && len(cfg.Providers) > 0 {
		modelName = cfg.Providers[0].FirstModelName()
	}
	if a.engine != nil {
		if p, ok := providers[providerName]; ok {
			a.engine.SetProvider(p)
		}
		a.engine.SetModel(modelName)
			a.status.SetModel(modelName)
	}

	// Build quota fetcher from the resolved provider (nil if unsupported).
	if p, ok := a.providerConfigs[providerName]; ok {
		a.quotaFetcher = quota.Detect(p)
	} else if len(cfg.Providers) > 0 {
		a.quotaFetcher = quota.Detect(&cfg.Providers[0])
	}
}

// CurrentProvider returns the active provider name, read from the engine —
// the single source of truth. Returns "" if the engine has no provider set.
func (a *App) CurrentProvider() string {
	if a.engine == nil {
		return ""
	}
	if p := a.engine.Provider(); p != nil {
		return p.Name()
	}
	return ""
}

// CurrentModel returns the active model name, read from the engine.
func (a *App) CurrentModel() string {
	if a.engine == nil {
		return ""
	}
	return a.engine.Model()
}

// Engine returns the active engine.
func (a *App) Engine() *engine.Engine {
	return a.engine
}

// SetContextUsed updates the status bar's context usage counter.
// The context window (denominator) always comes from the engine's own config.
func (a *App) SetContextUsed(used int) {
	a.status.SetContext(used, a.engine.ContextWindow())
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

// SetKillAllFn sets the callback to kill all background jobs on double-press Escape.
func (a *App) SetKillAllFn(fn func()) {
	a.killAllFn = fn
}

// ActiveEngine returns the currently active engine, or nil.
func (a *App) ActiveEngine() *engine.Engine {
	return a.engine
}

// persistModelSelection writes the active engine's model to meta.json so
// it survives restart. settings.json's model.default is left alone — it's
// a seed value used only when meta.json doesn't exist yet (first launch
// in a new workspace).
func (a *App) persistModelSelection() {
	if a.engineMgr == nil {
		return
	}
	fullModel := a.CurrentProvider() + "/" + a.CurrentModel()
	a.engineMgr.SetActiveModel(fullModel)
	if err := a.engineMgr.PersistMeta(a.projectDir); err != nil {
		slog.Warn("model: failed to persist per-engine meta", "error", err)
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

	// Mirror session ID into the active EngineViewState so persistWorkspaceMeta
	// (which reads from the manager) emits the correct CurrentSessionID.
	if a.engineMgr != nil {
		if vs := a.engineMgr.Active(); vs != nil {
			vs.ActiveSessionID = sessionID
		}
	}

	// Sync repl.messages from engine on resume.
	// Leave committedCount at 0 so WindowSizeMsg can commit them to scrollback
	// via tea.Println. View() returns "Loading..." while width==0, so there's
	// no double-render between SetStore and WindowSizeMsg.
	if len(a.engine.Messages()) > 0 {
		a.repl.messages = engineMessagesToViews(a.engine.Messages(), a.engine.AllTools())
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
	// Disabled in daemon mode — TakeSnapshot walks the working directory and
	// reads every file into memory, which OOMs when workingDir is ~/.gbot/.
	if sessionID != "" && !a.disableFileHistory {
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
// Renders text, thinking, and tool blocks so resume looks close to the original session.
func engineMessagesToViews(msgs []types.Message, tools map[string]tool.Tool) []MessageView {
	// First pass: collect all tool_results keyed by tool_use_id across all messages.
	// tool_result blocks live in user messages (the API response), while tool_use
	// blocks live in assistant messages — they're never in the same message.
	toolResults := make(map[string]types.ContentBlock)
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult && block.ToolUseID != "" {
				toolResults[block.ToolUseID] = block
			}
		}
	}

	views := make([]MessageView, 0, len(msgs))
	var current *MessageView

	for _, msg := range msgs {
		if msg.Flags&types.FlagMeta != 0 {
			continue
		}

		switch msg.Role {
		case types.RoleAssistant:
			// If current was flushed (nil) or we're accumulating, just keep going.
			// Text within a single message doesn't break the accumulation.
			if current == nil {
				current = &MessageView{Role: "assistant"}
			}
			for _, block := range msg.Content {
				switch block.Type {
				case types.ContentTypeText:
					if strings.TrimSpace(block.Text) != "" {
						current.Blocks = append(current.Blocks, ContentBlock{Type: BlockText, Text: block.Text})
					}
				case types.ContentTypeThinking:
					if strings.TrimSpace(block.Thinking) != "" {
						current.Blocks = append(current.Blocks, ContentBlock{
							Type: BlockThinking,
							Thinking: ThinkingView{
								Text: block.Thinking,
								Done: true,
							},
						})
					}
				case types.ContentTypeToolUse:
					inputStr := formatToolInput(block.Input)
					srk := classifyToolName(block.Name, block.Input)
					result, hasResult := toolResults[block.ID]
					output := ""
					isErr := false
					elapsed := time.Duration(0)
					if hasResult {
						output, elapsed = renderToolOutput(block.Name, result.Content, tools)
						isErr = result.IsError
					}
					current.Blocks = append(current.Blocks, ContentBlock{
						Type: BlockTool,
						ToolCall: ToolCallView{
							ID:         block.ID,
							Name:       formatToolDisplayName(block.Name),
							Summary:    computeToolSummary(block.Name, block.Input, tools),
							Input:      inputStr,
							Output:     output,
							IsError:    isErr,
							Done:       true,
							Elapsed:    elapsed,
							SearchRead: srk,
						},
					})
				}
			}
		case types.RoleUser:
			hasNonToolResult := false
			for _, block := range msg.Content {
				if block.Type != types.ContentTypeToolResult {
					hasNonToolResult = true
					break
				}
			}
			if hasNonToolResult {
				if current != nil && len(current.Blocks) > 0 {
					views = append(views, *current)
					current = nil
				}
				mv := MessageView{Role: "user"}
				for _, block := range msg.Content {
					if block.Type == types.ContentTypeText && strings.TrimSpace(block.Text) != "" {
						mv.Blocks = append(mv.Blocks, ContentBlock{Type: BlockText, Text: block.Text})
					}
				}
				if len(mv.Blocks) > 0 {
					views = append(views, mv)
				}
			}
		}
	}
	if current != nil && len(current.Blocks) > 0 {
		views = append(views, *current)
	}

	return views
}

// formatToolInput returns a pretty-printed JSON string for tool input.
func formatToolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// renderToolOutput renders persisted tool_result content using the tool's own
// RenderResult logic. This produces the same display as normal streaming.
func renderToolOutput(toolName string, raw json.RawMessage, tools map[string]tool.Tool) (string, time.Duration) {
	if len(raw) == 0 {
		return "", 0
	}

	// Unwrap: tool_result content is a JSON string (gbot's double-wrapped format).
	var s string
	if json.Unmarshal(raw, &s) != nil {
		// Try as array of content blocks (Anthropic format).
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n"), 0
			}
		}
		return string(raw), 0
	}

	// Parse duration prefix: "[Tool spent Xs]"
	rest := s
	elapsed := time.Duration(0)
	if strings.HasPrefix(rest, "[Tool spent ") {
		if idx := strings.Index(rest, "]"); idx >= 0 {
			inner := strings.TrimPrefix(rest[:idx+1], "[Tool spent ")
			inner = strings.TrimSuffix(inner, "s]")
			if sec, err := strconv.ParseFloat(inner, 64); err == nil {
				elapsed = time.Duration(sec * float64(time.Second))
			}
			rest = rest[idx+1:]
		}
	}
	if rest == "" {
		return "", elapsed
	}

	// Handle <persisted-output>: read file from disk, then render via tool.
	if strings.HasPrefix(rest, "<persisted-output>") {
		if data := readPersistedFile(rest); data != nil {
			if rendered := renderViaTool(toolName, data, tools); rendered != "" {
				return rendered, elapsed
			}
		}
		// Fallback: show preview
		return extractPersistedPreview(rest), elapsed
	}

	// Try tool's RenderResult with the raw JSON content.
	// If the tool exists, its RenderResult is authoritative — even empty output
	// means the tool handled it (e.g. Bash with no stdout). Don't fallback.
	if t, ok := tools[toolName]; ok {
		return t.RenderResult(json.RawMessage(rest)), elapsed
	}

	// Fallback: try unwrapping one more level (gbot's {"output":"..."} format).
	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(rest), &obj) == nil && obj.Output != "" {
		return obj.Output, elapsed
	}

	return rest, elapsed
}

// renderViaTool finds the tool and calls RenderResult with the raw JSON.
func renderViaTool(toolName string, raw json.RawMessage, tools map[string]tool.Tool) string {
	t, ok := tools[toolName]
	if !ok {
		return ""
	}
	return t.RenderResult(raw)
}

// readPersistedFile extracts the file path from a <persisted-output> block
// and reads the full content from disk.
func readPersistedFile(s string) json.RawMessage {
	_, after, ok := strings.Cut(s, "Full output saved to: ")
	if !ok {
		return nil
	}
	pathEnd := strings.IndexByte(after, '\n')
	if pathEnd < 0 {
		pathEnd = len(after)
	}
	filePath := strings.TrimSpace(after[:pathEnd])
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

// extractPersistedPreview is a last-resort fallback showing the preview text.
func extractPersistedPreview(s string) string {
	_, after, ok := strings.Cut(s, "Preview (")
	if !ok {
		return "<output saved to file>"
	}
	_, after0, ok0 := strings.Cut(after, "):\n")
	if !ok0 {
		return "<output saved to file>"
	}
	preview := after0
	lines := strings.SplitN(preview, "\n", 6)
	if len(lines) > 5 {
		lines = lines[:5]
		lines = append(lines, "...")
	}
	result := strings.Join(lines, "\n")
	if result == "" {
		return "<output saved to file>"
	}
	return result
}

// classifyToolName returns SearchReadKind based on tool name and input.
// Best-effort for resume — streaming path has richer classification.
func classifyToolName(name string, input json.RawMessage) tool.SearchReadKind {
	switch name {
	case "Read":
		return tool.SearchReadKind{IsRead: true}
	case "Grep", "Glob":
		return tool.SearchReadKind{IsSearch: true}
	case "Lsp":
		return tool.SearchReadKind{IsLsp: true}
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			srk := bash.IsSearchOrRead(input)
			if srk.IsCollapsible() {
				return srk
			}
		}
	}
	return tool.SearchReadKind{}
}

// computeToolSummary generates a summary string for a tool call during resume,
// using the tool's own Description function — same path as normal rendering.
func computeToolSummary(name string, input json.RawMessage, tools map[string]tool.Tool) string {
	t, ok := tools[name]
	if !ok {
		return ""
	}
	desc, err := t.Description(input)
	if err != nil {
		return ""
	}
	return desc
}

// resetDisplayState zeros all App-level display fields for a clean session.
// Called by createNewSession so both /clear and /session -n benefit.
func (a *App) resetDisplayState() {
	a.scrollOffset = 0
	a.scrollTotal = 0
	a.userScrolled = false
	a.contentCache = ""
	a.contentDirty = false
	a.taskListDirty = true
	a.allToolsExpanded = false
	a.repl.displayedInputTokens = 0
	a.repl.displayedOutputTokens = 0
	a.repl.outputTokenTarget = 0
	a.repl.inputTokenTarget = 0
	if a.repl != nil {
		a.repl.ResetStreamingUIState()
	}
	a.pasteStore = make(map[int]string)
	a.nextPasteID = 1
	a.toolBlink = false
	a.toolBlinkTick = 0
	a.retryActive = false
	a.retryAttempt = 0
	a.retryMax = 0
	a.retryRemaining = 0
	a.retryStart = time.Time{}
	a.retryErrorType = ""
	a.repl.pendingQueue = nil
	a.status.SetUsage(types.Usage{})
}

// ---------------------------------------------------------------------------
// tea.Model interface
// ---------------------------------------------------------------------------

// activateStreamingUI syncs App-level streaming indicators (status bar
// flag, spinner animation) with a.repl's streaming state and returns a
// tea.Cmd that bootstraps the spinner tick chain if the engine is
// mid-stream.
//
// Call this whenever a.repl is rebound to a different engine (switchEngine,
// NewAppWithManager, createNewEngine). Centralizing the setup here prevents
// the recurring bug where a new activation path forgets one of: status
// flag, spinner.Start(), or the tick bootstrap cmd.
func (a *App) activateStreamingUI() tea.Cmd {
	if a.repl == nil || !a.repl.IsStreaming() {
		a.status.SetStreaming(false)
		a.spinner.Stop()
		return nil
	}
	a.status.SetStreaming(true)
	a.spinner.Start()
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// bgEngineStreaming reports whether any non-active engine is currently
// streaming. Used by the bgTickMsg handler to decide whether to keep
// the background dot blink chain alive.
func (a *App) bgEngineStreaming() bool {
	if a.engineMgr == nil {
		return false
	}
	activeID := a.engineMgr.ActiveID()
	for _, vs := range a.engineMgr.List() {
		if vs.ID == activeID {
			continue
		}
		if vs.Repl != nil && vs.Repl.IsStreaming() {
			return true
		}
	}
	return false
}

// commitPendingMessagesCmd renders a.repl.messages into a string and returns
// a tea.Println cmd that writes them to the terminal scrollback. Also bumps
// committedCount so the rendered messages are not re-rendered by View().
//
// Returns nil when:
//   - committedCount is already in sync (no pending work),
//   - there are no messages to commit, or
//   - the repl is streaming (mid-query messages must not be committed;
//     committing them and then having the stream continue would blank the
//     TUI on the next SIGWINCH).
//
// Called from the WindowSizeMsg handler (first resize after SetStore) and
// from switchEngine (so engine switches redraw history immediately without
// waiting for a WindowSizeMsg that never fires on switch).
// recommitHistoryCmd re-writes already-committed messages to terminal
// scrollback after a ClearScreen (engine switch). Unlike
// commitPendingMessagesCmd, this does NOT change committedCount — it
// writes messages[:committedCount] to scrollback to restore the visual
// state the user had before the switch.
func (a *App) recommitHistoryCmd() tea.Cmd {
	if a.repl.committedCount == 0 || len(a.repl.messages) == 0 {
		return nil
	}
	msgs := a.repl.messages[:a.repl.committedCount]
	const maxResumeMessages = 30
	skipped := 0
	if len(msgs) > maxResumeMessages {
		skipped = len(msgs) - maxResumeMessages
		msgs = msgs[len(msgs)-maxResumeMessages:]
	}
	var rendered string
	if skipped > 0 {
		skipNote := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(
			fmt.Sprintf("... (%d earlier messages skipped) ...", skipped))
		rendered = skipNote + "\n" + renderMessagesFull(msgs, a.width, a.allToolsExpanded, "", false, false, 0)
	} else {
		rendered = renderMessagesFull(msgs, a.width, a.allToolsExpanded, "", false, false, 0)
	}
	return tea.Println(rendered + "\n")
}

func (a *App) commitPendingMessagesCmd() tea.Cmd {
	if a.repl.committedCount != 0 || len(a.repl.messages) == 0 || a.repl.IsStreaming() {
		return nil
	}
	const maxResumeMessages = 30
	msgs := a.repl.messages
	skipped := 0
	if len(msgs) > maxResumeMessages {
		skipped = len(msgs) - maxResumeMessages
		msgs = msgs[len(msgs)-maxResumeMessages:]
	}
	var rendered string
	if skipped > 0 {
		skipNote := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render(
			fmt.Sprintf("... (%d earlier messages skipped) ...", skipped))
		rendered = skipNote + "\n" + renderMessagesFull(msgs, a.width, a.allToolsExpanded, "", false, false, 0)
	} else {
		rendered = renderMessagesFull(msgs, a.width, a.allToolsExpanded, "", false, false, 0)
	}
	a.repl.committedCount = len(a.repl.messages)
	a.contentCache = ""
	a.contentDirty = false
	return tea.Println(rendered + "\n")
}

// Init initializes the TUI.
// No EnterAltScreen — terminal native scrollback handles scrolling,
// matching TS behavior where Ink writes content and the terminal scrolls.
func (a *App) Init() tea.Cmd {
	// Start the event reader on launch so connector messages (e.g. WeChat)
	// dispatched before the first user query are picked up. Without this,
	// events pile up in appCh with no reader when the active engine is a
	// connector engine and no local query has been submitted yet.
	return a.readEvents()
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
				if a.repl.IsStreaming() {
					return a, tea.Batch(
						a.readEvents(),
						tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
							return spinnerTickMsg{}
						}),
					)
				}
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
		case modelQuotaFetchedMsg:
			// Fall through to the main type switch — dialog items need updating.
		default:
			return a, nil
		}
	}

	// Info overlay: intercept Esc/q to dismiss, scroll to navigate.
	if a.infoOverlay != "" {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			a.width = msg.Width
			a.height = msg.Height
			return a, nil
		case tea.KeyMsg:
			switch msg.String() {
			case "esc", "q", "ctrl+c":
				a.infoOverlay = ""
				a.infoOverlayScroll = 0
				return a, nil
			case "up", "k":
				a.infoOverlayScroll = max(a.infoOverlayScroll-1, 0)
				return a, nil
			case "down", "j":
				a.infoOverlayScroll++
				return a, nil
			case "pgup":
				a.infoOverlayScroll = max(a.infoOverlayScroll-max(a.height/2, 1), 0)
				return a, nil
			case "pgdown":
				a.infoOverlayScroll += max(a.height/2, 1)
				return a, nil
			}
			return a, nil
		case tea.MouseMsg:
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				a.infoOverlayScroll = max(a.infoOverlayScroll-3, 0)
			case tea.MouseButtonWheelDown:
				a.infoOverlayScroll += 3
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
		// Commit resumed messages to terminal scrollback on first resize.
		// SetStore leaves committedCount=0 with messages present; this is
		// the first Update where width > 0, so we can render and commit.
		//
		// Guard against streaming: a fresh query after /clear also has
		// committedCount=0 + messages>0 (user + assistant placeholder),
		// but those are mid-stream and must NOT be committed — otherwise
		// any SIGWINCH during stream blanks the TUI (blank render bug).
		if cmd := a.commitPendingMessagesCmd(); cmd != nil {
			return a, cmd
		}
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
		infoMsg, errMsg, submitMsg, spinnerTickMsg, bgTickMsg,
		permissionAskMsg, inputAskMsg, retryAttemptMsg,
		quotaUpdatedMsg, modelQuotaFetchedMsg, userMessageMsg:
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
	if len(a.repl.pendingQueue) == 0 {
		return ""
	}
	maxWidth := max(a.width-renderedPromptWidth, 10)
	var sb strings.Builder
	prefix := styleDim.Render("❯ ")
	indent := strings.Repeat(" ", lipgloss.Width(prefix))
	for _, item := range a.repl.pendingQueue {
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

	// Info overlay (read-only text, dismiss with Esc/q)
	if a.infoOverlay != "" {
		return a.renderInfoOverlay()
	}

	// Input dialog overlay (interactive PTY input with countdown)
	if a.activeInput != nil {
		return a.renderInputOverlay()
	}

	uncommitted := a.repl.messages[a.repl.committedCount:]

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
		if a.repl.committedCount == 0 {
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

	// Progress line: spinner + elapsed + tokens + thinking when streaming
	if a.repl.IsStreaming() && !a.repl.StreamingStart().IsZero() {
		// Retry display: show user-friendly error + countdown for attempts >= 4
		// Source: TS SystemAPIErrorMessage.tsx — hidden for attempts < 4
		if a.retryActive && a.retryAttempt >= 4 && !a.repl.IsThinking() {
			secs := max(int((a.retryRemaining-time.Since(a.retryStart)).Seconds())+1, 0)
			errLine := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(formatRetryError(a.retryErrorType))
			var countdownLine string
			if secs > 0 {
				countdownLine = lipgloss.NewStyle().Faint(true).Render(
					fmt.Sprintf("Retrying in %ds… (attempt %d/%d)", secs, a.retryAttempt, a.retryMax))
			} else {
				countdownLine = lipgloss.NewStyle().Faint(true).Render(
					fmt.Sprintf("Connecting… (attempt %d/%d)", a.retryAttempt, a.retryMax))
			}
			sb.WriteString(errLine + "\n" + countdownLine + "\n")
		} else {
			spinnerFrame := a.spinner.View()
			elapsedStr := utils.FormatDuration(time.Since(a.repl.StreamingStart()))
			tokensStr := fmt.Sprintf("↑%s ↓%s tokens", types.FormatTokenCount(a.repl.displayedInputTokens), types.FormatTokenCount(a.repl.displayedOutputTokens))
			var thinkingStr string
			if a.repl.IsThinking() {
				thinkingStr = " · thinking"
			} else if a.repl.ThinkingDuration() > 0 {
				thinkingStr = fmt.Sprintf(" · thought for %.1fs", a.repl.ThinkingDuration().Seconds())
			}
			var toolsStr string
			if tc := a.repl.toolCount; tc > 0 {
				if tc == 1 {
					toolsStr = " · 1 tool"
				} else {
					toolsStr = fmt.Sprintf(" · %d tools", tc)
				}
			}
			// Throttle rate display to 1 refresh/sec to avoid jitter.
			if time.Since(a.rateDisplayTime) >= 500*time.Millisecond {
				a.rateDisplayVal = a.repl.TokenRate().Rate()
				a.rateDisplayTime = time.Now()
			}
			rateStr := fmt.Sprintf(" · %.1f t/s", a.rateDisplayVal)
			progressLine := spinnerFrame + " (" + elapsedStr + " · " + tokensStr + rateStr + toolsStr + thinkingStr + ")"
			sb.WriteString(progressLine)
			sb.WriteString("\n")
		}
	}

	// Task list panel (between progress line and input box)
	if taskPanel != "" {
		sb.WriteString(taskPanel)
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
	// Multi-engine indicator row (only rendered when >1 engine exists).
	if engineBar := a.renderEngineStatusBar(); engineBar != "" {
		sb.WriteString(engineBar)
		sb.WriteString("\n")
	}
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
		slog.Info("tui:key_escape",
			"string", msg.String(),
			"alt", msg.Alt,
			"runes", len(msg.Runes),
			"completions_visible", a.completions.Visible(),
			"streaming", a.repl.IsStreaming(),
		)
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

	case tea.KeyCtrlJ:
		a.input.InsertNewline()
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
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value), a.commands)
		return a, nil

	case tea.KeyDelete:
		a.resetNavAndAccum()
		a.input.DeleteForward()
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value), a.commands)
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
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value), a.commands)
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
		}
		a.repl.FinishStream(nil)
		// Commit uncommitted messages to scrollback
		var cmd tea.Cmd
		uncommitted := a.repl.messages[a.repl.committedCount:]
		if len(uncommitted) > 0 {
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", false, false, 0)
			a.repl.committedCount = len(a.repl.messages)
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
		a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value), a.commands)
		return a, nil
	}
	a.resetNavAndAccum()
	for _, ch := range msg.Runes {
		if ch == '\r' {
			ch = '\n'
		}
		a.input.InsertChar(ch)
	}
	a.completions.Update(a.input.Value(), a.input.cursor == len(a.input.value), a.commands)
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

// renderInfoOverlay renders the info overlay: peek content + bordered content + hint.
func (a *App) renderInfoOverlay() string {
	peek := a.computePeek()

	content := a.infoOverlay
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Reserve: 2 border + 2 padding + 1 hint = 5; plus peek lines
	peekLines := a.countPeekLines(peek)
	maxContent := max(a.height-5-peekLines, 1)

	// Clamp scroll
	if a.infoOverlayScroll > totalLines-maxContent {
		a.infoOverlayScroll = max(totalLines-maxContent, 0)
	}
	if a.infoOverlayScroll < 0 {
		a.infoOverlayScroll = 0
	}

	end := min(a.infoOverlayScroll+maxContent, totalLines)
	visible := strings.Join(lines[a.infoOverlayScroll:end], "\n")

	hint := dialogHint.Render("↑/k up · ↓/j down · PgUp/PgDown · Esc close")
	border := dialogBorderStyle.Render(visible + "\n\n" + hint)

	if peek == "" {
		return border
	}
	return peek + "\n" + border
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
	uncommitted := a.repl.messages[a.repl.committedCount:]
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
