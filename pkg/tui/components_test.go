package tui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
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
	i.InsertChar('h')
	i.InsertChar('e')
	i.InsertChar('l')
	i.InsertChar('l')
	i.InsertChar('o')
	i.InsertChar(' ')
	i.InsertChar('w')
	i.InsertChar('o')
	i.InsertChar('r')
	i.InsertChar('l')
	i.InsertChar('d')
	if i.Value() != "hello world" {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello world")
	}
}

func TestInput_Backspace_Chinese(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('你')
	i.InsertChar('好')
	i.InsertChar('吗')
	// cursor at end, backspace should remove last rune '吗'
	i.Backspace()
	if i.Value() != "你好" {
		t.Errorf("Value() = %q, want %q", i.Value(), "你好")
	}
	// cursor should be at end
	i.InsertChar('！')
	if i.Value() != "你好！" {
		t.Errorf("Value() after insert = %q, want %q", i.Value(), "你好！")
	}
}

func TestInput_CursorLeftRight_Chinese(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('你')
	i.InsertChar('好')
	i.InsertChar('世')
	i.InsertChar('界')
	// value = [你,好,世,界], cursor at rune pos 4 (end)
	i.CursorLeft() // cursor → pos 3 (between 世 and 界)
	// Insert between 世 and 界
	i.InsertChar('的')
	if i.Value() != "你好世的界" {
		t.Errorf("Value() = %q, want %q", i.Value(), "你好世的界")
	}

	// Move cursor right past 刚插入的, then left again
	i.CursorLeft()  // pos 3 (的)
	i.CursorLeft()  // pos 2 (世)
	i.CursorLeft()  // pos 1 (好)
	i.InsertChar('很')
	// Insert at pos 1 → [你,很,好,世,的,界]
	if i.Value() != "你很好世的界" {
		t.Errorf("Value() after insert = %q, want %q", i.Value(), "你很好世的界")
	}
}

func TestInput_DeleteWord_Chinese(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.InsertChar('你')
	i.InsertChar('好')
	i.InsertChar(' ')
	i.InsertChar('世')
	i.InsertChar('界')
	// cursor at end, delete "世界"
	i.DeleteWord()
	if i.Value() != "你好 " {
		t.Errorf("Value() = %q, want %q", i.Value(), "你好 ")
	}
}

func TestInput_InsertChar_Middle(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("ac")
	// cursor is at end after SetValue; move left to position 1
	i.CursorLeft()
	i.InsertChar('b')
	if i.Value() != "abc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "abc")
	}
}

func TestInput_Backspace(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	// cursor is at end; backspace removes 'c'
	i.Backspace()
	if i.Value() != "ab" {
		t.Errorf("Value() = %q, want %q", i.Value(), "ab")
	}
}

func TestInput_Backspace_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Backspace() // should not panic
	if i.Value() != "" {
		t.Errorf("Value() = %q, want empty", i.Value())
	}
}

func TestInput_DeleteWord(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world")
	// cursor at end, delete "world"
	i.DeleteWord()
	if i.Value() != "hello " {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello ")
	}
}

func TestInput_DeleteWord_Middle(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("hello world foo")
	// cursor at end, delete "foo"
	i.DeleteWord()
	// now delete trailing spaces + "world"
	i.DeleteWord()
	if i.Value() != "hello " {
		t.Errorf("Value() = %q, want %q", i.Value(), "hello ")
	}
}

func TestInput_DeleteWord_Empty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.DeleteWord() // should not panic
	if i.Value() != "" {
		t.Errorf("Value() = %q, want empty", i.Value())
	}
}

func TestInput_CursorLeftRight(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	// cursor at end (pos 3)
	i.CursorLeft()
	i.CursorLeft()
	// cursor at pos 1
	i.InsertChar('X')
	if i.Value() != "aXbc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "aXbc")
	}

	i.CursorRight()
	i.InsertChar('Y')
	if i.Value() != "aXbYc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "aXbYc")
	}
}

func TestInput_CursorLeft_AtStart(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.Home()
	i.CursorLeft() // should not go negative
	// cursor should still be at 0
	i.InsertChar('Z')
	if i.Value() != "Zabc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "Zabc")
	}
}

