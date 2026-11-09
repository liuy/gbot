package skills

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// Shell block execution tests
// Source: src/utils/promptShellExecution.ts
// ---------------------------------------------------------------------------

// mockExecutor is a test ShellExecutor that returns canned output.
type mockExecutor struct {
	calls   atomic.Int32
	outputs map[string]string // command → output
	failCmd string            // command that should fail
	errMsg  string
}

func (m *mockExecutor) Execute(_ *tool.ToolUseContext, command string) (string, error) {
	m.calls.Add(1)
	if command == m.failCmd {
		return "", &ShellCommandError{Pattern: command, Message: m.errMsg}
	}
	if out, ok := m.outputs[command]; ok {
		return out, nil
	}
	return "output: " + command, nil
}

func TestExecuteShellBlocks_BlockPattern(t *testing.T) {
	t.Parallel()

	content := "Before\n```!\necho hello\n```\nAfter"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "output: echo hello") {
		t.Errorf("expected shell output substituted, got %q", result)
	}
	if strings.Contains(result, "```!") {
		t.Errorf("block pattern should be replaced, got %q", result)
	}
}

func TestExecuteShellBlocks_InlinePattern(t *testing.T) {
	t.Parallel()

	content := "Current date: !`date +%Y` end"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "output: date +%Y") {
		t.Errorf("expected inline output substituted, got %q", result)
	}
	if strings.Contains(result, "!`") {
		t.Errorf("inline pattern should be replaced, got %q", result)
	}
}

func TestExecuteShellBlocks_MCPSkip(t *testing.T) {
	t.Parallel()

	content := "```!\necho hello\n```"
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, true) // isMCP=true
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("MCP skills should skip shell execution, got %q", result)
	}
	if exec.calls.Load() != 0 {
		t.Error("no commands should execute for MCP skills")
	}
}

func TestExecuteShellBlocks_NoBlocks(t *testing.T) {
	t.Parallel()

	content := "Plain text without any shell commands."
	exec := &mockExecutor{}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("expected unchanged content, got %q", result)
	}
}

func TestExecuteShellBlocks_MultipleBlocks(t *testing.T) {
	t.Parallel()

	content := "```!\ncmd1\n```\nMiddle\n```!\ncmd2\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"cmd1": "OUTPUT1",
			"cmd2": "OUTPUT2",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "OUTPUT1") {
		t.Errorf("expected OUTPUT1 in result, got %q", result)
	}
	if !strings.Contains(result, "OUTPUT2") {
		t.Errorf("expected OUTPUT2 in result, got %q", result)
	}
	if exec.calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", exec.calls.Load())
	}
}

func TestExecuteShellBlocks_Error(t *testing.T) {
	t.Parallel()

	content := "```!\nbad-command\n```"
	exec := &mockExecutor{
		failCmd: "bad-command",
		errMsg:  "command not found",
	}
	_, err := ExecuteShellBlocks(content, exec, nil, false)
	if err == nil {
		t.Fatal("expected error for failing command")
	}
	if !strings.Contains(err.Error(), "command not found") && !strings.Contains(err.Error(), "bad-command") {
		t.Fatalf("error should mention command or message, got %v", err)
	}
	sce, ok := err.(*ShellCommandError)
	if !ok {
		t.Fatalf("expected ShellCommandError, got %T: %v", err, err)
	}
	if !strings.Contains(sce.Message, "command not found") {
		t.Errorf("error message should contain 'command not found', got %q", sce.Message)
	}
}

func TestExecuteShellBlocks_MixedInlineAndBlock(t *testing.T) {
	t.Parallel()

	content := "Inline !`echo hi` and block:\n```!\necho bye\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"echo hi":  "HI",
			"echo bye": "BYE",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "HI") {
		t.Errorf("expected inline output HI, got %q", result)
	}
	if !strings.Contains(result, "BYE") {
		t.Errorf("expected block output BYE, got %q", result)
	}
}

