package tui

import "testing"

func TestLookupSlashCommand(t *testing.T) {
	tests := []struct {
		input    string
		wantOK   bool
		wantName string
		wantArgs string
	}{
		{"/switch", true, "switch", ""},
		{"/switch -n", true, "switch", "-n"},
		{"/switch -n title", true, "switch", "-n title"},
		{"/switch title", true, "switch", "title"},
		{"/switch   extra   spaces", true, "switch", "extra   spaces"},
		{"/clear", true, "clear", ""},
		{"/unknown", false, "", ""},
		{"hello", false, "", ""},
		{"not a command", false, "", ""},
		{"", false, "", ""},
		{"  /switch  -n test  ", true, "switch", "-n test"},
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
