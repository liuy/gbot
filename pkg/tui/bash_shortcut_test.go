package tui

import (
	"strings"
	"testing"
)

func TestIsBashShortcut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  bool
	}{
		{"!ls", true},
		{"! make check", true},
		{"!ls -la", true},
		{"!", false},
		{"!  ", false},
		{"echo hello", false},
		{"", false},
		{"/agent e2", false},
	}
	for _, tc := range cases {
		got := isBashShortcut(tc.input)
		if got != tc.want {
			t.Errorf("isBashShortcut(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestStripBangPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
	}{
		{"!ls", "ls"},
		{"! make check", "make check"},
		{"!ls -la", "ls -la"},
		{"!  echo  hello  ", "echo  hello"},
	}
	for _, tc := range cases {
		got := stripBangPrefix(tc.input)
		if got != tc.want {
			t.Errorf("stripBangPrefix(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRunBashShortcut_CreatesToolBlock(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()

	cmd := app.runBashShortcut("echo hello")
	if cmd == nil {
		t.Fatal("runBashShortcut returned nil cmd")
	}

	// Must have added user message with a tool block inside.
	if len(app.repl.messages) < 1 {
		t.Fatalf("messages = %d, want >= 1", len(app.repl.messages))
	}
	last := app.repl.messages[len(app.repl.messages)-1]
	var foundTool bool
	for _, blk := range last.Blocks {
		if blk.Type == BlockTool && blk.ToolCall.Name == "Bash" {
			foundTool = true
			if blk.ToolCall.Done {
				t.Error("tool should not be Done yet — it's running")
			}
		}
	}
	if !foundTool {
		t.Error("no Bash tool block found in last message")
	}

	// The cmd must produce a toolEndMsg.
	msg := cmd()
	m, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want toolEndMsg", msg)
	}
	if m.ToolUseID == "" {
		t.Error("ToolUseID is empty")
	}
	if !strings.Contains(m.Output, "hello") {
		t.Errorf("Output = %q, want to contain 'hello'", m.Output)
	}
	if m.IsError {
		t.Error("IsError = true, want false for 'echo hello'")
	}
}

func TestRunBashShortcut_NonZeroExit(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()

	cmd := app.runBashShortcut("exit 1")
	msg := cmd()
	m, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want toolEndMsg", msg)
	}
	if !m.IsError {
		t.Error("IsError = false for 'exit 1', want true")
	}
	if !strings.Contains(m.Output, "exit code") {
		t.Errorf("Output = %q, want to contain 'exit code'", m.Output)
	}
}

func TestRunBashShortcut_ExecError(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()

	// A command likely to fail.
	cmd := app.runBashShortcut("nonexistent_command_xyz")
	msg := cmd()
	m, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("cmd() produced %T, want toolEndMsg", msg)
	}
	if m.ToolUseID == "" {
		t.Error("ToolUseID is empty")
	}
}

func TestHandleSubmitRepl_BangShortcut(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()

	// Add some existing messages so we can verify commit behavior.
	app.repl.messages = append(app.repl.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "prior"}},
	})
	app.repl.messages = append(app.repl.messages, MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{{Type: BlockText, Text: "prior response"}},
	})
	app.repl.committedCount = 2

	cmd := app.handleSubmitRepl("!echo hello")
	if cmd == nil {
		t.Fatal("handleSubmitRepl returned nil cmd")
	}

	// Must have added user message with tool block.
	if len(app.repl.messages) < 3 {
		t.Fatalf("messages = %d, want >= 3 (2 prior + user msg with tool)", len(app.repl.messages))
	}
}

func TestHandleSubmitRepl_BangShortcut_DuringStreaming_Enqueues(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()
	app.repl.StartQuery() // streaming

	cmd := app.handleSubmitRepl("!echo hello")
	if cmd != nil {
		t.Fatalf("handleSubmitRepl during streaming should return nil (enqueued), got %v", cmd)
	}
	if len(app.repl.pendingQueue) != 1 {
		t.Errorf("pendingQueue = %d, want 1 (should have been enqueued)", len(app.repl.pendingQueue))
	}
}

func TestHandleSubmitRepl_BangShortcut_Empty(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.projectDir = t.TempDir()

	// Bare "!" with no command should NOT execute bash — fall through to normal path.
	cmd := app.handleSubmitRepl("!")
	if cmd == nil {
		t.Fatal("handleSubmitRepl('!') returned nil, want a normal submit cmd")
	}
}
