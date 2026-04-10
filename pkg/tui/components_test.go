package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

func stripAnsiPrintable(s string) string {
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
	if !i.Focused() {
		t.Error("NewInput() should be focused by default")
	}
	if i.Value() != "" {
		t.Errorf("Value() = %q, want empty", i.Value())
	}
}

func TestInput_FocusBlur(t *testing.T) {
	t.Parallel()

	i := NewInput()
	if !i.Focused() {
		t.Error("expected focused after NewInput")
	}

	i.Blur()
	if i.Focused() {
		t.Error("expected blurred after Blur()")
	}

	i.Focus()
	if !i.Focused() {
		t.Error("expected focused after Focus()")
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

func TestInput_DeleteWord(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world")
	i.End()
	i.DeleteWord()
	if i.Value() != "hello " {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello ")
	}
}

func TestInput_DeleteWord_MidWord(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world")
	i.CursorLeft()
	i.CursorLeft()
	i.CursorLeft()
	i.CursorLeft()
	i.CursorLeft() // at position 6 (at 'w' in "world")
	i.DeleteWord()
	// DeleteWord deletes the word before cursor, leaving chars after cursor
	if i.Value() != "world" {
		t.Errorf("Value() = %q, want %q", i.Value(), "world")
	}
}

func TestInput_DeleteWord_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.DeleteWord() // should be no-op
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
}

func TestInput_End(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.End()
	if i.Value() != "abc" {
		t.Errorf("Value() unchanged = %q", i.Value())
	}
}

func TestInput_View_Focused(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	v := i.View()
	if !strings.Contains(v, "❯") {
		t.Error("View() should contain prompt '❯'")
	}
	if !strings.Contains(v, "hello") {
		t.Error("View() should contain value")
	}
}

func TestInput_View_Blurred(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	i.Blur()
	v := i.View()
	if !strings.Contains(v, "❯") {
		t.Error("View() should contain prompt")
	}
}

func TestInput_View_Placeholder(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Blur()
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
	s.SetUsage(100, 50)
	v := s.View()
	if !strings.Contains(v, "in:100") {
		t.Errorf("View() = %q, should contain input tokens", v)
	}
	if !strings.Contains(v, "out:50") {
		t.Errorf("View() = %q, should contain output tokens", v)
	}
}

func TestStatusBar_SetError(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetError("rate limit")
	v := s.View()
	if !strings.Contains(v, "err:") {
		t.Errorf("View() = %q, should contain error", v)
	}
	if !strings.Contains(v, "rate limit") {
		t.Errorf("View() = %q, should contain error message", v)
	}
}

// ---------------------------------------------------------------------------
// Spinner
// ---------------------------------------------------------------------------

func TestNewSpinner(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.Active() {
		t.Error("NewSpinner() should not be active")
	}
}

func TestSpinner_StartStop(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	if !s.Active() {
		t.Error("expected active after Start()")
	}
	s.Stop()
	if s.Active() {
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
	v := m.View(80, false)
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
	v := m.View(30, false) // narrow width triggers wrapping
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
	out := prefixUserLine("hello", 80)
	if out != "❯ hello" {
		t.Errorf("single line: got %q, want %q", out, "❯ hello")
	}

	// Multi-line
	out = prefixUserLine("line1\nline2\nline3", 80)
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
	v := m.View(80, false)
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
	v := m.View(80, false)
	if !strings.Contains(v, "system msg") {
		t.Errorf("View() = %q, should contain content", v)
	}
}

func TestMessageView_WithToolCalls_Running(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "working on it"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Read", Input: `{"file":"test.go"}`, Done: false}},
		},
	}
	v := m.View(80, false)
	// & suffix per TS convention for running state
	if !strings.Contains(v, "&") {
		t.Errorf("View() = %q, should contain '&' for running state", v)
	}
	if !strings.Contains(v, "Read") {
		t.Errorf("View() = %q, should contain tool name 'Read'", v)
	}
}

func TestMessageView_WithToolCalls_Done(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "done"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Grep", Output: "found match", Done: true, IsError: false}},
		},
	}
	v := m.View(80, false)
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
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "failed"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "exit code 1", Done: true, IsError: true}},
		},
	}
	v := m.View(80, false)
	// Error shows tool name with red dot
	if !strings.Contains(v, "Bash") {
		t.Errorf("View() = %q, should contain 'Bash'", v)
	}
	if !strings.Contains(v, "exit code 1") {
		t.Errorf("View() = %q, should contain error output", v)
	}
}