func TestInput_CursorRight_AtEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.CursorRight() // already at end, should not go past
	i.CursorRight()
	i.InsertChar('!')
	if i.Value() != "abc!" {
		t.Errorf("Value() = %q, want %q", i.Value(), "abc!")
	}
}

func TestInput_HomeEnd(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abc")
	i.Home()
	// cursor at 0
	i.InsertChar('!')
	if i.Value() != "!abc" {
		t.Errorf("Value() = %q, want %q", i.Value(), "!abc")
	}

	i.End()
	i.InsertChar('@')
	if i.Value() != "!abc@" {
		t.Errorf("Value() = %q, want %q", i.Value(), "!abc@")
	}
}

func TestInput_SetWidth(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetWidth(80)
	// SetWidth just stores; verify no panic
}

func TestInput_View_Focused(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Focus()
	i.SetValue("test")
	v := i.View()
	if !strings.Contains(v, "test") {
		t.Errorf("View() = %q, should contain 'test'", v)
	}
	if !strings.Contains(v, ">") {
		t.Errorf("View() = %q, should contain prompt '>'", v)
	}
}

func TestInput_View_Blurred(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Blur()
	v := i.View()
	if !strings.Contains(v, "Type a message...") {
		t.Errorf("View() = %q, should contain placeholder", v)
	}
}

func TestInput_View_FocusedEmpty(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.Focus()
	v := i.View()
	if !strings.Contains(v, ">") {
		t.Errorf("View() = %q, should contain prompt", v)
	}
}

// ---------------------------------------------------------------------------
// StatusBar
// ---------------------------------------------------------------------------

func TestNewStatusBar(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	if s.model != "" {
		t.Errorf("model = %q, want empty", s.model)
	}
}

func TestStatusBar_SetModel(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetModel("claude-3")
	v := s.View()
	if !strings.Contains(v, "claude-3") {
		t.Errorf("View() = %q, should contain 'claude-3'", v)
	}
}

func TestStatusBar_DefaultModel(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	v := s.View()
	if !strings.Contains(v, "gbot") {
		t.Errorf("View() = %q, should contain default 'gbot'", v)
	}
}

func TestStatusBar_SetStreaming(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetStreaming(true)
	v := s.View()
	if !strings.Contains(v, "working") {
		t.Errorf("View() = %q, should contain 'working' when streaming", v)
	}

	s.SetStreaming(false)
	v = s.View()
	if strings.Contains(v, "working") {
		t.Error("View() should not contain 'working' when not streaming")
	}
}

func TestStatusBar_SetUsage(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetUsage(100, 50)
	v := s.View()
	if !strings.Contains(v, "in:100") {
		t.Errorf("View() = %q, should contain 'in:100'", v)
	}
	if !strings.Contains(v, "out:50") {
		t.Errorf("View() = %q, should contain 'out:50'", v)
	}
}

func TestStatusBar_SetWidth(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetWidth(120)
	s.SetModel("test")
	v := s.View()
	// Should render without panic; content is padded to fill width
	if v == "" {
		t.Error("View() returned empty string")
	}
}

func TestStatusBar_SetError(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetError("something failed")
	v := s.View()
	if !strings.Contains(v, "something failed") {
		t.Errorf("View() = %q, should contain error message", v)
	}
}

func TestStatusBar_SetError_ClearsManually(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetError("first error")
	v1 := s.View()
	if !strings.Contains(v1, "first error") {
		t.Error("first View() should contain error message")
	}
	// Note: View() uses a value receiver, so s.err is NOT cleared on the original.
	// The caller must explicitly clear the error.
	s.SetError("")
	v2 := s.View()
	if strings.Contains(v2, "first error") {
		t.Error("error should not appear after SetError('')")
	}
}

func TestStatusBar_View(t *testing.T) {
	t.Parallel()

	s := NewStatusBar()
	s.SetModel("test-model")
	s.SetStreaming(true)
	s.SetUsage(42, 7)
	s.SetWidth(100)
	v := s.View()
	if !strings.Contains(v, "test-model") {
		t.Error("View should contain model name")
	}
	if !strings.Contains(v, "working") {
		t.Error("View should contain streaming indicator")
	}
	if !strings.Contains(v, "in:42") {
		t.Error("View should contain input token count")
	}
}

// ---------------------------------------------------------------------------
// Spinner
// ---------------------------------------------------------------------------

