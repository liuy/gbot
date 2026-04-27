package tui

import (
	"slices"
	"testing"
)

func TestAllCommands(t *testing.T) {
	cmds := AllCommands()
	if len(cmds) != 3 {
		t.Fatalf("AllCommands() returned %d commands, want 3", len(cmds))
	}
	// Must be sorted alphabetically
	if !slices.IsSorted(cmds) {
		t.Errorf("AllCommands() not sorted: %v", cmds)
	}
	// Must contain all known commands
	want := []string{"clear", "model", "session"}
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