func TestExecuteShellBlocks_EmptyBlock(t *testing.T) {
	t.Parallel()

	content := "```!\n\n```"
	exec := &mockExecutor{}
	_, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty block should not be executed
	if exec.calls.Load() != 0 {
		t.Errorf("empty block should not execute, got %d calls", exec.calls.Load())
	}
}

func TestExecuteShellBlocks_ParallelExecution(t *testing.T) {
	t.Parallel()

	content := "```!\ncmd1\n```\n```!\ncmd2\n```\n```!\ncmd3\n```"
	exec := &mockExecutor{
		outputs: map[string]string{
			"cmd1": "OUT1",
			"cmd2": "OUT2",
			"cmd3": "OUT3",
		},
	}
	result, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec.calls.Load() != 3 {
		t.Errorf("expected 3 parallel calls, got %d", exec.calls.Load())
	}
	for _, expected := range []string{"OUT1", "OUT2", "OUT3"} {
		if !strings.Contains(result, expected) {
			t.Errorf("expected %s in result, got %q", expected, result)
		}
	}
}

func TestShellCommandError(t *testing.T) {
	t.Parallel()

	err := &ShellCommandError{Pattern: "```!bad```", Message: "exit code 1"}
	msg := err.Error()
	if !strings.Contains(msg, "shell command failed") {
		t.Errorf("error should contain 'shell command failed', got %q", msg)
	}
	if !strings.Contains(msg, "```!bad```") {
		t.Errorf("error should contain pattern, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Match indexing, worker bound, failure envelope
// ---------------------------------------------------------------------------

type recordingExecutor struct {
	mu     sync.Mutex
	order  []string
	output func(n int) string
	err    error
}

func (r *recordingExecutor) Execute(_ *tool.ToolUseContext, command string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.order)
	r.order = append(r.order, command)
	if r.err != nil {
		return "", r.err
	}
	return r.output(n), nil
}

func TestExecuteShellBlocks_ReplacementKeepsSurroundingText(t *testing.T) {
	exec := &recordingExecutor{output: func(int) string { return "OUT" }}
	block := "before ```!\necho hi\n``` after"
	got, err := ExecuteShellBlocks(block, exec, nil, false)
	if err != nil {
		t.Fatalf("ExecuteShellBlocks: %v", err)
	}
	if got != "before OUT after" {
		t.Errorf("content = %q, want %q", got, "before OUT after")
	}
	if len(exec.order) != 1 || exec.order[0] != "echo hi" {
		t.Errorf("commands = %v, want [echo hi]", exec.order)
	}
	inline := "x !`echo a` y !`echo b` z"
	got2, err := ExecuteShellBlocks(inline, &recordingExecutor{output: func(int) string { return "O" }}, nil, false)
	if err != nil {
		t.Fatalf("ExecuteShellBlocks inline: %v", err)
	}
	if got2 != "xO yO z" {
		t.Errorf("inline content = %q, want %q (the inline match carries the leading space)", got2, "xO yO z")
	}
}

func TestExecuteShellBlocks_FindsEveryBlockMatch(t *testing.T) {
	exec := &recordingExecutor{output: func(int) string { return "O" }}
	content := "```!\none\n```\nmid\n```!\ntwo\n```"
	got, err := ExecuteShellBlocks(content, exec, nil, false)
	if err != nil {
		t.Fatalf("ExecuteShellBlocks: %v", err)
	}
	sorted := append([]string(nil), exec.order...)
	sort.Strings(sorted)
	if len(sorted) != 2 || sorted[0] != "one" || sorted[1] != "two" {
		t.Errorf("commands = %v, want [one two]", sorted)
	}
	if strings.Contains(got, "```") {
		t.Errorf("content still holds a fence: %q", got)
	}
}

// concurrencyExecutor parks each command until released so the worker bound is
// observable as the number of commands that can be in flight at once.
type concurrencyExecutor struct {
	arrived chan string
	release chan struct{}
}

func (c *concurrencyExecutor) Execute(_ *tool.ToolUseContext, command string) (string, error) {
	c.arrived <- command
	<-c.release
	return "O", nil
}

func TestExecuteShellBlocks_WorkerBoundIsExactlyTen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exec := &concurrencyExecutor{arrived: make(chan string, 16), release: make(chan struct{})}
		var sb strings.Builder
		for i := range 11 {
			fmt.Fprintf(&sb, "!`cmd%02d` ", i)
		}
		done := make(chan error, 1)
		go func() {
			_, err := ExecuteShellBlocks(sb.String(), exec, nil, false)
			done <- err
		}()
		started := make([]string, 0, 11)
		for range 10 {
			started = append(started, <-exec.arrived)
		}
		// Park every goroutine before the negative check: without this, "the
		// 11th hasn't arrived yet" and "the 11th can never arrive" are
		// indistinguishable and an unbounded worker pool slips through.
		synctest.Wait()
		select {
		case extra := <-exec.arrived:
			t.Errorf("11th command %q started; the worker bound is above 10", extra)
		default:
		}
		close(exec.release)
		if err := <-done; err != nil {
			t.Fatalf("ExecuteShellBlocks: %v", err)
		}
		if len(started) != 10 {
			t.Errorf("started commands = %d, want 10", len(started))
		}
	})
}

