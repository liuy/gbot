package tui

import (
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
	v := m.View(80, false, "")
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
	v := m.View(30, false, "") // narrow width triggers wrapping
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
	// "running..." suffix for running state
	if !strings.Contains(v, "running...") {
		t.Errorf("View() = %q, should contain 'running...' for running state", v)
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v2 := m2.View(80, false, "")
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
	v3 := m3.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(20, false, "")
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
	v := m.View(20, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := renderMessages(msgs, 80, 10, false, "")
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

	v := renderMessages([]MessageView{}, 80, 10, false, "")
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessages(nil) = %q, should contain welcome", v)
	}
}

func TestRenderMessages_EmptySlice(t *testing.T) {
	t.Parallel()

	v := renderMessages([]MessageView{}, 80, 10, false, "")
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
	v := renderMessages(msgs, 80, 10, false, "")
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
	v := renderMessages(msgs, 80, 2, false, "")
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
	v := m.View(5, false, "") // below minimum of 10
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
	blk.renderToolCall(&sb, 80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	v := m.View(80, false, "")
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
	for j := 0; j < 20; j++ {
		i.CursorRight()
	}
	v := i.View()
	lines := strings.Split(v, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2+ lines, got %d: %q", len(lines), v)
	}
	// Second line should have content (the cursor is there)
	second := stripAnsiPrintable(lines[1])
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

func TestInput_HasWrappedLines(t *testing.T) {
	t.Parallel()

	i := NewInput()
	// Short text, no width set → no wrapping
	if i.HasWrappedLines() {
		t.Error("short text without width should not have wrapped lines")
	}

	i.SetWidth(10)
	i.SetValue("hello world") // 11 chars > 10, should wrap
	if !i.HasWrappedLines() {
		t.Error("long text with narrow width should have wrapped lines")
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
	v := m.View(30, false, "")
	// Header should be wrapped — output should contain newlines
	if !strings.Contains(v, "\n") {
		t.Errorf("long tool header should wrap at width 30, got: %q", v)
	}
	// Each stripped line should not exceed width by much
	for _, line := range strings.Split(v, "\n") {
		stripped := stripAnsiPrintable(line)
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