func TestMessageView_BlankLineAfterToolBeforeText(t *testing.T) {
	t.Parallel()

	// Completed tool followed by text block → should have double newline (blank line)
	m := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "ok", Done: true}},
			{Type: BlockText, Text: "Here is the result"},
		},
	}
	v := m.View(80, false)
	if !strings.Contains(v, "\n\n") {
		t.Errorf("completed tool followed by text should have blank line, got: %q", v)
	}

	// Running tool (not done) → no blank line
	m2 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Done: false}},
			{Type: BlockText, Text: "should be no blank line"},
		},
	}
	v2 := m2.View(80, false)
	if strings.Contains(v2, "\n\n") {
		t.Errorf("running tool followed by text should NOT have blank line, got: %q", v2)
	}

	// Tool at end (no following block) → no extra blank line (no \n\n)
	m3 := MessageView{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "done", Done: true}},
		},
	}
	v3 := m3.View(80, false)
	if strings.Contains(v3, "\n\n") {
		t.Errorf("tool at end should not have blank line, got: %q", v3)
	}
}

func TestMessageView_ToolCallLongOutput(t *testing.T) {
	t.Parallel()

	longOutput := strings.Repeat("x", 300)
	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "result"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Read", Output: longOutput, Done: true, IsError: false}},
		},
	}
	v := m.View(80, false)
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
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "working"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Write", Input: longInput, Done: false}},
		},
	}
	v := m.View(80, false)
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
	v := m.View(20, false)
	lines := strings.Split(v, "\n")
	// Each line should be reasonably short (no line longer than width + some ANSI margin)
	for _, line := range lines {
		stripped := stripAnsiPrintable(line)
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
	v := m.View(20, false)
	if !strings.Contains(v, "这") {
		t.Error("should contain content")
	}
	// Should have multiple lines (wrapped)
	lines := strings.Split(v, "\n")
	if len(lines) < 3 {
		t.Errorf("expected wrapping, got %d lines: %q", len(lines), v)
	}
}

func TestMessageView_ToolCallEmptyOutput(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:   "assistant",
		Blocks: []ContentBlock{
			{Type: BlockText, Text: "result"},
			{Type: BlockTool, ToolCall: ToolCallView{Name: "Bash", Output: "", Done: true, IsError: false}},
		},
	}
	v := m.View(80, false)
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
	v := m.View(80, false)
	if v != "" {
		t.Errorf("Empty Blocks should return empty string, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// renderMessages
// ---------------------------------------------------------------------------

func TestRenderMessages_NoExtraTrailingNewline(t *testing.T) {
	t.Parallel()

	// 单条消息，末尾不应有多余换行
	msgs := []MessageView{
		{Role: "assistant", Blocks: []ContentBlock{{Type: BlockText, Text: "hello"}}},
	}
	v := renderMessages(msgs, 80, 10, false)
	// Count trailing newlines
	trailing := 0
	for i := len(v) - 1; i >= 0; i-- {
		if v[i] == '\n' {
			trailing++
		} else {
			break
		}
	}
	if trailing > 1 {
		t.Errorf("expected at most 1 trailing newline, got %d: %q", trailing, v)
	}
}

func TestRenderMessages_Empty(t *testing.T) {
	t.Parallel()

	v := renderMessages([]MessageView{}, 80, 10, false)
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessages(nil) = %q, should contain welcome", v)
	}
}

func TestRenderMessages_EmptySlice(t *testing.T) {
	t.Parallel()

	v := renderMessages([]MessageView{}, 80, 10, false)
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessages([]) should contain welcome")
	}
}

func TestRenderMessages_WithMessages(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "hello"}}},
		{Role: "assistant", Blocks: []ContentBlock{{Type: BlockText, Text: "hi"}}},
	}
	v := renderMessages(msgs, 80, 10, false)
	if !strings.Contains(v, "hello") {
		t.Error("should contain user message")
	}
	if !strings.Contains(v, "hi") {
		t.Error("should contain assistant message")
	}
}

