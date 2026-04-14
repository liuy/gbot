package bash

import (
	"os/exec"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Integration tests: buildCommand with proper quoting
// These demonstrate real bugs in the current %q-based quoting.
// RED: these fail because eval "cmd" expands variables before eval runs.
// GREEN: after switching to eval 'cmd' (single-quote wrapping).
// ---------------------------------------------------------------------------

func TestBuildCommand_VariableExpansion(t *testing.T) {
	t.Parallel()
	// Bug: eval "FOO=bar; echo $FOO" → $FOO expanded before eval, outputs empty
	// Fix: eval 'FOO=bar; echo $FOO' → $FOO preserved for eval, outputs "bar"
	cmd := buildCommand("FOO=bar; echo $FOO", nil, "/tmp/cwd-test")
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "bar" {
		t.Errorf("got %q, want %q — single-quote wrapping should preserve $FOO for eval", got, "bar")
	}
}

func TestBuildCommand_Heredoc(t *testing.T) {
	t.Parallel()
	// Bug: eval "cat <<EOF\nhello\nEOF" → newlines escaped to \n, heredoc breaks
	// Fix: eval 'cat <<EOF\nhello\nEOF' → newlines preserved, heredoc works
	cmd := buildCommand("cat <<EOF\nhello world\nEOF", nil, "/tmp/cwd-test")
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Errorf("got %q, want to contain 'hello world' — newlines must be preserved for heredocs", string(out))
	}
}

func TestBuildCommand_StdinRedirect(t *testing.T) {
	t.Parallel()
	// Source: bashProvider.ts — shouldAddStdinRedirect adds < /dev/null
	// to prevent commands from hanging on stdin pipe.
	cmd := buildCommand("echo hello", nil, "/tmp/cwd-test")
	if !strings.Contains(cmd, "< /dev/null") {
		t.Errorf("buildCommand() = %q, should contain '< /dev/null' for non-heredoc commands", cmd)
	}
}

func TestBuildCommand_HeredocNoStdinRedirect(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:59-61 — heredocs provide their own stdin, don't add redirect
	cmd := buildCommand("cat <<EOF\nhello\nEOF", nil, "/tmp/cwd-test")
	if strings.Contains(cmd, "< /dev/null") {
		t.Errorf("buildCommand() = %q, should NOT contain '< /dev/null' for heredoc commands", cmd)
	}
}

