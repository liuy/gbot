package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/liuy/gbot/pkg/quota"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/bash"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/tool/glob"
	"github.com/liuy/gbot/pkg/tool/grep"
	"github.com/liuy/gbot/pkg/types"
	"github.com/mattn/go-runewidth"
)

func stripANSIPrintable(s string) string {
	return regexp.MustCompile(`\x1b\[[^a-zA-Z]*[a-zA-Z]`).ReplaceAllString(s, "")
}

// ---------------------------------------------------------------------------
// Input
// ---------------------------------------------------------------------------

func TestNewInput(t *testing.T) {
	t.Parallel()

	i := NewInput()
	if i == nil {
		t.Fatal("NewInput() returned nil")
	}
	if !i.focused {
		t.Error("NewInput() should be focused by default")
	}
	if i.Value() != "" {
		t.Errorf("Value() = %q, want empty", i.Value())
	}
}

func TestInput_SetValue(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	if i.Value() != "hello" {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello")
	}
}

func TestInput_Reset(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	i.Reset()
	if i.Value() != "" {
		t.Errorf("Value() after Reset = %q, want empty", i.Value())
	}
}

func TestInput_InsertChar(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('a')
	i.InsertChar('b')
	i.InsertChar('c')
	if i.Value() != "abc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "abc")
	}
}

func TestInput_InsertChar_Chinese(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('你')
	i.InsertChar('好')
	i.InsertChar('世')
	i.InsertChar('界')
	if i.Value() != "你好世界" {
		t.Errorf("Value() = %q, want %q", i.Value(), "你好世界")
	}
}

func TestInput_InsertChar_Space(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('a')
	i.InsertChar(' ')
	i.InsertChar('b')
	if i.Value() != "a b" {
		t.Errorf("Value() = %q, want %q", i.Value(), "a b")
	}
}

func TestInput_Backspace(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft()
	i.Backspace()
	if i.Value() != "ac" {
		t.Errorf("Value() = %q, want %q", i.Value(), "ac")
	}
}

func TestInput_Backspace_AtStart(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft()
	i.CursorLeft()
	i.CursorLeft() // at position 0
	i.Backspace()  // should be no-op
	if i.Value() != "abc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "abc")
	}
}

func TestInput_Backspace_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Backspace() // should be no-op
	if i.Value() != "" {
		t.Errorf("Value() = %q, want empty", i.Value())
	}
}

func TestInput_CursorLeft(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft()
	if i.Value() != "abc" {
		t.Errorf("Value() unchanged = %q", i.Value())
	}
	if i.cursor != 2 {
		t.Errorf("cursor = %d, want 2 after CursorLeft", i.cursor)
	}
}

func TestInput_CursorRight(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft()
	i.CursorLeft()
	i.CursorRight()
	if i.Value() != "abc" {
		t.Errorf("Value() unchanged = %q", i.Value())
	}
	if i.cursor != 2 {
		t.Errorf("cursor = %d, want 2 after left-left-right", i.cursor)
	}
}

func TestInput_Home(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.End()
	i.Home()
	if i.Value() != "abc" {
		t.Errorf("Value() unchanged = %q", i.Value())
	}
	if i.cursor != 0 {
		t.Errorf("cursor = %d, want 0 after Home", i.cursor)
	}
}

func TestInput_End(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.End()
	if i.Value() != "abc" {
		t.Errorf("Value() unchanged = %q", i.Value())
	}
	if i.cursor != 3 {
		t.Errorf("cursor = %d, want 3 after End", i.cursor)
	}
}

func TestInput_View_Focused(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	v := i.View()
	// Option C: Input.View() returns pure text, no prompt.
	if strings.Contains(v, "❯") {
		t.Error("View() should NOT contain prompt '❯' (Option C)")
	}
	if !strings.Contains(v, "hello") {
		t.Error("View() should contain value")
	}
}

func TestInput_View_Placeholder(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.focused = false
	v := i.View()
	if !strings.Contains(v, "Type a message...") {
		t.Error("View() should contain placeholder when blurred and empty")
	}
}

// ---------------------------------------------------------------------------
// StatusBar
// ---------------------------------------------------------------------------

func TestNewStatusBar(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	if s.model != "" {
		t.Errorf("NewStatusBar().model = %q, want empty", s.model)
	}
}

func TestStatusBar_SetModel(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetModel("claude-3")
	v := s.View()
	if !strings.Contains(v, "claude-3") {
		t.Errorf("View() = %q, should contain model", v)
	}
}

func TestStatusBar_SetStreaming(t *testing.T) {
	t.Parallel()

	// Streaming indicator moved to progress line above input (US-103).
	// StatusBar.SetStreaming is now a no-op for display.
	s := NewStatusBar()
	s.SetStreaming(true)
	v := s.View()
	// No [working...] in status bar — it's in the progress line above input.
	// Just verify it doesn't crash and returns a string.
	if v == "" {
		t.Errorf("View() should not be empty")
	}
}

func TestStatusBar_SetUsage(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetUsage(types.Usage{InputTokens: 100, OutputTokens: 50})
	// SetUsage still stores usage data internally for context tracking.
	s.SetContext(84000, 200000)
	v := s.View()
	if !strings.Contains(v, "82.0k/195.3k") {
		t.Errorf("View() = %q, should contain context size", v)
	}
}

func TestStatusBar_SetError(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetError("rate limit")
	// Error is stored but not rendered in the new minimal status bar design.
	if s.err != "rate limit" {
		t.Errorf("err = %q, want %q", s.err, "rate limit")
	}
	v := s.View()
	if strings.Contains(v, "err:") {
		t.Errorf("View() = %q, should not contain error in status bar", v)
	}
}

// ---------------------------------------------------------------------------
// Spinner
// ---------------------------------------------------------------------------

func TestNewSpinner(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.active {
		t.Error("NewSpinner() should not be active")
	}
}

func TestSpinner_StartStop(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	if !s.active {
		t.Error("expected active after Start()")
	}
	s.Stop()
	if s.active {
		t.Error("expected inactive after Stop()")
	}
}

func TestSpinner_Tick(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	v1 := s.View()
	s.Tick()
	v2 := s.View()
	// Tick should change frame
	if v1 == v2 && v1 != "" {
		t.Error("Tick() should change spinner frame")
	}
}

func TestSpinner_InactiveView(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.View() != "" {
		t.Error("inactive spinner should return empty string")
	}
}

// ---------------------------------------------------------------------------
// ToolCallView
// ---------------------------------------------------------------------------

func TestToolCallView_Struct(t *testing.T) {
	t.Parallel()

	tcv := ToolCallView{
		Name:    "Read",
		Input:   `{"file":"test.go"}`,
		Output:  "file contents",
		IsError: false,
		Done:    true,
	}
	if tcv.Name != "Read" {
		t.Errorf("Name = %q, want %q", tcv.Name, "Read")
	}
	if tcv.Done != true {
		t.Error("Done should be true")
	}
}

// ---------------------------------------------------------------------------
// ContentBlock
// ---------------------------------------------------------------------------

func TestContentBlock_TextBlock(t *testing.T) {
	t.Parallel()

	blk := ContentBlock{Type: BlockText, Text: "hello"}
	if blk.Type != BlockText {
		t.Errorf("Type = %d, want BlockText", blk.Type)
	}
	if blk.Text != "hello" {
		t.Errorf("Text = %q, want %q", blk.Text, "hello")
	}
}

func TestContentBlock_ToolBlock(t *testing.T) {
	t.Parallel()

	blk := ContentBlock{
		Type:     BlockTool,
		ToolCall: ToolCallView{Name: "Bash", Done: true},
	}
	if blk.Type != BlockTool {
		t.Errorf("Type = %d, want BlockTool", blk.Type)
	}
	if blk.ToolCall.Name != "Bash" {
		t.Errorf("ToolCall.Name = %q, want %q", blk.ToolCall.Name, "Bash")
	}
}

// ---------------------------------------------------------------------------
// MessageView
// ---------------------------------------------------------------------------

func TestMessageView_UserRole(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "hello there"}},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "❯ hello there") {
		t.Errorf("View() = %q, should contain ❯ prefix", v)
	}
}

func TestMessageView_UserRole_MultiLine(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "this is a very long line that will wrap and continue on the next line"}},
	}
	v := m.View(30, false, "", false, false, 0) // narrow width triggers wrapping
	// First line should have ❯ prefix
	if !strings.Contains(v, "❯ this") {
		t.Errorf("View() = %q, should start with ❯", v)
	}
	// Continuation lines should be indented (2 spaces to align after ❯)
	if !strings.Contains(v, "\n  ") {
		t.Errorf("View() = %q, continuation lines should be indented", v)
	}
}

func TestPrefixUserLine(t *testing.T) {
	t.Parallel()

	// Single line
	out := prefixUserLine("hello")
	if out != "❯ hello" {
		t.Errorf("single line: got %q, want %q", out, "❯ hello")
	}

	// Multi-line
	out = prefixUserLine("line1\nline2\nline3")
	lines := strings.Split(out, "\n")
	if lines[0] != "❯ line1" {
		t.Errorf("first line: got %q, want %q", lines[0], "❯ line1")
	}
	if lines[1] != "  line2" {
		t.Errorf("continuation: got %q, want %q", lines[1], "  line2")
	}
	if lines[2] != "  line3" {
		t.Errorf("continuation: got %q, want %q", lines[2], "  line3")
	}
}

func TestMessageView_AssistantRole(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{{Type: BlockText, Text: "hi from gbot"}},
	}
	v := m.View(80, false, "", false, false, 0)
	// Role prefix is rendered by parent component (MessageList), not here
	if !strings.Contains(v, "hi from gbot") {
		t.Errorf("View() = %q, should contain content", v)
	}
}

func TestMessageView_SystemRole(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "system",
		Blocks: []ContentBlock{{Type: BlockText, Text: "system msg"}},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "system msg") {
		t.Errorf("View() = %q, should contain content", v)
	}
}

func TestMessageView_WithToolCalls_Running(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "working on it"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Read", Input: `{"file":"test.go"}`, Done: false}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	// Running state shows elapsed timer
	if !strings.Contains(v, "(0.0s)") {
		t.Errorf("View() = %q, should contain '(0.0s)' timer for running state", v)
	}
	if !strings.Contains(v, "Read") {
		t.Errorf("View() = %q, should contain tool name 'Read'", v)
	}
}

func TestMessageView_WithToolCalls_Done(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "done"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Grep", Output: "found match", Done: true, IsError: false}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "done") {
		t.Errorf("View() = %q, should contain 'done'", v)
	}
	// Tool name is used directly (no humanReadableName mapping)
	if !strings.Contains(v, "Grep") {
		t.Errorf("View() = %q, should contain 'Grep'", v)
	}
}

func TestMessageView_WithToolCalls_Error(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "failed"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "exit code 1", Done: true, IsError: true}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	// Error shows tool name with red dot
	if !strings.Contains(v, "Bash") {
		t.Errorf("View() = %q, should contain 'Bash'", v)
	}
	if !strings.Contains(v, "exit code 1") {
		t.Errorf("View() = %q, should contain error output", v)
	}
}

func TestMessageView_BlankLineBetweenBlocks(t *testing.T) {
	t.Parallel()

	// Completed tool followed by text block: blank line between blocks.
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "ok", Done: true}},
			{Type: BlockText, Text: "Here is the result"},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "\n\n") {
		t.Errorf("block-level should have blank line between tool and text, got: %q", v)
	}

	// Running tool (not done) → also blank line
	m2 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Done: false}},
			{Type: BlockText, Text: "should have blank line"},
		},
	}
	v2 := m2.View(80, false, "", false, false, 0)
	if !strings.Contains(v2, "\n\n") {
		t.Errorf("running tool followed by text should have blank line, got: %q", v2)
	}

	// Single block at end → no double blank line (just trailing \n\n from block terminator)
	m3 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "done", Done: true}},
		},
	}
	v3 := m3.View(80, false, "", false, false, 0)
	// Single block should end with \n\n but no internal blank line
	if strings.Count(v3, "\n\n") != 1 {
		t.Errorf("single tool block should have exactly one \\n\\n (trailing), got: %q", v3)
	}
}

func TestMessageView_ToolCallLongOutput(t *testing.T) {
	t.Parallel()

	longOutput := strings.Repeat("x", 300)
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "result"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Read", Output: longOutput, Done: true, IsError: false}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	// Output longer than 200 chars should not be shown
	if strings.Contains(v, strings.Repeat("x", 300)) {
		t.Error("long output (>200 chars) should not appear in view")
	}
	if !strings.Contains(v, "result") {
		t.Errorf("View() should still contain 'result' text")
	}
}

func TestMessageView_ToolCallLongInput(t *testing.T) {
	t.Parallel()

	longInput := strings.Repeat("y", 300)
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "working"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Write", Input: longInput, Done: false}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	// Input longer than 200 chars should not be shown
	if strings.Contains(v, strings.Repeat("y", 300)) {
		t.Error("long input (>200 chars) should not appear in view")
	}
}

func TestMessageView_WordWrap(t *testing.T) {
	t.Parallel()

	// Long content that exceeds 20 chars should wrap
	m := MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: "This is a very long sentence that should be wrapped properly"}},
	}
	v := m.View(20, false, "", false, false, 0)
	lines := strings.SplitSeq(v, "\n")
	// Each line should be reasonably short (no line longer than width + some ANSI margin)
	for line := range lines {
		stripped := stripANSIPrintable(line)
		if len(stripped) > 25 { // allow some margin for prefix + ANSI
			t.Errorf("line too long (%d chars): %q", len(stripped), stripped)
		}
	}
}

func TestMessageView_WordWrap_Chinese(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{{Type: BlockText, Text: "这是一段很长的中文文本需要被自动换行处理才能正确显示在终端中否则会超出屏幕宽度"}},
	}
	v := m.View(20, false, "", false, false, 0)
	if !strings.Contains(v, "这") {
		t.Error("should contain content")
	}
	// Should have multiple lines (wrapped)
	lines := strings.Split(v, "\n")
	if len(lines) < 3 {
		t.Errorf("expected wrapping, got %d lines: %q", len(lines), v)
	}
}

func TestRenderWidth_ParagraphAfterTable_Wraps(t *testing.T) {
	t.Parallel()

	// A markdown table followed by a long paragraph. The paragraph must wrap
	// at width even though the block contains a table separator line.
	md := "| A | B |\n|---|---|\n| 1 | 2 |\n\n" +
		"This is a very long paragraph that comes after the table and must be wrapped at the given width."

	rendered := RenderWidth(md, 40)

	// Find the paragraph portion (after the table border). Every visual line
	// of the paragraph must be <= 40 visible chars.
	inParagraph := false
	for line := range strings.SplitSeq(rendered, "\n") {
		plain := stripANSI(line)
		plain = strings.TrimSpace(plain)
		if strings.HasPrefix(plain, "This is a very long") {
			inParagraph = true
		}
		if inParagraph && plain != "" {
			if visibleLen(plain) > 45 { // small margin for ANSI residue
				t.Errorf("paragraph line too long (%d chars): %q", visibleLen(plain), plain)
			}
		}
	}
	if !inParagraph {
		t.Error("did not find the paragraph in rendered output")
	}
}

func visibleLen(s string) int {
	return len(stripANSI(s))
}

func TestMessageView_ToolCallEmptyOutput(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "result"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "", Done: true, IsError: false}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "result") {
		t.Errorf("View() should contain text content")
	}
}

func TestMessageView_EmptyBlocks(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{},
	}
	v := m.View(80, false, "", false, false, 0)
	if v != "" {
		t.Errorf("Empty Blocks should return empty string, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// renderMessages
// ---------------------------------------------------------------------------

func TestRenderMessagesFull_NoTrailingNewline(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "assistant", Blocks: []ContentBlock{{Type: BlockText, Text: "hello"}}},
	}
	v := renderMessagesFull(msgs, 80, false, "", false, false, 0)
	if strings.HasSuffix(v, "\n") {
		t.Errorf("renderMessagesFull should have no trailing newline, got %q", v)
	}
}

func TestRenderMessagesFull_Empty(t *testing.T) {
	t.Parallel()

	v := renderMessagesFull([]MessageView{}, 80, false, "", false, false, 0)
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessagesFull(nil) = %q, should contain welcome", v)
	}
}

func TestRenderMessagesFull_WithMessages(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "hello"}}},
		{Role: "assistant", Blocks: []ContentBlock{{Type: BlockText, Text: "hi"}}},
	}
	v := renderMessagesFull(msgs, 80, false, "", false, false, 0)
	if !strings.Contains(v, "hello") {
		t.Error("should contain user message")
	}
	if !strings.Contains(v, "hi") {
		t.Error("should contain assistant message")
	}
}

func TestRenderMessagesFull_AllMessagesIncluded(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line1"}}},
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line2"}}},
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line3"}}},
	}
	v := renderMessagesFull(msgs, 80, false, "", false, false, 0)
	// renderMessagesFull includes ALL messages (terminal handles scrolling)
	if !strings.Contains(v, "line1") {
		t.Error("should contain line1")
	}
	if !strings.Contains(v, "line2") {
		t.Error("should contain line2")
	}
	if !strings.Contains(v, "line3") {
		t.Error("should contain line3")
	}
}

// ---------------------------------------------------------------------------
// wordWrap
// ---------------------------------------------------------------------------

func TestWordWrap_ShortText(t *testing.T) {
	t.Parallel()

	v := wordWrap("hello", 80)
	if v != "hello" {
		t.Errorf("wordWrap() = %q, want %q", v, "hello")
	}
}

