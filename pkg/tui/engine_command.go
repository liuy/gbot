package tui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/quota"
)

// EngineFactoryFn builds a new *engine.Engine for a fresh engine ID, name, and
// model, plus the TUIHandler already subscribed to the engine's dedicated Hub.
// Set on App by main.go (or a test) — required for /engine new. When nil,
// /engine new reports an error instead of creating.
//
// The factory owns Hub construction (one Hub per engine — the critical
// isolation invariant for multi-engine event routing). It must:
//  1. Construct engineHub := hub.NewHub()
//  2. Create a per-engine TUIHandler
//  3. Subscribe the handler to engineHub
//  4. Pass Dispatcher: engineHub to engine.New(...)
//
// Returning the handler lets createNewEngine store it on EngineViewState.Handler
// so switchEngine can flip drain roles later.
type EngineFactoryFn func(id, name, model string) (*engine.Engine, *TUIHandler, error)

// handleEngine implements the /engine command.
//
//	/engine          → open engine picker (list all engines, switch on select)
//	/engine new      → create new engine with auto-generated name
//	/engine new NAME → create new engine with custom name
//	/engine <name>   → switch to an existing engine by Name or ID
func (a *App) handleEngine(args string, commitCmd tea.Cmd) tea.Cmd {
	args = strings.TrimSpace(args)
	if args == "" {
		return a.openEnginePicker(commitCmd)
	}
	if args == "new" || strings.HasPrefix(args, "new ") {
		name := ""
		if after, ok := strings.CutPrefix(args, "new "); ok {
			name = strings.TrimSpace(after)
			if name == "" {
				return a.showInfo("Engine name cannot be empty. Usage: /engine new <name>")
			}
		}
		return a.createNewEngine(name, commitCmd)
	}
	// Otherwise treat args as engine Name or ID and switch directly.
	if a.engineMgr == nil {
		return a.showInfo("Engine manager not initialized")
	}
	for _, vs := range a.engineMgr.List() {
		if vs.Name == args || vs.ID == args {
			if vs.ID == a.engineMgr.ActiveID() {
				return a.showInfo(fmt.Sprintf("Already on engine: %s", vs.Name))
			}
			_, cmd := a.switchEngine(vs.ID)
			return cmd
		}
	}
	return a.showInfo(fmt.Sprintf("Engine not found: %s (use /engine to list)", args))
}

// SetEngineFactory registers the engine construction callback. Called by
// main.go after the bootstrap engine is wired so /engine new can build
// additional engines using the same provider/MCP/hook setup.
func (a *App) SetEngineFactory(fn EngineFactoryFn) {
	a.engineFactory = fn
}

// openEnginePicker shows the list of engines and switches on selection.
func (a *App) openEnginePicker(commitCmd tea.Cmd) tea.Cmd {
	if a.engineMgr == nil || a.engineMgr.Count() < 1 {
		return a.showInfo("No engines available")
	}
	if a.activeDialog != nil {
		return a.showInfo("A dialog is already open")
	}
	views := a.engineMgr.List()
	items := make([]enginePickerItem, len(views))
	for i, vs := range views {
		items[i] = enginePickerItem{vs: vs, isActive: vs.ID == a.engineMgr.ActiveID()}
	}
	pickerItems := make([]PickerItem, len(items))
	for i := range items {
		pickerItems[i] = &items[i]
	}
	a.activeDialog = NewDialog("Switch Engine", pickerItemsToOptions(pickerItems))
	a.activeDialog.width = a.width
	captured := items
	a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
		return a.handleEnginePickerDone(d, captured)
	}
	return commitCmd
}

// enginePickerItem adapts an EngineViewState for the picker list.
type enginePickerItem struct {
	vs       *engine.EngineViewState
	isActive bool
}

func (e *enginePickerItem) Label() string {
	prefix := "  "
	if e.isActive {
		prefix = "● "
	}
	name := e.vs.Name
	status := "idle"
	if e.vs.Engine == nil {
		status = "lazy"
	} else if e.vs.Repl != nil && e.vs.Repl.IsStreaming() {
		tool := e.vs.Repl.CurrentToolName()
		if tool == "" {
			status = "streaming"
		} else {
			status = tool
		}
	}
	return fmt.Sprintf("%s%-15s %s", prefix, name, status)
}

