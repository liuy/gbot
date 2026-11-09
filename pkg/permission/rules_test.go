package permission

import (
	"testing"
)

func ptr(s string) *string { return new(s) }

func TestParseRuleValue(t *testing.T) {
	tests := []struct {
		input string
		want  RuleValue
	}{
		// Bare tool name — no parentheses
		{input: "Bash", want: RuleValue{ToolName: "Bash", RuleContent: nil}},
		// Content-specific rule
		{input: "Bash(npm install)", want: RuleValue{ToolName: "Bash", RuleContent: ptr("npm install")}},
		// Wildcard content = bare tool name (matches all)
		{input: "Bash(*)", want: RuleValue{ToolName: "Bash", RuleContent: nil}},
		// Empty content = bare tool name (matches all)
		{input: "Bash()", want: RuleValue{ToolName: "Bash", RuleContent: nil}},
		// Escaped parentheses in content — TS: permissionRuleParser.ts:91
		{input: `Bash(python -c "print\(1\)")`, want: RuleValue{ToolName: "Bash", RuleContent: ptr(`python -c "print(1)"`)}},
		// Malformed: no closing paren — whole string is tool name
		{input: "(foo)", want: RuleValue{ToolName: "(foo)", RuleContent: nil}},
		// Malformed: content after closing paren
		{input: "Bash(foo)bar", want: RuleValue{ToolName: "Bash(foo)bar", RuleContent: nil}},
		// Malformed: empty toolName before paren
		{input: "(foo)", want: RuleValue{ToolName: "(foo)", RuleContent: nil}},
		// Multiple args
		{input: "Bash(git push origin main)", want: RuleValue{ToolName: "Bash", RuleContent: ptr("git push origin main")}},
		// Escaped backslash in content
		{input: `Bash(echo \\)`, want: RuleValue{ToolName: "Bash", RuleContent: ptr(`echo \`)}},
		// Write tool with file path
		{input: "Write(.env)", want: RuleValue{ToolName: "Write", RuleContent: ptr(".env")}},
		// Read tool bare
		{input: "Read", want: RuleValue{ToolName: "Read", RuleContent: nil}},
		// MCP tool bare
		{input: "mcp__server", want: RuleValue{ToolName: "mcp__server", RuleContent: nil}},
		// MCP tool with wildcard content
		{input: "mcp__server__*", want: RuleValue{ToolName: "mcp__server__*", RuleContent: nil}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseRuleValue(tt.input)
			if got.ToolName != tt.want.ToolName {
				t.Errorf("ToolName: got %q, want %q", got.ToolName, tt.want.ToolName)
			}
			if (got.RuleContent == nil) != (tt.want.RuleContent == nil) {
				t.Errorf("RuleContent nil mismatch: got nil=%v, want nil=%v", got.RuleContent == nil, tt.want.RuleContent == nil)
			}
			if got.RuleContent != nil && tt.want.RuleContent != nil {
				if *got.RuleContent != *tt.want.RuleContent {
					t.Errorf("RuleContent: got %q, want %q", *got.RuleContent, *tt.want.RuleContent)
				}
			}
		})
	}
}

func TestRuleValueRoundtrip(t *testing.T) {
	tests := []RuleValue{
		{ToolName: "Bash", RuleContent: nil},
		{ToolName: "Bash", RuleContent: ptr("npm install")},
		{ToolName: "Bash", RuleContent: ptr(`python -c "print(1)"`)},
		{ToolName: "Bash", RuleContent: ptr(`echo \`)},
		{ToolName: "Read", RuleContent: nil},
		{ToolName: "Write", RuleContent: ptr(".env")},
	}
	for _, rv := range tests {
		t.Run(RuleValueToString(rv), func(t *testing.T) {
			s := RuleValueToString(rv)
			parsed := ParseRuleValue(s)
			if parsed.ToolName != rv.ToolName {
				t.Errorf("ToolName: got %q, want %q", parsed.ToolName, rv.ToolName)
			}
			if (parsed.RuleContent == nil) != (rv.RuleContent == nil) {
				t.Fatalf("RuleContent nil mismatch")
			}
			if parsed.RuleContent != nil && rv.RuleContent != nil {
				if *parsed.RuleContent != *rv.RuleContent {
					t.Errorf("RuleContent: got %q, want %q", *parsed.RuleContent, *rv.RuleContent)
				}
			}
		})
	}
}

func TestEscapeRuleContent(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// TS: permissionRuleParser.ts:52-53
		{input: `psycopg2.connect()`, want: `psycopg2.connect\(\)`},
		{input: `echo "test\nvalue"`, want: `echo "test\\nvalue"`},
		{input: `simple`, want: `simple`},
		{input: ``, want: ``},
		{input: `back\slash`, want: `back\\slash`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := EscapeRuleContent(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnescapeRuleContent(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// TS: permissionRuleParser.ts:72-73
		{input: `psycopg2.connect\(\)`, want: `psycopg2.connect()`},
		{input: `echo "test\\nvalue"`, want: `echo "test\nvalue"`},
		{input: `simple`, want: `simple`},
		{input: ``, want: ``},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := UnescapeRuleContent(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEscapeUnescapeRoundtrip(t *testing.T) {
	contents := []string{
		`psycopg2.connect()`,
		`echo "test\nvalue"`,
		`simple command`,
		`back\slash`,
		`(nested (parens))`,
		`mixed \(\) \\ stuff`,
	}
	for _, c := range contents {
		t.Run(c, func(t *testing.T) {
			escaped := EscapeRuleContent(c)
			unescaped := UnescapeRuleContent(escaped)
			if unescaped != c {
				t.Errorf("roundtrip failed: got %q, want %q", unescaped, c)
			}
		})
	}
}

func TestFindFirstUnescapedChar(t *testing.T) {
	tests := []struct {
		s    string
		char byte
		want int
	}{
		{s: "abc(def)", char: '(', want: 3},
		{s: "abcdef", char: '(', want: -1},
		{s: `abc\(def)`, char: '(', want: -1}, // escaped
		{s: `abc\\(def)`, char: '(', want: 5}, // escaped backslash, unescaped paren
		{s: "", char: '(', want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := findFirstUnescapedChar(tt.s, tt.char)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestFindLastUnescapedChar(t *testing.T) {
	tests := []struct {
		s    string
		char byte
		want int
	}{
		{s: "abc(def)", char: ')', want: 7},
		{s: "abcdef", char: ')', want: -1},
		{s: `abc(def\)`, char: ')', want: -1}, // escaped
		{s: `(a)(b)`, char: ')', want: 5},     // last unescaped
		{s: "", char: ')', want: -1},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := findLastUnescapedChar(tt.s, tt.char)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseRuleValueCloseParenBeforeOpen(t *testing.T) {
	// ")foo(" — close paren before open → entire string as toolName
	rv := ParseRuleValue(")foo(")
	if rv.ToolName != ")foo(" {
		t.Errorf("got toolName %q, want %q", rv.ToolName, ")foo(")
	}
	if rv.RuleContent != nil {
		t.Errorf("expected nil RuleContent, got %v", *rv.RuleContent)
	}
}

func TestParseRuleValue_MalformedAndEscapes(t *testing.T) {
	content := func(s string) *string { p := &s; return p }
	cases := []struct {
		in        string
		tool      string
		ruleValue *string
	}{
		{"Bash", "Bash", nil},
		{"Bash(ls *)", "Bash", content("ls *")},
		{"Bash()", "Bash", nil},
		{"Bash(*)", "Bash", nil},
		{"(x)", "(x)", nil},
		{"Bash(a\\(b)", "Bash", content("a(b")},
		{"Bash(a\\)b)", "Bash", content("a)b")},
		{`Bash\\(x)`, `Bash\\`, content("x")},
		{"Bash(a(b)c)", "Bash", content("a(b)c")},
		{"Bash(a\\)", "Bash(a\\)", nil},
		{"Bash(", "Bash(", nil},
		{"Bash)x(", "Bash)x(", nil},
		{"x)y(", "x)y(", nil},
		{"Bash(x)y", "Bash(x)y", nil},
		{"\\(a)", "\\(a)", nil},
		{"Bash(\\\\)", "Bash", content("\\")},
	}
	for _, tc := range cases {
		got := ParseRuleValue(tc.in)
		if got.ToolName != tc.tool {
			t.Errorf("ParseRuleValue(%q).ToolName = %q, want %q", tc.in, got.ToolName, tc.tool)
		}
		switch {
		case tc.ruleValue == nil && got.RuleContent != nil:
			t.Errorf("ParseRuleValue(%q).RuleContent = %q, want nil", tc.in, *got.RuleContent)
		case tc.ruleValue != nil && got.RuleContent == nil:
			t.Errorf("ParseRuleValue(%q).RuleContent = nil, want %q", tc.in, *tc.ruleValue)
		case tc.ruleValue != nil && *got.RuleContent != *tc.ruleValue:
			t.Errorf("ParseRuleValue(%q).RuleContent = %q, want %q", tc.in, *got.RuleContent, *tc.ruleValue)
		}
	}
}

func TestEscapeUnescapeRuleContent(t *testing.T) {
	for _, s := range []string{`plain`, `a(b)c`, `a\)b`, `\`, `\\`, `a\\b(c)`} {
		esc := EscapeRuleContent(s)
		if got := UnescapeRuleContent(esc); got != s {
			t.Errorf("round-trip %q → %q → %q", s, esc, got)
		}
	}
	if got := EscapeRuleContent(`a(b`); got != `a\(b` {
		t.Errorf("EscapeRuleContent = %q, want %q", got, `a\(b`)
	}
	if got := RuleValueToString(RuleValue{ToolName: "Bash", RuleContent: new("ls *")}); got != "Bash(ls *)" {
		t.Errorf("RuleValueToString = %q, want Bash(ls *)", got)
	}
	if got := RuleValueToString(RuleValue{ToolName: "Bash"}); got != "Bash" {
		t.Errorf("RuleValueToString bare = %q, want Bash", got)
	}
}