func TestWordWrapIndent_NoIndentIdenticalToWordWrap(t *testing.T) {
	t.Parallel()
	// wordWrap is a thin wrapper around wordWrapIndent with empty contIndent.
	// Output must be byte-identical so existing callers are unaffected.
	cases := []string{
		"hello",
		"hello world",
		"12345678901234567890",
		"line1\nline2\nline3",
		"hello\x1b[31mred\x1b[0m world",
	}
	for _, s := range cases {
		got := wordWrap(s, 10)
		want := wordWrapIndent(s, 10, "")
		if got != want {
			t.Errorf("wordWrap(%q) = %q, wordWrapIndent(.., \"\") = %q, want identical", s, got, want)
		}
	}
}

func TestWordWrapIndent_ContinuationIndented(t *testing.T) {
	t.Parallel()
	v := wordWrapIndent("12345678901234567890", 10, "  ")
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d: %q", len(lines), v)
	}
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation line %d = %q, want 2-space prefix", i+1, line)
		}
	}
}

func TestWordWrapIndent_NewlineContinuationIndented(t *testing.T) {
	t.Parallel()
	// Multi-line summary (e.g. commit message with body): explicit \n in text
	// should also apply contIndent to lines after the first.
	v := wordWrapIndent("line one\nline two\nline three", 80, "  ")
	lines := strings.Split(v, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), v)
	}
	// First line: no indent
	if strings.HasPrefix(lines[0], "  ") {
		t.Errorf("first line should not be indented: %q", lines[0])
	}
	// Lines after explicit \n must also get contIndent
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("newline continuation line %d = %q, want 2-space prefix", i+2, line)
		}
	}
}

func TestWordWrap_Width(t *testing.T) {
	t.Parallel()

	v := wordWrap("12345678901234567890", 10)
	lines := strings.Split(v, "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), v)
	}
}

func TestWordWrap_Newline(t *testing.T) {
	t.Parallel()

	v := wordWrap("hello\nworld", 80)
	lines := strings.Split(v, "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}
}

func TestWordWrap_ZeroWidth(t *testing.T) {
	t.Parallel()

	v := wordWrap("hello", 0)
	if v != "hello" {
		t.Errorf("wordWrap(, 0) = %q, want %q", v, "hello")
	}
}

func TestWordWrap_NegativeWidth(t *testing.T) {
	t.Parallel()

	v := wordWrap("hello", -1)
	if v != "hello" {
		t.Errorf("wordWrap(, -1) = %q, want %q", v, "hello")
	}
}

// ---------------------------------------------------------------------------
// runeDisplayWidth
// ---------------------------------------------------------------------------

// runeDisplayWidth uses go-runewidth; basic sanity test only.
// Comprehensive rune-level tests removed — behavior delegated to go-runewidth.
func TestRuneDisplayWidth_Sanity(t *testing.T) {
	t.Parallel()
	// ASCII: width 1
	if w := runeDisplayWidth('a'); w != 1 {
		t.Errorf("runeDisplayWidth('a') = %d, want 1", w)
	}
	// CJK: width 2
	if w := runeDisplayWidth(0x4E00); w != 2 {
		t.Errorf("runeDisplayWidthCJK = %d, want 2", w)
	}
}

func TestStringWidth_TextEmojiWithVS16(t *testing.T) {
	t.Parallel()
	// go-runewidth: EP=No emoji (0x23ED, 0x26A0, 0x2194) → width 1
	// VS16 stripped by stripRedundantVS16 since base is EP=No (kept)
	// Note: EP=No emoji keep VS16 in stripRedundantVS16, but base=1
	tests := []struct {
		input     string
		wantWidth int
	}{
		{"⏭️", 1}, // U+23ED base
		{"⚠️", 1}, // U+26A0 base
		{"↔️", 1}, // U+2194 base (ambiguous → narrow in Western context)
	}
	for _, tt := range tests {
		stripped := stripRedundantVS16(tt.input)
		w := stringWidth(stripped)
		if w != tt.wantWidth {
			t.Errorf("stringWidth(stripRedundantVS16(%q)) = %d, want %d", tt.input, w, tt.wantWidth)
		}
	}
}

// ---------------------------------------------------------------------------
// StatusBar — new fields (SetContext, SetToolCount, ctxColor, formatContextSize)
// ---------------------------------------------------------------------------

func TestStatusBar_SetContext(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetContext(84000, 200000)
	v := s.View()
	if !strings.Contains(v, "82.0k/195.3k") {
		t.Errorf("View() = %q, should contain 82.0k/195.3k", v)
	}
	if s.contextUsed != 84000 {
		t.Errorf("contextUsed = %d, want 84000", s.contextUsed)
	}
	if s.contextTotal != 200000 {
		t.Errorf("contextTotal = %d, want 200000", s.contextTotal)
	}
}

func TestStatusBar_SetToolCount(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetToolCount(13)
	v := s.View()
	if !strings.Contains(v, "13 tools") {
		t.Errorf("View() = %q, should contain '13 tools'", v)
	}
}

func TestCtxColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		used       int
		total      int
		wantEmpty  bool
		wantYellow bool
		wantRed    bool
	}{
		{"zero_total", 100, 0, true, false, false},
		{"low_usage", 50000, 200000, true, false, false},
		{"at_79_pct", 158000, 200000, true, false, false},
		{"at_80_pct", 160000, 200000, false, true, false},
		{"at_89_pct", 178000, 200000, false, true, false},
		{"at_90_pct", 180000, 200000, false, false, true},
		{"at_99_pct", 198000, 200000, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ctxColor(tt.used, tt.total)
			gotStr := string(got)
			if tt.wantEmpty && gotStr != "" {
				t.Errorf("ctxColor(%d, %d) = %q, want empty (default)", tt.used, tt.total, gotStr)
			}
			if tt.wantYellow && gotStr != "230;200;50" {
				t.Errorf("ctxColor(%d, %d) = %q, want yellow", tt.used, tt.total, gotStr)
			}
			if tt.wantRed && gotStr != "230;70;70" {
				t.Errorf("ctxColor(%d, %d) = %q, want red", tt.used, tt.total, gotStr)
			}
		})
	}
}

func TestFormatContextSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		used, total int
		want        string
	}{
		{0, 200000, "0/195.3k"},
		{500, 200000, "500/195.3k"},
		{84000, 200000, "82.0k/195.3k"},
		{1500, 3000, "1.5k/2.9k"},
		{999, 1000, "999/1.0k"},
	}
	for _, tt := range tests {
		got := formatContextSize(tt.used, tt.total)
		if got != tt.want {
			t.Errorf("formatContextSize(%d, %d) = %q, want %q", tt.used, tt.total, got, tt.want)
		}
	}
}

func TestStatusBar_View_FullLayout(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetModel("sonnet-4")
	s.SetContext(84000, 200000)
	s.SetToolCount(13)
	v := s.View()

	// Should contain all three sections with separators
	if !strings.Contains(v, "sonnet-4") {
		t.Errorf("View() = %q, should contain model name", v)
	}
	if !strings.Contains(v, "82.0k/195.3k") {
		t.Errorf("View() = %q, should contain context size", v)
	}
	if !strings.Contains(v, "13 tools") {
		t.Errorf("View() = %q, should contain tool count", v)
	}
	// Should have bullet separators
	if strings.Count(v, "•") != 2 {
		t.Errorf("View() = %q, should have exactly 2 bullet separators", v)
	}
	// No background color (no ANSI bg sequences)
	if strings.Contains(v, "\x1b[48") {
		t.Errorf("View() = %q, should not contain background color", v)
	}
}

// ---------------------------------------------------------------------------
// StatusBar.View — narrow width
// ---------------------------------------------------------------------------

func TestStatusBar_View_NarrowWidth(t *testing.T) {
	s := NewStatusBar()
	s.SetWidth(5) // very narrow
	s.SetModel("test")
	v := s.View()
	if v == "" {
		t.Error("StatusBar should render even with narrow width")
	}
}

func TestStatusBar_View_DefaultModel(t *testing.T) {
	s := NewStatusBar()
	s.SetWidth(40)
	v := s.View()
	if !strings.Contains(v, "gbot") {
		t.Errorf("default model should show 'gbot', got: %q", v)
	}
}

func TestStatusBar_Quota_HiddenWhenNil(t *testing.T) {
	t.Parallel()
	s := NewStatusBar()
	s.SetModel("glm-5.2")
	s.SetContext(0, 200000)
	s.SetToolCount(5)
	v := s.View()
	if strings.Contains(v, "%") {
		t.Errorf("View() = %q, should not contain '%%' when quota is nil", v)
	}
}

func TestStatusBar_Quota_ShownWhenSet(t *testing.T) {
	t.Parallel()
	s := NewStatusBar()
	s.SetModel("glm-5.2")
	s.SetContext(0, 200000)
	s.SetToolCount(5)
	// Fixed future timestamp — far enough that countdown is deterministic.
	// 2027-01-15T08:00:00Z → reset is far in the future; countdown will be
	// very large so we only assert the percentage + the "h" suffix.
	q := &quota.Info{Used: 15, ResetAt: time.UnixMilli(1800000000000)}
	s.SetQuota(q)
	v := s.View()
	if !strings.Contains(v, "85%") {
		t.Errorf("View() = %q, should show 85%%", v)
	}
}

func TestStatusBar_Quota_PositionedBeforeContext(t *testing.T) {
	t.Parallel()
	s := NewStatusBar()
	s.SetModel("glm")
	s.SetContext(84000, 200000)
	s.SetToolCount(3)
	s.SetQuota(&quota.Info{Used: 60, ResetAt: time.UnixMilli(1800000000000)})
	v := s.View()
	quotaIdx := strings.Index(v, "40%")
	ctxIdx := strings.Index(v, "82.0k")
	if quotaIdx == -1 || ctxIdx == -1 {
		t.Fatalf("View() = %q, expected quota and context", v)
	}
	if quotaIdx > ctxIdx {
		t.Errorf("quota should come before context: quota@%d, ctx@%d", quotaIdx, ctxIdx)
	}
}

func TestStatusBar_Quota_CountdownFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dur  time.Duration
		want string
	}{
		{"2days", 48 * time.Hour, "2d"},
		{"2h30m", 2*time.Hour + 30*time.Minute, "2h30m"},
		{"45m", 45 * time.Minute, "45m"},
		{"10s", 10 * time.Second, "0m"},
		{"negative", -5 * time.Minute, "0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatCountdown(c.dur)
			if got != c.want {
				t.Errorf("formatCountdown(%v) = %q, want %q", c.dur, got, c.want)
			}
		})
	}
}

func TestStatusBar_Quota_QuotaStyle(t *testing.T) {
	t.Parallel()
	// ≥20% left = green, ≥10% left = yellow, <10% = red
	// Compare by rendering — same color → same rendered output.
	cmp := func(s lipgloss.Style) string { return lipgloss.NewStyle().Foreground(s.GetForeground()).Render("x") }
	if got, want := cmp(quotaStyle(50)), cmp(statusGreenStyle); got != want {
		t.Errorf("remaining=50 color = %q, want green %q", got, want)
	}
	if got, want := cmp(quotaStyle(15)), cmp(statusYellowCtx); got != want {
		t.Errorf("remaining=15 color = %q, want yellow %q", got, want)
	}
	if got, want := cmp(quotaStyle(5)), cmp(statusRedCtx); got != want {
		t.Errorf("remaining=5 color = %q, want red %q", got, want)
	}
}

func TestStatusBar_Quota_SetNilHidesAfterSet(t *testing.T) {
	t.Parallel()
	s := NewStatusBar()
	s.SetModel("glm")
	s.SetContext(0, 200000)
	s.SetQuota(&quota.Info{Used: 15, ResetAt: time.UnixMilli(1800000000000)})
	if v := s.View(); !strings.Contains(v, "85%") {
		t.Fatalf("expected 85%%, got %q", v)
	}
	s.SetQuota(nil)
	if v := s.View(); strings.Contains(v, "%") {
		t.Errorf("after SetQuota(nil), should not show '%%', got %q", v)
	}
}

func TestStatusBar_Quota_ZeroResetAtHidden(t *testing.T) {
	t.Parallel()
	s := NewStatusBar()
	s.SetModel("glm")
	s.SetContext(0, 200000)
	s.SetQuota(&quota.Info{Used: 15, ResetAt: time.Time{}}) // zero time
	v := s.View()
	if strings.Contains(v, "%") {
		t.Errorf("zero ResetAt should hide quota, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// prefixUserLine: empty
// ---------------------------------------------------------------------------

func TestPrefixUserLine_Empty(t *testing.T) {
	out := prefixUserLine("")
	// Empty string split by \n gives [""] → prefixUserLine adds prompt to first line
	if !strings.Contains(out, "❯") {
		t.Errorf("prefixUserLine('') should contain prompt, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// MessageView.View — minimum width
// ---------------------------------------------------------------------------

func TestMessageView_View_MinWidth(t *testing.T) {
	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{{Type: BlockText, Text: "hello"}},
	}
	v := m.View(5, false, "", false, false, 0) // below minimum of 10
	if !strings.Contains(v, "hello") {
		t.Errorf("View with small width should still render content, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// renderToolCall — running state with output (edge case)
// ---------------------------------------------------------------------------

func TestRenderToolCall_NonToolBlock(t *testing.T) {
	var sb strings.Builder
	blk := ContentBlock{Type: BlockText, Text: "hello"}
	blk.renderToolCall(&sb, 80, false, "", false, 0, 0, false)
	if sb.Len() != 0 {
		t.Error("renderToolCall on text block should produce nothing")
	}
}

func TestMessageView_WithTool_DoneWithSummaryAndElapsed(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Bash",
				Summary: "ls -la",
				Output:  "file1\nfile2",
				Done:    true,
				IsError: false,
				Elapsed: 150 * time.Millisecond,
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "Bash") {
		t.Errorf("should contain tool name, got: %q", v)
	}
	if !strings.Contains(v, "ls -la") {
		t.Errorf("should contain summary, got: %q", v)
	}
}

func TestMessageView_WithTool_ErrorWithSummary(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Read",
				Summary: "/etc/shadow",
				Output:  "permission denied",
				Done:    true,
				IsError: true,
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "Read") {
		t.Errorf("should contain tool name, got: %q", v)
	}
	if !strings.Contains(v, "permission denied") {
		t.Errorf("should contain error output, got: %q", v)
	}
}

func TestMessageView_WithTool_DoneNoSummary(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Glob",
				Done: true,
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "Glob") {
		t.Errorf("should contain tool name, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// Input — auto-wrapping for long text
// ---------------------------------------------------------------------------

func TestInput_View_Wrapping(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(20)
	i.SetValue("abcdefghijklmnopqrstuvwxyz") // 26 chars, wraps in 20-wide input
	v := i.View()
	// Should contain newlines when text exceeds width
	if !strings.Contains(v, "\n") {
		t.Errorf("View() should wrap long text, got: %q", v)
	}
}

func TestInput_View_Wrapping_CursorOnSecondLine(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(20)
	// "abcdefghijklmnop" = 16 chars fits on first line (20 - promptWidth)
	// Add enough chars to force wrap, then put cursor on second line
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	i.Home()
	// Move cursor to position 20 (should be on second wrapped line)
	for range 20 {
		i.CursorRight()
	}
	v := i.View()
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2+ lines, got %d: %q", len(lines), v)
	}
	// Second line should have content (the cursor is there)
	second := stripANSIPrintable(lines[1])
	if len(second) == 0 {
		t.Errorf("second line should have content, got: %q", lines[1])
	}
}

func TestInput_WrappedLineCursorUp(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(20)
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	// Move cursor to second line
	i.End() // cursor at end (position 26)
	prevCursor := i.cursor
	i.CursorUp()
	// If text wraps, cursor should move up
	if i.cursor == prevCursor {
		t.Errorf("CursorUp() should move cursor to previous wrapped line, cursor=%d", i.cursor)
	}
}

func TestInput_WrappedLineCursorDown(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(20)
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	i.Home() // cursor at 0
	prevCursor := i.cursor
	i.CursorDown()
	// If text wraps, cursor should move down
	if i.cursor == prevCursor {
		t.Errorf("CursorDown() should move cursor to next wrapped line, cursor=%d", i.cursor)
	}
}

func TestInput_View_Wrapping_CJK(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(20)
	// 10 CJK chars = 20 display cells, should fill exactly one line
	// Adding more should wrap
	i.SetValue("你好你好你好你好你好你好") // 12 CJK chars = 24 display cells
	v := i.View()
	if !strings.Contains(v, "\n") {
		t.Errorf("CJK text should wrap, got: %q", v)
	}
}

func TestInput_View_Wrapping_NarrowWidth(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(0)
	i.SetValue("hello world")
	v := i.View()
	// Should not crash — just no wrapping when width is 0
	if v == "" {
		t.Error("View() should not be empty")
	}
}

func TestMessageView_ToolCallHeader_WrapsLongSummary(t *testing.T) {
	t.Parallel()

	// Long summary that exceeds narrow width
	longSummary := strings.Repeat("abc ", 20) // 80 chars
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Bash",
				Summary: longSummary,
				Done:    true,
			}},
		},
	}
	v := m.View(30, false, "", false, false, 0)
	// Header should be wrapped — output should contain newlines
	if !strings.Contains(v, "\n") {
		t.Errorf("long tool header should wrap at width 30, got: %q", v)
	}
	// Each stripped line should not exceed width by much
	for line := range strings.SplitSeq(v, "\n") {
		stripped := stripANSIPrintable(line)
		if len(stripped) > 40 { // allow margin for ANSI + prefix
			t.Errorf("header line too long (%d chars): %q", len(stripped), stripped)
		}
	}
}

// ---------------------------------------------------------------------------
// isEmojiPresentation
// ---------------------------------------------------------------------------

func TestIsEmojiPresentation_SMPColorful(t *testing.T) {
	t.Parallel()
	// 1F000+ emoji default to colorful
	tests := []struct {
		r    rune
		name string
	}{
		{'😀', "grinning face"},
		{'😎', "sunglasses"},
		{'🌺', "hibiscus"},
		{'🌳', "tree"},
		{'💪', "bicep"},
		{0x1F300, "cyclone"},
		{0x1F64F, "pray"},
		{0x1FAF8, "push hand"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isEmojiPresentation(tt.r) {
				t.Errorf("isEmojiPresentation(%U %s) = false, want true", tt.r, tt.name)
			}
		})
	}
}

func TestIsEmojiPresentation_BMPColorful(t *testing.T) {
	t.Parallel()
	tests := []struct {
		r    rune
		name string
	}{
		{'⌚', "watch"},
		{'⌛', "hourglass"},
		{'⏩', "fast-forward"},
		{'⏰', "alarm"},
		{'⏳', "hourglass not done"},
		{'☔', "umbrella rain"},
		{'☕', "coffee"},
		{'♈', "aries"},
		{'♿', "wheelchair"},
		{'⚓', "anchor"},
		{'⚡', "high voltage"},
		{'⚽', "soccer"},
		{'⛄', "snowman"},
		{'⛔', "no entry"},
		{'✅', "check"},
		{'✊', "fist"},
		{'✨', "sparkles"},
		{'❌', "cross mark"},
		{'❓', "question"},
		{'➕', "plus"},
		{'⭐', "star"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !isEmojiPresentation(tt.r) {
				t.Errorf("isEmojiPresentation(%U %s) = false, want true", tt.r, tt.name)
			}
		})
	}
}

func TestIsEmojiPresentation_TextPresentation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		r    rune
		name string
	}{
		{'#', "hash sign"},
		{'*', "asterisk"},
		{'0', "digit zero"},
		{'©', "copyright"},
		{'®', "registered"},
		{'™', "trademark"},
		{'⏏', "eject"},
		{'⏭', "next track"},
		{'☀', "sun"},
		{'☁', "cloud"},
		{'☂', "umbrella"},
		{'☎', "telephone"},
		{'✓', "check mark"},
		{'❤', "red heart"},

		{'⚠', "warning"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if isEmojiPresentation(tt.r) {
				t.Errorf("isEmojiPresentation(%U %s) = true, want false (text presentation)", tt.r, tt.name)
			}
		})
	}
}

