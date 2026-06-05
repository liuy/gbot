package tui

import (
	"slices"
	"testing"
)

func TestAllCommands(t *testing.T) {
	cmds := AllCommands()
	if len(cmds) != 5 {
		t.Fatalf("AllCommands() returned %d commands, want 5", len(cmds))
	}
	// Must be sorted alphabetically
	if !slices.IsSorted(cmds) {
		t.Errorf("AllCommands() not sorted: %v", cmds)
	}
	// Must contain all known commands
	want := []string{"clear", "context", "model", "rewind", "session"}
	for _, w := range want {
		if !slices.Contains(cmds, w) {
			t.Errorf("AllCommands() missing %q", w)
		}
	}
}

func TestCommandDefs_HasDescription(t *testing.T) {
	for name, def := range commandDefs {
		if def.Description == "" {
			t.Errorf("command %q has empty Description", name)
		}
	}
}

// ---------------------------------------------------------------------------
// RegisterSlashCommands — dynamic skill registration for tab completion
// ---------------------------------------------------------------------------

func TestRegisterSlashCommands_AddsToAllCommands(t *testing.T) {
	defer func() { skillDefs = nil; sortedCommands = AllCommands() }()
	RegisterSlashCommands(map[string]CommandDef{
		"commit":                     {Description: "Create a commit", HasArgs: true},
		"oh-my-claudecode:autopilot": {Description: "Run autopilot mode", HasArgs: true},
	})

	cmds := AllCommands()

	// Builtin commands must still be present
	for _, builtin := range []string{"session", "clear", "model"} {
		if !slices.Contains(cmds, builtin) {
			t.Errorf("builtin command %q missing from AllCommands", builtin)
		}
	}

	// Plugin skills must appear
	if !slices.Contains(cmds, "commit") {
		t.Error("expected 'commit' in AllCommands after RegisterSlashCommands")
	}
	if !slices.Contains(cmds, "oh-my-claudecode:autopilot") {
		t.Error("expected 'oh-my-claudecode:autopilot' in AllCommands after RegisterSlashCommands")
	}
}

func TestRegisterSlashCommands_NotDispatched(t *testing.T) {
	defer func() { skillDefs = nil; sortedCommands = AllCommands() }()
	// Skills registered for completion must NOT be dispatched by LookupSlashCommand.
	// They should fall through to the engine as regular user messages.
	RegisterSlashCommands(map[string]CommandDef{
		"commit": {Description: "Create a commit", HasArgs: true},
	})

	_, ok := LookupSlashCommand("/commit")
	if ok {
		t.Error("LookupSlashCommand should not match registered skill commands — they must fall through to engine")
	}

	// Builtins still work
	_, ok = LookupSlashCommand("/clear")
	if !ok {
		t.Error("LookupSlashCommand should still match builtin commands")
	}
}

func TestRegisterSlashCommands_Idempotent(t *testing.T) {
	defer func() { skillDefs = nil; sortedCommands = AllCommands() }()
	RegisterSlashCommands(map[string]CommandDef{
		"commit": {Description: "v1", HasArgs: true},
	})
	RegisterSlashCommands(map[string]CommandDef{
		"commit": {Description: "v2", HasArgs: false},
	})

	def, ok := getCommandDef("commit")
	if !ok {
		t.Fatal("expected 'commit' to be registered")
	}
	if def.Description != "v2" {
		t.Errorf("expected last-write wins, got description %q", def.Description)
	}
}

// ---------------------------------------------------------------------------
// commandParts — part splitting
// Source: TS commandSuggestions.ts:36-39
// ---------------------------------------------------------------------------

func TestCommandParts(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{"oh-my-claudecode:ralph", []string{"oh", "my", "claudecode", "ralph"}},
		{"session", []string{"session"}},
		{"foo_bar:baz-qux", []string{"foo", "bar", "baz", "qux"}},
		{"a:b:c", []string{"a", "b", "c"}},
		{"", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := commandParts(tc.name)
			if len(got) != len(tc.want) {
				t.Fatalf("commandParts(%q) = %v, want %v", tc.name, got, tc.want)
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Errorf("commandParts(%q)[%d] = %q, want %q", tc.name, i, v, tc.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// commandMatchPriority — priority-based matching
// Source: TS commandSuggestions.ts:406-497
// ---------------------------------------------------------------------------

func TestCommandMatchPriority(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		// Exact match
		{"session", "session", 0},
		// Full prefix match
		{"session", "s", 1},
		{"session", "ses", 1},
		{"oh-my-claudecode:ralph", "oh", 1},
		// Part prefix match
		{"oh-my-claudecode:ralph", "ral", 2},
		{"oh-my-claudecode:ralph", "ralph", 2},
		{"oh-my-claudecode:autopilot", "auto", 2},
		{"oh-my-claudecode:autopilot", "claude", 2},
		// No match
		{"session", "xyz", -1},
		{"oh-my-claudecode:ralph", "zzz", -1},
		{"clear", "xxx", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+tc.query, func(t *testing.T) {
			got := commandMatchPriority(tc.name, tc.query)
			if got != tc.want {
				t.Errorf("commandMatchPriority(%q, %q) = %d, want %d", tc.name, tc.query, got, tc.want)
			}
		})
	}
}

func TestLookupSlashCommand(t *testing.T) {
	tests := []struct {
		input    string
		wantOK   bool
		wantName string
		wantArgs string
	}{
		{"/session", true, "session", ""},
		{"/session -n", true, "session", "-n"},
		{"/session -n title", true, "session", "-n title"},
		{"/session title", true, "session", "title"},
		{"/session   extra   spaces", true, "session", "extra   spaces"},
		{"/clear", true, "clear", ""},
		{"/unknown", false, "", ""},
		{"hello", false, "", ""},
		{"not a command", false, "", ""},
		{"", false, "", ""},
		{"  /session  -n test  ", true, "session", "-n test"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			cmd, ok := LookupSlashCommand(tc.input)
			if ok != tc.wantOK {
				t.Errorf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				if cmd.Name != tc.wantName {
					t.Errorf("Name = %q, want %q", cmd.Name, tc.wantName)
				}
				if cmd.Args != tc.wantArgs {
					t.Errorf("Args = %q, want %q", cmd.Args, tc.wantArgs)
				}
			}
		})
	}
}
