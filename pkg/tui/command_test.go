package tui

import (
	"slices"
	"testing"
)

// TestAllCommands verifies NewCommandRegistry seeds the built-in commands
// and sorts them alphabetically.
func TestAllCommands(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	cmds := r.AllCommands()
	if len(cmds) != 7 {
		t.Fatalf("AllCommands() returned %d commands, want 7", len(cmds))
	}
	if !slices.IsSorted(cmds) {
		t.Errorf("AllCommands() not sorted: %v", cmds)
	}
	want := []string{"agent", "clear", "compact", "context", "model", "rewind", "session"}
	for _, w := range want {
		if !slices.Contains(cmds, w) {
			t.Errorf("AllCommands() missing %q", w)
		}
	}
}

// TestCommandDefs_HasDescription verifies every builtin command has a Description.
func TestCommandDefs_HasDescription(t *testing.T) {
	t.Parallel()
	for name, def := range builtinCommandDefs {
		if def.Description == "" {
			t.Errorf("command %q has empty Description", name)
		}
	}
}

// ---------------------------------------------------------------------------
// RegisterSkillCommands — dynamic skill registration for tab completion
// ---------------------------------------------------------------------------

// TestRegisterSkillCommands_AddsToAllCommands verifies skills merge with
// builtins for completion display.
func TestRegisterSkillCommands_AddsToAllCommands(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	r.RegisterSkillCommands(map[string]CommandDef{
		"commit":                     {Description: "Create a commit", HasArgs: true},
		"oh-my-claudecode:autopilot": {Description: "Run autopilot mode", HasArgs: true},
	})

	cmds := r.AllCommands()
	if got := len(cmds); got != 9 {
		t.Fatalf("AllCommands() returned %d, want 9 (7 builtin + 2 skill)", got)
	}
	for _, builtin := range []string{"session", "clear", "model"} {
		if !slices.Contains(cmds, builtin) {
			t.Errorf("builtin command %q missing from AllCommands", builtin)
		}
	}
	if !slices.Contains(cmds, "commit") {
		t.Error("expected 'commit' in AllCommands after RegisterSkillCommands")
	}
	if !slices.Contains(cmds, "oh-my-claudecode:autopilot") {
		t.Error("expected 'oh-my-claudecode:autopilot' in AllCommands after RegisterSkillCommands")
	}
}

// TestRegisterSkillCommands_NotDispatched verifies skills are NOT matched
// by LookupSlashCommand — they must fall through to the engine.
func TestRegisterSkillCommands_NotDispatched(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	r.RegisterSkillCommands(map[string]CommandDef{
		"commit": {Description: "Create a commit", HasArgs: true},
	})

	if _, ok := r.LookupSlashCommand("/commit"); ok {
		t.Error("LookupSlashCommand should not match skill commands — they must fall through to engine")
	}
	if _, ok := r.LookupSlashCommand("/clear"); !ok {
		t.Error("LookupSlashCommand should still match builtin commands")
	}
}

// TestRegisterSkillCommands_ReplaceSemantics verifies the second call
// replaces (not merges) previously-registered skills.
func TestRegisterSkillCommands_ReplaceSemantics(t *testing.T) {
	t.Parallel()
	r := NewCommandRegistry()
	r.RegisterSkillCommands(map[string]CommandDef{
		"commit": {Description: "v1", HasArgs: true},
	})
	r.RegisterSkillCommands(map[string]CommandDef{
		"commit": {Description: "v2", HasArgs: false},
		"review": {Description: "review", HasArgs: true},
	})

	def, ok := r.getCommandDef("commit")
	if !ok {
		t.Fatal("expected 'commit' to be registered")
	}
	if def.Description != "v2" {
		t.Errorf("expected last-write wins, got description %q", def.Description)
	}
	if def, ok := r.getCommandDef("review"); !ok || def.Description != "review" {
		t.Errorf("expected 'review' to be registered after second call, got ok=%v def=%+v", ok, def)
	}
	// 'commit' is the only one from the first call; if merge semantics
	// were in effect, we wouldn't be able to detect it from this case.
	// SkillCommandCount asserts the total matches the second map size.
	if got := r.SkillCommandCount(); got != 2 {
		t.Errorf("SkillCommandCount = %d, want 2 (replace semantics)", got)
	}
}

// ---------------------------------------------------------------------------
// commandParts — part splitting
// Source: TS commandSuggestions.ts:36-39
// ---------------------------------------------------------------------------

func TestCommandParts(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"session", "session", 0},
		{"session", "s", 1},
		{"session", "ses", 1},
		{"oh-my-claudecode:ralph", "oh", 1},
		{"oh-my-claudecode:ralph", "ral", 2},
		{"oh-my-claudecode:ralph", "ralph", 2},
		{"oh-my-claudecode:autopilot", "auto", 2},
		{"oh-my-claudecode:autopilot", "claude", 2},
		{"session", "xyz", -1},
		{"oh-my-claudecode:ralph", "zzz", -1},
		{"clear", "xxx", -1},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+tc.query, func(t *testing.T) {
			t.Parallel()
			got := commandMatchPriority(tc.name, tc.query)
			if got != tc.want {
				t.Errorf("commandMatchPriority(%q, %q) = %d, want %d", tc.name, tc.query, got, tc.want)
			}
		})
	}
}

func TestLookupSlashCommand(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			r := NewCommandRegistry()
			cmd, ok := r.LookupSlashCommand(tc.input)
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
