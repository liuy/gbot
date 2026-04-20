package tui

import (
	"log/slog"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// SlashCommand represents a parsed slash command from user input.
type SlashCommand struct {
	Name string // e.g. "switch"
	Args string // everything after the command name, e.g. "-n title"
}

// commandDefs maps slash command names to their definitions.
var commandDefs = map[string]struct{}{
	"switch": {},
	"clear":  {},
}

// LookupSlashCommand checks if the input text is a slash command.
// Returns the parsed command and true, or false if not a slash command.
func LookupSlashCommand(text string) (SlashCommand, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return SlashCommand{}, false
	}

	// Split into command name and remaining arg text
	spaceIdx := strings.Index(trimmed[1:], " ")
	if spaceIdx == -1 {
		name := trimmed[1:]
		if _, ok := commandDefs[name]; !ok {
			return SlashCommand{}, false
		}
		return SlashCommand{Name: name, Args: ""}, true
	}

	name := trimmed[1 : 1+spaceIdx]
	if _, ok := commandDefs[name]; !ok {
		return SlashCommand{}, false
	}

	args := strings.TrimSpace(trimmed[1+spaceIdx:])
	return SlashCommand{Name: name, Args: args}, true
}

// handleSlashCommand dispatches a slash command to the appropriate handler.
// Returns a tea.Cmd that may include the commitCmd for scrollback.
func (a *App) handleSlashCommand(cmd SlashCommand, commitCmd tea.Cmd) tea.Cmd {
	slog.Info("tui:slash_command", "name", cmd.Name, "args", cmd.Args)

	switch cmd.Name {
	case "switch":
		return a.handleSwitch(cmd.Args, commitCmd)
	case "clear":
		return a.handleClear(commitCmd)
	default:
		slog.Warn("tui:unknown slash command", "name", cmd.Name)
		return commitCmd
	}
}

// handleClear implements the /clear command.
// Source: TS src/commands/clear/clear.ts — clearConversation
func (a *App) handleClear(commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot clear while streaming")
	}
	if a.store == nil {
		return a.showInfo("Session storage not available")
	}
	return a.createNewSession("", "Cleared", commitCmd)
}