// skillsLogHandler captures slog records for failure-path assertions.
type skillsLogHandler struct {
	mu   sync.Mutex
	recs []skillsLogRec
}

type skillsLogRec struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *skillsLogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *skillsLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, skillsLogRec{level: r.Level, msg: r.Message, attrs: attrs})
	h.mu.Unlock()
	return nil
}

func (h *skillsLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *skillsLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *skillsLogHandler) find(msg string) []skillsLogRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []skillsLogRec
	for _, r := range h.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func captureSkillsLogs(t *testing.T) *skillsLogHandler {
	t.Helper()
	h := &skillsLogHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestExecuteShellBlocks_FailureLogsAndCarriesPattern(t *testing.T) {
	logs := captureSkillsLogs(t)
	exec := &recordingExecutor{err: errors.New("boom")}
	content := "a !`echo hi` b"
	got, err := ExecuteShellBlocks(content, exec, nil, false)
	if got != "" {
		t.Errorf("content on failure = %q, want empty", got)
	}
	shellErr, ok := err.(*ShellCommandError)
	if !ok {
		t.Fatalf("error type = %T, want *ShellCommandError", err)
	}
	if shellErr.Pattern != " !`echo hi`" {
		t.Errorf("Pattern = %q, want %q", shellErr.Pattern, " !`echo hi`")
	}
	wantErr := `shell command failed for pattern " !` + "`echo hi`" + `": boom`
	if shellErr.Error() != wantErr {
		t.Errorf("Error() = %q", shellErr.Error())
	}
	recs := logs.find("skills: shell command failed")
	if len(recs) != 1 {
		t.Fatalf("failure logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["pattern"] != " !`echo hi`" {
		t.Errorf("log pattern attr = %v, want %q", recs[0].attrs["pattern"], " !`echo hi`")
	}
}

// The first command's output is itself an inline pattern, so a replacement
// count above 1 would rewrite text the first pass just inserted.
func TestExecuteShellBlocks_ReplacementCountIsOnePerMatch(t *testing.T) {
	byCmd := map[string]string{"c": " !`d`", "d": "D"}
	exec2 := &mapExecutor{out: byCmd}
	got, err := ExecuteShellBlocks("A !`c` B !`d` C", exec2, nil, false)
	if err != nil {
		t.Fatalf("ExecuteShellBlocks: %v", err)
	}
	if got != "AD B !`d` C" {
		t.Errorf("content = %q, want %q (each match replaced exactly once, in match order)", got, "AD B !`d` C")
	}
}

// mapExecutor returns a fixed output per command, so parallel scheduling does
// not change the result.
type mapExecutor struct{ out map[string]string }

func (m *mapExecutor) Execute(_ *tool.ToolUseContext, command string) (string, error) {
	return m.out[command], nil
}