func TestRenderMessages_HeightLimit(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line1"}}},
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line2"}}},
		{Role: "user", Blocks: []ContentBlock{{Type: BlockText, Text: "line3"}}},
	}
	v := renderMessages(msgs, 80, 2, false)
	// Should only show 2 lines max
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	if len(lines) > 2 {
		t.Errorf("expected at most 2 lines, got %d", len(lines))
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

func TestRuneDisplayWidth_ASCII(t *testing.T) {
	t.Parallel()

	if r := runeDisplayWidth('a'); r != 1 {
		t.Errorf("runeDisplayWidth('a') = %d, want 1", r)
	}
}

func TestRuneDisplayWidth_Control(t *testing.T) {
	t.Parallel()

	if r := runeDisplayWidth(0); r != 0 {
		t.Errorf("runeDisplayWidth(0) = %d, want 0", r)
	}
}

func TestRuneDisplayWidth_CJK(t *testing.T) {
	t.Parallel()

	if r := runeDisplayWidth('你'); r != 2 {
		t.Errorf("runeDisplayWidth('你') = %d, want 2", r)
	}
}

func TestRuneDisplayWidth_Hiragana(t *testing.T) {
	t.Parallel()

	if r := runeDisplayWidth('あ'); r != 2 {
		t.Errorf("runeDisplayWidth('あ') = %d, want 2", r)
	}
}

// ---------------------------------------------------------------------------
// stripAnsi
// ---------------------------------------------------------------------------

func TestStripAnsi(t *testing.T) {
	t.Parallel()

	v := stripAnsi("\x1b[31mred\x1b[0m")
	// stripAnsi removes ANSI escape sequences, returning visible text
	if v != "red" {
		t.Errorf("stripAnsi() = %q, want %q", v, "red")
	}
}

func TestStripAnsi_NoAnsi(t *testing.T) {
	t.Parallel()

	v := stripAnsi("hello")
	if v != "hello" {
		t.Errorf("stripAnsi('hello') = %q, want %q", v, "hello")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestMin(t *testing.T) {
	t.Parallel()

	if min(1, 2) != 1 {
		t.Error("min(1, 2) should be 1")
	}
	if min(2, 1) != 1 {
		t.Error("min(2, 1) should be 1")
	}
	if min(5, 5) != 5 {
		t.Error("min(5, 5) should be 5")
	}
}

// ---------------------------------------------------------------------------
// prettyJSON
// ---------------------------------------------------------------------------

func TestPrettyJSON(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"a":1,"b":2}`)
	v := prettyJSON(raw)
	if !strings.Contains(v, "a") || !strings.Contains(v, "1") {
		t.Errorf("prettyJSON() = %q, should be formatted", v)
	}
}

func TestPrettyJSON_Empty(t *testing.T) {
	t.Parallel()

	v := prettyJSON(nil)
	if v != "" {
		t.Errorf("prettyJSON(nil) = %q, want empty", v)
	}
}

func TestPrettyJSON_Invalid(t *testing.T) {
	t.Parallel()

	v := prettyJSON(json.RawMessage(`{invalid`))
	if v != `{invalid` {
		t.Errorf("prettyJSON(invalid) = %q, want original", v)
	}
}

// ---------------------------------------------------------------------------
// firstMeaningfulLine
// ---------------------------------------------------------------------------

func TestFirstMeaningfulLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"hello\nworld", "hello"},
		{"\n\n  hello\nworld", "hello"},
		{"", ""},
		{"\n\n", ""},
		{"  indented  ", "indented"},
	}
	for _, tt := range tests {
		got := firstMeaningfulLine(tt.input)
		if got != tt.want {
			t.Errorf("firstMeaningfulLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// DeleteForward
// ---------------------------------------------------------------------------

func TestInput_DeleteForward(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft() // cursor at position 2
	i.DeleteForward()
	if i.Value() != "ab" {
		t.Errorf("DeleteForward() = %q, want %q", i.Value(), "ab")
	}
}

func TestInput_DeleteForward_AtEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	// cursor at end (position 3) — no-op
	i.DeleteForward()
	if i.Value() != "abc" {
		t.Errorf("DeleteForward at end should be no-op, got %q", i.Value())
	}
}

func TestInput_DeleteForward_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.DeleteForward()
	if i.Value() != "" {
		t.Errorf("DeleteForward on empty should be no-op, got %q", i.Value())
	}
}

// ---------------------------------------------------------------------------
// PrevWord
// ---------------------------------------------------------------------------

func TestInput_PrevWord(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world foo")
	i.End() // cursor at end
	i.PrevWord()
	if i.cursor != 12 { // start of "foo"
		t.Errorf("PrevWord() cursor = %d, want 12", i.cursor)
	}
	i.PrevWord()
	if i.cursor != 6 { // start of "world"
		t.Errorf("PrevWord() cursor = %d, want 6", i.cursor)
	}
}

func TestInput_PrevWord_AtStart(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	i.Home()
	i.PrevWord()
	if i.cursor != 0 {
		t.Errorf("PrevWord at start should be no-op, cursor = %d", i.cursor)
	}
}

func TestInput_PrevWord_LeadingSpaces(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello   world")
	i.End()
	i.PrevWord()
	if i.cursor != 8 { // start of "world" after spaces
		t.Errorf("PrevWord() cursor = %d, want 8", i.cursor)
	}
}

// ---------------------------------------------------------------------------
// NextWord
// ---------------------------------------------------------------------------

func TestInput_NextWord(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world foo")
	i.Home() // cursor at 0
	i.NextWord()
	if i.cursor != 6 { // start of "world"
		t.Errorf("NextWord() cursor = %d, want 6", i.cursor)
	}
	i.NextWord()
	if i.cursor != 12 { // start of "foo"
		t.Errorf("NextWord() cursor = %d, want 12", i.cursor)
	}
}

func TestInput_NextWord_AtEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	i.End()
	i.NextWord()
	if i.cursor != 5 {
		t.Errorf("NextWord at end should stay at end, cursor = %d", i.cursor)
	}
}

func TestInput_NextWord_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.NextWord()
	if i.cursor != 0 {
		t.Errorf("NextWord on empty should be no-op, cursor = %d", i.cursor)
	}
}

// ---------------------------------------------------------------------------
// DeleteWordForward
// ---------------------------------------------------------------------------

func TestInput_DeleteWordForward(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world foo")
	i.Home() // cursor at 0
	deleted := i.DeleteWordForward()
	if deleted != "hello " {
		t.Errorf("DeleteWordForward() deleted = %q, want %q", deleted, "hello ")
	}
	if i.Value() != "world foo" {
		t.Errorf("DeleteWordForward() Value() = %q, want %q", i.Value(), "world foo")
	}
}

func TestInput_DeleteWordForward_AtEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello")
	i.End()
	deleted := i.DeleteWordForward()
	if deleted != "" {
		t.Errorf("DeleteWordForward at end should return empty, got %q", deleted)
	}
	if i.Value() != "hello" {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello")
	}
}

func TestInput_DeleteWordForward_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	deleted := i.DeleteWordForward()
	if deleted != "" {
		t.Errorf("DeleteWordForward on empty should return empty, got %q", deleted)
	}
}

// ---------------------------------------------------------------------------
// InsertChar — cursor beyond end
// ---------------------------------------------------------------------------

func TestInput_InsertChar_CursorBeyondEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	// Force cursor beyond end
	i.cursor = 10
	i.InsertChar('x')
	if i.Value() != "abcx" {
		t.Errorf("InsertChar with cursor beyond end: Value() = %q, want %q", i.Value(), "abcx")
	}
}

// ---------------------------------------------------------------------------
// Input.View — focused cursor variations
// ---------------------------------------------------------------------------

func TestInput_View_FocusedCursorAtEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("ab")
	v := i.View()
	if !strings.Contains(v, "ab") {
		t.Errorf("View() should contain value, got %q", v)
	}
}

func TestInput_View_FocusedCursorMiddle(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorLeft() // cursor at 2 (on 'c')
	i.CursorLeft() // cursor at 1 (on 'b') — triggers after = "c"
	v := i.View()
	if !strings.Contains(v, "abc") {
		t.Errorf("View() should contain value, got %q", v)
	}
	// Cursor at position 1: before="a", cursorChar="b", after="c"
	// Verify "c" appears in output (from the after branch)
	if !strings.Contains(v, "c") {
		t.Errorf("View() should show chars after cursor, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// Spinner.Tick inactive
// ---------------------------------------------------------------------------

func TestSpinner_TickInactive(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	idx := s.idx
	s.Tick()
	if s.idx != idx {
		t.Error("Tick on inactive spinner should be no-op")
	}
}

// ---------------------------------------------------------------------------
// prefixLine continuation
// ---------------------------------------------------------------------------

func TestPrefixLine_Continuation(t *testing.T) {
	out := prefixLine(1, "hello")
	if out != "  hello" {
		t.Errorf("prefixLine(1, ...) = %q, want %q", out, "  hello")
	}
	out0 := prefixLine(0, "hello")
	if !strings.Contains(out0, "⎿") {
		t.Errorf("prefixLine(0, ...) should contain ⎿, got %q", out0)
	}
}

// ---------------------------------------------------------------------------
// formatToolOutput — comprehensive coverage
// ---------------------------------------------------------------------------

func TestFormatToolOutput_Empty(t *testing.T) {
	out := formatToolOutput("", false, false, 80)
	if out != "" {
		t.Errorf("empty input should return empty, got %q", out)
	}
}

func TestFormatToolOutput_FewLines_NoCollapse(t *testing.T) {
	output := "line1\nline2\nline3"
	out := formatToolOutput(output, false, false, 80)
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("3 lines should not collapse, got: %q", out)
	}
}

func TestFormatToolOutput_Collapse(t *testing.T) {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i)
	}
	output := strings.Join(lines, "\n")
	out := formatToolOutput(output, false, false, 80)
	if !strings.Contains(out, "ctrl+o to expand") {
		t.Errorf("10 lines should collapse with hint, got: %s", out)
	}
}

func TestFormatToolOutput_CollapseError(t *testing.T) {
	lines := make([]string, 15)
	for i := range lines {
		lines[i] = fmt.Sprintf("err line%d", i)
	}
	output := strings.Join(lines, "\n")
	out := formatToolOutput(output, true, false, 80)
	if !strings.Contains(out, "ctrl+o to see all") {
		t.Errorf("error with many lines should collapse with error hint, got: %s", out)
	}
}

func TestFormatToolOutput_Expand(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i)
	}
	output := strings.Join(lines, "\n")
	out := formatToolOutput(output, false, true, 80)
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("expanded should not show collapse hint")
	}
}

func TestFormatToolOutput_JustOverThreshold(t *testing.T) {
	// 4 lines — threshold is 3+1=4, so 4 lines are shown without collapse
	output := "line1\nline2\nline3\nline4"
	out := formatToolOutput(output, false, false, 80)
	if strings.Contains(out, "ctrl+o") {
		t.Errorf("4 lines (<=3+1) should not collapse, got: %s", out)
	}
}

func TestFormatToolOutput_TrailingNewlines(t *testing.T) {
	output := "line1\nline2\n\n\n"
	out := formatToolOutput(output, false, false, 80)
	if strings.HasSuffix(out, "\n") {
		t.Errorf("trailing newlines should be trimmed, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// consumeAnsiEscape — comprehensive coverage
// ---------------------------------------------------------------------------

func TestConsumeAnsiEscape_CSI(t *testing.T) {
	seq := consumeAnsiEscape("\x1b[31mrest")
	if seq != "\x1b[31m" {
		t.Errorf("CSI sequence = %q, want %q", seq, "\x1b[31m")
	}
}

func TestConsumeAnsiEscape_OSC(t *testing.T) {
	seq := consumeAnsiEscape("\x1b]0;title\x07rest")
	if seq != "\x1b]0;title\x07" {
		t.Errorf("OSC sequence = %q, want %q", seq, "\x1b]0;title\x07")
	}
}

func TestConsumeAnsiEscape_OSCTerminatedByST(t *testing.T) {
	seq := consumeAnsiEscape("\x1b]0;title\x1b\\rest")
	if seq != "\x1b]0;title\x1b\\" {
		t.Errorf("OSC with ST terminator = %q, want %q", seq, "\x1b]0;title\x1b\\")
	}
}

func TestConsumeAnsiEscape_TwoCharEscape(t *testing.T) {
	// CSI consumes until final byte 0x40-0x7E; 'r' (0x72) is in that range
	seq := consumeAnsiEscape("\x1b[rest")
	if seq != "\x1b[r" {
		t.Errorf("CSI escape = %q, want %q", seq, "\x1b[r")
	}
}

func TestConsumeAnsiEscape_BareEscape(t *testing.T) {
	seq := consumeAnsiEscape("\x1bXrest")
	if seq != "\x1bX" {
		t.Errorf("bare escape = %q, want %q", seq, "\x1bX")
	}
}

func TestConsumeAnsiEscape_ShortInput(t *testing.T) {
	seq := consumeAnsiEscape("\x1b")
	if seq != "\x1b" {
		t.Errorf("short escape = %q, want %q", seq, "\x1b")
	}
}

func TestConsumeAnsiEscape_NotEscape(t *testing.T) {
	seq := consumeAnsiEscape("hello")
	if seq != "h" {
		t.Errorf("non-escape = %q, want %q", seq, "h")
	}
}

// ---------------------------------------------------------------------------
// runeDisplayWidth — comprehensive coverage
// ---------------------------------------------------------------------------

func TestRuneDisplayWidth_Latin1(t *testing.T) {
	// Latin-1 supplement: 0x80+ falls through to default → 1
	// (our simplified function doesn't distinguish C1 controls)
	if w := runeDisplayWidth(0x80); w != 1 {
		t.Errorf("0x80 = %d, want 1", w)
	}
	if w := runeDisplayWidth(0xA0); w != 1 {
		t.Errorf("NBSP 0xA0 = %d, want 1", w)
	}
}

func TestRuneDisplayWidth_Hangul(t *testing.T) {
	// Hangul Jamo 0x1100-0x115F → width 2
	if w := runeDisplayWidth(0x1100); w != 2 {
		t.Errorf("Hangul Jamo 0x1100 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_CJKRadicals(t *testing.T) {
	// CJK Radicals 0x2E80-0x303E → width 2
	if w := runeDisplayWidth(0x2E80); w != 2 {
		t.Errorf("CJK Radical 0x2E80 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_Katakana(t *testing.T) {
	// Katakana 0x30A0 → width 2
	if w := runeDisplayWidth(0x30A0); w != 2 {
		t.Errorf("Katakana 0x30A0 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_CJKUnified(t *testing.T) {
	// CJK Unified Ideographs 0x4E00 → width 2
	if w := runeDisplayWidth(0x4E00); w != 2 {
		t.Errorf("CJK Unified 0x4E00 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_HangulSyllables(t *testing.T) {
	// Hangul Syllables 0xAC00 → width 2
	if w := runeDisplayWidth(0xAC00); w != 2 {
		t.Errorf("Hangul Syllable 0xAC00 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_CJKCompatibility(t *testing.T) {
	// CJK Compatibility Ideographs 0xF900 → width 2
	if w := runeDisplayWidth(0xF900); w != 2 {
		t.Errorf("CJK Compat 0xF900 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_FullwidthForms(t *testing.T) {
	// Fullwidth Forms 0xFF01 → width 2
	if w := runeDisplayWidth(0xFF01); w != 2 {
		t.Errorf("Fullwidth 0xFF01 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_FullwidthCurrency(t *testing.T) {
	// Fullwidth currency 0xFFE0 → width 2
	if w := runeDisplayWidth(0xFFE0); w != 2 {
		t.Errorf("Fullwidth currency 0xFFE0 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_CJKExtB(t *testing.T) {
	// CJK Extension B 0x20000 → width 2
	if w := runeDisplayWidth(0x20000); w != 2 {
		t.Errorf("CJK Ext B 0x20000 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_CJKExtA(t *testing.T) {
	// CJK Extension A range 0x3040-0x9FFF covered → 2
	if w := runeDisplayWidth(0x3400); w != 2 {
		t.Errorf("CJK Ext A 0x3400 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_SmallFormVariants(t *testing.T) {
	// Small Form Variants 0xFE50 → width 2
	if w := runeDisplayWidth(0xFE50); w != 2 {
		t.Errorf("Small Form 0xFE50 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_OtherWide(t *testing.T) {
	// 0x30000 → width 2
	if w := runeDisplayWidth(0x30000); w != 2 {
		t.Errorf("0x30000 = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_OtherNonASCII(t *testing.T) {
	// Non-ASCII, non-wide → width 1 (default)
	if w := runeDisplayWidth('é'); w != 1 {
		t.Errorf("é = %d, want 1", w)
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

// ---------------------------------------------------------------------------
// prefixUserLine — empty
// ---------------------------------------------------------------------------

func TestPrefixUserLine_Empty(t *testing.T) {
	out := prefixUserLine("", 80)
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
	v := m.View(5, false) // below minimum of 10
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
	blk.renderToolCall(&sb, 80, false)
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
	v := m.View(80, false)
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
	v := m.View(80, false)
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
	v := m.View(80, false)
	if !strings.Contains(v, "Glob") {
		t.Errorf("should contain tool name, got: %q", v)
	}
}
