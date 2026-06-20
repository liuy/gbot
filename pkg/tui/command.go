package tui

import (
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// handleSlashCommand dispatches a slash command to the appropriate handler.
// Returns a tea.Cmd that may include the commitCmd for scrollback.
func (a *App) handleSlashCommand(cmd SlashCommand, commitCmd tea.Cmd) tea.Cmd {
	slog.Info("tui:slash_command", "name", cmd.Name, "args", cmd.Args)

	var resultCmd tea.Cmd
	switch cmd.Name {
	case "session":
		resultCmd = a.handleSession(cmd.Args, commitCmd)
	case "clear":
		resultCmd = a.handleClear(commitCmd)
	case "model":
		resultCmd = a.handleModel(cmd.Args, commitCmd)
	case "rewind":
		resultCmd = a.handleRewind(commitCmd)
	case "context":
		resultCmd = a.handleContext(cmd.Args, commitCmd)
	case "engine":
		resultCmd = a.handleEngine(cmd.Args, commitCmd)
	default:
		slog.Warn("tui:unknown slash command", "name", cmd.Name)
		resultCmd = commitCmd
	}
	a.restoreStash()
	return resultCmd
}

// handleClear implements the /clear command.
// Source: TS src/commands/clear/clear.ts — clearConversation

// commandParts splits a command name on :, -, _ for part-based matching.
// Source: TS commandSuggestions.ts:36-39
func commandParts(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return r == ':' || r == '-' || r == '_'
	})
}

// commandMatchPriority returns match quality (lower = better): 0=exact, 1=full prefix, 2=part prefix, -1=none.
// Source: TS commandSuggestions.ts:406-497
func commandMatchPriority(name, query string) int {
	if name == query {
		return 0
	}
	if strings.HasPrefix(name, query) {
		return 1
	}
	for _, part := range commandParts(name) {
		if strings.HasPrefix(part, query) {
			return 2
		}
	}
	return -1
}

func (a *App) handleClear(commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot clear while streaming")
	}
	if !a.engine.HasStore() {
		return a.showInfo("Session storage not available")
	}
	// Discard commitCmd (tea.Println of old messages from handleSubmitRepl).
	// /clear should wipe everything — passing commitCmd would race with
	// ClearScreen in tea.Batch and re-print old content after the clear.
	return a.createNewSession("", "Cleared", nil)
}