func TestNewSpinner(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.Active() {
		t.Error("NewSpinner should not be active")
	}
	if len(s.frames) != 10 {
		t.Errorf("frames count = %d, want 10", len(s.frames))
	}
}

func TestSpinner_StartStop(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.Active() {
		t.Error("should start inactive")
	}

	s.Start()
	if !s.Active() {
		t.Error("should be active after Start()")
	}

	s.Stop()
	if s.Active() {
		t.Error("should be inactive after Stop()")
	}
}

func TestSpinner_Tick(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()

	// Tick should advance the frame
	s.Tick()
	s.Tick()
	// View should return non-empty when active
	v := s.View()
	if v == "" {
		t.Error("View() should return non-empty when active")
	}
}

func TestSpinner_TickInactive(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	// Tick while inactive should be no-op
	s.Tick()
	if s.idx != 0 {
		t.Errorf("idx = %d, want 0 when inactive", s.idx)
	}
}

func TestSpinner_StopResetsIndex(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	s.Tick()
	s.Tick()
	s.Stop()
	if s.idx != 0 {
		t.Errorf("idx after Stop = %d, want 0", s.idx)
	}
}

func TestSpinner_ViewInactive(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	if s.View() != "" {
		t.Errorf("View() when inactive = %q, want empty", s.View())
	}
}

func TestSpinner_ViewActive(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	v := s.View()
	if v == "" {
		t.Error("View() when active should not be empty")
	}
}

func TestSpinner_TickWraps(t *testing.T) {
	t.Parallel()

	s := NewSpinner()
	s.Start()
	// Tick through all frames; should wrap without panic
	for range 20 {
		s.Tick()
	}
	v := s.View()
	if v == "" {
		t.Error("View() after wrap should not be empty")
	}
}

// ---------------------------------------------------------------------------
// MessageView
// ---------------------------------------------------------------------------

func TestMessageView_UserRole(t *testing.T) {
	t.Parallel()

	m := MessageView{Role: "user", Content: "hello there"}
	v := m.View(80)
	if !strings.Contains(v, "You:") {
		t.Errorf("View() = %q, should contain 'You:'", v)
	}
	if !strings.Contains(v, "hello there") {
		t.Errorf("View() = %q, should contain content", v)
	}
}

func TestMessageView_AssistantRole(t *testing.T) {
	t.Parallel()

	m := MessageView{Role: "assistant", Content: "hi from gbot"}
	v := m.View(80)
	if !strings.Contains(v, "gbot:") {
		t.Errorf("View() = %q, should contain 'gbot:'", v)
	}
	if !strings.Contains(v, "hi from gbot") {
		t.Errorf("View() = %q, should contain content", v)
	}
}

func TestMessageView_SystemRole(t *testing.T) {
	t.Parallel()

	m := MessageView{Role: "system", Content: "system msg"}
	v := m.View(80)
	if !strings.Contains(v, "system msg") {
		t.Errorf("View() = %q, should contain content", v)
	}
	// system role uses default branch — no prefix
	if strings.Contains(v, "You:") || strings.Contains(v, "gbot:") {
		t.Error("system role should not have user/assistant prefix")
	}
}

func TestMessageView_WithToolCalls_Running(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:    "assistant",
		Content: "working on it",
		ToolCalls: []ToolCallView{
			{Name: "Read", Input: `{"file":"test.go"}`, Done: false},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "running...") {
		t.Errorf("View() = %q, should contain 'running...'", v)
	}
	if !strings.Contains(v, "Read") {
		t.Errorf("View() = %q, should contain tool name 'Read'", v)
	}
}

func TestMessageView_WithToolCalls_Done(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:    "assistant",
		Content: "done",
		ToolCalls: []ToolCallView{
			{Name: "Grep", Output: "found match", Done: true, IsError: false},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "done") {
		t.Errorf("View() = %q, should contain 'done'", v)
	}
	if !strings.Contains(v, "Grep") {
		t.Errorf("View() = %q, should contain tool name 'Grep'", v)
	}
}

func TestMessageView_WithToolCalls_Error(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:    "assistant",
		Content: "failed",
		ToolCalls: []ToolCallView{
			{Name: "Bash", Output: "exit code 1", Done: true, IsError: true},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "ERROR") {
		t.Errorf("View() = %q, should contain 'ERROR'", v)
	}
	if !strings.Contains(v, "exit code 1") {
		t.Errorf("View() = %q, should contain error output", v)
	}
}