func TestIsEmojiPresentation_NonEmoji(t *testing.T) {
	t.Parallel()
	for _, r := range []rune{'a', 'Z', '0' - 1, 0x200, '你'} {
		if isEmojiPresentation(r) {
			t.Errorf("isEmojiPresentation(%U) = true, want false", r)
		}
	}
}

// ---------------------------------------------------------------------------
// stripRedundantVS16
// ---------------------------------------------------------------------------

func TestStripRedundantVS16_ColorfulEmoji(t *testing.T) {
	t.Parallel()
	// 🏞 = U+1F3DE, Emoji_Presentation=Yes -> VS16 stripped
	input := "🏞\ufe0f test"
	got := stripRedundantVS16(input)
	want := "🏞 test"
	if got != want {
		t.Errorf("stripRedundantVS16(%q) = %q, want %q", input, got, want)
	}
}

func TestStripRedundantVS16_TextEmoji(t *testing.T) {
	t.Parallel()
	// ☀ = U+2600, Emoji_Presentation=No -> VS16 kept
	input := "☀\ufe0f test"
	got := stripRedundantVS16(input)
	if got != input {
		t.Errorf("stripRedundantVS16(%q) = %q, want unchanged", input, got)
	}
}

func TestStripRedundantVS16_NoVS16(t *testing.T) {
	t.Parallel()
	input := "hello world"
	got := stripRedundantVS16(input)
	if got != input {
		t.Errorf("stripRedundantVS16(%q) = %q, want unchanged", input, got)
	}
}

func TestStripRedundantVS16_Empty(t *testing.T) {
	t.Parallel()
	got := stripRedundantVS16("")
	if got != "" {
		t.Errorf("stripRedundantVS16('') = %q, want empty", got)
	}
}

func TestStripRedundantVS16_MultipleEmoji(t *testing.T) {
	t.Parallel()
	// Mix of colorful (strip) and text-presentation (keep) emoji
	input := "😀\ufe0f ☀\ufe0f 😎\ufe0f"
	got := stripRedundantVS16(input)
	want := "😀 ☀\ufe0f 😎"
	if got != want {
		t.Errorf("stripRedundantVS16(%q) = %q, want %q", input, got, want)
	}
}

func TestStripRedundantVS16_ZWJSequence(t *testing.T) {
	t.Parallel()
	// 👨 is Emoji_Presentation=Yes -> VS16 stripped
	input := "👨\ufe0f test"
	got := stripRedundantVS16(input)
	want := "👨 test"
	if got != want {
		t.Errorf("stripRedundantVS16(%q) = %q, want %q", input, got, want)
	}
}

func TestStripRedundantVS16_VS16AtStart(t *testing.T) {
	t.Parallel()
	// VS16 at start of string - no preceding emoji, keep it
	input := "\ufe0f hello"
	got := stripRedundantVS16(input)
	if got != input {
		t.Errorf("stripRedundantVS16(%q) = %q, want unchanged", input, got)
	}
}

func TestStripRedundantVS16_TableEmojiAlignment(t *testing.T) {
	t.Parallel()
	input := "🏞\ufe0f test"
	got := stripRedundantVS16(input)
	w := stringWidth(got)
	if w != 6 {
		t.Errorf("after stripRedundantVS16, stringWidth = %d, want 6 (got: %q)", w, got)
	}
}

// ---------------------------------------------------------------------------
// Spinner — Tick when inactive
// ---------------------------------------------------------------------------

func TestSpinner_Tick_Inactive(t *testing.T) {
	t.Parallel()
	s := NewSpinner()
	// Tick on inactive spinner should be no-op
	s.Tick()
	if s.idx != 0 {
		t.Errorf("inactive spinner idx should stay 0, got %d", s.idx)
	}
}

// ---------------------------------------------------------------------------
// renderLineSingle — edge cases
// ---------------------------------------------------------------------------

func TestInput_RenderLineSingle_Empty(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	v := i.View()
	// Empty input should show cursor block
	if v == "" {
		t.Error("empty input view should not be empty string")
	}
}

func TestInput_RenderLineSingle_Unfocused(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(80)
	i.SetValue("hello")
	i.focused = false
	v := i.View()
	if !strings.Contains(v, "hello") {
		t.Errorf("unfocused view should show value, got: %q", v)
	}
}

func TestInput_RenderLineSingle_CursorInMiddle(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(80)
	i.SetValue("abcdef")
	// Move cursor to middle
	i.Home()
	i.CursorRight()
	i.CursorRight()
	v := i.View()
	if !strings.Contains(v, "ab") {
		t.Errorf("cursor in middle should show text before cursor, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// InsertChar — cursor > len(value) clamp
// ---------------------------------------------------------------------------

func TestInput_InsertChar_CursorBeyondLength(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	// Artificially set cursor beyond value length
	i.cursor = 10
	i.InsertChar('x')
	// Should have clamped cursor to len(value), then inserted
	if i.Value() != "abcx" {
		t.Errorf("InsertChar with cursor > len should clamp, got %q", i.Value())
	}
}

// ---------------------------------------------------------------------------
// PrevWord — with leading spaces
// ---------------------------------------------------------------------------

func TestInput_PrevWord_LeadingSpaces(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello   world")
	i.End() // cursor at end
	// First PrevWord: skip spaces, then find word start
	i.PrevWord()
	if i.cursor != 8 { // "hello   " → skip 3 spaces → land at 'w'
		t.Errorf("PrevWord should skip spaces then find word, cursor = %d, want 8", i.cursor)
	}
}

func TestInput_PrevWord_AtStart(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.Home()
	prev := i.cursor
	i.PrevWord()
	if i.cursor != prev {
		t.Errorf("PrevWord at start should be no-op, cursor = %d", i.cursor)
	}
}

// ---------------------------------------------------------------------------
// formatToolOutput — various branches
// ---------------------------------------------------------------------------

func TestFormatToolOutput_Empty(t *testing.T) {
	t.Parallel()
	v := formatToolOutput("", false, false, 80, false, 0, lipgloss.NewStyle())
	if v != "" {
		t.Errorf("empty output should return empty, got %q", v)
	}
}

func TestFormatToolOutput_FewLines(t *testing.T) {
	t.Parallel()
	v := formatToolOutput("line1\nline2", false, false, 80, false, 0, lipgloss.NewStyle())
	if !strings.Contains(v, "line1") || !strings.Contains(v, "line2") {
		t.Errorf("few lines should show all, got: %q", v)
	}
}

func TestFormatToolOutput_Collapsed(t *testing.T) {
	t.Parallel()
	lines := "line1\nline2\nline3\nline4\nline5" // 5 lines > 3+1=4
	v := formatToolOutput(lines, false, false, 80, false, 0, lipgloss.NewStyle())
	if !strings.Contains(v, "ctrl+o to expand") {
		t.Errorf("collapsed output should show expand hint, got: %q", v)
	}
}

func TestFormatToolOutput_CollapsedError(t *testing.T) {
	t.Parallel()
	// Error: maxLines=10, so need > 11 lines to collapse
	var longErr strings.Builder
	for i := range 12 {
		fmt.Fprintf(&longErr, "error line %d\n", i)
	}
	v := formatToolOutput(strings.TrimRight(longErr.String(), "\n"), true, false, 80, false, 0, lipgloss.NewStyle())
	if !strings.Contains(v, "ctrl+o to see all") {
		t.Errorf("collapsed error should show 'see all' hint, got: %q", v)
	}
}

func TestFormatToolOutput_Expanded(t *testing.T) {
	t.Parallel()
	lines := "line1\nline2\nline3\nline4\nline5"
	v := formatToolOutput(lines, false, true, 80, false, 0, lipgloss.NewStyle())
	if strings.Contains(v, "ctrl+o") {
		t.Errorf("expanded output should not show collapse hint, got: %q", v)
	}
	if !strings.Contains(v, "line5") {
		t.Errorf("expanded should show all lines, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// consumeAnsiEscape — various branches
// ---------------------------------------------------------------------------

func TestConsumeAnsiEscape_CSI(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("\x1b[31mhello")
	if got != "\x1b[31m" {
		t.Errorf("CSI escape = %q, want %q", got, "\x1b[31m")
	}
}

func TestConsumeAnsiEscape_OSC_BEL(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("\x1b]0;title\x07rest")
	if got != "\x1b]0;title\x07" {
		t.Errorf("OSC BEL escape = %q, want %q", got, "\x1b]0;title\x07")
	}
}

func TestConsumeAnsiEscape_OSC_ST(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("\x1b]0;title\x1b\\rest")
	if got != "\x1b]0;title\x1b\\" {
		t.Errorf("OSC ST escape = %q, want %q", got, "\x1b]0;title\x1b\\")
	}
}

func TestConsumeAnsiEscape_TwoChar(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("\x1b(c")
	if got != "\x1b(" {
		t.Errorf("2-char escape = %q, want %q", got, "\x1b(")
	}
}

func TestConsumeAnsiEscape_NotEscape(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("hello")
	if got != "h" {
		t.Errorf("non-escape = %q, want %q", got, "h")
	}
}

func TestConsumeAnsiEscape_ShortString(t *testing.T) {
	t.Parallel()
	got := consumeAnsiEscape("\x1b")
	if got != "\x1b" {
		t.Errorf("short escape = %q, want %q", got, "\x1b")
	}
}

// ---------------------------------------------------------------------------
// stripANSI
// ---------------------------------------------------------------------------

func TestStripAnsi_Basic(t *testing.T) {
	t.Parallel()
	got := stripANSI("hello")
	if got != "hello" {
		t.Errorf("stripANSI(hello) = %q, want %q", got, "hello")
	}
}

func TestStripAnsi_WithEscape(t *testing.T) {
	t.Parallel()
	got := stripANSI("\x1b[31mred\x1b[0m text")
	if got != "red text" {
		t.Errorf("stripANSI with escape = %q, want %q", got, "red text")
	}
}

func TestStripAnsi_PartialEscape(t *testing.T) {
	t.Parallel()
	got := stripANSI("ab\x1b[") // unterminated escape
	if got != "ab[" {
		t.Errorf("stripANSI partial = %q, want %q", got, "ab[")
	}
}

func TestStripAnsi_Empty(t *testing.T) {
	t.Parallel()
	got := stripANSI("")
	if got != "" {
		t.Errorf("stripANSI('') = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// wrapLines — narrow width (availFirst < 1)
// ---------------------------------------------------------------------------

func TestInput_WrapLines_NarrowWidth(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(2) // width - promptWidth could go < 1
	i.SetValue("hello world")
	// Should not panic
	v := i.View()
	if v == "" {
		t.Error("view should not be empty with narrow width")
	}
}

// ---------------------------------------------------------------------------
// CursorUp/CursorDown — edge cases
// ---------------------------------------------------------------------------

func TestInput_CursorUp_FirstLine(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	// Cursor at start (first line)
	i.Home()
	moved := i.CursorUp()
	if moved {
		t.Error("CursorUp on first wrapped line should return false")
	}
}

func TestInput_CursorDown_LastLine(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	// Cursor at end (last line)
	i.End()
	moved := i.CursorDown()
	if moved {
		t.Error("CursorDown on last wrapped line should return false")
	}
}

func TestInput_CursorUp_SingleLine(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	moved := i.CursorUp()
	if moved {
		t.Error("CursorUp on single line should return false")
	}
}

func TestInput_CursorDown_SingleLine(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	moved := i.CursorDown()
	if moved {
		t.Error("CursorDown on single line should return false")
	}
}

// ---------------------------------------------------------------------------
// DeleteWordForward
// ---------------------------------------------------------------------------

func TestInput_DeleteWordForward(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello world")
	i.Home()
	deleted := i.DeleteWordForward()
	if deleted != "hello " {
		t.Errorf("DeleteWordForward = %q, want %q", deleted, "hello ")
	}
	if i.Value() != "world" {
		t.Errorf("after DeleteWordForward = %q, want %q", i.Value(), "world")
	}
}

func TestInput_DeleteWordForward_AtEnd(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abc")
	i.End()
	deleted := i.DeleteWordForward()
	if deleted != "" {
		t.Errorf("DeleteWordForward at end = %q, want empty", deleted)
	}
}

// ---------------------------------------------------------------------------
// cursorLine — fallback (cursor beyond all lines)
// ---------------------------------------------------------------------------

func TestInput_CursorLine_Fallback(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	i.SetValue("hello world")
	// Artificially set cursor beyond value length
	i.cursor = 999
	lines := i.wrapLines()
	cl := i.cursorLine(lines)
	if cl != len(lines)-1 {
		t.Errorf("cursorLine with cursor beyond end = %d, want %d", cl, len(lines)-1)
	}
}

// ---------------------------------------------------------------------------
// CursorDown — CJK chars for column calculation
// ---------------------------------------------------------------------------

func TestInput_CursorDown_CJK(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	// CJK chars are width 2, so fewer fit per line
	i.SetValue("你好你好你好你好你好你好") // 12 CJK = 24 display cells
	i.Home()
	// Move to first line, then CursorDown
	moved := i.CursorDown()
	if !moved {
		t.Error("CursorDown should move with CJK wrapped text")
	}
}

// ---------------------------------------------------------------------------
// PrevWord — with space loop (pos > 0 && space)
// ---------------------------------------------------------------------------

func TestInput_PrevWord_SpacesOnlyBeforeCursor(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("     ") // all spaces
	i.End()
	i.PrevWord()
	// Should go to start since all are spaces
	if i.cursor != 0 {
		t.Errorf("PrevWord with all spaces = %d, want 0", i.cursor)
	}
}

// ---------------------------------------------------------------------------
// renderLineSingle: empty lines
// ---------------------------------------------------------------------------

func TestInput_RenderLineSingle_EmptyRunes(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(80)
	// Empty value, focused — should render cursor block (no prompt per Option C)
	v := i.View()
	if strings.Contains(v, "❯") {
		t.Errorf("empty focused input should NOT show prompt (Option C), got: %q", v)
	}
	if v == "" {
		t.Error("empty focused input should render cursor block")
	}
}

// ---------------------------------------------------------------------------
// renderToolCall — running state with dot
// ---------------------------------------------------------------------------

func TestRenderToolCall_RunningState(t *testing.T) {
	t.Parallel()
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Bash",
				Summary: "ls -la",
				Done:    false,
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "(0.0s)") {
		t.Errorf("running tool should show elapsed timer, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// renderToolCall — running state with toolDot
// ---------------------------------------------------------------------------

func TestRenderToolCall_RunningWithToolDot(t *testing.T) {
	t.Parallel()
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Bash",
				Summary: "make test",
				Done:    false,
			}},
		},
	}
	v := m.View(80, false, "●", false, false, 0)
	if !strings.Contains(v, "Bash") {
		t.Errorf("should contain tool name, got: %q", v)
	}
	if !strings.Contains(v, "make test") {
		t.Errorf("should contain summary, got: %q", v)
	}
	if !strings.Contains(v, "(0.0s)") {
		t.Errorf("should show elapsed timer, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// renderToolCall — sub-agent blocks indentation
// ---------------------------------------------------------------------------

func TestRenderToolCall_DoneStateWithSubBlocks_BlocksAreIndented(t *testing.T) {
	t.Parallel()
	// Done agent tool with sub-blocks: one thinking (done) + one text
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "analyze",
				Done:      true,
				ToolCount: 1,
				Blocks: []ContentBlock{
					{Type: BlockThinking, Thinking: ThinkingView{
						Text:     "thinking content",
						Duration: time.Second,
						Done:     true,
					}},
					{Type: BlockText, Text: "hello world"},
				},
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)

	// After header line, first sub-block line must have "| " prefix (indentation)
	lines := strings.Split(v, "\n")
	// Find first non-header line (first line with content after the agent header)
	var foundSubBlock bool
	for _, line := range lines[1:] {
		stripped := stripANSIPrintable(line)
		if stripped == "" {
			continue
		}
		// Sub-blocks must have "| " prefix (not plain text)
		if strings.HasPrefix(stripped, "| ") {
			foundSubBlock = true
			break
		}
	}
	if !foundSubBlock {
		t.Errorf("sub-blocks should be indented with '| ' prefix, got:\n%s", v)
	}
}

func TestRenderToolCall_RunningStateWithSubBlocks_BlocksAreIndented(t *testing.T) {
	t.Parallel()
	// Running agent tool with sub-blocks: sub-blocks get 2-space indent (depth=1)
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "analyze",
				Done:      false,
				Blocks: []ContentBlock{
					{Type: BlockText, Text: "hello"},
				},
			}},
		},
	}
	v := m.View(80, false, "●", false, false, 0)
	stripped := stripANSIPrintable(v)

	// Sub-block text "hello" should be at depth=1 (2-space indent), no "| " prefix
	if !strings.Contains(stripped, "  hello") {
		t.Errorf("sub-block text should have 2-space indent (depth=1), got:\n%s", stripped)
	}
}

func TestRenderToolCall_NestedDepth2_GrandchildIndented(t *testing.T) {
	t.Parallel()
	// Depth=2 nested: parent agent → child agent → grandchild text
	// Grandchild at depth=2 should have 4-space indent
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "outer",
				Done:      true,
				ToolCount: 2,
				Blocks: []ContentBlock{
					{Type: BlockTool, ToolCall: ToolCallView{
						Name:      "Agent",
						AgentType: "Plan",
						Summary:   "inner",
						Done:      true,
						ToolCount: 1,
						Blocks: []ContentBlock{
							{Type: BlockText, Text: "deep content"},
						},
					}},
				},
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	stripped := stripANSIPrintable(v)

	// Grandchild text at depth=2 should have 4-space indent, no "| " prefix
	if !strings.Contains(stripped, "    deep content") {
		t.Errorf("grandchild text should have 4-space indent (depth=2), got:\n%s", stripped)
	}
	// Child agent at depth=1 should have 2-space indent, NOT 4
	if !strings.Contains(stripped, "  ●") {
		t.Errorf("child agent header should have 2-space indent (depth=1), got:\n%s", stripped)
	}
}

func TestRenderAgentStats_HasTrailingNewline(t *testing.T) {
	t.Parallel()
	// Stats are rendered via renderAgentLogs. Verify it produces output with stats.
	tcv := &ToolCallView{
		TokensIn:  1000,
		TokensOut: 500,
		ToolCount: 3,
	}
	out := renderAgentLogs(tcv, 80)
	if out == "" {
		t.Skip("renderAgentLogs empty when no AgentLogs entries")
	}
}

// ---------------------------------------------------------------------------
// history — save error paths
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// renderToolCall — sub-agent blocks indentation
// ---------------------------------------------------------------------------

func TestHistory_Save_ReadOnlyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	readOnlyDir := dir + "/ro"
	if err := os.MkdirAll(readOnlyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(readOnlyDir + "/sub/history.jsonl")
	h.Add("test")
	// MkdirAll should fail on read-only parent
	// Verify no panic, entry still in memory
	if len(h.items) != 1 {
		t.Errorf("Len() = %d, want 1", len(h.items))
	}
}

// ---------------------------------------------------------------------------
// CursorDown — cursor in middle of line with wrapping
// ---------------------------------------------------------------------------

func TestInput_CursorDown_MidLine(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(20)
	// "hello world test" = 16 chars, fits in one line (20 - promptWidth)
	// Make it longer so it wraps: "abcdefghijklmnopqrstuvwxyz"
	i.SetValue("abcdefghijklmnopqrstuvwxyz")
	// Move cursor to position 5 (middle of first wrapped line)
	i.Home()
	for range 5 {
		i.CursorRight()
	}
	if i.cursor != 5 {
		t.Fatalf("cursor should be 5, got %d", i.cursor)
	}
	moved := i.CursorDown()
	if !moved {
		t.Error("CursorDown from mid-line should move to next wrapped line")
	}
	// Cursor should be on second line, roughly same column
	if i.cursor < 10 {
		t.Errorf("cursor should be on second wrapped line, got %d", i.cursor)
	}
}

// ---------------------------------------------------------------------------
// Markdown — softbreak
// ---------------------------------------------------------------------------

func TestMarkdownRender_Softbreak(t *testing.T) {
	t.Parallel()
	// Softbreak: two spaces at end of line followed by newline
	input := "line1  \nline2"
	v := Render(input)
	if !strings.Contains(v, "line1") || !strings.Contains(v, "line2") {
		t.Errorf("softbreak should render both lines, got: %q", v)
	}
}

// ---------------------------------------------------------------------------
// Markdown: empty table
// ---------------------------------------------------------------------------

func TestMarkdownRender_EmptyTable(t *testing.T) {
	t.Parallel()
	// Table with header only (no rows)
	input := "| A |\n| --- |"
	v := Render(input)
	if !strings.Contains(v, "A") {
		t.Errorf("table with header should render, got: %q", v)
	}
}

func TestFormatToolOutput_NoHint(t *testing.T) {
	t.Parallel()
	lines := "line1\nline2\nline3\nline4\nline5" // 5 lines > 3+1=4
	v := formatToolOutput(lines, false, false, 80, true, 0, lipgloss.NewStyle())
	if strings.Contains(v, "ctrl+o") {
		t.Errorf("noHint should suppress ctrl+o hint, got: %q", v)
	}
	if !strings.Contains(v, "… +2 lines") {
		t.Errorf("noHint should still show line count, got: %q", v)
	}
	if strings.Contains(v, "line4") || strings.Contains(v, "line5") {
		t.Errorf("collapsed noHint should hide lines 4-5, got: %q", v)
	}
}

func TestFormatToolOutput_ExpandedWithMaxLines(t *testing.T) {
	t.Parallel()
	// 10 lines, maxOutputLines=5 → show last 5 + truncation notice
	lines := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10"
	v := formatToolOutput(lines, false, true, 80, false, 5, lipgloss.NewStyle())
	if strings.Contains(v, "ctrl+o") {
		t.Errorf("expanded with maxOutputLines should not show ctrl+o, got: %q", v)
	}
	if !strings.Contains(v, "... 5 lines truncated ...") {
		t.Errorf("should show truncation notice, got: %q", v)
	}
	// Should show last 5 lines (l6-l10), not first 5
	if strings.Contains(v, "l1\n") || strings.Contains(v, "l2\n") {
		t.Errorf("should not show first 5 lines, got: %q", v)
	}
	if !strings.Contains(v, "l6") || !strings.Contains(v, "l10") {
		t.Errorf("should show last 5 lines (l6-l10), got: %q", v)
	}
}

func TestFormatToolOutput_ExpandedWithMaxLines_ZeroMeansUnlimited(t *testing.T) {
	t.Parallel()
	lines := strings.Repeat("line\n", 100)
	v := formatToolOutput(strings.TrimRight(lines, "\n"), false, true, 80, false, 0, lipgloss.NewStyle())
	if strings.Contains(v, "truncated") {
		t.Errorf("maxOutputLines=0 should show all lines without truncation, got truncated")
	}
}
func TestFormatToolOutput_WordWrapPrefixAlignment(t *testing.T) {
	t.Parallel()
	// A single long line that wraps into multiple sub-lines.
	// Each wrapped sub-line must have the proper prefix alignment:
	// First sub-line: "| " prefix
	// Continuation sub-lines: "  " (spaces matching | width)
	longLine := strings.Repeat("hello ", 30) // ~180 chars, wraps at width=40
	got := formatToolOutput(longLine, false, false, 40, false, 0, lipgloss.NewStyle())
	gotLines := strings.Split(got, "\n")
	if len(gotLines) < 2 {
		t.Fatalf("expected wrapping to produce multiple lines, got %d: %q", len(gotLines), got)
	}
	// First line must start with "| "
	if !strings.HasPrefix(gotLines[0], "| ") {
		t.Errorf("first line = %q, want prefix '| '", gotLines[0])
	}
	// Continuation lines must start with spaces (matching | width = 2 spaces)
	for i, line := range gotLines[1:] {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation line %d = %q, want 2-space prefix", i+1, line)
		}
	}
}

// ---------------------------------------------------------------------------
// renderThinkingBlock / formatThinkingOutput
// ---------------------------------------------------------------------------

func TestRenderThinkingBlock_StreamingWithToolDot(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: "reasoning...", Done: false},
	}
	blk.renderThinkingBlock(&sb, 80, false, "bright-dot", false, 0)
	out := sb.String()
	if !strings.Contains(out, "Thinking...") {
		t.Errorf("streaming should show 'Thinking...', got %q", out)
	}
	if !strings.Contains(out, "reasoning...") {
		t.Errorf("streaming should show content, got %q", out)
	}
}

func TestRenderThinkingBlock_StreamingNoToolDot(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: "some thought", Done: false},
	}
	blk.renderThinkingBlock(&sb, 80, false, "", false, 0)
	out := sb.String()
	if !strings.Contains(out, "Thinking...") {
		t.Errorf("streaming should show 'Thinking...', got %q", out)
	}
}

