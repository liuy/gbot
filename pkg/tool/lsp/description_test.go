package lsptool

import "testing"

func TestFormatLspDescription(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Input
		want string
	}{
		// Position-based actions.
		{"definition file+symbol", Input{Action: "definition", File: "pkg/foo.go", Symbol: "Bar"}, "definition pkg/foo.go:Bar"},
		{"definition symbol only", Input{Action: "definition", Symbol: "Bar"}, "definition Bar"},
		{"definition file only", Input{Action: "definition", File: "pkg/foo.go"}, "definition pkg/foo.go"},
		{"definition nothing", Input{Action: "definition"}, "definition"},
		{"hover file+symbol", Input{Action: "hover", File: "pkg/foo.go", Symbol: "Bar#2"}, "hover pkg/foo.go:Bar#2"},
		{"references", Input{Action: "references", File: "a.go", Symbol: "Foo"}, "references a.go:Foo"},
		{"callers", Input{Action: "callers", Symbol: "Handle"}, "callers Handle"},
		{"impact", Input{Action: "impact", File: "a.go", Symbol: "X"}, "impact a.go:X"},

		// Rename.
		{"rename with both", Input{Action: "rename", Symbol: "Old", NewName: "New"}, "rename Old → New"},
		{"rename symbol only", Input{Action: "rename", Symbol: "Old"}, "rename Old"},
		{"rename new only", Input{Action: "rename", NewName: "New"}, "rename → New"},
		{"rename nothing", Input{Action: "rename"}, "rename"},

		// Rename file.
		{"rename_file both", Input{Action: "rename_file", Symbol: "old.go", NewName: "new.go"}, "rename_file old.go → new.go"},
		{"rename_file new only", Input{Action: "rename_file", NewName: "new.go"}, "rename_file → new.go"},

		// Symbols (file-scoped).
		{"symbols with file", Input{Action: "symbols", File: "pkg/foo.go"}, "symbols pkg/foo.go"},
		{"symbols no file", Input{Action: "symbols"}, "symbols"},

		// Workspace symbol.
		{"workspace_symbol with query", Input{Action: "workspace_symbol", Query: "Handler"}, "workspace_symbol Handler"},
		{"workspace_symbol empty", Input{Action: "workspace_symbol"}, "workspace_symbol"},

		// Request.
		{"request method", Input{Action: "request", Query: "textDocument/hover"}, "request textDocument/hover"},
		{"request no method", Input{Action: "request"}, "request"},

		// No-param actions.
		{"status", Input{Action: "status"}, "status"},
		{"capabilities", Input{Action: "capabilities"}, "capabilities"},
		{"reload", Input{Action: "reload"}, "reload"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := formatLspDescription(c.in); got != c.want {
				t.Errorf("formatLspDescription(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