func (a *App) handleEnginePickerDone(d *Dialog, items []enginePickerItem) (tea.Model, tea.Cmd) {
	if d.Aborted() {
		return a, nil
	}
	idx := d.SelectedIndex()
	if idx < 0 || idx >= len(items) {
		return a, nil
	}
	selected := items[idx]
	if selected.isActive {
		return a, a.showInfo("Already on this engine")
	}
	return a.switchEngine(selected.vs.ID)
}

// switchEngine changes the active engine. Caller must have already verified the
// new engine exists and the active engine is not streaming.
//
// Flips drain roles on the old/new TUIHandlers, refreshes the cached
// a.engine/a.repl/a.sessionID/a.tuiHandler pointers, and persists meta so
// restart resumes the right engine.
//
// Hub ownership is per-engine (one Hub, one subscribed handler per engine).
// a.tuiHandler always points at the ACTIVE engine's handler so readEvents
// drains the right appCh. Each EngineViewState.Handler is the permanent home
// for that engine's *TUIHandler; switchEngine flips drainFn on it directly
// (background mode when demoted, nil when promoted to active).
func (a *App) switchEngine(id string) (tea.Model, tea.Cmd) {
	if a.engineMgr == nil {
		return a, a.showInfo("Engine manager not initialized")
	}
	target := a.engineMgr.Get(id)
	if target == nil {
		return a, a.showInfo("Engine not found: " + id)
	}

	oldID := a.engineMgr.ActiveID()
	if oldID == id {
		// No-op switch (caller already verified, but be defensive).
		return a, nil
	}

	// Demote: flip the old engine's handler into background drain mode.
	// The handler lives on oldVS.Handler (set by restoreEngines at startup
	// or by createNewEngine at runtime) — single source of truth, no
	// separate runtime map needed.
	if oldVS := a.engineMgr.Get(oldID); oldVS != nil {
		if h, ok := oldVS.Handler.(*TUIHandler); ok && h != nil {
			h.SetDrainFn(a.buildBackgroundDrainFn(oldVS))
		}
	}

	// Promote: drain the new engine's handler backlog, clear its drainFn,
	// and bind it as a.tuiHandler so readEvents drains the right appCh.
	newHandler, _ := target.Handler.(*TUIHandler)
	if newHandler == nil {
		return a, a.showInfo("No handler registered for engine: " + id)
	}
	newHandler.DrainBacklog(a.buildBackgroundDrainFn(target))
	newHandler.SetDrainFn(nil)
	a.tuiHandler = newHandler

	if err := a.engineMgr.SetActive(id); err != nil {
		return a, a.showInfo(fmt.Sprintf("Failed to activate engine: %v", err))
	}

	// Sync cached pointers to the newly-active engine.
	a.engine = target.Engine
	if r, ok := target.Repl.(replSnapshotAdapter); ok && r.r != nil {
		a.repl = r.r
	} else if target.Engine != nil {
		// target.Repl is nil — restoreEngines in main.go adds view states
		// with Engine + Handler but no Repl. Build a fresh ReplState
		// populated from the engine's already-loaded messages so the
		// screen shows the restored engine's history on switch. Cache it
		// back on vs.Repl so the next switch reuses it instead of
		// rebuilding.
		fresh := NewReplState()
		fresh.messages = engineMessagesToViews(target.Engine.Messages(), target.Engine.AllTools())
		a.repl = fresh
		target.Repl = newReplAdapter(fresh)
	}
	a.sessionID = target.ActiveSessionID

	// Reset display state so the new engine's render starts fresh.
	a.resetDisplayState()
	a.committedCount = 0
	a.contentCache = ""
	a.contentDirty = true

	// If the target engine is mid-stream, restore the streaming UI state
	// (progress line + status bar streaming flag). Otherwise the user
	// sees a frozen view: messages present but no elapsed counter, no
	// spinner — looks dead even though events still arrive via the drain
	// fn that just got cleared.
	if a.repl != nil && a.repl.IsStreaming() {
		a.progressStart = a.repl.StreamingStart()
		a.status.SetStreaming(true)
	}

	// Status bar reflects new active engine's model.
	if target.Engine != nil {
		a.status.SetModel(target.Engine.Model())
		a.status.SetContext(target.Engine.GetContextTokens(), target.Engine.ContextWindow())
	}

	// Sync currentProvider/currentModel to the new engine so /model picker
	// highlights the right row and /model fuzzy-match searches the right
	// provider. engine.Model() is authoritative for the model name; the
	// provider lookup walks providerConfigs for a matching model.
	if target.Engine != nil {
		a.currentModel = target.Engine.Model()
		if a.cfg != nil {
			for i := range a.cfg.Providers {
				pc := &a.cfg.Providers[i]
				if slices.Contains(pc.ModelNames(), a.currentModel) {
					a.currentProvider = pc.Name
					a.quotaFetcher = quota.Detect(pc)
					break
				}
			}
		}
	}

	// Persist meta so restart resumes the right engine.
	if err := a.persistWorkspaceMeta(); err != nil {
		slog.Warn("engine: persist meta failed", "error", err)
	}

	slog.Info("engine: switched", "from", oldID, "to", id)
	cmds := []tea.Cmd{tea.ClearScreen, a.showInfo(fmt.Sprintf("Switched to engine: %s", target.Name))}
	// Commit the new engine's history to scrollback immediately so the
	// screen shows it after the clear — without this, switched-to engines
	// with non-empty history render blank until the next WindowSizeMsg
	// (which never fires on switch).
	if commitCmd := a.commitPendingMessagesCmd(); commitCmd != nil {
		cmds = append(cmds, commitCmd)
	}
	return a, tea.Batch(cmds...)
}

