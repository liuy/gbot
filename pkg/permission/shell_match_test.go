package permission

import (
	"testing"
)

func TestHasWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		want    bool
	}{
		{pattern: "git *", want: true},
		{pattern: "git status", want: false},
		{pattern: `git \*`, want: false},
		{pattern: "* run *", want: true},
		{pattern: "npm:*", want: true},
		{pattern: "", want: false},
		{pattern: "simple", want: false},
		{pattern: `\\*`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			got := HasWildcards(tt.pattern)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMatchWildcardPattern(t *testing.T) {
	tests := []struct {
		pattern, command string
		want             bool
	}{
		{pattern: "git *", command: "git add .", want: true},
		{pattern: "git *", command: `git commit -m "foo"`, want: true},
		{pattern: "git *", command: "git", want: true},
		{pattern: "git status", command: "git status", want: true},
		{pattern: "git status", command: "git status --short", want: false},
		{pattern: "* run *", command: "npm run build", want: true},
		{pattern: "* run *", command: "npm run", want: false},
		{pattern: `echo \*`, command: "echo *", want: true},
		{pattern: `echo \*`, command: "echo hello", want: false},
		{pattern: "*status", command: "git status", want: true},
		{pattern: "*status", command: "git log", want: false},
		{pattern: "*", command: "anything", want: true},
		{pattern: "*", command: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.command, func(t *testing.T) {
			got := MatchWildcardPattern(tt.pattern, tt.command)
			if got != tt.want {
				t.Errorf("MatchWildcardPattern(%q, %q) = %v, want %v", tt.pattern, tt.command, got, tt.want)
			}
		})
	}
}

func TestParseShellRule(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		rule := ParseShellRule("git status")
		if rule.Type != ShellRuleExact {
			t.Errorf("got type %v, want ShellRuleExact", rule.Type)
		}
		if rule.re != nil {
			t.Error("exact rule should not have pre-compiled regex")
		}
	})
	t.Run("wildcard", func(t *testing.T) {
		rule := ParseShellRule("git *")
		if rule.Type != ShellRuleWildcard {
			t.Errorf("got type %v, want ShellRuleWildcard", rule.Type)
		}
		if rule.re == nil {
			t.Error("wildcard rule should have pre-compiled regex")
		}
	})
}

func TestMatchShellCommand(t *testing.T) {
	tests := []struct {
		pattern, command string
		want             bool
	}{
		{pattern: "git status", command: "git status", want: true},
		{pattern: "git status", command: "git log", want: false},
		{pattern: "git *", command: "git add .", want: true},
		{pattern: "git *", command: "git", want: true},
		{pattern: "git *", command: "svn update", want: false},
		{pattern: "rm -rf *", command: "rm -rf /", want: true},
		{pattern: "rm -rf *", command: "rm -rf /tmp/test", want: true},
		{pattern: "shutdown*", command: "shutdown now", want: true},
		{pattern: "shutdown*", command: "shutdown", want: true},
	}
	for _, tt := range tests {
		rule := ParseShellRule(tt.pattern)
		t.Run(tt.pattern+"/"+tt.command, func(t *testing.T) {
			got := MatchShellCommand(rule, tt.command)
			if got != tt.want {
				t.Errorf("MatchShellCommand(ParseShellRule(%q), %q) = %v, want %v", tt.pattern, tt.command, got, tt.want)
			}
		})
	}
}

func TestMatchWildcardPatternPrecompiled(t *testing.T) {
	patterns := []string{"git *", "rm -rf *", "* run *", "shutdown*"}
	commands := []string{"git add .", "git", "rm -rf /", "npm run build", "npm run", "shutdown now", "shutdown"}

	for _, pat := range patterns {
		rule := ParseShellRule(pat)
		for _, cmd := range commands {
			precompiled := MatchShellCommand(rule, cmd)
			ontime := MatchWildcardPattern(pat, cmd)
			if precompiled != ontime {
				t.Errorf("mismatch for pattern=%q command=%q: precompiled=%v ontime=%v", pat, cmd, precompiled, ontime)
			}
		}
	}
}

func TestMatchShellCommandNilRegex(t *testing.T) {
	rule := ShellRule{Type: ShellRuleWildcard, Pattern: "git *", re: nil}
	got := MatchShellCommand(rule, "git status")
	if got {
		t.Error("nil regex should return false (fail-secure)")
	}
}

func TestMatchShellCommandUnknownType(t *testing.T) {
	rule := ShellRule{Type: ShellRuleType(99), Pattern: "git status"}
	got := MatchShellCommand(rule, "git status")
	if got {
		t.Error("unknown type should return false")
	}
}

func TestMatchWildcardPatternEscapedBackslash(t *testing.T) {
	got := MatchWildcardPattern(`foo\\bar`, `foo\bar`)
	if !got {
		t.Error(`foo\\bar should match foo\bar`)
	}
}

func TestMatchWildcardPatternDotAll(t *testing.T) {
	got := MatchWildcardPattern("foo *", "foo bar\nbaz")
	if !got {
		t.Error("wildcard should match across newlines with (?s) flag")
	}
}

func TestCompileWildcardPatternEscapedBackslash(t *testing.T) {
	rule := ParseShellRule(`foo\\*`)
	if rule.Type != ShellRuleWildcard {
		t.Fatal("expected ShellRuleWildcard")
	}
	got := MatchShellCommand(rule, `foo\bar`)
	if !got {
		t.Error(`ParseShellRule("foo\\*") should match "foo\\bar"`)
	}
	got = MatchShellCommand(rule, `foo\`)
	if !got {
		t.Error(`ParseShellRule("foo\\*") should match "foo\\"`)
	}
}

func TestCompileWildcardPatternNonEscapeBackslash(t *testing.T) {
	rule := ParseShellRule(`foo\a*`)
	if rule.Type != ShellRuleWildcard {
		t.Fatal("expected ShellRuleWildcard")
	}
	got := MatchShellCommand(rule, `foo\a`)
	if !got {
		t.Error(`ParseShellRule("foo\\a*") should match "foo\\a"`)
	}
}

func TestCompileWildcardPatternEscapedStarAndWildcard(t *testing.T) {
	// Pattern with both escaped star and unescaped star: \**
	rule := ParseShellRule(`echo\**`)
	if rule.Type != ShellRuleWildcard {
		t.Fatal("expected ShellRuleWildcard")
	}
	got := MatchShellCommand(rule, `echo*hello`)
	if !got {
		t.Error(`ParseShellRule("echo\\**") should match "echo*hello"`)
	}
	got = MatchShellCommand(rule, `echohello`)
	if got {
		t.Error(`ParseShellRule("echo\\**") should not match "echohello" (missing literal *)`)
	}
}