func TestMessageView_ToolCallLongOutput(t *testing.T) {
	t.Parallel()

	longOutput := strings.Repeat("x", 300)
	m := MessageView{
		Role:    "assistant",
		Content: "result",
		ToolCalls: []ToolCallView{
			{Name: "Read", Output: longOutput, Done: true, IsError: false},
		},
	}
	v := m.View(80)
	// Output longer than 200 chars should not be shown
	if strings.Contains(v, strings.Repeat("x", 300)) {
		t.Error("long output (>200 chars) should not appear in view")
	}
	if !strings.Contains(v, "done") {
		t.Errorf("View() should still contain 'done' marker")
	}
}

func TestMessageView_ToolCallLongInput(t *testing.T) {
	t.Parallel()

	longInput := strings.Repeat("y", 300)
	m := MessageView{
		Role:    "assistant",
		Content: "working",
		ToolCalls: []ToolCallView{
			{Name: "Write", Input: longInput, Done: false},
		},
	}
	v := m.View(80)
	// Input longer than 200 chars should not be shown
	if strings.Contains(v, strings.Repeat("y", 300)) {
		t.Error("long input (>200 chars) should not appear in view")
	}
}

func TestMessageView_WordWrap(t *testing.T) {
	t.Parallel()

	// Long content that exceeds 20 chars should wrap
	m := MessageView{Role: "user", Content: "This is a very long sentence that should be wrapped properly"}
	v := m.View(20)
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

	m := MessageView{Role: "assistant", Content: "这是一段很长的中文文本需要被自动换行处理才能正确显示在终端中否则会超出屏幕宽度"}
	v := m.View(20)
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
		Role:    "assistant",
		Content: "result",
		ToolCalls: []ToolCallView{
			{Name: "Bash", Output: "", Done: true, IsError: false},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "done") {
		t.Errorf("View() should contain 'done'")
	}
}

// ---------------------------------------------------------------------------
// renderMessages
// ---------------------------------------------------------------------------

func TestRenderMessages_Empty(t *testing.T) {
	t.Parallel()

	v := renderMessages(nil, 80, 10)
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessages(nil) = %q, should contain welcome", v)
	}
}

func TestRenderMessages_EmptySlice(t *testing.T) {
	t.Parallel()

	v := renderMessages([]MessageView{}, 80, 10)
	if !strings.Contains(v, "Welcome to gbot") {
		t.Errorf("renderMessages([]) should contain welcome")
	}
}

func TestRenderMessages_WithMessages(t *testing.T) {
	t.Parallel()

	msgs := []MessageView{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	v := renderMessages(msgs, 80, 10)
	if !strings.Contains(v, "hello") {
		t.Error("should contain user message")
	}
	if !strings.Contains(v, "hi") {
		t.Error("should contain assistant message")
	}
}

func TestRenderMessages_HeightLimit(t *testing.T) {
	t.Parallel()

	// Create more messages than maxHeight allows
	msgs := []MessageView{
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "msg2"},
		{Role: "user", Content: "msg3"},
		{Role: "assistant", Content: "msg4"},
	}
	// Only 2 lines available — should truncate older messages
	v := renderMessages(msgs, 80, 2)
	// Most recent messages should appear
	if !strings.Contains(v, "msg4") {
		t.Error("should contain most recent message")
	}
}

func TestRenderMessages_PreservesBlankLines(t *testing.T) {
	t.Parallel()

	// 中间有空白行的消息，空行必须保留
	msgs := []MessageView{
		{Role: "assistant", Content: "第一段\n\n第二段"},
	}
	v := renderMessages(msgs, 80, 10)
	lines := strings.Split(v, "\n")
	// 统计空行（ANSI prefix之后）
	blankCount := 0
	for _, line := range lines {
		if line == "" {
			blankCount++
		}
	}
	// 输入有2个\n，产生至少2个空行段
	if blankCount < 2 {
		t.Errorf("blank lines not preserved: got %d blank lines in %v", blankCount, lines)
	}
}

// ---------------------------------------------------------------------------
// prettyJSON
// ---------------------------------------------------------------------------

