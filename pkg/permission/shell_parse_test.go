package permission

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestParseShellCommand(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
		wantErr bool
	}{
		{
			name:    "simple command",
			command: "git status",
			want:    []string{"git status"},
		},
		{
			name:    "compound &&",
			command: "git add . && rm -rf /",
			want:    []string{"git add .", "rm -rf /"},
		},
		{
			name:    "compound ||",
			command: "echo foo || echo bar",
			want:    []string{"echo foo", "echo bar"},
		},
		{
			name:    "compound semicolon",
			command: "cd /tmp; ls",
			want:    []string{"cd /tmp", "ls"},
		},
		{
			name:    "compound pipe",
			command: "cat file | grep pattern",
			want:    []string{"cat file", "grep pattern"},
		},
		{
			name:    "command substitution",
			command: "echo $(rm -rf /)",
			want:    []string{"rm -rf /", "echo $(rm -rf /)"}, // inner CmdSubst + outer command both extracted
		},
		{
			name:    "shell interpreter -c recursion",
			command: `bash -c "rm -rf /"`,
			want:    []string{"rm -rf /"}, // recursive parse extracts inner command
		},
		{
			name:    "shell interpreter without -c",
			command: "bash script.sh",
			want:    []string{"bash script.sh"}, // no recursion without -c
		},
		{
			name:    "empty command",
			command: "",
			want:    nil,
		},
		{
			name:    "multiple compound",
			command: "a && b || c; d",
			want:    []string{"a", "b", "c", "d"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShellCommand(tt.command)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("command %q should fail to parse", tt.command)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d commands %v, want %d commands %v", len(got), got, len(tt.want), tt.want)
			}
			for i, cmd := range got {
				if cmd != tt.want[i] {
					t.Errorf("command[%d]: got %q, want %q", i, cmd, tt.want[i])
				}
			}
		})
	}
}

