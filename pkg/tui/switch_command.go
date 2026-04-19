package tui

import (
	"fmt"
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"time"

	"github.com/liuy/gbot/pkg/memory/short"
)

// handleSwitch implements the /switch command.
//
//	/switch          → show session picker (US-009)
//	/switch -n       → create new empty session
//	/switch -n title → create new session with title
//	/switch title    → fork current session and switch to fork
func (a *App) handleSwitch(args string, commitCmd tea.Cmd) tea.Cmd {
	// Guard: no switching while streaming
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot switch sessions while streaming")
	}

	// Guard: no store
	if a.store == nil {
		return a.showInfo("Session storage not available")
	}

	// Parse args
	if args == "" {
		// /switch with no args → open session picker
		return a.openPicker(commitCmd)
		}
	if args == "-n" {
		return a.createNewSession("", commitCmd)
	}

	if strings.HasPrefix(args, "-n ") {
		title := strings.TrimSpace(args[3:])
		if title == "" {
			return a.showInfo("Title cannot be empty. Usage: /switch -n <title>")
		}
		return a.createNewSession(title, commitCmd)
	}

	// Otherwise, treat as title → fork current session
	return a.forkCurrentSession(args, commitCmd)
}

// createNewSession creates a new empty session and switches to it.
func (a *App) createNewSession(title string, commitCmd tea.Cmd) tea.Cmd {
	session, err := a.store.CreateSession("", a.engine.Model())
	if err != nil {
		slog.Error("switch: create session failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to create session: %v", err))
	}

	if title != "" {
		if err := a.store.UpdateSessionTitle(session.SessionID, title); err != nil {
			slog.Error("switch: set title failed", "error", err)
		}
	}

	// Reset engine state for new session
	a.engine.Reset()
	a.engine.SetSessionID(session.SessionID)
	a.sessionID = session.SessionID
	a.lastPersistedIdx = 0

	// Reset REPL state
	*a.repl = *NewReplState()
	a.committedCount = 0

	// Update workspace meta
	if err := WriteWorkspaceMeta(a.sessionID); err != nil {
		slog.Warn("switch: write workspace meta failed", "error", err)
	}

	displayTitle := title
	if displayTitle == "" {
		displayTitle = session.SessionID[:8]
	}
	slog.Info("switch: created new session", "sessionID", session.SessionID, "title", title)

	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to new session: %s", displayTitle)))
}

// forkCurrentSession forks the current session with the given title.
func (a *App) forkCurrentSession(title string, commitCmd tea.Cmd) tea.Cmd {
	if a.sessionID == "" {
		return a.showInfo("No active session to fork")
	}

	// Duplicate title detection
	sessions, err := a.store.ListSessions("", 1000)
	if err != nil {
		slog.Error("switch: list sessions failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to check titles: %v", err))
	}
	for _, s := range sessions {
		if s.Title == title {
			return a.showInfo(fmt.Sprintf("Session with title %q already exists", title))
		}
	}

	// Fork: forkPointSeq=0 means fork all messages
	forked, err := a.store.ForkSession(a.sessionID, 0, "")
	if err != nil {
		slog.Error("switch: fork session failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to fork session: %v", err))
	}

	// Set title on forked session
	if err := a.store.UpdateSessionTitle(forked.SessionID, title); err != nil {
		slog.Error("switch: set fork title failed", "error", err)
	}

	// Load and convert forked messages
	engineMsgs, err := loadAndConvertMessages(a.store, forked.SessionID)
	if err != nil {
		slog.Error("switch: load/convert forked messages failed", "error", err)
		return a.showInfo(fmt.Sprintf("Failed to load forked messages: %v", err))
	}

	// Update engine state
	a.engine.SetMessages(engineMsgs)
	a.engine.SetSessionID(forked.SessionID)
	parentID := a.sessionID
	a.sessionID = forked.SessionID
	a.lastPersistedIdx = len(engineMsgs)

	// Reset REPL state
	*a.repl = *NewReplState()
	a.committedCount = 0

	// Update workspace meta
	if err := WriteWorkspaceMeta(a.sessionID); err != nil {
		slog.Warn("switch: write workspace meta failed", "error", err)
	}

	slog.Info("switch: forked session", "parent", parentID, "child", forked.SessionID, "title", title)

	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Forked session: %s", title)))
}

// showInfo returns a tea.Cmd that displays a transient info message.
func (a *App) showInfo(msg string) tea.Cmd {
	return func() tea.Msg {
		return infoMsg(msg)
	}
}

// WriteWorkspaceMeta updates .gbot/meta.json with the current session ID.
func WriteWorkspaceMeta(sessionID string) error {
	meta := &short.WorkspaceMeta{
		CurrentSessionID: sessionID,
		LastActiveAt:     time.Now(),
	}
	return short.WriteWorkspaceMeta(".", meta)
}
