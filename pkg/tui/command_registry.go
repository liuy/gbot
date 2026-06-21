package tui

import (
	"maps"
	"slices"
	"strings"
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

// builtinCommandDefs maps built-in slash command names to their definitions.
// Only builtins are dispatched through (*App).handleSlashCommand.
var builtinCommandDefs = map[string]CommandDef{
	"session": {Description: "Switch or manage sessions", HasArgs: true},
	"clear":   {Description: "Clear conversation", HasArgs: false},
	"model":   {Description: "Switch model", HasArgs: true},
	"rewind":  {Description: "Restore conversation to a previous point", HasArgs: false},
	"context": {Description: "Visualize context window usage (args: dump)", HasArgs: true},
	"agent":   {Description: "Switch or create agents", HasArgs: true},
}

// CommandRegistry holds the per-App slash command tables.
//
// builtinDefs are the fixed built-in commands (/clear, /model, etc.).
// skillDefs are plugin/user skill commands shown in the completion
// dropdown but not dispatched by handleSlashCommand — they fall through
// to the engine as regular user messages.
//
// Each App owns its own CommandRegistry, so tests can run in parallel
// without polluting each other's state.
type CommandRegistry struct {
	builtinDefs    map[string]CommandDef
	skillDefs      map[string]CommandDef
	sortedCommands []string
}

// NewCommandRegistry creates a registry seeded with the built-in commands.
func NewCommandRegistry() *CommandRegistry {
	return NewCommandRegistryWithBuiltins(builtinCommandDefs)
}

// NewCommandRegistryWithBuiltins creates a registry with caller-supplied
// builtin command definitions. Used by tests that need to isolate the
// command table from the production builtins.
func NewCommandRegistryWithBuiltins(builtins map[string]CommandDef) *CommandRegistry {
	r := &CommandRegistry{
		builtinDefs: make(map[string]CommandDef, len(builtins)),
	}
	maps.Copy(r.builtinDefs, builtins)
	r.rebuildSorted()
	return r
}

// RegisterSkillCommands replaces the registered skill commands.
// The supplied map replaces any previously-registered skills — callers
// must pass the full current set, not an incremental delta.
func (r *CommandRegistry) RegisterSkillCommands(cmds map[string]CommandDef) {
	r.skillDefs = cmds
	r.rebuildSorted()
}

// SkillCommandCount returns the number of registered skill commands.
// Used by tests to assert clean state.
func (r *CommandRegistry) SkillCommandCount() int {
	return len(r.skillDefs)
}

// getCommandDef returns the CommandDef for a name, checking builtins then skills.
func (r *CommandRegistry) getCommandDef(name string) (CommandDef, bool) {
	if def, ok := r.builtinDefs[name]; ok {
		return def, true
	}
	if r.skillDefs != nil {
		if def, ok := r.skillDefs[name]; ok {
			return def, true
		}
	}
	return CommandDef{}, false
}

// AllCommands returns all command names (builtins + skills) sorted alphabetically.
func (r *CommandRegistry) AllCommands() []string {
	return slices.Clone(r.sortedCommands)
}

// LookupSlashCommand checks if the input text is a builtin slash command.
// Returns the parsed command and true, or false if not a builtin slash command.
func (r *CommandRegistry) LookupSlashCommand(text string) (SlashCommand, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return SlashCommand{}, false
	}

	spaceIdx := strings.Index(trimmed[1:], " ")
	if spaceIdx == -1 {
		name := trimmed[1:]
		if _, ok := r.builtinDefs[name]; !ok {
			return SlashCommand{}, false
		}
		return SlashCommand{Name: name, Args: ""}, true
	}

	name := trimmed[1 : 1+spaceIdx]
	if _, ok := r.builtinDefs[name]; !ok {
		return SlashCommand{}, false
	}

	args := strings.TrimSpace(trimmed[1+spaceIdx:])
	return SlashCommand{Name: name, Args: args}, true
}

// LookupSkillCommand checks if text is a registered skill slash command
// (user/plugin skill, not builtin). Returns (name, args, true) if matched.
func (r *CommandRegistry) LookupSkillCommand(text string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	rest := trimmed[1:]
	spaceIdx := strings.Index(rest, " ")
	if spaceIdx == -1 {
		name = rest
		args = ""
	} else {
		name = rest[:spaceIdx]
		args = strings.TrimSpace(rest[spaceIdx:])
	}
	if _, isBuiltin := r.builtinDefs[name]; isBuiltin {
		return "", "", false
	}
	if r.skillDefs == nil {
		return "", "", false
	}
	if _, exists := r.skillDefs[name]; !exists {
		return "", "", false
	}
	return name, args, true
}

// rebuildSorted recomputes the sorted command name list.
// Called whenever builtinDefs or skillDefs changes.
func (r *CommandRegistry) rebuildSorted() {
	names := make([]string, 0, len(r.builtinDefs)+len(r.skillDefs))
	for name := range r.builtinDefs {
		names = append(names, name)
	}
	for name := range r.skillDefs {
		names = append(names, name)
	}
	slices.Sort(names)
	r.sortedCommands = names
}