// createNewEngine builds and registers a new engine via the registered factory.
// Switches to the new engine immediately so the user can start typing.
func (a *App) createNewEngine(name string, commitCmd tea.Cmd) tea.Cmd {
	if a.engineMgr == nil {
		return a.showInfo("Engine manager not initialized")
	}
	if a.engineFactory == nil {
		return a.showInfo("Engine factory not configured (multi-engine disabled)")
	}
	if name == "" {
		name = a.engineMgr.NewEngineName()
	}
	id := a.engineMgr.NewEngineID()

	model := ""
	if a.engine != nil {
		model = a.engine.Model()
	}
	eng, handler, err := a.engineFactory(id, name, model)
	if err != nil {
		return a.showInfo(fmt.Sprintf("Failed to create engine: %v", err))
	}
	if a.engine != nil && a.engine.HasStore() {
		// Mirror the active engine's store wiring so the new engine persists.
		eng.SetStore(a.engine.Store(), a.projectDir)
	}
	if err := eng.NewSession(a.projectDir, ""); err != nil {
		eng.Close()
		return a.showInfo(fmt.Sprintf("Failed to create session: %v", err))
	}
	vs := &engine.EngineViewState{
		Engine:          eng,
		Handler:         handler,
		Repl:            newReplAdapter(NewReplState()),
		ID:              id,
		Name:            name,
		ActiveSessionID: eng.SessionID(),
		Model:           eng.Model(),
	}
	a.engineMgr.Add(vs)
	// Handler is already subscribed to the engine's own Hub inside the
	// factory; vs.Handler is the single source of truth for switchEngine.

	slog.Info("engine: created", "id", id, "name", name)
	// Switch to the new engine immediately. switchEngine returns a
	// tea.ClearScreen batched with the "Switched to" info message —
	// return that so the TUI actually clears the previous engine's
	// scrollback instead of leaving it on screen.
	if _, swCmd := a.switchEngine(id); swCmd != nil {
		if commitCmd == nil {
			return swCmd
		}
		return tea.Batch(commitCmd, swCmd)
	}
	return commitCmd
}

// NewEngineHubWithHandler constructs an isolated Hub + TUIHandler pair for one
// engine. The handler is subscribed to the returned Hub so engine events flow
// to it. drainFn sets the handler's initial drain mode (nil = active mode).
//
// Exposed so main.go's bootstrap path and the test factory both build their
// Hub+handler through the same code — guarantees the per-engine isolation
// invariant (one Hub per engine, one subscribed handler per Hub).
func NewEngineHubWithHandler(engineID string, drainFn func(tea.Msg)) (*hub.Hub, *TUIHandler) {
	h := hub.NewHub()
	handler := NewTUIHandlerForEngine(engineID, drainFn)
	h.Subscribe(handler)
	return h, handler
}