func TestStripSafeWrappers(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "timeout stripped", input: "timeout 10 npm install", want: "npm install"},
		{name: "time stripped", input: "time go test", want: "go test"},
		{name: "nice stripped", input: "nice -n 19 go build", want: "go build"},
		{name: "nice bare stripped", input: "nice go build", want: "go build"},
		{name: "nohup stripped", input: "nohup python app.py", want: "python app.py"},
		{name: "safe env var stripped", input: "GOOS=linux go build", want: "go build"},
		{name: "unsafe env var kept", input: "PATH=/evil ls", want: "PATH=/evil ls"},
		{name: "no wrapper", input: "git status", want: "git status"},
		{name: "combined timeout+env", input: "timeout 5 GOOS=linux go build", want: "GOOS=linux go build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSafeWrappers(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripAllLeadingEnvVars(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{name: "strips all env vars", input: "FOO=bar NODE_ENV=prod rm -rf /", want: "rm -rf /"},
		{name: "strips unsafe var", input: "PATH=/evil ls", want: "ls"},
		{name: "no env vars", input: "git status", want: "git status"},
		{name: "single env var", input: "MYVAR=value echo hi", want: "echo hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripAllLeadingEnvVars(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNeedsShellMatching(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{cmd: "git status", want: false},
		{cmd: "git add . && rm -rf /", want: true},
		{cmd: "echo foo | grep bar", want: true},
		{cmd: "echo $(pwd)", want: true},
		{cmd: "echo hello", want: false},
		{cmd: "a;b", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := needsShellMatching(tt.cmd)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseShellCommandSubshell(t *testing.T) {
	cmds, err := ParseShellCommand("(cd /tmp && rm -rf *)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 2 {
		t.Fatalf("expected at least 2 commands from subshell, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandCmdSubst(t *testing.T) {
	cmds, err := ParseShellCommand("echo $(git status)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(cmds, "git status") {
		t.Errorf("expected 'git status' in extracted commands, got %v", cmds)
	}
}

func TestParseShellCommandIfClause(t *testing.T) {
	cmds, err := ParseShellCommand("if true; then echo yes; else echo no; fi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 2 {
		t.Fatalf("expected at least 2 commands from if/else, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandWhileClause(t *testing.T) {
	cmds, err := ParseShellCommand("while true; do echo loop; done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 1 {
		t.Fatalf("expected at least 1 command from while, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandForClause(t *testing.T) {
	cmds, err := ParseShellCommand("for f in *.go; do gofmt -w $f; done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 1 {
		t.Fatalf("expected at least 1 command from for, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandCaseClause(t *testing.T) {
	cmds, err := ParseShellCommand("case $x in a) echo a ;; b) echo b ;; esac")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 2 {
		t.Fatalf("expected at least 2 commands from case, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandFuncDecl(t *testing.T) {
	// Function declarations define the function but don't execute the body inline.
	cmds, err := ParseShellCommand("myfunc() { echo hello; }")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// FuncDecl body commands are deferred, not executed.
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands from FuncDecl, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandMaxSubCommands(t *testing.T) {
	var parts []string
	for i := range 60 {
		parts = append(parts, fmt.Sprintf("echo %d", i))
	}
	cmd := strings.Join(parts, " && ")
	cmds, err := ParseShellCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != MaxSubCommands {
		t.Errorf("expected %d commands (MaxSubCommands), got %d", MaxSubCommands, len(cmds))
	}
}

func TestParseShellCommandEmptyArgs(t *testing.T) {
	cmds, err := ParseShellCommand("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands for empty input, got %d", len(cmds))
	}
}

func TestStripCommentLinesAllComments(t *testing.T) {
	input := "# comment1\n# comment2"
	got := stripCommentLines(input)
	if got != input {
		t.Errorf("got %q, want original %q when all lines are comments", got, input)
	}
}

func TestStripCommentLinesMixed(t *testing.T) {
	got := stripCommentLines("# comment\necho hello")
	want := "echo hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseShellCommandShCWithSingleQuotes(t *testing.T) {
	cmds, err := ParseShellCommand(`sh -c 'echo hello'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 || cmds[0] != "echo hello" {
		t.Errorf("expected [echo hello], got %v", cmds)
	}
}

func TestParseShellCommandInterpreterWithVariable(t *testing.T) {
	cmds, err := ParseShellCommand("echo $HOME")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 {
		t.Errorf("expected 1 command, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandNestedCmdSubst(t *testing.T) {
	cmds, err := ParseShellCommand("echo $(cat $(which python))")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 2 {
		t.Errorf("expected multiple commands from nested CmdSubst, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandNewline(t *testing.T) {
	cmds, err := ParseShellCommand("echo hello\necho world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 2 {
		t.Errorf("expected 2 commands, got %d: %v", len(cmds), cmds)
	}
}

func TestWalkStmtsMaxLimit(t *testing.T) {
	var commands []string
	for i := range 55 {
		stmts, _ := ParseShellCommand(fmt.Sprintf("echo %d", i))
		commands = append(commands, stmts...)
		if len(commands) >= MaxSubCommands {
			break
		}
	}
	if len(commands) > MaxSubCommands {
		t.Errorf("expected max %d commands, got %d", MaxSubCommands, len(commands))
	}
}

func TestParseShellCommandInterpreterCFails(t *testing.T) {
	cmds, err := ParseShellCommand(`bash -c "echo 'hello"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 1 {
		t.Errorf("expected at least 1 command from interpreter -c fallback, got %d", len(cmds))
	}
}

func TestParseShellCommandCallExprNoArgs(t *testing.T) {
	cmds, err := ParseShellCommand("env")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 || cmds[0] != "env" {
		t.Errorf("expected [env], got %v", cmds)
	}
}

func TestParseShellCommandRedirect(t *testing.T) {
	cmds, err := ParseShellCommand("echo hello > /tmp/out")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d: %v", len(cmds), cmds)
	}
	if !strings.Contains(cmds[0], "echo") {
		t.Errorf("expected command to contain 'echo', got %q", cmds[0])
	}
}

func TestWalkStmtsGuardViaNewlines(t *testing.T) {
	var parts []string
	for i := range 60 {
		parts = append(parts, fmt.Sprintf("echo %d", i))
	}
	cmd := strings.Join(parts, "\n")
	cmds, err := ParseShellCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != MaxSubCommands {
		t.Errorf("expected %d commands, got %d", MaxSubCommands, len(cmds))
	}
}

func TestParseShellCommandStandaloneCmdSubst(t *testing.T) {
	cmds, err := ParseShellCommand("$(git status)")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, c := range cmds {
		if c == "git status" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'git status' in commands, got %v", cmds)
	}
}

func TestParseShellCommandInterpreterRecursionOverflow(t *testing.T) {
	var innerParts []string
	for i := 2; i <= 53; i++ {
		innerParts = append(innerParts, fmt.Sprintf("echo %d", i))
	}
	inner := strings.Join(innerParts, " && ")
	cmd := fmt.Sprintf(`echo 1 && bash -c "%s"`, inner)
	cmds, err := ParseShellCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) != MaxSubCommands {
		t.Errorf("expected %d commands (MaxSubCommands), got %d", MaxSubCommands, len(cmds))
	}
}

func TestParseShellCommandBareShellC(t *testing.T) {
	cmds, err := ParseShellCommand("bash -c")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 1 {
		t.Errorf("expected at least 1 command, got %d: %v", len(cmds), cmds)
	}
}

func TestParseShellCommandVarExpansionInInterpreter(t *testing.T) {
	cmds, err := ParseShellCommand(`bash -c "${HOME}/script"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cmds) < 1 {
		t.Errorf("expected at least 1 command, got %d: %v", len(cmds), cmds)
	}
}

// mvdan/sh Parser and Printer carry mutable parse state; sharing one instance
// across concurrent permission checks (parallel tool execution, sub-agents)
// corrupts that state. Pinned here so the instances stay per-call.
func TestParseShellCommandConcurrent(t *testing.T) {
	const goroutines = 8
	const iterations = 200

	cases := []struct {
		command string
		want    []string
	}{
		{"git status; git diff --stat", []string{"git status", "git diff --stat"}},
		{"if [ -x /bin/ls ]; then ls; fi", []string{"[ -x /bin/ls ]", "ls"}},
		{`bash -c "echo hi"`, []string{"echo hi"}},
	}

	var wg sync.WaitGroup
	failures := make(chan string, goroutines*iterations)
	for range goroutines {
		wg.Go(func() {
			for i := range iterations {
				tc := cases[i%len(cases)]
				got, err := ParseShellCommand(tc.command)
				if err != nil {
					failures <- fmt.Sprintf("%q: unexpected error: %v", tc.command, err)
					continue
				}
				if !slices.Equal(got, tc.want) {
					failures <- fmt.Sprintf("%q: got %q, want %q", tc.command, got, tc.want)
				}
			}
		})
	}
	wg.Wait()
	close(failures)
	var msgs []string
	for m := range failures {
		msgs = append(msgs, m)
	}
	if len(msgs) != 0 {
		limit := min(len(msgs), 10)
		t.Fatalf("%d/%d concurrent parses wrong:\n%s", len(msgs), goroutines*iterations,
			strings.Join(msgs[:limit], "\n"))
	}
}

// Membership of shellInterpreters is what makes `<shell> -c '<cmd>'` reach the
// checker as <cmd> instead of as the opaque wrapper call. Dropping an entry
// turns that shell into a bypass, so the whole set is pinned.
func TestParseShellCommandShellInterpretersDecomposeDashC(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"sh", "bash", "zsh", "fish", "csh", "tcsh", "ksh", "dash"} {
		cmd := shell + ` -c "rm -rf /"`
		got, err := ParseShellCommand(cmd)
		if err != nil {
			t.Fatalf("%s: ParseShellCommand(%q) error: %v", shell, cmd, err)
		}
		if !slices.Equal(got, []string{"rm -rf /"}) {
			t.Errorf("%s: ParseShellCommand(%q) = %q, want [\"rm -rf /\"]", shell, cmd, got)
		}
	}
}

func TestParseShellCommand_Structures(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"ls", []string{"ls"}},
		{"ls # a comment", []string{"ls"}},
		{"echo $(date)", []string{"date", "echo $(date)"}},
		{"if x; then a; else b; fi", []string{"x", "a", "b"}},
		{"while c; do d; done", []string{"c", "d"}},
		{"for i in a; do x; done", []string{"x"}},
		{"bash -c 'rm -rf /'", []string{"rm -rf /"}},
		{"bash -x 'echo hi'", []string{"bash -x 'echo hi'"}},
		{"echo -c foo", []string{"echo -c foo"}},
		{"git status && git push", []string{"git status", "git push"}},
	}
	for _, tc := range cases {
		got, err := ParseShellCommand(tc.in)
		if err != nil {
			t.Errorf("ParseShellCommand(%q): %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("ParseShellCommand(%q) = %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseShellCommand(%q) = %q, want %q", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestParseShellCommand_MaxSubCommandsBoundary(t *testing.T) {
	if MaxSubCommands != 50 {
		t.Errorf("MaxSubCommands = %d, want 50", MaxSubCommands)
	}
	under := strings.Repeat("x;", MaxSubCommands-1) + "x"
	got, err := ParseShellCommand(under)
	if err != nil || len(got) != MaxSubCommands {
		t.Errorf("%d commands → len %d err %v, want %d and no error", MaxSubCommands, len(got), err, MaxSubCommands)
	}
	over := strings.Repeat("x;", MaxSubCommands*2-1) + "x"
	got, err = ParseShellCommand(over)
	if err != nil {
		t.Errorf("over-limit parse returned %v, want a truncated walk", err)
	}
	if len(got) != MaxSubCommands {
		t.Errorf("over-limit command count = %d, want the walk to stop at %d", len(got), MaxSubCommands)
	}
}

func TestParseShellCommand_SyntaxErrorEnvelope(t *testing.T) {
	_, err := ParseShellCommand("if true; then")
	if err == nil {
		t.Fatal("an unterminated if statement parsed without error")
	}
	if !strings.HasPrefix(err.Error(), "shell parse error: ") {
		t.Errorf("error = %q, want the shell-parse prefix", err.Error())
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the parser error", err.Error())
	}
}

func TestStripSafeWrappers_Table(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"timeout 5 rm -rf /", "rm -rf /"},
		{"timeout -k 1s 5 rm -rf /", "rm -rf /"},
		{"time ls -la", "ls -la"},
		{"nice -n 10 make", "make"},
		{"nice -5 make", "make"},
		{"nohup sleep 10", "sleep 10"},
		{"stdbuf -o0 cat f", "cat f"},
		{"NODE_ENV=prod node app.js", "node app.js"},
		{"EVIL=1 ls", "EVIL=1 ls"},
		{"# a comment\nls", "ls"},
		{"timeout 5 ls", "ls"},
		{"ls -la", "ls -la"},
	} {
		if got := StripSafeWrappers(tc.in); got != tc.want {
			t.Errorf("StripSafeWrappers(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripAllLeadingEnvVars_Table(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FOO=bar rm -rf /", "rm -rf /"},
		{"A=1 B=2 C=3 ls", "ls"},
		{"ls -la", "ls -la"},
		{"# c\nFOO=x ls", "ls"},
	} {
		if got := StripAllLeadingEnvVars(tc.in); got != tc.want {
			t.Errorf("StripAllLeadingEnvVars(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripCommentLines_KeepsOriginalWhenAllComments(t *testing.T) {
	if got := stripCommentLines("ls\n# comment\npwd"); got != "ls\npwd" {
		t.Errorf("stripCommentLines = %q, want %q", got, "ls\npwd")
	}
	if got := stripCommentLines("# only\n# comments"); got != "# only\n# comments" {
		t.Errorf("all-comment input = %q, want it returned unchanged", got)
	}
	if got := stripCommentLines("   # indented\nls"); got != "ls" {
		t.Errorf("indented comment = %q, want %q", got, "ls")
	}
}

func TestNeedsShellMatching_Metacharacters(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"ls && pwd", true},
		{"a | b", true},
		{"a;b", true},
		{"echo $(date)", true},
		{"echo `date`", true},
		{"a\nb", true},
		{"ls -la", false},
		{"git status", false},
	} {
		if got := needsShellMatching(tc.in); got != tc.want {
			t.Errorf("needsShellMatching(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// `bash -c '<unparseable>'` must fall back to the raw inner command rather than
// dropping it: the invocation is still a shell interpreter running something.
func TestParseShellCommand_InnerParseFailureKeepsRawInner(t *testing.T) {
	got, err := ParseShellCommand(`bash -c 'if true'`)
	if err != nil {
		t.Fatalf("ParseShellCommand: %v", err)
	}
	if len(got) != 1 || got[0] != "if true" {
		t.Errorf("commands = %q, want [if true] (raw inner kept when the inner parse fails)", got)
	}
}