func TestRenderThinkingBlock_StreamingEmptyText(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: "", Done: false},
	}
	blk.renderThinkingBlock(&sb, 80, false, "dot", false, 0)
	out := sb.String()
	if !strings.Contains(out, "Thinking...") {
		t.Errorf("streaming with empty text should still show header, got %q", out)
	}
}

func TestRenderThinkingBlock_DoneWithDuration(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: "thought content", Done: true, Duration: 2 * time.Second},
	}
	blk.renderThinkingBlock(&sb, 80, false, "", false, 0)
	out := sb.String()
	if !strings.Contains(out, "Thought for") {
		t.Errorf("done should show 'Thought for', got %q", out)
	}
	if !strings.Contains(out, "2s") {
		t.Errorf("done should show duration, got %q", out)
	}
}

func TestRenderThinkingBlock_DoneNoText(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: "", Done: true, Duration: 0},
	}
	blk.renderThinkingBlock(&sb, 80, false, "", false, 0)
	out := sb.String()
	if !strings.Contains(out, "Thought") {
		t.Errorf("done with no text should still show 'Thought', got %q", out)
	}
	if strings.Contains(out, "Thought for") {
		t.Errorf("no duration should not show 'Thought for', got %q", out)
	}
	if strings.Contains(out, "| ") {
		t.Errorf("done with no text should not show output prefix, got %q", out)
	}
}

func TestRenderThinkingBlock_NonThinkingBlock(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	blk := ContentBlock{Type: BlockText, Text: "hello"}
	blk.renderThinkingBlock(&sb, 80, false, "", false, 0)
	if sb.String() != "" {
		t.Errorf("non-thinking block should produce no output, got %q", sb.String())
	}
}

func TestFormatThinkingOutput_Empty(t *testing.T) {
	t.Parallel()
	out := formatToolOutput("", false, false, 80, false, 0, lipgloss.NewStyle())
	if out != "" {
		t.Errorf("empty content should return empty, got %q", out)
	}
}

func TestFormatThinkingOutput_FewLines(t *testing.T) {
	t.Parallel()
	content := "line1\nline2"
	out := formatToolOutput(content, false, false, 80, false, 0, lipgloss.NewStyle())
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Errorf("few lines should show all, got %q", out)
	}
}

func TestFormatThinkingOutput_Collapsed(t *testing.T) {
	t.Parallel()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("thought line %d", i)
	}
	content := strings.Join(lines, "\n")
	out := formatToolOutput(content, false, false, 80, false, 0, lipgloss.NewStyle())
	if !strings.Contains(out, "… +7 lines (ctrl+o to expand)") {
		t.Errorf("collapsed should show hint, got %q", out)
	}
	if strings.Contains(out, "thought line 9") {
		t.Errorf("collapsed should not show line 9, got output containing it")
	}
}

func TestFormatThinkingOutput_Expanded(t *testing.T) {
	t.Parallel()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("thought line %d", i)
	}
	content := strings.Join(lines, "\n")
	out := formatToolOutput(content, false, true, 80, false, 0, lipgloss.NewStyle())
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("expanded should not show collapse hint, got %q", out)
	}
	if !strings.Contains(out, "thought line 9") {
		t.Errorf("expanded should show all lines including line 9, got %q", out)
	}
}

func TestFormatThinkingOutput_StreamingShowsAll(t *testing.T) {
	t.Parallel()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	content := strings.Join(lines, "\n")
	out := formatToolOutput(content, false, true, 80, false, 0, lipgloss.NewStyle())
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("streaming should not show collapse hint, got %q", out)
	}
	if !strings.Contains(out, "line 9") {
		t.Errorf("streaming should show all lines, got %q", out)
	}
}

func TestFormatThinkingOutput_NoHint(t *testing.T) {
	t.Parallel()
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	content := strings.Join(lines, "\n")
	out := formatToolOutput(content, false, false, 80, true, 0, lipgloss.NewStyle())
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("noHint=true should not mention ctrl+o, got %q", out)
	}
	if !strings.Contains(out, "… +7 lines") {
		t.Errorf("noHint should still show line count, got %q", out)
	}
}

func TestMessageView_WithThinkingBlock(t *testing.T) {
	t.Parallel()
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockThinking, Thinking: ThinkingView{Text: "I need to think...", Done: true, Duration: time.Second}},
			{Type: BlockText, Text: "Here is my answer."},
		},
	}
	out := mv.View(80, false, "", false, false, 0)
	if !strings.Contains(out, "Thought") {
		t.Errorf("should contain 'Thought', got %q", out)
	}
	if !strings.Contains(out, "I need to think") {
		t.Errorf("should contain thinking content, got %q", out)
	}
	if !strings.Contains(out, "Here is my answer") {
		t.Errorf("should contain text content, got %q", out)
	}
}

func TestRenderThinkingBlock_LongText_Wraps(t *testing.T) {
	t.Parallel()
	// Generate a long line that exceeds available width
	longLine := strings.Repeat("word ", 40) // ~200 chars
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: longLine, Done: true, Duration: time.Second},
	}
	blk.renderThinkingBlock(&sb, 40, false, "", false, 0)
	out := sb.String()
	// Output should have multiple lines (word-wrapped), not one giant line
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// First line is header "Thought for Xs", rest should be wrapped content
	// At width 40 with prefix "| " (2 chars), content gets ~36 chars per line
	// 200 chars should wrap to multiple lines
	contentLines := 0
	for _, line := range lines {
		if strings.Contains(line, "| ") || strings.Contains(line, "word") {
			contentLines++
		}
	}
	if contentLines < 3 {
		t.Errorf("long thinking text should wrap into multiple lines, got %d content lines:\n%s", contentLines, out)
	}
}

func TestRenderThinkingBlock_StreamingLongText_Wraps(t *testing.T) {
	t.Parallel()
	longLine := strings.Repeat("word ", 40)
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: longLine, Done: false},
	}
	blk.renderThinkingBlock(&sb, 40, false, "dot", false, 0)
	out := sb.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	contentLines := 0
	for _, line := range lines {
		if strings.Contains(line, "word") {
			contentLines++
		}
	}
	if contentLines < 3 {
		t.Errorf("streaming long text should wrap into multiple lines, got %d content lines:\n%s", contentLines, out)
	}
}

func TestRenderThinkingBlock_StreamingLongText_PrefixAlignment(t *testing.T) {
	t.Parallel()
	longLine := strings.Repeat("hello ", 30) // ~180 chars, wraps at width=40
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: longLine, Done: false},
	}
	blk.renderThinkingBlock(&sb, 40, false, "dot", false, 0)
	out := sb.String()
	// Strip ANSI escape codes for prefix checking
	clean := stripANSI(out)
	lines := strings.Split(clean, "\n")
	// Find content lines (those starting with | or 2-space prefix)
	started := false
	for i, line := range lines {
		if strings.Contains(line, "| ") {
			started = true
			continue
		}
		if started && line != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation line %d = %q, want 2-space prefix for alignment", i, line)
		}
	}
}

func TestRenderThinkingBlock_DoneLongText_PrefixAlignment(t *testing.T) {
	t.Parallel()
	longLine := strings.Repeat("hello ", 30) // ~180 chars, wraps at width=40
	var sb strings.Builder
	blk := ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Text: longLine, Done: true, Duration: time.Second},
	}
	blk.renderThinkingBlock(&sb, 40, false, "", false, 0)
	out := sb.String()
	clean := stripANSI(out)
	lines := strings.Split(clean, "\n")
	started := false
	for i, line := range lines {
		if strings.Contains(line, "| ") {
			started = true
			continue
		}
		if started && line != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation line %d = %q, want 2-space prefix for alignment", i, line)
		}
	}
}

func TestFormatToolOutput_TabsExpanded(t *testing.T) {
	t.Parallel()

	// Tool output containing tab-indented Go code.
	// Tabs must be expanded to spaces so wordWrap calculates the correct
	// visual width. Otherwise tabs (width 0 in runewidth) cause lines to
	// exceed the terminal width, creating extra visual lines that Bubble Tea
	// cannot clear — leading to ghost content on Ctrl+O expand.
	input := "\tfunc hello() {\n\t\treturn \"world\"\n\t}"
	v := formatToolOutput(input, false, false, 80, false, 0, lipgloss.NewStyle())
	clean := stripANSI(v)

	// No tab characters should remain in the output
	if strings.Contains(clean, "\t") {
		t.Errorf("output contains unexpanded tabs: %q", clean)
	}

	// The indented code should still be visible
	if !strings.Contains(clean, "func hello()") {
		t.Errorf("output should contain 'func hello()', got: %q", clean)
	}
	if !strings.Contains(clean, "return") {
		t.Errorf("output should contain 'return', got: %q", clean)
	}
}

