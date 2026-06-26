package tui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/llm"
)

// handleSession implements the /session command.
//
//	/session          → show session picker (US-009)
//	/session -n       → create new empty session
//	/session -n title → create new session with title
//	/session title    → fork current session and switch to fork
func (a *App) handleSession(args string, commitCmd tea.Cmd) tea.Cmd {
	// Guard: no switching while streaming
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot switch session while streaming")
	}

	// Guard: no store
	if !a.engine.HasStore() {
		return a.showInfo("Session storage not available")
	}

	// Parse args
	if args == "" {
		// /session with no args → open session picker
		return a.openPicker(commitCmd)
	}
	if args == "-n" {
		return a.createNewSession("", "Switched to", commitCmd)
	}

	if strings.HasPrefix(args, "-n ") {
		title := strings.TrimSpace(args[3:])
		if title == "" {
			return a.showInfo("Title cannot be empty. Usage: /session -n <title>")
		}
		return a.createNewSession(title, "Switched to", commitCmd)
	}

	// Otherwise, treat as title → fork current session
	return a.forkCurrentSession(args, commitCmd)
}

// createNewSession creates a new empty session and switches to it.
func (a *App) createNewSession(title, verb string, commitCmd tea.Cmd) tea.Cmd {
	if err := a.engine.NewSession(a.projectDir, title); err != nil {
		slog.Error("session: create session failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to create session: %v", err))
	}

	a.sessionID = a.engine.SessionID()
	if a.engineMgr != nil {
		if vs := a.engineMgr.Active(); vs != nil {
			vs.ActiveSessionID = a.sessionID
		}
	}

	// Reset REPL state
	a.repl.Reset()
	a.repl.committedCount = 0

	// Reset prompt cache break detection (main thread only, preserve sub-agent state)
	llm.ResetMainThreadCacheBreakDetection()

	// Reset all display state (token counters, scroll, thinking, etc.)
	a.resetDisplayState()

	// Close old idleStop goroutine to prevent leak
	if a.idleStop != nil {
		close(a.idleStop)
	}
	a.idleStop = make(chan struct{})

	// Update workspace meta
	if err := a.persistWorkspaceMeta(); err != nil {
		slog.Warn("session: write workspace meta failed", "error", err)
	}

	displayTitle := title
	if displayTitle == "" {
		displayTitle = a.sessionID[:8]
	}
	slog.Info("session: created new session", "sessionID", a.sessionID, "title", title)

	return tea.Batch(commitCmd, tea.ClearScreen, a.showInfo(fmt.Sprintf("%s new session: %s", verb, displayTitle)), a.readEvents())
}

// forkCurrentSession forks the current session with the given title.
func (a *App) forkCurrentSession(title string, commitCmd tea.Cmd) tea.Cmd {
	if a.sessionID == "" {
		return a.showInfo("No active session to fork")
	}

	// Duplicate title detection
	sessions, err := a.engine.ListSessions(1000)
	if err != nil {
		slog.Error("session: list sessions failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to check titles: %v", err))
	}
	for _, s := range sessions {
		if s.Title == title {
			return a.showInfo(fmt.Sprintf("Session with title %q already exists", title))
		}
	}

	engineMsgs, err := a.engine.ForkSession(title)
	if err != nil {
		slog.Error("session: fork session failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to fork session: %v", err))
	}

	parentID := a.sessionID
	a.sessionID = a.engine.SessionID()
	if a.engineMgr != nil {
		if vs := a.engineMgr.Active(); vs != nil {
			vs.ActiveSessionID = a.sessionID
		}
	}

	a.repl.Reset()
	a.repl.messages = engineMessagesToViews(engineMsgs, a.engine.AllTools())
	// committedCount=0 so WindowSizeMsg re-commits the forked messages
	a.repl.committedCount = 0
	a.resetDisplayState()

	// Update workspace meta
	if err := a.persistWorkspaceMeta(); err != nil {
		slog.Warn("session: write workspace meta failed", "error", err)
	}

	slog.Info("session: forked session", "parent", parentID, "child", a.sessionID, "title", title)

	return tea.Batch(commitCmd, tea.ClearScreen, a.showInfo(fmt.Sprintf("Forked session: %s", title)), a.readEvents())
}

// showInfo returns a tea.Cmd that displays a transient info message.
func (a *App) showInfo(msg string) tea.Cmd {
	return func() tea.Msg {
		return infoMsg(msg)
	}
}

// persistWorkspaceMeta writes .gbot/meta.json reflecting the current state of
// all engines. No-op when a.projectDir == "" (tests). Serialized via
// EngineManager.mu — safe to call from App goroutine and from main.go during
// bootstrap (manager has no background writers at that point).
func (a *App) persistWorkspaceMeta() error {
	if a.projectDir == "" {
		return nil
	}
	if a.engineMgr == nil {
		return nil
	}
	return a.engineMgr.PersistMeta(a.projectDir)
}
