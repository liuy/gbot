package permission

import (
	"fmt"
	"slices"
	"strings"
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