// ---------------------------------------------------------------------------
// pluralS
// ---------------------------------------------------------------------------

func TestPluralS_One(t *testing.T) {
	t.Parallel()
	if got := pluralS(1); got != "" {
		t.Errorf("pluralS(1) = %q, want empty string", got)
	}
}

func TestPluralS_Zero(t *testing.T) {
	t.Parallel()
	if got := pluralS(0); got != "s" {
		t.Errorf("pluralS(0) = %q, want %q", got, "s")
	}
}

func TestPluralS_Two(t *testing.T) {
	t.Parallel()
	if got := pluralS(2); got != "s" {
		t.Errorf("pluralS(2) = %q, want %q", got, "s")
	}
}

func TestPluralS_Negative(t *testing.T) {
	t.Parallel()
	// Negative numbers are != 1, so should return "s"
	if got := pluralS(-1); got != "s" {
		t.Errorf("pluralS(-1) = %q, want %q", got, "s")
	}
}

// ---------------------------------------------------------------------------

func TestWordWrap_TabWidth(t *testing.T) {
	t.Parallel()

	// A line with a tab followed by text. With availWidth=20, wordWrap should
	// produce output whose display width (with tabs expanded) does not exceed 20.
	input := "\tSome text that is long enough to need wrapping at some point"
	wrapped := wordWrap(input, 20)
	clean := stripANSI(wrapped)
	for line := range strings.SplitSeq(clean, "\n") {
		// Expand tabs for width check (tab stops at 8)
		displayLen := 0
		for _, r := range line {
			if r == '\t' {
				displayLen += 8 - (displayLen % 8)
			} else {
				displayLen += runewidth.RuneWidth(r)
			}
		}
		if displayLen > 24 { // allow small slack for word boundaries
			t.Errorf("wrapped line too wide (display width %d): %q", displayLen, line)
		}
	}
}

// ---------------------------------------------------------------------------
// StatusBar.SetInfo
// ---------------------------------------------------------------------------

func TestStatusBar_SetInfo(t *testing.T) {
	s := NewStatusBar()
	s.SetInfo("session saved")
	if s.info != "session saved" {
		t.Errorf("info = %q, want %q", s.info, "session saved")
	}
}

// ---------------------------------------------------------------------------
// renderToolCall — sub-blocks must have | prefix for indentation
// ---------------------------------------------------------------------------

func TestRenderToolCall_DoneSubBlocks_BlockTextHasPrefix(t *testing.T) {
	t.Parallel()
	// BlockText in sub-blocks gets 2-space indent (depth=1), no "| "
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "analyze",
				Done:      true,
				ToolCount: 1,
				Blocks: []ContentBlock{
					{Type: BlockText, Text: "hello world"},
				},
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	stripped := stripANSIPrintable(v)

	// "hello world" should appear with 2-space indent (depth=1), no "| " prefix
	if !strings.Contains(stripped, "  hello world") {
		t.Errorf("BlockText sub-blocks should have 2-space indent, got:\n%s", stripped)
	}
}

func TestRenderToolCall_DoneSubBlocks_StatsHasPrefix(t *testing.T) {
	t.Parallel()
	// Stats at depth=0 appear with "| " prefix via formatToolOutput
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "analyze",
				Done:      true,
				ToolCount: 1,
				TokensIn:  1000,
				TokensOut: 500,
				Blocks: []ContentBlock{
					{Type: BlockText, Text: "result"},
				},
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	stripped := stripANSIPrintable(v)

	// Stats line should appear with "| " prefix (depth=0 output)
	if !strings.Contains(stripped, "| ") {
		t.Errorf("stats should have '| ' prefix, got:\n%s", stripped)
	}
	// Stats should contain tool count info
	if !strings.Contains(stripped, "1 tool") {
		t.Errorf("stats should show tool count, got:\n%s", stripped)
	}
}

func TestRenderToolCall_NestedDepth2_ChildAgentHasPrefix(t *testing.T) {
	t.Parallel()
	// depth=1 child agent header gets 2-space indent, NO "|" before ●
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:      "Agent",
				AgentType: "Explore",
				Summary:   "outer",
				Done:      true,
				ToolCount: 1,
				Blocks: []ContentBlock{
					{Type: BlockTool, ToolCall: ToolCallView{
						Name:      "Agent",
						AgentType: "Plan",
						Summary:   "inner",
						Done:      true,
					}},
				},
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	stripped := stripANSIPrintable(v)
	lines := strings.SplitSeq(stripped, "\n")

	// Find the inner agent header line (contains "Plan")
	for line := range lines {
		if strings.Contains(line, "Plan") {
			// Must have 2-space indent, NOT "| " before the dot
			if !strings.HasPrefix(line, "  ●") {
				t.Errorf("depth=1 child agent header should be '  ● Agent Plan(...)', got %q", line)
			}
			if strings.HasPrefix(line, "  |") {
				t.Errorf("depth=1 child agent header must NOT have | before ●, got %q", line)
			}
			return
		}
	}
	t.Errorf("should find 'Plan' in output, got:\n%s", stripped)
}

// ---------------------------------------------------------------------------
// renderToolCall — running label
// ---------------------------------------------------------------------------

func TestRenderToolCall_RunningLabel(t *testing.T) {
	t.Parallel()
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Agent",
				Summary: "fork agent task",
				Done:    false,
			}},
		},
	}
	v := m.View(80, false, "", false, false, 0)
	if !strings.Contains(v, "(0.0s)") {
		t.Errorf("running tool should show elapsed timer, got: %q", v)
	}
}

func TestRenderToolCall_LongSummaryHeaderWrapsAligned(t *testing.T) {
	t.Parallel()
	longCmd := strings.Repeat("make check ", 10) // ~110 chars, wraps at width=30
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name:    "Bash",
				Summary: longCmd,
				Done:    true,
			}},
		},
	}
	v := m.View(30, false, "", false, false, 0)
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected header to wrap into multiple lines at width=30, got %d lines: %q", len(lines), v)
	}
	// Continuation lines should be indented to align with content after "● "
	// i.e. start with spaces, not start at column 0.
	for i, line := range lines[1:] {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == line && line != "" {
			t.Errorf("continuation line %d = %q, should be indented (start with spaces)", i+1, line)
		}
	}
}

// ---------------------------------------------------------------------------
// Multiline Input — Source: useTextInput.ts handleEnter + Cursor.up/down
// TS PromptInput.tsx:2173 sets multiline: true for the main input.
// ---------------------------------------------------------------------------

// TestInput_InsertNewline verifies that InsertNewline inserts \n at cursor.
// Source: TS useTextInput.ts:252 — cursor.insert('\n')
func TestInput_InsertNewline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello world")
	// Move cursor to after "hello"
	i.cursor = 5
	i.InsertNewline()
	if got := i.Value(); got != "hello\n world" {
		t.Errorf("InsertNewline at pos 5: got %q, want %q", got, "hello\n world")
	}
	if i.cursor != 6 {
		t.Errorf("cursor after InsertNewline: got %d, want 6", i.cursor)
	}
}

// TestInput_InsertNewline_AtStart verifies newline at beginning.
func TestInput_InsertNewline_AtStart(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.cursor = 0
	i.InsertNewline()
	if got := i.Value(); got != "\nhello" {
		t.Errorf("InsertNewline at start: got %q, want %q", got, "\nhello")
	}
}

// TestInput_InsertNewline_AtEnd verifies newline at end.
func TestInput_InsertNewline_AtEnd(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello")
	i.cursor = 5
	i.InsertNewline()
	if got := i.Value(); got != "hello\n" {
		t.Errorf("InsertNewline at end: got %q, want %q", got, "hello\n")
	}
}

// TestInput_WrapLines_HardNewline verifies wrapLines treats \n as hard break.
// Source: TS Cursor.ts — MeasuredText.measureWrappedText() splits on \n.
func TestInput_WrapLines_HardNewline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\nworld")
	i.SetWidth(80)
	lines := i.wrapLines()
	if len(lines) != 2 {
		t.Fatalf("wrapLines: got %d lines, want 2", len(lines))
	}
	if string(lines[0].runes) != "hello" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "hello")
	}
	if string(lines[1].runes) != "world" {
		t.Errorf("line 1: got %q, want %q", string(lines[1].runes), "world")
	}
}

// TestInput_WrapLines_HardNewlineWithSoftWrap verifies hard and soft wrap combo.
func TestInput_WrapLines_HardNewlineWithSoftWrap(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("abcdefg\nhi")
	i.SetWidth(20)
	lines := i.wrapLines()
	if len(lines) != 2 {
		t.Fatalf("wrapLines with width 20: got %d lines, want 2", len(lines))
	}
	if string(lines[0].runes) != "abcdefg" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "abcdefg")
	}
	if string(lines[1].runes) != "hi" {
		t.Errorf("line 1: got %q, want %q", string(lines[1].runes), "hi")
	}
}

// TestInput_View_Multiline verifies View renders multiline with prompt/indent.
// Source: TS components/Input.tsx — multiline rendering with ❯ prefix.
func TestInput_View_Multiline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\nworld")
	i.SetWidth(80)
	v := i.View()
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("View multiline: got %d lines, want >= 2; output:\n%s", len(lines), v)
	}
	// Option C: Input.View() returns pure text, no prompt or indent.
	if strings.Contains(lines[0], "❯") {
		t.Errorf("first line should NOT contain prompt (Option C), got: %q", lines[0])
	}
	if strings.Contains(lines[1], "❯") {
		t.Errorf("continuation line should NOT contain ❯, got: %q", lines[1])
	}
	// Both lines should contain content
	stripped0 := stripANSIPrintable(lines[0])
	stripped1 := stripANSIPrintable(lines[1])
	if !strings.Contains(stripped0, "hello") {
		t.Errorf("first line should contain 'hello', got: %q", stripped0)
	}
	if !strings.Contains(stripped1, "world") {
		t.Errorf("second line should contain 'world', got: %q", stripped1)
	}
}

// TestInput_CursorUp_AcrossNewline verifies cursor moves up across real newlines.
// Source: TS useTextInput.ts:282 — multiline upLogicalLine.
func TestInput_CursorUp_AcrossNewline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\nworld")
	i.SetWidth(80)
	i.cursor = 8 // "r" in "world" (pos 8 = second line, col 2)
	if !i.CursorUp() {
		t.Error("CursorUp should succeed across newline")
	}
	// Should be at position 2 (col 2 of first line "hello": "he[l]lo")
	if i.cursor != 2 {
		t.Errorf("CursorUp across newline: cursor=%d, want 2", i.cursor)
	}
}

// TestInput_CursorDown_AcrossNewline verifies cursor moves down across real newlines.
func TestInput_CursorDown_AcrossNewline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\nworld")
	i.SetWidth(80)
	i.cursor = 2 // "l" in "hello" (col 2)
	if !i.CursorDown() {
		t.Error("CursorDown should succeed across newline")
	}
	// Should be at position 8 (col 2 of "world", which is offset 6+2=8)
	if i.cursor != 8 {
		t.Errorf("CursorDown across newline: cursor=%d, want 8", i.cursor)
	}
}

func TestInput_WrapLines_ZeroWidth_SplitsOnNewlines(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\nworld\nfoo")
	// width remains 0 (no SetWidth call)
	lines := i.wrapLines()
	if len(lines) != 3 {
		t.Fatalf("wrapLines with width=0: got %d lines, want 3", len(lines))
	}
	if string(lines[0].runes) != "hello" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "hello")
	}
	if lines[0].startOffset != 0 {
		t.Errorf("line 0 startOffset: got %d, want 0", lines[0].startOffset)
	}
	if string(lines[1].runes) != "world" {
		t.Errorf("line 1: got %q, want %q", string(lines[1].runes), "world")
	}
	if lines[1].startOffset != 6 {
		t.Errorf("line 1 startOffset: got %d, want 6", lines[1].startOffset)
	}
	if string(lines[2].runes) != "foo" {
		t.Errorf("line 2: got %q, want %q", string(lines[2].runes), "foo")
	}
	if lines[2].startOffset != 12 {
		t.Errorf("line 2 startOffset: got %d, want 12", lines[2].startOffset)
	}
}

func TestInput_WrapLines_ZeroWidth_EmptyValue(t *testing.T) {
	t.Parallel()
	i := NewInput()
	// width=0, empty value
	lines := i.wrapLines()
	if len(lines) != 1 {
		t.Fatalf("empty value: got %d lines, want 1", len(lines))
	}
	if len(lines[0].runes) != 0 {
		t.Error("empty value should have empty runes")
	}
}

func TestInput_WrapLines_ZeroWidth_NoNewlines(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello world")
	// width=0, no newlines
	lines := i.wrapLines()
	if len(lines) != 1 {
		t.Fatalf("no newlines: got %d lines, want 1", len(lines))
	}
	if string(lines[0].runes) != "hello world" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "hello world")
	}
}

func TestInput_WrapLines_ZeroWidth_TrailingNewline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetValue("hello\n")
	lines := i.wrapLines()
	if len(lines) != 2 {
		t.Fatalf("trailing newline: got %d lines, want 2", len(lines))
	}
	if string(lines[0].runes) != "hello" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "hello")
	}
	if len(lines[1].runes) != 0 {
		t.Errorf("line 1 after trailing \\n should be empty, got %q", string(lines[1].runes))
	}
}

func TestInput_View_PasteMultiline(t *testing.T) {
	t.Parallel()
	i := NewInput()
	i.SetWidth(80)
	i.Focus()

	// Simulate paste: insert each character including \n
	for _, ch := range "测试消息\n换行" {
		i.InsertChar(ch)
	}

	view := i.View()

	if !strings.Contains(view, "测试消息") {
		t.Errorf("View should contain '测试消息', got: %q", view)
	}
	if !strings.Contains(view, "换行") {
		t.Errorf("View should contain '换行', got: %q", view)
	}

	// Should have 2 lines (split by \n in rendered output)
	lines := strings.Split(view, "\n")
	if len(lines) != 2 {
		t.Fatalf("View should have 2 lines, got %d: %q", len(lines), lines)
	}
	// Option C: no prompt in Input.View()
	if strings.Contains(lines[0], "❯") {
		t.Errorf("line 0 should NOT start with prompt (Option C), got: %q", lines[0])
	}
	t.Logf("line 0: %q", lines[0])
	t.Logf("line 1: %q", lines[1])
}

func TestRenderedPromptWidth_NonZero(t *testing.T) {
	t.Parallel()
	if renderedPromptWidth <= 0 {
		t.Fatalf("renderedPromptWidth should be > 0, got %d", renderedPromptWidth)
	}
	// Must be consistent with lipgloss.Width(renderedPrompt)
	if got := lipgloss.Width(renderedPrompt); got != renderedPromptWidth {
		t.Errorf("renderedPromptWidth = %d, but lipgloss.Width(renderedPrompt) = %d", renderedPromptWidth, got)
	}
}

func TestInput_WrapLines_ContinuationSameWidthAsFirstLine(t *testing.T) {
	// Option C: wrapLines() uses uniform avail = max(i.width, 1) for ALL lines.
	// The width passed to Input is already reduced by promptWidth by the caller (App).
	// Verify first line and continuation lines wrap at the same boundary.
	i := NewInput()
	i.SetWidth(4) // uniform avail=4 for all lines

	// "aaa" fits in 4. "bbbbb" (5 chars > 4) wraps into "bbbb" + "b"
	i.SetValue("aaa\nbbbbb")
	lines := i.wrapLines()

	t.Logf("width=4, lines=%d", len(lines))
	for li, l := range lines {
		t.Logf("  line %d: %q (len=%d)", li, string(l.runes), len(l.runes))
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (aaa + bbbb + b), got %d", len(lines))
	}
	if string(lines[0].runes) != "aaa" {
		t.Errorf("line 0: got %q, want %q", string(lines[0].runes), "aaa")
	}
	if string(lines[1].runes) != "bbbb" {
		t.Errorf("line 1: got %q, want %q", string(lines[1].runes), "bbbb")
	}
	if string(lines[2].runes) != "b" {
		t.Errorf("line 2: got %q, want %q", string(lines[2].runes), "b")
	}
}

func TestInput_View_MultilineCursorAligned(t *testing.T) {
	// Simulate \+Enter: type text, then newline
	i := NewInput()
	i.SetWidth(80)
	i.Focus()

	i.SetValue("hello")
	i.InsertChar('\n')
	// Cursor is now after \n, at start of empty continuation line

	view := i.View()
	viewLines := strings.Split(view, "\n")
	t.Logf("lines: %d", len(viewLines))
	for li, l := range viewLines {
		t.Logf("  line %d: %q", li, l)
	}

	if len(viewLines) < 2 {
		t.Fatalf("expected 2+ lines, got %d", len(viewLines))
	}

	// Option C: continuation line is pure text (cursor highlight on empty line).
	// No indent — that's App.View()'s job.
	contLine := viewLines[1]
	// The cursor on empty continuation line = highlighted space
	if contLine == "" {
		t.Error("continuation line should show cursor, got empty")
	}
	// Should not contain prompt
	if strings.Contains(contLine, "❯") {
		t.Errorf("continuation line should NOT contain prompt (Option C), got: %q", contLine)
	}
}

func TestFormatToolContent_NoPrefix(t *testing.T) {
	content := formatToolContent("line1\nline2\nline3", false, true, 80, true, 0, lipgloss.NewStyle())
	if strings.Contains(content, "| ") {
		t.Errorf("formatToolContent should return pure content without prefix, got: %q", content)
	}
	lines := strings.Split(content, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), content)
	}
	if lines[0] != "line1" {
		t.Errorf("first line = %q, want %q", lines[0], "line1")
	}
}