func TestPrettyJSON_Empty(t *testing.T) {
	t.Parallel()

	if v := prettyJSON(nil); v != "" {
		t.Errorf("prettyJSON(nil) = %q, want empty", v)
	}
	if v := prettyJSON(json.RawMessage{}); v != "" {
		t.Errorf("prettyJSON(empty) = %q, want empty", v)
	}
}

func TestPrettyJSON_ValidObject(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"key":"value","num":42}`)
	v := prettyJSON(raw)
	if !strings.Contains(v, "key") {
		t.Errorf("prettyJSON() = %q, should contain 'key'", v)
	}
	if !strings.Contains(v, "value") {
		t.Errorf("prettyJSON() = %q, should contain 'value'", v)
	}
	// Should be indented
	if !strings.Contains(v, "  ") {
		t.Errorf("prettyJSON() = %q, should be indented", v)
	}
}

func TestPrettyJSON_InvalidJSON(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`not valid json`)
	v := prettyJSON(raw)
	if v != "not valid json" {
		t.Errorf("prettyJSON(invalid) = %q, want original string", v)
	}
}

func TestPrettyJSON_Array(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`[1,2,3]`)
	v := prettyJSON(raw)
	if !strings.Contains(v, "1") {
		t.Errorf("prettyJSON(array) = %q, should contain elements", v)
	}
}

// ---------------------------------------------------------------------------
// stripAnsi
// ---------------------------------------------------------------------------

func TestStripAnsi_PlainString(t *testing.T) {
	t.Parallel()

	v := stripAnsi("hello world")
	if len(v) != len("hello world") {
		t.Errorf("stripAnsi(plain) length = %d, want %d", len(v), len("hello world"))
	}
}

func TestStripAnsi_WithEscapeCodes(t *testing.T) {
	t.Parallel()

	// \x1b[31m is red foreground
	v := stripAnsi("\x1b[31mhello\x1b[0m")
	// stripAnsi replaces printable chars with 'x' — should have 5 chars for "hello"
	if len(v) != 5 {
		t.Errorf("stripAnsi(escaped) length = %d, want 5", len(v))
	}
}

func TestStripAnsi_Empty(t *testing.T) {
	t.Parallel()

	v := stripAnsi("")
	if v != "" {
		t.Errorf("stripAnsi('') = %q, want empty", v)
	}
}

func TestStripAnsi_OnlyEscape(t *testing.T) {
	t.Parallel()

	v := stripAnsi("\x1b[1;31m\x1b[0m")
	if v != "" {
		t.Errorf("stripAnsi(only escapes) = %q, want empty", v)
	}
}

// ---------------------------------------------------------------------------
// wordWrap
// ---------------------------------------------------------------------------

func TestWordWrap_ShortText(t *testing.T) {
	result := wordWrap("hello", 80)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestWordWrap_ZeroWidth(t *testing.T) {
	result := wordWrap("hello world", 0)
	if result != "hello world" {
		t.Errorf("zero width should return original, got %q", result)
	}
}

func TestWordWrap_NegativeWidth(t *testing.T) {
	result := wordWrap("hello", -1)
	if result != "hello" {
		t.Errorf("negative width should return original, got %q", result)
	}
}

func TestWordWrap_LongLine(t *testing.T) {
	result := wordWrap("abcdefghij", 5)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), result)
	}
	if lines[0] != "abcde" {
		t.Errorf("first line = %q, want 'abcde'", lines[0])
	}
	if lines[1] != "fghij" {
		t.Errorf("second line = %q, want 'fghij'", lines[1])
	}
}

func TestWordWrap_Newlines(t *testing.T) {
	result := wordWrap("hello\nworld", 80)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0] != "hello" {
		t.Errorf("first line = %q", lines[0])
	}
}

func TestWordWrap_CJK(t *testing.T) {
	// CJK chars are 2-wide, width=4 should fit 2 CJK chars per line
	result := wordWrap("你好世界", 4)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines for CJK, got %d: %q", len(lines), result)
	}
}

// ---------------------------------------------------------------------------
// runeDisplayWidth
// ---------------------------------------------------------------------------

func TestRuneDisplayWidth_ASCII(t *testing.T) {
	if w := runeDisplayWidth('A'); w != 1 {
		t.Errorf("ASCII 'A' width = %d, want 1", w)
	}
}

func TestRuneDisplayWidth_Control(t *testing.T) {
	if w := runeDisplayWidth('\t'); w != 0 {
		t.Errorf("tab width = %d, want 0", w)
	}
}

func TestRuneDisplayWidth_CJK(t *testing.T) {
	if w := runeDisplayWidth('你'); w != 2 {
		t.Errorf("CJK '你' width = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_Hangul(t *testing.T) {
	if w := runeDisplayWidth('한'); w != 2 {
		t.Errorf("Hangul width = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_Japanese(t *testing.T) {
	if w := runeDisplayWidth('あ'); w != 2 {
		t.Errorf("Hiragana width = %d, want 2", w)
	}
}

func TestRuneDisplayWidth_Emoji(t *testing.T) {
	// Emoji outside CJK ranges should default to 1
	if w := runeDisplayWidth('😀'); w != 1 {
		t.Errorf("emoji width = %d, want 1", w)
	}
}

func TestRuneDisplayWidth_AllCJKRanges(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want int
	}{
		{"Hangul Jamo", 0x1100, 2},
		{"CJK Misc 2E80", 0x2E80, 2},
		{"CJK Compatibility F900", 0xF900, 2},
		{"CJK Forms FE30", 0xFE30, 2},
		{"Fullwidth Forms FF01", 0xFF01, 2},
		{"Fullwidth Signs FFE0", 0xFFE0, 2},
		{"CJK Extension B 20000", 0x20000, 2},
		{"CJK Extension G 30000", 0x30000, 2},
		{"Latin extended default", 'é', 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runeDisplayWidth(tt.r); got != tt.want {
				t.Errorf("runeDisplayWidth(%U) = %d, want %d", tt.r, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// min
// ---------------------------------------------------------------------------

func TestMin_ALessThanB(t *testing.T) {
	if v := min(1, 2); v != 1 {
		t.Errorf("min(1,2) = %d, want 1", v)
	}
}

func TestMin_BLessThanA(t *testing.T) {
	if v := min(3, 2); v != 2 {
		t.Errorf("min(3,2) = %d, want 2", v)
	}
}

func TestMin_Equal(t *testing.T) {
	if v := min(5, 5); v != 5 {
		t.Errorf("min(5,5) = %d, want 5", v)
	}
}

// ---------------------------------------------------------------------------
// InsertChar cursor > len(value) edge case
// ---------------------------------------------------------------------------

func TestInput_InsertChar_CursorPastEnd(t *testing.T) {
	i := NewInput()
	i.SetValue("abc")
	// Force cursor past the end (shouldn't normally happen)
	i.cursor = 100
	i.InsertChar('X')
	if i.Value() != "abcX" {
		t.Errorf("expected 'abcX', got %q", i.Value())
	}
}

// ---------------------------------------------------------------------------
// Input.View — cursor in middle of text
// ---------------------------------------------------------------------------

func TestInput_View_CursorInMiddle(t *testing.T) {
	t.Parallel()

	i := NewInput()
	i.SetValue("abcde")
	i.Focus()
	// Move cursor to position 2 (between 'b' and 'c')
	i.CursorLeft()
	i.CursorLeft()
	i.CursorLeft()
	v := i.View()
	if !strings.Contains(v, "abcde") {
		t.Errorf("View() = %q, should contain full text", v)
	}
}

// ---------------------------------------------------------------------------
// MessageView — edge cases for tool call rendering
// ---------------------------------------------------------------------------

func TestMessageView_ToolCallError_NoOutput(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:    "assistant",
		Content: "failed",
		ToolCalls: []ToolCallView{
			{Name: "Bash", Output: "", Done: true, IsError: true},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "ERROR") {
		t.Errorf("View() = %q, should contain 'ERROR'", v)
	}
}

func TestMessageView_ToolCallRunning_NoInput(t *testing.T) {
	t.Parallel()

	m := MessageView{
		Role:    "assistant",
		Content: "working",
		ToolCalls: []ToolCallView{
			{Name: "Read", Input: "", Done: false},
		},
	}
	v := m.View(80)
	if !strings.Contains(v, "running...") {
		t.Errorf("View() = %q, should contain 'running...'", v)
	}
}
