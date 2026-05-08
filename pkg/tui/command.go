package tui

import (
	"log/slog"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// SlashCommand represents a parsed slash command from user input.
type SlashCommand struct {
	Name string // e.g. "session"
	Args string // everything after the command name, e.g. "-n title"
}

// CommandDef describes a slash command for both lookup and tab completion.
type CommandDef struct {
	Description string // shown in suggestion dropdown
	HasArgs     bool   // true = command takes arguments (Enter doesn't auto-execute)
}

// commandDefs maps built-in slash command names to their definitions.
// Only builtins are dispatched through handleSlashCommand.
var commandDefs = map[string]CommandDef{
	"session": {Description: "Switch or manage sessions", HasArgs: true},
	"clear":   {Description: "Clear conversation", HasArgs: false},
	"model":   {Description: "Switch model", HasArgs: true},
}

// skillDefs maps plugin/user skill names for completion display only.
// These appear in the / dropdown but are NOT dispatched through handleSlashCommand.
// Instead, they fall through to the engine as regular user messages.
var skillDefs map[string]CommandDef

// sortedCommands is pre-sorted at init time to avoid re-sorting on every Update().
var sortedCommands []string

func init() {
	sortedCommands = AllCommands()
}

// RegisterSlashCommands adds skill commands for tab completion.
// These appear in the / dropdown but fall through to the engine (not handleSlashCommand).
func RegisterSlashCommands(cmds map[string]CommandDef) {
	if skillDefs == nil {
		skillDefs = make(map[string]CommandDef, len(cmds))
	}
	maps.Copy(skillDefs, cmds)
	sortedCommands = AllCommands()
}

// ResetSlashCommands clears registered skill commands (for testing).
func ResetSlashCommands() {
	skillDefs = nil
	sortedCommands = AllCommands()
}

// getCommandDef returns the CommandDef for a name, checking builtins then skills.
func getCommandDef(name string) (CommandDef, bool) {
	if def, ok := commandDefs[name]; ok {
		return def, true
	}
	if skillDefs != nil {
		if def, ok := skillDefs[name]; ok {
			return def, true
		}
	}
	return CommandDef{}, false
}

// AllCommands returns all command names sorted alphabetically.
func AllCommands() []string {
	names := make([]string, 0, len(commandDefs)+len(skillDefs))
	for name := range commandDefs {
		names = append(names, name)
	}
	for name := range skillDefs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
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
	case "session":
		return a.handleSession(cmd.Args, commitCmd)
	case "clear":
		return a.handleClear(commitCmd)
	case "model":
		return a.handleModel(cmd.Args, commitCmd)
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