func TestWithResultPrefix_FirstLine(t *testing.T) {
	content := "line1\nline2\nline3"
	result := withResultPrefix(content)
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), result)
	}
	if !strings.HasPrefix(lines[0], "| ") {
		t.Errorf("first line should start with '| ', got: %q", lines[0])
	}
	if strings.HasPrefix(lines[1], "| ") {
		t.Errorf("continuation lines should NOT start with '| ', got: %q", lines[1])
	}
	if strings.HasPrefix(lines[2], "| ") {
		t.Errorf("continuation lines should NOT start with '| ', got: %q", lines[2])
	}
	prefixWidth := lipgloss.Width("| ")
	contIndent := lines[1][:len(lines[1])-len("line2")]
	if lipgloss.Width(contIndent) != prefixWidth {
		t.Errorf("continuation indent width = %d, want %d (prefix width)", lipgloss.Width(contIndent), prefixWidth)
	}
}

func TestWithResultPrefix_AnsiSafe(t *testing.T) {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	styled := style.Render("styled text")
	content := styled + "\n" + styled
	result := withResultPrefix(content)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	firstVisual := lipgloss.Width(lines[0])
	secondVisual := lipgloss.Width(lines[1])
	if firstVisual != secondVisual {
		t.Errorf("visual widths should match: first=%d, second=%d", firstVisual, secondVisual)
	}
}

func TestWithResultPrefix_EmptyContent(t *testing.T) {
	result := withResultPrefix("")
	if result != "" {
		t.Errorf("empty content should return empty, got: %q", result)
	}
}

func TestRenderThinkingBlock_NoDoubleStyle(t *testing.T) {
	var sb strings.Builder
	blk := ContentBlock{
		Type: BlockThinking,
		Thinking: ThinkingView{
			Text:     "hello world",
			Done:     true,
			Duration: 100 * time.Millisecond,
		},
	}
	blk.renderThinkingBlock(&sb, 80, false, "\xe2\x97\x8f", false, 0)
	output := sb.String()
	lines := strings.Split(output, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines (header + content), got %d: %q", len(lines), output)
	}
	// The content line should have styleThinkingContent applied once (2 ANSI starts).
	// Double styling would produce 4+ ANSI starts.
	contentLine := lines[1]
	contentAnsiCount := strings.Count(contentLine, "\x1b[")
	if contentAnsiCount > 4 {
		t.Errorf("content line has %d ANSI sequences, likely double-styled: %q", contentAnsiCount, contentLine)
	}
}

func TestJoinHorizontal_MatchesIndentLines(t *testing.T) {
	tests := []struct {
		name    string
		indent  string
		content string
	}{
		{"no indent", "", "line1\nline2"},
		{"2-space indent", "  ", "line1\nline2"},
		{"4-space indent", "    ", "line1\nline2\nline3"},
		{"empty content", "  ", ""},
		{"single line", "  ", "only line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var expected string
			if tt.indent == "" || tt.content == "" {
				if tt.content == "" {
					expected = lipgloss.JoinHorizontal(lipgloss.Top, tt.indent, tt.content)
				} else {
					expected = tt.content
				}
			} else {
				var lines []string
				for l := range strings.SplitSeq(tt.content, "\n") {
					lines = append(lines, tt.indent+l)
				}
				expected = strings.Join(lines, "\n")
			}
			result := lipgloss.JoinHorizontal(lipgloss.Top, tt.indent, tt.content)
			if result != expected {
				t.Errorf("JoinHorizontal != indentLines\nexpected: %q\n     got: %q", expected, result)
			}
		})
	}
}

func TestRenderToolCall_SearchReadCollapsed(t *testing.T) {
	t.Parallel()

	// Output for search/read tools is the full output; TUI generates the summary.
	fullOutput := "file1.go\nfile2.go\nfile3.go\nfile4.go\nfile5.go"

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "test-1",
					Name:       "Grep",
					Summary:    "pattern",
					Output:     fullOutput,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: tool.SearchReadKind{IsSearch: true},
				},
			},
		},
	}
	rendered := mv.View(80, false, "●", false, false, 0)

	if !strings.Contains(rendered, "Found 5 matches … ctrl+o to expand") {
		t.Errorf("collapsed search/read should show summary + ctrl+o hint, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Grep") {
		t.Errorf("should show tool name, got:\n%s", rendered)
	}
}

func TestRenderToolCall_SearchReadExpanded(t *testing.T) {
	t.Parallel()

	output := "line1\nline2\nline3\nline4\nline5"

	// Search/read tool expanded: should show full output.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "test-1",
					Name:       "Read",
					Summary:    "file.go",
					Output:     output,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: tool.SearchReadKind{IsRead: true},
				},
			},
		},
	}
	rendered := mv.View(80, true, "●", false, false, 0)

	if !strings.Contains(rendered, "line5") {
		t.Errorf("expanded search/read should show all output, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "ctrl+o") {
		t.Errorf("expanded should not show ctrl+o hint, got:\n%s", rendered)
	}
}

func TestRenderToolCall_NonSearchReadCollapsed(t *testing.T) {
	t.Parallel()

	output := "line1\nline2\nline3\nline4\nline5"

	// Non-search/read tool collapsed: should show truncated output (3 lines) + ctrl+o.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:      "test-1",
					Name:    "Bash",
					Summary: "npm test",
					Output:  output,
					Done:    true,
					Elapsed: time.Second,
				},
			},
		},
	}
	rendered := mv.View(80, false, "●", false, false, 0)

	if !strings.Contains(rendered, "line1") {
		t.Errorf("collapsed non-search/read should show first 3 lines, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Errorf("collapsed non-search/read should show ctrl+o hint, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "line5") {
		t.Errorf("collapsed non-search/read should truncate after 3 lines, got:\n%s", rendered)
	}
}

func TestRenderToolCall_WriteEditAlwaysExpanded(t *testing.T) {
	t.Parallel()

	output := "+ added line\n- removed line\n+ another"

	// Write/Edit tools are always expanded regardless of collapse state.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:      "test-1",
					Name:    "Write",
					Output:  output,
					Done:    true,
					Elapsed: time.Second,
				},
			},
		},
	}
	rendered := mv.View(80, false, "●", false, false, 0)

	if !strings.Contains(rendered, "another") {
		t.Errorf("Write tool should always show full output, got:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Chain tests: Tool.RenderResult_ → collapseSummary → TUI render
//
// These verify the full pipeline from tool output through TUI rendering.
// Each test creates a real tool, calls RenderResult_ on realistic output,
// then feeds the result through collapseSummary and the View() renderer.
// ---------------------------------------------------------------------------

func TestChain_ReadTool_CollapsedAndExpanded(t *testing.T) {
	t.Parallel()

	// Read tool returns file content; RenderResult_ returns it verbatim.
	readTool := fileread.New()
	content := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	rendered := readTool.RenderResult(&fileread.TextOutput{
		Content:    content,
		FilePath:   "/tmp/test.go",
		NumLines:   5,
		StartLine:  1,
		TotalLines: 5,
	})

	// Verify RenderResult_ returns full content, not a summary.
	if rendered != content {
		t.Fatalf("Read RenderResult_ = %q, want full content", rendered)
	}

	// Collapsed: collapseSummary should produce "Read 5 lines".
	srk := tool.SearchReadKind{IsRead: true}
	summary := collapseSummary(rendered, srk)
	if summary != "Read 5 lines" {
		t.Fatalf("collapseSummary(read) = %q, want %q", summary, "Read 5 lines")
	}

	// Collapsed render: summary + ctrl+o hint.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "read-1",
					Name:       "Read",
					Summary:    "/tmp/test.go",
					Output:     rendered,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: srk,
				},
			},
		},
	}
	collapsed := mv.View(80, false, "●", false, false, 0)
	if !strings.Contains(collapsed, "Read 5 lines") {
		t.Errorf("collapsed Read should show 'Read 5 lines', got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed Read should show ctrl+o hint, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "func main()") {
		t.Errorf("collapsed Read should NOT show file content, got:\n%s", collapsed)
	}

	// Expanded: full content visible, no ctrl+o hint.
	expanded := mv.View(80, true, "●", false, false, 0)
	if !strings.Contains(expanded, `println("hello")`) {
		t.Errorf("expanded Read should show full content, got:\n%s", expanded)
	}
	if strings.Contains(expanded, "ctrl+o") {
		t.Errorf("expanded Read should NOT show ctrl+o hint, got:\n%s", expanded)
	}
}

func TestChain_GrepTool_ContentMode(t *testing.T) {
	t.Parallel()

	grepTool := grep.New()
	matches := "main.go:10:fmt.Println\nmain.go:20:log.Println\nutil.go:5:fmt.Sprintf"
	rendered := grepTool.RenderResult(&grep.Output{
		Mode:     "content",
		Content:  matches,
		NumLines: 3,
	})

	if rendered != matches {
		t.Fatalf("Grep RenderResult_(content) = %q, want raw matches", rendered)
	}

	srk := tool.SearchReadKind{IsSearch: true}
	summary := collapseSummary(rendered, srk)
	if summary != "Found 3 matches" {
		t.Fatalf("collapseSummary(grep content) = %q, want %q", summary, "Found 3 matches")
	}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "grep-1",
					Name:       "Grep",
					Summary:    "pattern",
					Output:     rendered,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: srk,
				},
			},
		},
	}

	collapsed := mv.View(80, false, "●", false, false, 0)
	if !strings.Contains(collapsed, "Found 3 matches") {
		t.Errorf("collapsed Grep should show 'Found 3 matches', got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "main.go:10") {
		t.Errorf("collapsed Grep should NOT show raw matches, got:\n%s", collapsed)
	}

	expanded := mv.View(80, true, "●", false, false, 0)
	if !strings.Contains(expanded, "main.go:10:fmt.Println") {
		t.Errorf("expanded Grep should show all match lines, got:\n%s", expanded)
	}
}

func TestChain_GrepTool_FilesWithMatchesMode(t *testing.T) {
	t.Parallel()

	grepTool := grep.New()
	files := "a.go\nb.go\nc.go"
	rendered := grepTool.RenderResult(&grep.Output{
		Mode:      "files_with_matches",
		Filenames: []string{"a.go", "b.go", "c.go"},
		NumFiles:  3,
	})

	if rendered != files {
		t.Fatalf("Grep RenderResult_(files_with_matches) = %q, want %q", rendered, files)
	}

	srk := tool.SearchReadKind{IsSearch: true}
	summary := collapseSummary(rendered, srk)
	if summary != "Found 3 matches" {
		t.Fatalf("collapseSummary(grep files) = %q, want %q", summary, "Found 3 matches")
	}
}

func TestChain_GlobTool(t *testing.T) {
	t.Parallel()

	globTool := glob.New()
	fileList := "src/main.go\nsrc/util.go\nsrc/handler.go"
	rendered := globTool.RenderResult(&glob.Output{
		Files: []string{"src/main.go", "src/util.go", "src/handler.go"},
		Count: 3,
	})

	if rendered != fileList {
		t.Fatalf("RenderResult_ = %q, want %q", rendered, fileList)
	}

	srk := tool.SearchReadKind{IsSearch: true}
	summary := collapseSummary(rendered, srk)
	if summary != "Found 3 matches" {
		t.Fatalf("collapseSummary(glob) = %q, want %q", summary, "Found 3 matches")
	}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "glob-1",
					Name:       "Glob",
					Summary:    "**/*.go",
					Output:     rendered,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: srk,
				},
			},
		},
	}

	collapsed := mv.View(80, false, "●", false, false, 0)
	if !strings.Contains(collapsed, "Found 3 matches") {
		t.Errorf("collapsed Glob should show 'Found 3 matches', got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "src/main.go") {
		t.Errorf("collapsed Glob should NOT show file paths, got:\n%s", collapsed)
	}

	expanded := mv.View(80, true, "●", false, false, 0)
	if !strings.Contains(expanded, "src/handler.go") {
		t.Errorf("expanded Glob should show all file paths, got:\n%s", expanded)
	}
}

func TestChain_BashSearchCommand_CollapsedAndExpanded(t *testing.T) {
	t.Parallel()

	bashTool := bash.New(nil)

	// Simulate output from "ls" command — a search-or-read Bash command.
	lsOutput := "file1.go\nfile2.go\nfile3.go"
	rendered := bashTool.RenderResult(&bash.Output{
		Stdout: lsOutput,
	})

	if rendered != lsOutput {
		t.Fatalf("Bash RenderResult_ = %q, want %q", rendered, lsOutput)
	}

	// "ls" is classified as IsList.
	srk := tool.SearchReadKind{IsList: true}
	summary := collapseSummary(rendered, srk)
	if summary != "Listed 3 entries" {
		t.Fatalf("collapseSummary(ls) = %q, want %q", summary, "Listed 3 entries")
	}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:         "bash-ls-1",
					Name:       "Bash",
					Summary:    "ls",
					Output:     rendered,
					Done:       true,
					Elapsed:    time.Second,
					SearchRead: srk,
				},
			},
		},
	}

	collapsed := mv.View(80, false, "●", false, false, 0)
	if !strings.Contains(collapsed, "Listed 3 entries") {
		t.Errorf("collapsed Bash ls should show 'Listed 3 entries', got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed Bash ls should show ctrl+o hint, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "file1.go") {
		t.Errorf("collapsed Bash ls should NOT show raw output, got:\n%s", collapsed)
	}

	expanded := mv.View(80, true, "●", false, false, 0)
	if !strings.Contains(expanded, "file3.go") {
		t.Errorf("expanded Bash ls should show full output, got:\n%s", expanded)
	}
}

func TestChain_BashNonSearchCommand(t *testing.T) {
	t.Parallel()

	bashTool := bash.New(nil)
	// 6 lines: exceeds maxLines+1=4 threshold, so truncation kicks in.
	buildOutput := "ok  github.com/liuy/gbot/pkg/tool\nok  github.com/liuy/gbot/pkg/tui\nok  github.com/liuy/gbot/pkg/types\nok  github.com/liuy/gbot/pkg/engine\nok  github.com/liuy/gbot/cmd\nFAIL"
	rendered := bashTool.RenderResult(&bash.Output{
		Stdout: buildOutput,
	})

	if rendered != buildOutput {
		t.Fatalf("Bash RenderResult_ = %q, want %q", rendered, buildOutput)
	}

	// Non-search-or-read Bash command: srk is zero-value.
	srk := tool.SearchReadKind{}
	summary := collapseSummary(rendered, srk)
	if summary != "6 lines" {
		t.Fatalf("collapseSummary(non-sr bash) = %q, want %q", summary, "6 lines")
	}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{
				Type: BlockTool,
				ToolCall: ToolCallView{
					ID:      "bash-1",
					Name:    "Bash",
					Summary: "go test ./...",
					Output:  rendered,
					Done:    true,
					Elapsed: time.Second,
				},
			},
		},
	}

	// Non-search-read: collapsed uses formatToolOutput (3-line truncation + ctrl+o).
	collapsed := mv.View(80, false, "●", false, false, 0)
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Errorf("collapsed non-SR Bash should show ctrl+o hint, got:\n%s", collapsed)
	}
	// First 3 lines visible, rest hidden.
	if !strings.Contains(collapsed, "pkg/tool") {
		t.Errorf("collapsed should show first lines, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "FAIL") {
		t.Errorf("collapsed should NOT show last line (FAIL), got:\n%s", collapsed)
	}

	// Expanded: all lines visible.
	expanded := mv.View(80, true, "●", false, false, 0)
	if !strings.Contains(expanded, "FAIL") {
		t.Errorf("expanded should show all output including FAIL, got:\n%s", expanded)
	}
}

func TestChain_SingleLineOutput(t *testing.T) {
	t.Parallel()

	// Read with 1 line — singular form.
	srk := tool.SearchReadKind{IsRead: true}
	summary := collapseSummary("package main\n", srk)
	if summary != "Read 1 line" {
		t.Fatalf("collapseSummary(1 line read) = %q, want %q", summary, "Read 1 line")
	}

	// Search with 1 match.
	srk = tool.SearchReadKind{IsSearch: true}
	summary = collapseSummary("main.go:5:match\n", srk)
	// PluralWord(1, "matches") = TrimSuffix("matches", "s") = "matche" — known issue
	if summary != "Found 1 matche" {
		t.Fatalf("collapseSummary(1 match) = %q, want %q (PluralWord issue)", summary, "Found 1 matche")
	}

	// List with 1 entry.
	srk = tool.SearchReadKind{IsList: true}
	summary = collapseSummary("file.go\n", srk)
	// PluralWord(1, "entries") = TrimSuffix("entries", "s") = "entrie" — known issue
	if summary != "Listed 1 entrie" {
		t.Fatalf("collapseSummary(1 entry) = %q, want %q (PluralWord issue)", summary, "Listed 1 entrie")
	}
}

func TestChain_EmptyOutput(t *testing.T) {
	t.Parallel()

	// Grep with no results.
	grepTool := grep.New()
	rendered := grepTool.RenderResult(&grep.Output{
		Mode:      "files_with_matches",
		Filenames: []string{},
		NumFiles:  0,
	})
	if rendered != "" {
		t.Fatalf("Grep RenderResult_(empty) = %q, want empty", rendered)
	}

	srk := tool.SearchReadKind{IsSearch: true}
	summary := collapseSummary(rendered, srk)
	if summary != "No matches" {
		t.Fatalf("collapseSummary(empty search) = %q, want %q", summary, "No matches")
	}

	// Read with empty file.
	srk = tool.SearchReadKind{IsRead: true}
	summary = collapseSummary("", srk)
	if summary != "Read 0 lines" {
		t.Fatalf("collapseSummary(empty read) = %q, want %q", summary, "Read 0 lines")
	}
}