func TestBuildCommand_DoubleQuotePreserved(t *testing.T) {
	t.Parallel()
	// Double quotes inside the command must be preserved literally
	cmd := buildCommand(`echo "hello world"`, nil, "/tmp/cwd-test")
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestBuildCommand_SingleQuotePreserved(t *testing.T) {
	t.Parallel()
	// Single quotes inside the command must be escaped correctly
	cmd := buildCommand("echo 'hello world'", nil, "/tmp/cwd-test")
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		t.Fatalf("bash error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestBuildCommand_WindowsNullRedirect(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:124-128 — >nul should be rewritten to >/dev/null
	cmd := buildCommand("ls 2>nul", nil, "/tmp/cwd-test")
	if strings.Contains(cmd, ">nul") && !strings.Contains(cmd, ">/dev/null") {
		t.Errorf("buildCommand() = %q, should rewrite >nul to >/dev/null", cmd)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: quoting helper functions
// Source: utils/bash/shellQuoting.ts
// ---------------------------------------------------------------------------

func TestHasStdinRedirect(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:81-86
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"redirect present", "echo hi < /dev/null", true},
		{"redirect short form", "echo hi </tmp/file", true},
		{"redirect with space", "cat < input.txt", true},
		{"no redirect", "echo hello", false},
		{"heredoc not redirect", "cat <<EOF\nhello\nEOF", false},
		{"process substitution", "cat <(echo hi)", false},
		{"bit shift", "echo $((1 << 2))", false},
		{"append redirect is not stdin", "echo hi >> /tmp/out", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasStdinRedirect(tc.cmd)
			if got != tc.want {
				t.Errorf("hasStdinRedirect(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestShouldAddStdinRedirect(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:93-106
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"basic command", "echo hello", true},
		{"has redirect", "echo hi < /dev/null", false},
		{"heredoc", "cat <<EOF\nhello\nEOF", false},
		{"pipe command", "echo hello | grep h", true},
		{"empty command", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldAddStdinRedirect(tc.cmd)
			if got != tc.want {
				t.Errorf("shouldAddStdinRedirect(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestRewriteWindowsNullRedirect(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:124-128
	tests := []struct {
		name string
		cmd  string
		want string
	}{
		{"2>nul", "ls 2>nul", "ls 2>/dev/null"},
		{">nul", "ls >nul", "ls >/dev/null"},
		{">>nul", "ls >>nul", "ls >>/dev/null"},
		{"&>nul", "ls &>nul", "ls &>/dev/null"},
		{">null unchanged", "ls >null", "ls >null"},
		{">nul.txt unchanged", "ls >nul.txt", "ls >nul.txt"},
		{"no redirect", "echo hello", "echo hello"},
		{"mixed case Nul", "ls 2>Nul", "ls 2>/dev/null"},
		{"NUL uppercase", "ls >NUL", "ls >/dev/null"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := rewriteWindowsNullRedirect(tc.cmd)
			if got != tc.want {
				t.Errorf("rewriteWindowsNullRedirect(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestContainsHeredoc(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:7-22
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"heredoc <<EOF", "cat <<EOF\nhello\nEOF", true},
		{"heredoc <<'EOF'", "cat <<'EOF'\nhello\nEOF", true},
		{"heredoc <<\"EOF\"", "cat <<\"EOF\"\nhello\nEOF", true},
		{"heredoc <<- indent", "cat <<-EOF\nhello\nEOF", true},
		{"heredoc <<-'EOF'", "cat <<-'EOF'\nhello\nEOF", true},
		{"bit shift arithmetic", "echo $((1 << 2))", false},
		{"bit shift test", "[[ 1 << 2 ]]", false},
		{"no heredoc", "echo hello", false},
		{"heredoc with backslash", "cat <<\\EOF\nhello\nEOF", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := containsHeredoc(tc.cmd)
			if got != tc.want {
				t.Errorf("containsHeredoc(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestQuoteShellCommand(t *testing.T) {
	t.Parallel()
	// Source: shellQuoting.ts:46-74
	tests := []struct {
		name             string
		cmd              string
		addStdinRedirect bool
		wantEvalOutput   string // what eval produces when run through bash
		wantContains     string
		wantNotContains  string
	}{
		{
			name: "simple with redirect", cmd: "echo hello", addStdinRedirect: true,
			wantContains: "'echo hello'", wantNotContains: "",
		},
		{
			name: "simple no redirect", cmd: "echo hello", addStdinRedirect: false,
			wantContains: "'echo hello'", wantNotContains: "< /dev/null",
		},
		{
			name: "with redirect has stdin", cmd: "echo hello", addStdinRedirect: true,
			wantContains: "< /dev/null", wantNotContains: "",
		},
		{
			name: "preserves $VAR", cmd: "echo $HOME", addStdinRedirect: false,
			wantContains: "$HOME", wantNotContains: "",
		},
		{
			name: "single quote escaping", cmd: "echo 'hello'", addStdinRedirect: false,
			wantContains: "'", wantNotContains: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := quoteShellCommand(tc.cmd, tc.addStdinRedirect)
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Errorf("quoteShellCommand() = %q, want to contain %q", got, tc.wantContains)
			}
			if tc.wantNotContains != "" && strings.Contains(got, tc.wantNotContains) {
				t.Errorf("quoteShellCommand() = %q, want NOT to contain %q", got, tc.wantNotContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Eval integration: quoteShellCommand output must be correct when eval'd
// ---------------------------------------------------------------------------

func TestQuoteShellCommand_EvalVariable(t *testing.T) {
	t.Parallel()
	quoted := quoteShellCommand("FOO=bar; echo $FOO", false)
	out, err := exec.Command("bash", "-c", "eval "+quoted).Output()
	if err != nil {
		t.Fatalf("bash eval error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "bar" {
		t.Errorf("eval %q → %q, want %q", quoted, got, "bar")
	}
}

func TestQuoteShellCommand_EvalDoubleQuote(t *testing.T) {
	t.Parallel()
	quoted := quoteShellCommand(`echo "hello world"`, false)
	out, err := exec.Command("bash", "-c", "eval "+quoted).Output()
	if err != nil {
		t.Fatalf("bash eval error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "hello world" {
		t.Errorf("eval %q → %q, want %q", quoted, got, "hello world")
	}
}

func TestQuoteShellCommand_EvalSingleQuote(t *testing.T) {
	t.Parallel()
	quoted := quoteShellCommand("echo 'hello world'", false)
	out, err := exec.Command("bash", "-c", "eval "+quoted).Output()
	if err != nil {
		t.Fatalf("bash eval error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "hello world" {
		t.Errorf("eval %q → %q, want %q", quoted, got, "hello world")
	}
}

func TestQuoteShellCommand_EvalPipe(t *testing.T) {
	t.Parallel()
	quoted := quoteShellCommand("echo hello | tr h H", false)
	out, err := exec.Command("bash", "-c", "eval "+quoted).Output()
	if err != nil {
		t.Fatalf("bash eval error: %v", err)
	}
	got := strings.TrimSpace(string(out))
	if got != "Hello" {
		t.Errorf("eval %q → %q, want %q", quoted, got, "Hello")
	}
}