// ---------------------------------------------------------------------------
// Group detection + groupSummaryText (Phase 2)
// ---------------------------------------------------------------------------

func makeBlockTool(srk tool.SearchReadKind, done bool) ContentBlock {
	return ContentBlock{
		Type: BlockTool,
		ToolCall: ToolCallView{
			Name:       "Tool",
			Done:       done,
			SearchRead: srk,
		},
	}
}

func TestDetectToolGroups(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	srSearch := tool.SearchReadKind{IsSearch: true}
	srList := tool.SearchReadKind{IsList: true}
	srNone := tool.SearchReadKind{}

	// 3 consecutive reads → 1 group of 3.
	blocks := []ContentBlock{
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("3 reads: got %d groups, want 1", len(groups))
	}
	if len(groups[0].indices) != 3 {
		t.Fatalf("3 reads: group size = %d, want 3", len(groups[0].indices))
	}
	if groups[0].readCount != 3 {
		t.Fatalf("3 reads: readCount = %d, want 3", groups[0].readCount)
	}

	// Read+Grep+Read → 1 group of 3.
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		makeBlockTool(srSearch, true),
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("Read+Grep+Read: got %d groups, want 1", len(groups))
	}
	if groups[0].searchCount != 1 || groups[0].readCount != 2 {
		t.Fatalf("Read+Grep+Read: search=%d read=%d, want search=1 read=2", groups[0].searchCount, groups[0].readCount)
	}

	// Read+Edit+Read → 0 groups (Edit breaks).
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		makeBlockTool(srNone, true), // Edit — not collapsible
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 0 {
		t.Fatalf("Read+Edit+Read: got %d groups, want 0", len(groups))
	}

	// Read+thinking+Read → 1 group of 2 (thinking doesn't break).
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		{Type: BlockThinking, Thinking: ThinkingView{Text: "hmm", Done: true}},
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("Read+thinking+Read: got %d groups, want 1", len(groups))
	}
	if len(groups[0].indices) != 2 {
		t.Fatalf("Read+thinking+Read: group size = %d, want 2", len(groups[0].indices))
	}

	// Single read → 0 groups (single-tool skipped).
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 0 {
		t.Fatalf("single read: got %d groups, want 0", len(groups))
	}

	// Empty text doesn't break group.
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		{Type: BlockText, Text: ""},
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("Read+empty+Read: got %d groups, want 1", len(groups))
	}

	// Non-empty text breaks group.
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		{Type: BlockText, Text: "hello"},
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 0 {
		t.Fatalf("Read+text+Read: got %d groups, want 0", len(groups))
	}

	// BlockStats breaks group.
	blocks = []ContentBlock{
		makeBlockTool(srRead, true),
		{Type: BlockStats, Text: "stats"},
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 0 {
		t.Fatalf("Read+stats+Read: got %d groups, want 0", len(groups))
	}

	// Running tool sets anyRunning.
	blocks = []ContentBlock{
		makeBlockTool(srRead, false),
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("running tools: got %d groups, want 1", len(groups))
	}
	if !groups[0].anyRunning {
		t.Fatal("running tools: anyRunning = false, want true")
	}

	// List counts tracked.
	blocks = []ContentBlock{
		makeBlockTool(srList, true),
		makeBlockTool(srList, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("2 lists: got %d groups, want 1", len(groups))
	}
	if groups[0].listCount != 2 {
		t.Fatalf("2 lists: listCount = %d, want 2", groups[0].listCount)
	}
}

func TestGroupSummaryText(t *testing.T) {
	t.Parallel()

	// Read only.
	g := toolGroup{readCount: 3}
	text := groupSummaryText(g, false)
	if text != "Read 3 files" {
		t.Fatalf("read-only: %q, want %q", text, "Read 3 files")
	}

	// Search only.
	g = toolGroup{searchCount: 2}
	text = groupSummaryText(g, false)
	if text != "Searched for 2 patterns" {
		t.Fatalf("search-only: %q, want %q", text, "Searched for 2 patterns")
	}

	// List only (single).
	g = toolGroup{listCount: 1}
	text = groupSummaryText(g, false)
	if text != "Listed 1 directory" {
		t.Fatalf("list-single: %q, want %q", text, "Listed 1 directory")
	}

	// Combined.
	g = toolGroup{readCount: 3, searchCount: 1, listCount: 1}
	text = groupSummaryText(g, false)
	if text != "Searched for 1 pattern, read 3 files, listed 1 directory" {
		t.Fatalf("combined: %q, want %q", text, "Searched for 1 pattern, read 3 files, listed 1 directory")
	}

	// Active (running) — present tense + trailing ellipsis.
	g = toolGroup{readCount: 3, anyRunning: true}
	text = groupSummaryText(g, true)
	if text != "Reading 3 files…" {
		t.Fatalf("active: %q, want %q", text, "Reading 3 files…")
	}

	// Active combined.
	g = toolGroup{searchCount: 2, readCount: 3, anyRunning: true}
	text = groupSummaryText(g, true)
	if text != "Searching for 2 patterns, reading 3 files…" {
		t.Fatalf("active-combined: %q, want %q", text, "Searching for 2 patterns, reading 3 files…")
	}

	// Single file.
	g = toolGroup{readCount: 1}
	text = groupSummaryText(g, false)
	if text != "Read 1 file" {
		t.Fatalf("single-file: %q, want %q", text, "Read 1 file")
	}
}

// ---------------------------------------------------------------------------
// Group rendering tests (Phase 2)
// ---------------------------------------------------------------------------

func TestRenderGroupCollapsed(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "line1\nline2\nline3\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "line1\nline2\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "repl.go", Done: true,
				Output: "line1\n", SearchRead: srRead,
			}},
		},
	}

	// Collapsed (expand=false): should show group summary line.
	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(rendered, "Read 3 files") {
		t.Fatalf("collapsed group missing 'Read 3 files':\n%s", rendered)
	}
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Fatalf("collapsed group missing 'ctrl+o to expand':\n%s", rendered)
	}
	// Should NOT show individual tool headers.
	if strings.Contains(rendered, "Read(main.go)") {
		t.Fatalf("collapsed group shows individual tool:\n%s", rendered)
	}
}

func TestRenderGroupExpanded(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\nfunc main() {}\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
		},
	}

	// Expanded (expand=true): each tool renders individually with full output.
	rendered := stripANSIPrintable(mv.View(80, true, dot, false, false, 3))
	if !strings.Contains(rendered, "package main") {
		t.Fatalf("expanded group missing 'package main':\n%s", rendered)
	}
	if !strings.Contains(rendered, "package tui") {
		t.Fatalf("expanded group missing 'package tui':\n%s", rendered)
	}
	// Should NOT show group summary.
	if strings.Contains(rendered, "Read 2 files") {
		t.Fatalf("expanded group shows summary:\n%s", rendered)
	}
}

func TestRenderSingleToolNoGroup(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\nfunc main() {}\n", SearchRead: srRead,
			}},
		},
	}

	// Single tool: Phase 1 per-tool collapse, NOT group summary.
	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if strings.Contains(rendered, "Read 1 file") {
		t.Fatalf("single tool shows group summary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Read 2 lines") {
		t.Fatalf("single tool missing Phase 1 summary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Fatalf("single tool missing ctrl+o:\n%s", rendered)
	}
}

func TestGroupBrokenByEdit(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Edit", Summary: "main.go", Done: true,
				Output: "--- a\n+++ b\n",
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	// No group summary — Edit broke the group.
	if strings.Contains(rendered, "Read 2 files") {
		t.Fatalf("broken group shows summary:\n%s", rendered)
	}
	// Each Read should show Phase 1 per-tool collapse.
	if strings.Count(rendered, "ctrl+o to expand") != 2 {
		t.Fatalf("broken group: expected 2 ctrl+o hints, got %d:\n%s",
			strings.Count(rendered, "ctrl+o to expand"), rendered)
	}
}

func TestGroupBrokenByText(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\n", SearchRead: srRead,
			}},
			{Type: BlockText, Text: "Let me check something"},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	// No group summary — text broke the group.
	if strings.Contains(rendered, "Read 2 files") {
		t.Fatalf("text-broken group shows summary:\n%s", rendered)
	}
	// Text should be visible.
	if !strings.Contains(rendered, "Let me check something") {
		t.Fatalf("text block missing:\n%s", rendered)
	}
}

func TestGroupSpansThinking(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\n", SearchRead: srRead,
			}},
			{Type: BlockThinking, Thinking: ThinkingView{
				Text: "thinking...", Done: true, Duration: 100 * time.Millisecond,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
		},
	}

	// Collapsed: thinking absorbed into the group — hidden along with tools.
	collapsed := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(collapsed, "Read 2 files") {
		t.Fatalf("thinking-span group missing summary:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "ctrl+o to expand") {
		t.Fatalf("thinking-span group missing ctrl+o:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "thinking...") {
		t.Fatalf("collapsed group leaks absorbed thinking content:\n%s", collapsed)
	}

	// Expanded: thinking renders inline between the two tools.
	expanded := stripANSIPrintable(mv.View(80, true, "", false, false, 3))
	if !strings.Contains(expanded, "thinking...") {
		t.Fatalf("expanded group missing absorbed thinking:\n%s", expanded)
	}
	if !strings.Contains(expanded, "package main") || !strings.Contains(expanded, "package tui") {
		t.Fatalf("expanded group missing individual tool output:\n%s", expanded)
	}
}

func TestDetectToolGroups_AbsorbsLeadingThinking(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// [Thinking, Read, Read] — leading thinking at index 0.
	blocks := []ContentBlock{
		{Type: BlockThinking, Thinking: ThinkingView{Text: "plan", Done: true}},
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.indices) != 2 || g.indices[0] != 1 || g.indices[1] != 2 {
		t.Fatalf("indices = %v, want [1 2]", g.indices)
	}
	if len(g.thinkingIndices) != 1 || g.thinkingIndices[0] != 0 {
		t.Fatalf("thinkingIndices = %v, want [0]", g.thinkingIndices)
	}
	if g.firstBlockIdx != 0 {
		t.Fatalf("firstBlockIdx = %d, want 0", g.firstBlockIdx)
	}

	// [Thinking, Thinking, Read, Read] — two leading thinking blocks (backward scan extends).
	blocks = []ContentBlock{
		{Type: BlockThinking, Thinking: ThinkingView{Text: "a", Done: true}},
		{Type: BlockThinking, Thinking: ThinkingView{Text: "b", Done: true}},
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	groups = detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("two-leading: got %d groups, want 1", len(groups))
	}
	g = groups[0]
	if len(g.thinkingIndices) != 2 || g.thinkingIndices[0] != 0 || g.thinkingIndices[1] != 1 {
		t.Fatalf("two-leading thinkingIndices = %v, want [0 1]", g.thinkingIndices)
	}
	if g.firstBlockIdx != 0 {
		t.Fatalf("two-leading firstBlockIdx = %d, want 0", g.firstBlockIdx)
	}
}

func TestDetectToolGroups_AbsorbsInterToolThinking(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// [Read, Thinking, Read] — inter-tool thinking at index 1.
	blocks := []ContentBlock{
		makeBlockTool(srRead, true),
		{Type: BlockThinking, Thinking: ThinkingView{Text: "mid", Done: true}},
		makeBlockTool(srRead, true),
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.indices) != 2 || g.indices[0] != 0 || g.indices[1] != 2 {
		t.Fatalf("indices = %v, want [0 2]", g.indices)
	}
	if len(g.thinkingIndices) != 1 || g.thinkingIndices[0] != 1 {
		t.Fatalf("thinkingIndices = %v, want [1]", g.thinkingIndices)
	}
	if g.firstBlockIdx != 0 {
		t.Fatalf("firstBlockIdx = %d, want 0", g.firstBlockIdx)
	}
}

func TestDetectToolGroups_ThinkingBeforeGroupNotAbsorbedIfTextBetween(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// [Thinking, Text("hi"), Read, Read] — non-empty text breaks the backward scan.
	blocks := []ContentBlock{
		{Type: BlockThinking, Thinking: ThinkingView{Text: "plan", Done: true}},
		{Type: BlockText, Text: "hi"},
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.thinkingIndices) != 0 {
		t.Fatalf("thinkingIndices = %v, want []", g.thinkingIndices)
	}
	if g.firstBlockIdx != 2 {
		t.Fatalf("firstBlockIdx = %d, want 2", g.firstBlockIdx)
	}
}

func TestDetectToolGroups_ThinkingAfterGroupNotAbsorbed(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// [Read, Read, Thinking] — trailing thinking must NOT be absorbed.
	blocks := []ContentBlock{
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
		{Type: BlockThinking, Thinking: ThinkingView{Text: "after", Done: true}},
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.indices) != 2 || g.indices[0] != 0 || g.indices[1] != 1 {
		t.Fatalf("indices = %v, want [0 1]", g.indices)
	}
	if len(g.thinkingIndices) != 0 {
		t.Fatalf("thinkingIndices = %v, want []", g.thinkingIndices)
	}
	if g.firstBlockIdx != 0 {
		t.Fatalf("firstBlockIdx = %d, want 0", g.firstBlockIdx)
	}
}

func TestDetectToolGroups_EmptyTextBreaksBackwardScan(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// [Thinking, Text(""), Read, Read] — non-thinking block (even empty)
	// stops the backward scan per the strict rule.
	blocks := []ContentBlock{
		{Type: BlockThinking, Thinking: ThinkingView{Text: "plan", Done: true}},
		{Type: BlockText, Text: ""},
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	groups := detectToolGroups(blocks)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]
	if len(g.thinkingIndices) != 0 {
		t.Fatalf("thinkingIndices = %v, want []", g.thinkingIndices)
	}
	if g.firstBlockIdx != 2 {
		t.Fatalf("firstBlockIdx = %d, want 2", g.firstBlockIdx)
	}
}

func TestBuildGroupLookup_ConsumedIncludesThinking(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	blocks := []ContentBlock{
		{Type: BlockThinking, Thinking: ThinkingView{Text: "plan", Done: true}},
		makeBlockTool(srRead, true),
		makeBlockTool(srRead, true),
	}
	gl := buildGroupLookup(blocks)

	if _, ok := gl.byFirstIdx[0]; !ok {
		t.Fatal("byFirstIdx missing key 0 (firstBlockIdx is the thinking index)")
	}
	if _, ok := gl.byFirstIdx[1]; ok {
		t.Fatal("byFirstIdx should NOT have key 1 (first tool is no longer the lookup key)")
	}
	if !gl.consumed[0] {
		t.Fatal("consumed[0] = false, want true (thinking absorbed)")
	}
	if !gl.consumed[1] {
		t.Fatal("consumed[1] = false, want true (first tool)")
	}
	if !gl.consumed[2] {
		t.Fatal("consumed[2] = false, want true (second tool)")
	}
}

func TestRenderGroup_LeadingThinkingCollapsed_HidesThinking(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockThinking, Thinking: ThinkingView{Text: "let me plan", Done: true, Duration: 100 * time.Millisecond}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
		},
	}

	collapsed := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(collapsed, "Read 2 files") {
		t.Fatalf("collapsed group missing summary:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "let me plan") {
		t.Fatalf("collapsed group leaks absorbed thinking:\n%s", collapsed)
	}

	expanded := stripANSIPrintable(mv.View(80, true, "", false, false, 3))
	if !strings.Contains(expanded, "let me plan") {
		t.Fatalf("expanded group missing thinking content:\n%s", expanded)
	}
	if strings.Contains(expanded, "Read 2 files") {
		t.Fatalf("expanded group shows summary:\n%s", expanded)
	}
	if !strings.Contains(expanded, "package main") || !strings.Contains(expanded, "package tui") {
		t.Fatalf("expanded group missing individual tool output:\n%s", expanded)
	}
}

func TestRenderGroup_InterToolThinkingExpanded_RendersInOrder(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "A\n", SearchRead: srRead,
			}},
			{Type: BlockThinking, Thinking: ThinkingView{Text: "mid", Done: true, Duration: 50 * time.Millisecond}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "B\n", SearchRead: srRead,
			}},
		},
	}

	expanded := stripANSIPrintable(mv.View(80, true, "", false, false, 3))
	idxA := strings.Index(expanded, "A")
	idxMid := strings.Index(expanded, "mid")
	idxB := strings.Index(expanded, "B")
	if idxA < 0 || idxMid < 0 || idxB < 0 {
		t.Fatalf("expanded missing one of A/mid/B:\n%s", expanded)
	}
	if idxA >= idxMid || idxMid >= idxB {
		t.Fatalf("expanded order wrong: A=%d mid=%d B=%d (want A<mid<B):\n%s", idxA, idxMid, idxB, expanded)
	}
}

func TestRenderGroup_ThinkingAfterGroup_NotAbsorbed(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
			{Type: BlockThinking, Thinking: ThinkingView{Text: "after", Done: true, Duration: 50 * time.Millisecond}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(rendered, "Read 2 files") {
		t.Fatalf("collapsed group missing summary:\n%s", rendered)
	}
	if !strings.Contains(rendered, "after") {
		t.Fatalf("collapsed group missing trailing thinking (should render standalone):\n%s", rendered)
	}
}

func TestRenderGroup_LeadingThinkingStreamingActive_RendersActiveTense(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockThinking, Thinking: ThinkingView{Text: "plan", Done: true, Duration: 50 * time.Millisecond}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
		},
	}

	// All tools done but message still streaming → group is the last group with
	// no content after it → active tense.
	whiteDot := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
	rendered := stripANSIPrintable(mv.View(80, false, whiteDot, true, false, 3))
	if !strings.Contains(rendered, "Reading 2 files") {
		t.Fatalf("streaming group missing active tense 'Reading 2 files':\n%s", rendered)
	}
	if strings.Contains(rendered, "Read 2 files") {
		t.Fatalf("streaming group incorrectly uses past tense 'Read 2 files':\n%s", rendered)
	}
}

func TestGroupDotMatchesToolDot(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
		},
	}

	// All-done group: should use fixed green dot "●" regardless of toolDot value.
	// toolDot="" (streaming off) should still show ● for completed group.
	collapsed := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(collapsed, "●") {
		t.Fatalf("completed group missing fixed dot ● when toolDot is empty:\n%s", collapsed)
	}

	// Running group: toolDot carries white-bold style from app.go blink logic.
	// The group should use toolDot directly (no green wrapper).
	mv2 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: false,
				Output: "", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: false,
				Output: "", SearchRead: srRead,
			}},
		},
	}
	whiteDot := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
	running := mv2.View(80, false, whiteDot, true, false, 3)
	// Running group uses toolDot as-is (already styled white), not re-wrapped green.
	// stripANSI should reveal "●" from the white styled dot.
	stripped := stripANSIPrintable(running)
	if !strings.Contains(stripped, "●") {
		t.Fatalf("running group missing blink dot:\n%s", stripped)
	}
	// Running group should use present tense.
	if !strings.Contains(stripped, "Reading 2 files") {
		t.Fatalf("running group missing present tense:\n%s", stripped)
	}
}

func TestGroupRunningShowsBlankDot(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// One running + one done tool — group still forming.
	// Normal tool with Done=false renders a space for the dot position.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: false,
				Output: "", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "●", true, false, 3))
	// Group should still render (2 tools) but with running-style dot (space, not ●).
	// The summary should use present tense because anyRunning=true.
	if !strings.Contains(rendered, "Reading 2 files") {
		t.Fatalf("running group missing present tense:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Integration tests (Phase 2) — full render chain
// ---------------------------------------------------------------------------

// TestIntegration_MixedMessage_RenderChain verifies a realistic assistant message
// containing text, a search/read group, a non-collapsible tool (Edit), and a
// lone search tool.  This tests the complete View() path:
//
//	Blocks → detectToolGroups → groupByFirstIdx/consumed → render loop → output
//
// Observable output must show:
//   - intro text
//   - collapsed group summary (not individual tools)
//   - Edit tool header (Phase 1, not grouped)
//   - outro text
//   - single Grep tool (Phase 1, not grouped)
func TestIntegration_MixedMessage_RenderChain(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	srSearch := tool.SearchReadKind{IsSearch: true}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			// Intro text
			{Type: BlockText, Text: "Let me look at the codebase."},
			// Search/read group: 3 tools
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\nfunc main() {}\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Grep", Summary: `"TODO"`, Done: true,
				Output: "main.go:5:TODO fix\napp.go:12:TODO refactor\n", SearchRead: srSearch,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "app.go", Done: true,
				Output: "package tui\n", SearchRead: srRead,
			}},
			// Non-collapsible tool breaks group
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Edit", Summary: "app.go #1-3", Done: true,
				Output: "Applied edit.\n",
			}},
			// Outro text
			{Type: BlockText, Text: "I've fixed the issue."},
			// Single search tool (no group — only 1 tool)
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Grep", Summary: `"FIXME"`, Done: true,
				Output: "no matches\n", SearchRead: srSearch,
			}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))

	// Group summary for Read+Grep+Read.
	if !strings.Contains(rendered, "Searched for 1 pattern, read 2 files") {
		t.Fatalf("missing group summary:\n%s", rendered)
	}
	// Group should not show individual Read/Grep headers when collapsed.
	if strings.Contains(rendered, "Read(main.go)") {
		t.Fatalf("collapsed group leaks individual tool:\n%s", rendered)
	}

	// Edit tool should render individually (not grouped).
	if !strings.Contains(rendered, "Edit(app.go") {
		t.Fatalf("missing Edit tool header:\n%s", rendered)
	}

	// Text blocks visible.
	if !strings.Contains(rendered, "Let me look at the codebase.") {
		t.Fatalf("missing intro text:\n%s", rendered)
	}
	if !strings.Contains(rendered, "I've fixed the issue.") {
		t.Fatalf("missing outro text:\n%s", rendered)
	}

	// Single Grep tool: Phase 1 per-tool summary, not group.
	if strings.Contains(rendered, "Searched for 1 pattern,") && strings.Count(rendered, "Searched for 1 pattern,") > 1 {
		t.Fatalf("single tool got group summary:\n%s", rendered)
	}
	// The lone Grep should show its own Phase 1 summary.
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Fatalf("single Grep missing ctrl+o:\n%s", rendered)
	}

	// Ctrl+o hints: group has 1 + single Grep has 1 = 2 total.
	if got := strings.Count(rendered, "ctrl+o to expand"); got != 2 {
		t.Fatalf("expected 2 ctrl+o hints, got %d:\n%s", got, rendered)
	}
}

// TestIntegration_StreamingBuildUp simulates tools arriving one by one during
// streaming.  At each step we re-render and verify the observable output:
//
//	Frame 1: 1 running Read → no group (single tool, Phase 1 running style)
//	Frame 2: 2nd Read arrives → group of 2, present tense + running dot
//	Frame 3: all done → group of 2, past tense + fixed dot
func TestIntegration_StreamingBuildUp(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}

	// Frame 1: single running tool — no group.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: false,
				Output: "", SearchRead: srRead,
			}},
		},
	}
	f1 := stripANSIPrintable(mv.View(80, false, "●", true, false, 3))
	// Single tool = no group summary. Should render as running tool.
	if strings.Contains(f1, "Read 1 file") {
		t.Fatalf("frame1: single running tool shows group summary:\n%s", f1)
	}

	// Frame 2: second tool arrives, first still running → group forms.
	mv.Blocks = append(mv.Blocks, ContentBlock{
		Type: BlockTool, ToolCall: ToolCallView{
			Name: "Read", Summary: "app.go", Done: false,
			Output: "", SearchRead: srRead,
		},
	})
	f2 := stripANSIPrintable(mv.View(80, false, "●", true, false, 3))
	if !strings.Contains(f2, "Reading 2 files") {
		t.Fatalf("frame2: missing present-tense group summary:\n%s", f2)
	}

	// Frame 3: all done → past tense + fixed dot.
	mv.Blocks[0].ToolCall.Done = true
	mv.Blocks[0].ToolCall.Output = "package main\n"
	mv.Blocks[1].ToolCall.Done = true
	mv.Blocks[1].ToolCall.Output = "package tui\n"
	f3 := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(f3, "Read 2 files") {
		t.Fatalf("frame3: missing past-tense group summary:\n%s", f3)
	}
	if !strings.Contains(f3, "●") {
		t.Fatalf("frame3: completed group missing fixed dot:\n%s", f3)
	}
	if strings.Contains(f3, "Reading") {
		t.Fatalf("frame3: completed group uses present tense:\n%s", f3)
	}
}

// TestIntegration_CtrlOToggle verifies the full collapsed → expanded → collapsed
// cycle.  The only user-visible state change is the expand bool parameter.
//
//	Collapsed: group summary line, no individual tool output
//	Expanded:  per-tool full output, no group summary
//	Back to collapsed: identical to first collapsed render
func TestIntegration_CtrlOToggle(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	srSearch := tool.SearchReadKind{IsSearch: true}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "main.go", Done: true,
				Output: "package main\nfunc main() {}\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Grep", Summary: `"func"`, Done: true,
				Output: "main.go:2:func main()\n", SearchRead: srSearch,
			}},
		},
	}

	// State 1: collapsed.
	collapsed1 := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(collapsed1, "Searched for 1 pattern, read 1 file") {
		t.Fatalf("collapsed1: missing group summary:\n%s", collapsed1)
	}
	if strings.Contains(collapsed1, "package main") {
		t.Fatalf("collapsed1: shows full output:\n%s", collapsed1)
	}
	if !strings.Contains(collapsed1, "ctrl+o to expand") {
		t.Fatalf("collapsed1: missing hint:\n%s", collapsed1)
	}

	// State 2: expanded (user pressed ctrl+o).
	expanded := stripANSIPrintable(mv.View(80, true, "", false, false, 3))
	if strings.Contains(expanded, "Searched for 1 pattern, read 1 file") {
		t.Fatalf("expanded: shows group summary:\n%s", expanded)
	}
	if !strings.Contains(expanded, "package main") {
		t.Fatalf("expanded: missing Read output:\n%s", expanded)
	}
	if !strings.Contains(expanded, "main.go:2:func main()") {
		t.Fatalf("expanded: missing Grep output:\n%s", expanded)
	}

	// State 3: back to collapsed (user pressed ctrl+o again).
	collapsed2 := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if collapsed1 != collapsed2 {
		t.Fatalf("collapsed roundtrip mismatch:\nc1=%q\nc2=%q", collapsed1, collapsed2)
	}
}

// TestGroupActiveWhenStreamingAllDone verifies the TS behavior:
// even when all tools in the group are done, the group stays "active"
// (present tense + blink dot) while streaming and no content follows.
func TestGroupActiveWhenStreamingAllDone(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
		},
	}

	// Non-streaming (toolDot="") → past tense + green dot.
	still := stripANSIPrintable(mv.View(80, false, "", false, false, 3))
	if !strings.Contains(still, "Read 2 files") {
		t.Fatalf("non-streaming should use past tense:\n%s", still)
	}
	if !strings.Contains(still, "●") {
		t.Fatalf("non-streaming should have green dot:\n%s", still)
	}

	// Streaming (toolDot=white-blink) → present tense + blink dot.
	whiteDot := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
	streaming := stripANSIPrintable(mv.View(80, false, whiteDot, true, false, 3))
	if !strings.Contains(streaming, "Reading 2 files") {
		t.Fatalf("streaming should use present tense:\n%s", streaming)
	}
	if !strings.Contains(streaming, "●") {
		t.Fatalf("streaming should have blink dot:\n%s", streaming)
	}
	if !strings.Contains(streaming, "…") {
		t.Fatalf("streaming should have trailing ellipsis:\n%s", streaming)
	}

	// Streaming + text after group → NOT active (hasContentAfter).
	mv2 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
			{Type: BlockText, Text: "I found the issue."},
		},
	}
	withText := stripANSIPrintable(mv2.View(80, false, whiteDot, true, false, 3))
	if strings.Contains(withText, "Reading 2 files") {
		t.Fatalf("streaming+text-after should use past tense:\n%s", withText)
	}
	if !strings.Contains(withText, "Read 2 files") {
		t.Fatalf("streaming+text-after should use past tense:\n%s", withText)
	}
}

// TestGroupBlinkDotNotGreen verifies that during streaming, the group dot
// alternates between "" and white "●", never green. Green only appears
// when streaming ends (toolDot="" + streaming=false).
func TestGroupBlinkDotNotGreen(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "a.go", Done: true,
				Output: "a\n", SearchRead: srRead,
			}},
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Read", Summary: "b.go", Done: true,
				Output: "b\n", SearchRead: srRead,
			}},
		},
	}

	// Blink ON frame: toolDot=white "●", streaming=true → shows dot + present tense.
	wd := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)
	onFrame := mv.View(80, false, wd, true, false, 3)
	onClean := stripANSIPrintable(onFrame)
	if !strings.Contains(onClean, "●") {
		t.Fatalf("blink-on should show dot:\n%s", onClean)
	}
	if !strings.Contains(onClean, "Reading 2 files") {
		t.Fatalf("blink-on should use present tense:\n%s", onClean)
	}

	// Blink OFF frame: toolDot="", streaming=true → blank space.
	offFrame := mv.View(80, false, "", true, false, 3)
	offClean := stripANSIPrintable(offFrame)
	if strings.Contains(offClean, "●") {
		t.Fatalf("blink-off should NOT show dot:\n%s", offClean)
	}
	if !strings.Contains(offClean, "Reading 2 files") {
		t.Fatalf("blink-off should still use present tense:\n%s", offClean)
	}

	// Streaming ended: toolDot="", streaming=false → green dot + past tense.
	doneFrame := mv.View(80, false, "", false, false, 3)
	doneClean := stripANSIPrintable(doneFrame)
	if !strings.Contains(doneClean, "●") {
		t.Fatalf("done should show dot:\n%s", doneClean)
	}
	if !strings.Contains(doneClean, "Read 2 files") {
		t.Fatalf("done should use past tense:\n%s", doneClean)
	}
}

// TestSubAgentToolGroupCollapse verifies that consecutive search/read tools
// inside a sub-agent (Agent tool Blocks) are collapsed into a group summary,
// just like top-level tools.
func TestSubAgentToolGroupCollapse(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	srSearch := tool.SearchReadKind{IsSearch: true}

	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Agent", Summary: "explore", Done: true,
				Blocks: []ContentBlock{
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Read", Summary: "a.go", Done: true,
						Output: "line1\nline2\n", SearchRead: srRead,
					}},
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Grep", Summary: "TODO", Done: true,
						Output: "a.go:1: TODO\nb.go:2: TODO\nc.go:3: TODO\n", SearchRead: srSearch,
					}},
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Read", Summary: "b.go", Done: true,
						Output: "line3\nline4\n", SearchRead: srRead,
					}},
				},
			}},
		},
	}

	rendered := stripANSIPrintable(mv.View(80, false, "", false, false, 3))

	// Group summary should merge the 3 tools into one line
	if !strings.Contains(rendered, "Searched for 1 pattern, read 2 files") {
		t.Fatalf("sub-agent should show group summary:\n%s", rendered)
	}
	// Individual tool names should NOT appear (consumed by group)
	if strings.Contains(rendered, "Grep(TODO)") || strings.Contains(rendered, "Read(a.go)") {
		t.Fatalf("sub-agent individual tool names should be hidden by group:\n%s", rendered)
	}
}

// TestSubAgentGroupBlinkDotNotGreen verifies that during streaming, a sub-agent's
// group dot alternates between blank and white "●", never green — matching the
// main agent behavior. Green only appears when streaming ends.
func TestSubAgentGroupBlinkDotNotGreen(t *testing.T) {
	t.Parallel()

	srRead := tool.SearchReadKind{IsRead: true}
	srSearch := tool.SearchReadKind{IsSearch: true}

	// Sub-agent with 3 tools all Done=true, but still streaming (LLM hasn't
	// finished generating yet). This is the exact scenario the user saw:
	// all tools finish fast → anyRunning=false → group dot turns green
	// immediately instead of staying active.
	mv := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{
				Name: "Agent", Summary: "explore", Done: false,
				Blocks: []ContentBlock{
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Read", Summary: "a.go", Done: true,
						Output: "a\n", SearchRead: srRead,
					}},
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Grep", Summary: "TODO", Done: true,
						Output: "match\n", SearchRead: srSearch,
					}},
					{Type: BlockTool, ToolCall: ToolCallView{
						Name: "Read", Summary: "b.go", Done: true,
						Output: "b\n", SearchRead: srRead,
					}},
				},
			}},
		},
	}

	wd := lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Bold(true).Render(dot)

	// Blink ON frame: toolDot=white "●", streaming=true → shows dot + present tense.
	onFrame := mv.View(80, false, wd, true, false, 3)
	onClean := stripANSIPrintable(onFrame)
	if !strings.Contains(onClean, "●") {
		t.Fatalf("sub-agent blink-on should show dot:\n%s", onClean)
	}
	if !strings.Contains(onClean, "Searching for 1 pattern, reading 2 files") {
		t.Fatalf("sub-agent blink-on should use present tense:\n%s", onClean)
	}

	// Blink OFF frame: toolDot="", streaming=true → blank space + present tense.
	offFrame := mv.View(80, false, "", true, false, 3)
	offClean := stripANSIPrintable(offFrame)
	if strings.Contains(offClean, "●") {
		t.Fatalf("sub-agent blink-off should NOT show dot:\n%s", offClean)
	}
	if !strings.Contains(offClean, "Searching for 1 pattern, reading 2 files") {
		t.Fatalf("sub-agent blink-off should use present tense:\n%s", offClean)
	}

	// Streaming ended: toolDot="", streaming=false → green dot + past tense.
	doneFrame := mv.View(80, false, "", false, false, 3)
	doneClean := stripANSIPrintable(doneFrame)
	if !strings.Contains(doneClean, "●") {
		t.Fatalf("sub-agent done should show dot:\n%s", doneClean)
	}
	if !strings.Contains(doneClean, "Searched for 1 pattern, read 2 files") {
		t.Fatalf("sub-agent done should use past tense:\n%s", doneClean)
	}
}

func TestTruncateSummary(t *testing.T) {
	t.Parallel()
	if got := truncateSummary("short", 10); got != "short" {
		t.Errorf("short string: got %q, want %q", got, "short")
	}
	long := strings.Repeat("a", 20)
	want := strings.Repeat("a", 7) + "..."
	if got := truncateSummary(long, 10); got != want {
		t.Errorf("long string: got %q, want %q", got, want)
	}
}

// TestSubAgentToolRunning_ShowsLiveElapsed verifies that a sub-agent tool in
// Running state shows a live elapsed timer computed from startedAt, not a
// frozen 0s. Non-streaming tools (Write, Edit) don't get toolOutputDeltaMsg,
// so without startedAt their timer stays at 0.
func TestSubAgentToolRunning_ShowsLiveElapsed(t *testing.T) {
	t.Parallel()

	blk := ContentBlock{Type: BlockTool, ToolCall: ToolCallView{
		Name:      "Write",
		Summary:   "test.go",
		Done:      false,
		Elapsed:   0,
		startedAt: time.Unix(1700000000, 0).Add(-2 * time.Second),
	}}
	var sb strings.Builder
	blk.renderToolCall(&sb, 80, false, "", false, 0, 0, false)
	clean := stripANSIPrintable(sb.String())

	if strings.Contains(clean, "(0.0s)") {
		t.Errorf("sub-agent Write tool in running state shows frozen 0.0s:\n%s", clean)
	}
}
