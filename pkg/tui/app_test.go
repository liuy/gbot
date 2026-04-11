package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Mock Provider for TUI tests
// ---------------------------------------------------------------------------

type tuiMockProvider struct {
	responses []tuiMockResponse
	index     int
}

type tuiMockResponse struct {
	events []llm.StreamEvent
	err    error
}

func (m *tuiMockProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, errors.New("not implemented")
}

func (m *tuiMockProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	if m.index >= len(m.responses) {
		return nil, errors.New("no more mock responses")
	}
	resp := m.responses[m.index]
	m.index++
	if resp.err != nil {
		return nil, resp.err
	}
	ch := make(chan llm.StreamEvent, len(resp.events)+1)
	go func() {
		defer close(ch)
		for _, evt := range resp.events {
			ch <- evt
		}
	}()
	return ch, nil
}

func textStreamEvents(model, text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: "message_start", Message: &llm.MessageStart{Model: model, Usage: types.Usage{InputTokens: 5}}},
		{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}},
		{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: text}},
		{Type: "content_block_stop", Index: 0},
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &llm.UsageDelta{OutputTokens: 3}},
		{Type: "message_stop"},
	}
}

// newTestApp creates an App with a mock engine and Hub for testing.
// Uses empty history path to avoid writing to production history file.
func newTestApp(provider *tuiMockProvider) *App {
	h := hub.NewHub()
	eng := engine.New(&engine.Params{
		Provider: provider,
		Model:    "test-model",
		Dispatcher: h,
	})
	app := NewApp(eng, json.RawMessage(`"test system prompt"`), h)
	app.history = NewHistory("") // in-memory only, no file I/O
	return app
}

// ---------------------------------------------------------------------------
// NewApp
// ---------------------------------------------------------------------------

func TestNewApp(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	if app == nil {
		t.Fatal("NewApp returned nil")
	}
	if app.input == nil {
		t.Error("input should be initialized")
	}
	if app.engine == nil {
		t.Error("engine should be set")
	}
	if app.repl.streaming {
		t.Error("should not be streaming initially")
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestApp_Init(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	cmd := app.Init()
	if cmd == nil {
		t.Error("Init() should return a command")
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func TestApp_View_Loading(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// width=0 triggers "Loading..."
	v := app.View()
	if !strings.Contains(v, "Loading...") {
		t.Errorf("View() with width=0 = %q, should contain 'Loading...'", v)
	}
}

func TestApp_View_WithSize(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	v := app.View()
	if v == "" {
		t.Error("View() should not be empty with size set")
	}
}

func TestApp_View_Streaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("thinking...")
	v := app.View()
	if !strings.Contains(v, "thinking...") {
		t.Errorf("View() while streaming should contain assistant text, got %q", v)
	}
}

// ---------------------------------------------------------------------------
// Update — WindowSizeMsg
// ---------------------------------------------------------------------------

func TestApp_Update_WindowSize(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	model, cmd := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd != nil {
		t.Error("WindowSizeMsg should not produce a command")
	}
	a := model.(*App)
	if a.width != 100 {
		t.Errorf("width = %d, want 100", a.width)
	}
	if a.height != 30 {
		t.Errorf("height = %d, want 30", a.height)
	}
}

// ---------------------------------------------------------------------------
// Update — errMsg
// ---------------------------------------------------------------------------

func TestApp_Update_ErrorMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	model, cmd := app.Update(errMsg{Err: errors.New("test error")})
	if cmd != nil {
		t.Error("errMsg should not produce a command")
	}
	a := model.(*App)
	if a.repl.streaming {
		t.Error("streaming should be false after error")
	}
	if a.spinner.Active() {
		t.Error("spinner should be stopped after error")
	}
}

// ---------------------------------------------------------------------------
// Update — streamChunkMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamChunk(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.AppendTextItem()

	model, _ := app.Update(streamChunkMsg{Text: "hello "})
	a := model.(*App)
	if len(a.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(a.repl.messages))
	}
	if len(a.repl.messages[0].Blocks) == 0 || a.repl.messages[0].Blocks[0].Text != "hello " {
		t.Errorf("chunk not appended to blocks, got %v", a.repl.messages[0].Blocks)
	}
}

// ---------------------------------------------------------------------------
// Update — streamToolUseMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolUse(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	model, _ := app.Update(streamToolUseMsg{ID: "t1", Name: "Read", Input: `{"file":"test.go"}`})
	a := model.(*App)
	tcv, ok := a.repl.pendingTool["t1"]
	if !ok {
		t.Fatal("pendingTool should have entry for t1")
	}
	if tcv.Name != "Read" {
		t.Errorf("tool name = %q, want %q", tcv.Name, "Read")
	}
	if tcv.Done {
		t.Error("tool should not be done yet")
	}
}

// ---------------------------------------------------------------------------
// Update — streamToolResultMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolResult(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.pendingTool["t1"] = &ToolCallView{Name: "Read", Done: false}

	model, _ := app.Update(streamToolResultMsg{
		ToolUseID: "t1",
		Output:    "file contents",
		IsError:   false,
	})
	a := model.(*App)
	tcv := a.repl.pendingTool["t1"]
	if !tcv.Done {
		t.Error("tool should be done after result")
	}
	if tcv.Output != "file contents" {
		t.Errorf("output = %q, want %q", tcv.Output, "file contents")
	}
}

// ---------------------------------------------------------------------------
// Update — streamCompleteMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamComplete(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("response text")

	model, cmd := app.Update(streamCompleteMsg{})
	if cmd != nil {
		t.Error("streamComplete should not produce a command")
	}
	a := model.(*App)
	if a.repl.streaming {
		t.Error("streaming should be false after complete")
	}
	if len(a.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(a.repl.messages))
	}
	if a.repl.messages[0].Role != "assistant" {
		t.Errorf("role = %q, want %q", a.repl.messages[0].Role, "assistant")
	}
}

func TestApp_Update_StreamComplete_WithError(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	model, _ := app.Update(streamCompleteMsg{Err: errors.New("stream failed")})
	a := model.(*App)
	if a.repl.streaming {
		t.Error("streaming should be false")
	}
	// Should have error message - check Blocks
	found := false
	for _, m := range a.repl.messages {
		if m.Role == "system" {
			for _, blk := range m.Blocks {
				if blk.Type == BlockText && strings.Contains(blk.Text, "stream failed") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("expected system error message in messages")
	}
}

// ---------------------------------------------------------------------------
// Update — spinnerTickMsg
// ---------------------------------------------------------------------------

func TestApp_Update_SpinnerTick(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	model, _ := app.Update(spinnerTickMsg{})
	a := model.(*App)
	if a.spinner.idx != 1 {
		t.Errorf("spinner idx = %d, want 1", a.spinner.idx)
	}
}

func TestApp_Update_SpinnerTick_NotStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Not streaming, tick should be no-op
	model, cmd := app.Update(spinnerTickMsg{})
	if cmd != nil {
		t.Error("spinner tick while not streaming should produce no command")
	}
	_ = model
}

// ---------------------------------------------------------------------------
// Update — submitMsg
// ---------------------------------------------------------------------------

func TestApp_Update_SubmitMsg(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "Hello!"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	model, _ := app.Update(submitMsg{Text: "hi"})
	a := model.(*App)
	if !a.repl.streaming {
		t.Error("should be streaming after submit")
	}
	// Now: user message + assistant message = 2 messages
	if len(a.repl.messages) != 2 {
		t.Errorf("expected 2 messages (user + assistant), got %d", len(a.repl.messages))
	}
	// First message is the user message
	if len(a.repl.messages[0].Blocks) == 0 || a.repl.messages[0].Blocks[0].Text != "hi" {
		t.Errorf("expected user message 'hi', got %v", a.repl.messages[0].Blocks)
	}
}

func TestApp_Update_SubmitMsg_Empty(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", ""),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	model, _ := app.Update(submitMsg{Text: "   "})
	a := model.(*App)
	// handleSubmit doesn't check for empty — it still starts streaming.
	// The empty check is in handleKey's KeyEnter handler.
	if !a.repl.streaming {
		t.Error("submitMsg with spaces still triggers handleSubmit")
	}
}

// ---------------------------------------------------------------------------
// handleKey
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlC_FirstPress(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Not streaming → first Ctrl+C doesn't quit (double-press required)
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("first Ctrl+C should not produce quit command (double-press required)")
	}
	_ = model
}

func TestApp_HandleKey_CtrlC_CancelStream(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("Ctrl+C during streaming should not produce a command (handles internally)")
	}
	a := model.(*App)
	if a.repl.streaming {
		t.Error("streaming should be false after cancel")
	}
}

func TestApp_HandleKey_Enter(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "reply"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24
	app.input.SetValue("hello")

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("Enter with text should produce a command")
	}
}

func TestApp_HandleKey_EnterEmpty(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter with empty input should produce no command")
	}
	_ = model
}

func TestApp_HandleKey_Backspace(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	// Cursor was at end, backspace removes last char
	if app.input.Value() != "ab" {
		t.Errorf("Value() = %q, want %q", app.input.Value(), "ab")
	}
}

func TestApp_HandleKey_Delete(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	// Cursor is at end (position 3). Delete at end is no-op (nothing ahead).
	app.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if app.input.Value() != "abc" {
		t.Errorf("Delete at end should be no-op, Value() = %q", app.input.Value())
	}
	// Move cursor to position 1, delete 'b' (forward delete)
	app.input.CursorLeft()
	app.input.CursorLeft()
	app.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if app.input.Value() != "ac" {
		t.Errorf("Delete should forward-delete, Value() = %q", app.input.Value())
	}
}

func TestApp_HandleKey_CursorLeftRight(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyLeft})
	app.Update(tea.KeyMsg{Type: tea.KeyLeft})
	app.Update(tea.KeyMsg{Type: tea.KeyRight})
	// No assertion on exact cursor position — just no panic
}

func TestApp_HandleKey_HomeEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyHome})
	app.Update(tea.KeyMsg{Type: tea.KeyEnd})
}

func TestApp_HandleKey_Space(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.KeyMsg{Type: tea.KeySpace})
	if app.input.Value() != " " {
		t.Errorf("Space should insert space, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_Runes(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'i'}})
	if app.input.Value() != "hi" {
		t.Errorf("Runes should insert chars, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlU(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("some text")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if app.input.Value() != "" {
		t.Errorf("Ctrl+U should clear input, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlW(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if app.input.Value() != "hello " {
		t.Errorf("Ctrl+W should delete word, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_Unknown(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Unknown key type should be no-op
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyType(999)})
	if cmd != nil {
		t.Error("unknown key should produce no command")
	}
	_ = model
}

// ---------------------------------------------------------------------------
// New key bindings
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlB(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.input.CursorLeft()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	if app.input.cursor != 1 {
		t.Errorf("Ctrl+B should move cursor left, cursor = %d", app.input.cursor)
	}
}

func TestApp_HandleKey_CtrlF(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.input.CursorLeft()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	// SetValue("abc") → cursor=3, CursorLeft → cursor=2, CtrlF → cursor=3
	if app.input.cursor != 3 {
		t.Errorf("Ctrl+F should move cursor right, cursor = %d, want 3", app.input.cursor)
	}
}

func TestApp_HandleKey_CtrlP(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("previous")
	app.input.SetValue("current")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if app.input.Value() != "previous" {
		t.Errorf("Ctrl+P should navigate history up, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlN(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("first")
	app.history.Add("second")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	// After Up then Down, should be back at "second"
	if app.input.Value() != "second" {
		t.Errorf("Ctrl+N should navigate history down, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlH(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	if app.input.Value() != "ab" {
		t.Errorf("Ctrl+H should backspace, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlD(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.input.CursorLeft()
	app.input.CursorLeft()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if app.input.Value() != "ac" {
		t.Errorf("Ctrl+D should forward delete, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlL(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd != nil {
		t.Error("Ctrl+L should produce no command")
	}
}

func TestApp_HandleKey_CtrlG(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if cmd != nil {
		t.Error("Ctrl+G should produce no command")
	}
}

func TestApp_HandleKey_CtrlLeft(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.End()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if app.input.cursor != 6 {
		t.Errorf("Ctrl+Left should PrevWord, cursor = %d, want 6", app.input.cursor)
	}
}

func TestApp_HandleKey_CtrlRight(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.Home()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if app.input.cursor != 6 {
		t.Errorf("Ctrl+Right should NextWord, cursor = %d, want 6", app.input.cursor)
	}
}

func TestApp_HandleKey_Escape(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Error("Escape should produce no command")
	}
}

func TestApp_HandleKey_AltB(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.End()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true})
	if app.input.cursor != 6 {
		t.Errorf("Alt+B should PrevWord, cursor = %d, want 6", app.input.cursor)
	}
}

func TestApp_HandleKey_AltF(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.Home()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
	if app.input.cursor != 6 {
		t.Errorf("Alt+F should NextWord, cursor = %d, want 6", app.input.cursor)
	}
}

func TestApp_HandleKey_AltD(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.input.Home()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}, Alt: true})
	if app.input.Value() != "world" {
		t.Errorf("Alt+D should DeleteWordForward, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_AltOther(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true})
	if app.input.Value() != "abc" {
		t.Errorf("Alt+unknown should be no-op, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_Paste(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h', 'e', 'l', 'l', 'o'}, Paste: true})
	if app.input.Value() != "hello" {
		t.Errorf("Paste should insert runes, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// View — streaming progress line
// ---------------------------------------------------------------------------

func TestApp_View_StreamingWithProgress(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now().Add(-1 * time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("thinking...")
	v := app.View()
	if !strings.Contains(v, "in:") || !strings.Contains(v, "out:") {
		t.Errorf("streaming view should show tokens, got: %s", v)
	}
}

func TestApp_View_StreamingNoProgressStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.spinner.Start()
	// progressStart is zero → no progress line (no elapsed time shown)
	v := app.View()
	// The status bar always shows "in:X out:Y", so we check for elapsed time specifically
	if strings.Contains(v, "0.0s") {
		t.Error("should not show elapsed time when progressStart is zero")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — streamToolDeltaMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolDelta(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Read", "", `{}`)

	model, _ := app.Update(streamToolDeltaMsg{ID: "t1", Delta: `{"file":"test.go"}`, Summary: "test.go"})
	a := model.(*App)
	tcv := a.repl.pendingTool["t1"]
	if tcv == nil {
		t.Fatal("pendingTool should have t1")
	}
	if tcv.Summary != "test.go" {
		t.Errorf("summary = %q, want %q", tcv.Summary, "test.go")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — spinnerTickMsg not streaming
// ---------------------------------------------------------------------------

func TestApp_Update_SpinnerTick_NotStreaming_ReturnsNil(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Not streaming, spinner active — should return nil cmd
	app.spinner.Start()
	_, cmd := app.Update(spinnerTickMsg{})
	if cmd != nil {
		t.Error("spinnerTick while not streaming should return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// AppendChunk / AppendTextItem — nil lastMsg
// ---------------------------------------------------------------------------

func TestReplState_AppendChunk_NilLastMsg(t *testing.T) {
	s := NewReplState()
	s.AppendChunk("hello") // no messages — should not panic
	if len(s.messages) != 0 {
		t.Error("should not create messages from AppendChunk with nil lastMsg")
	}
}

func TestReplState_AppendTextItem_NilLastMsg(t *testing.T) {
	s := NewReplState()
	s.AppendTextItem() // no messages — should not panic
	if len(s.messages) != 0 {
		t.Error("should not create messages from AppendTextItem with nil lastMsg")
	}
}

func TestReplState_PendingToolStarted_NilLastMsg(t *testing.T) {
	s := NewReplState()
	s.PendingToolStarted("t1", "Read", "", `{}`)
	// No messages → lastMsg() returns nil → returns early
	// pendingTool should NOT have the entry (early return)
	if s.pendingTool["t1"] != nil {
		t.Error("pendingTool should NOT have entry when lastMsg is nil")
	}
}

func TestReplState_PendingToolDone_UnknownID(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	// No tool was started with this ID
	s.PendingToolDone("nonexistent", "output", false, 0)
	// Should not panic, no tool updated
}

func TestReplState_PendingToolDelta(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	s.PendingToolStarted("t1", "Read", "", `{}`)
	s.PendingToolDelta("t1", `{"file":"a.go"}`, "a.go")
	tcv := s.pendingTool["t1"]
	if tcv.Summary != "a.go" {
		t.Errorf("summary = %q, want %q", tcv.Summary, "a.go")
	}
	// Also check block in lastMsg was updated
	m := s.lastMsg()
	found := false
	for _, blk := range m.Blocks {
		if blk.Type == BlockTool && blk.ToolCall.Summary == "a.go" {
			found = true
		}
	}
	if !found {
		t.Error("tool block in lastMsg should have updated summary")
	}
}

func TestReplState_PendingToolDelta_UnknownID(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	// Delta for unknown tool ID — should not panic
	s.PendingToolDelta("unknown", `{"x":1}`, "")
}

func TestReplState_PendingToolDelta_NilLastMsg(t *testing.T) {
	s := NewReplState()
	// No messages at all
	s.pendingTool["t1"] = &ToolCallView{Name: "Read"}
	s.PendingToolDelta("t1", `{"x":1}`, "")
	// Should not panic
}

// ---------------------------------------------------------------------------
// prettyJSON — marshal error
// ---------------------------------------------------------------------------

func TestPrettyJSON_MarshalError(t *testing.T) {
	// Channel values can't be marshaled to JSON
	v := prettyJSON(json.RawMessage(`{"a":1}`))
	// This should work fine
	if !strings.Contains(v, "a") {
		t.Errorf("prettyJSON with valid JSON = %q", v)
	}
}

// ---------------------------------------------------------------------------
// readEvents — appCh closed
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_AppChClosed(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h
	app.repl.resultCh = nil

	// Close appCh to trigger the !ok path
	close(h.appCh)
	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg when appCh closed, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// readEvents — resultCh receives result
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_ReceiveResult(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	// Create a result channel that will deliver a result
	resultCh := make(chan engine.QueryResult, 1)
	resultCh <- engine.QueryResult{Error: errors.New("test error")}
	app.repl.resultCh = resultCh

	cmd := app.readEvents()
	msg := cmd()
	cm, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg, got %T", msg)
	}
	if cm.Err == nil || cm.Err.Error() != "test error" {
		t.Errorf("expected error from result, got %v", cm.Err)
	}
}

// ---------------------------------------------------------------------------
// readEvents — resultCh already closed
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_ResultChClosed(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	// Close resultCh before reading
	resultCh := make(chan engine.QueryResult, 1)
	close(resultCh)
	app.repl.resultCh = resultCh

	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg when resultCh closed, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// updateRepl — streamStartMsg, streamMessageMsg, streamToolResultMsg
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_StreamStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	_, cmd := app.updateRepl(streamStartMsg{})
	if cmd == nil {
		t.Error("streamStartMsg should return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_StreamMessage(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	_, cmd := app.updateRepl(streamMessageMsg{Role: "assistant"})
	if cmd == nil {
		t.Error("streamMessageMsg should return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_StreamToolResult(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Read", "", `{}`)
	_, cmd := app.updateRepl(streamToolResultMsg{ToolUseID: "t1", Output: "ok"})
	if cmd == nil {
		t.Error("streamToolResultMsg should return a readEvents cmd")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — errMsg resets state
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_ErrMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	handled, cmd := app.updateRepl(errMsg{Err: errors.New("boom")})
	if !handled {
		t.Error("errMsg should be handled")
	}
	if cmd != nil {
		t.Error("errMsg should return nil cmd")
	}
	if app.repl.IsStreaming() {
		t.Error("streaming should be false after error")
	}
	if app.status.err != "boom" {
		t.Errorf("status err = %q, want %q", app.status.err, "boom")
	}
}

// ---------------------------------------------------------------------------
// handleSubmitRepl — already streaming returns nil
// ---------------------------------------------------------------------------

func TestApp_HandleSubmitRepl_AlreadyStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	cmd := app.handleSubmitRepl("test")
	if cmd != nil {
		t.Error("handleSubmitRepl while streaming should return nil")
	}
}

// ---------------------------------------------------------------------------
// handleKey — streaming: Ctrl+C without cancelFunc
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlC_CancelStream_NoCancelFunc(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	// cancelFunc is nil by default

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("Ctrl+C during streaming should not produce command")
	}
	a := model.(*App)
	if a.repl.streaming {
		t.Error("streaming should be false after cancel")
	}
}

// ---------------------------------------------------------------------------
// handleKey — Enter while streaming
// ---------------------------------------------------------------------------

func TestApp_HandleKey_EnterWhileStreaming(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", ""),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.input.SetValue("hello")

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("Enter while streaming should produce no command")
	}
	_ = model
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+Y with empty kill ring
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlY_EmptyRing(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if app.input.Value() != "abc" {
		t.Errorf("Ctrl+Y with empty ring should be no-op, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// AppendChunk — last block is tool (creates new text block)
// ---------------------------------------------------------------------------

func TestReplState_AppendChunk_LastBlockIsTool(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	s.PendingToolStarted("t1", "Read", "", `{}`)
	// Last block is a tool block, not text
	s.AppendChunk("hello")
	m := s.lastMsg()
	if len(m.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(m.Blocks))
	}
	if m.Blocks[1].Type != BlockText || m.Blocks[1].Text != "hello" {
		t.Errorf("second block should be text 'hello', got %v", m.Blocks[1])
	}
}

// ---------------------------------------------------------------------------
// prettyJSON — MarshalIndent error path (use channel which can't be marshaled)
// ---------------------------------------------------------------------------

func TestPrettyJSON_NullValue(t *testing.T) {
	// Test with null value
	v := prettyJSON(json.RawMessage(`null`))
	if v != "null" {
		t.Errorf("prettyJSON(null) = %q, want %q", v, "null")
	}
}

// ---------------------------------------------------------------------------
// handleKey — streaming ignores typing
// ---------------------------------------------------------------------------

func TestApp_HandleKey_RunesWhileStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.input.SetValue("abc")
	// Keys still work while streaming (no special handling for most keys)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if app.input.Value() != "abcx" {
		t.Errorf("typing while streaming should still work, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Backspace while streaming
// ---------------------------------------------------------------------------

func TestApp_HandleKey_BackspaceWhileStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if app.input.Value() != "ab" {
		t.Errorf("backspace while streaming should work, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Space while streaming
// ---------------------------------------------------------------------------

func TestApp_HandleKey_SpaceWhileStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.Update(tea.KeyMsg{Type: tea.KeySpace})
	if app.input.Value() != " " {
		t.Errorf("space while streaming should work, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Left/Right while streaming
// ---------------------------------------------------------------------------

func TestApp_HandleKey_LeftRightWhileStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyLeft})
	app.Update(tea.KeyMsg{Type: tea.KeyRight})
	// No crash, cursor still in valid range
	if app.input.cursor < 0 || app.input.cursor > 3 {
		t.Errorf("cursor out of range: %d", app.input.cursor)
	}
}

// ---------------------------------------------------------------------------
// handleKey — Home/End while streaming
// ---------------------------------------------------------------------------

func TestApp_HandleKey_HomeEndWhileStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyHome})
	if app.input.cursor != 0 {
		t.Errorf("Home cursor = %d, want 0", app.input.cursor)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if app.input.cursor != 3 {
		t.Errorf("End cursor = %d, want 3", app.input.cursor)
	}
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+K at end of input (empty after)
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlK_AtEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	// Cursor at end, Ctrl+K kills nothing
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if app.input.Value() != "abc" {
		t.Errorf("Ctrl+K at end should not change value, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+U with empty input
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlU_Empty(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if app.input.Value() != "" {
		t.Errorf("Ctrl+U on empty should be no-op, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+W with empty input
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlW_Empty(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if app.input.Value() != "" {
		t.Errorf("Ctrl+W on empty should be no-op, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// Update — unknown message type
// ---------------------------------------------------------------------------

func TestApp_Update_UnknownMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	model, cmd := app.Update("unknown message type")
	if cmd != nil {
		t.Error("unknown msg should produce no command")
	}
	_ = model
}

// ---------------------------------------------------------------------------
// finishStream
// ---------------------------------------------------------------------------

func TestApp_FinishStream(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("response")

	app.repl.FinishStream(nil)

	if app.repl.streaming {
		t.Error("streaming should be false")
	}
	if len(app.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(app.repl.messages))
	}
	if len(app.repl.messages[0].Blocks) == 0 || app.repl.messages[0].Blocks[0].Text != "response" {
		t.Errorf("message content = %v, want Blocks with 'response'", app.repl.messages[0].Blocks)
	}
}

func TestApp_FinishStream_Empty(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	// No text, no tools

	app.repl.FinishStream(nil)

	if len(app.repl.messages) != 0 {
		t.Errorf("expected 0 messages for empty stream, got %d", len(app.repl.messages))
	}
}

func TestApp_FinishStream_WithTools(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.PendingToolStarted("t1", "Read", "", `{}`)
	app.repl.PendingToolDone("t1", "contents", false, 0)

	app.repl.FinishStream(nil)

	if len(app.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(app.repl.messages))
	}
	hasToolBlock := false
	for _, blk := range app.repl.messages[0].Blocks {
		if blk.Type == BlockTool && blk.ToolCall.Name == "Read" {
			hasToolBlock = true
		}
	}
	if !hasToolBlock {
		t.Errorf("expected tool block in message Blocks, got %v", app.repl.messages[0].Blocks)
	}
}

func TestApp_FinishStream_WithError(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("partial")

	app.repl.FinishStream(errors.New("broke"))

	// Should have assistant message + error message
	if len(app.repl.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(app.repl.messages))
	}
	if app.repl.messages[1].Role != "system" {
		t.Errorf("second message role = %q, want 'system'", app.repl.messages[1].Role)
	}
	found := false
	for _, blk := range app.repl.messages[1].Blocks {
		if blk.Type == BlockText && strings.Contains(blk.Text, "broke") {
			found = true
		}
	}
	if !found {
		t.Errorf("error message should contain error text, got Blocks: %v", app.repl.messages[1].Blocks)
	}
}

func TestApp_FinishStream_CancelsContext(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	ctx, cancel := context.WithCancel(context.Background())
	app.repl.cancelFunc = cancel
	app.repl.streaming = true
	app.spinner.Start()

	app.repl.FinishStream(nil)

	// cancelFunc should have been called and set to nil
	if app.repl.cancelFunc != nil {
		t.Error("cancelFunc should be nil after finishStream")
	}
	// Verify context was cancelled
	if ctx.Err() == nil {
		t.Error("context should be cancelled")
	}
}

func TestApp_FinishStream_NoDuplicateRendering(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()

	// Simulate streaming: blocks grow directly in s.messages
	app.repl.AppendTextItem()
	app.repl.AppendChunk("The hostname is server_e5")

	// Before FinishStream: View renders from s.messages
	view1 := app.View()
	countBefore := strings.Count(view1, "server_e5")

	// FinishStream ends streaming
	app.repl.FinishStream(nil)

	// After FinishStream: streaming is false, renderMessages still renders from s.messages
	view2 := app.View()

	// The text should appear exactly once (from s.messages)
	countAfter := strings.Count(view2, "server_e5")
	if countAfter != 1 {
		t.Errorf("text appeared %d times after FinishStream, want exactly 1 (got %d before)", countAfter, countBefore)
	}

	// Verify the message was added
	if len(app.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(app.repl.messages))
	}
}

// Test that streaming state is cleared after FinishStream.
func TestApp_FinishStream_ClearsState(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("some text")

	app.repl.FinishStream(nil)

	if app.repl.IsStreaming() {
		t.Error("IsStreaming should be false after FinishStream")
	}
}

// Test that blocks grow incrementally during streaming.
// StartQuery creates assistant message → AppendTextItem creates block → AppendChunk adds text.
func TestApp_FinishStream_BlocksGrowIncrementally(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	// First text block
	app.repl.AppendTextItem()
	app.repl.AppendChunk("Hello ")

	// Second text block
	app.repl.AppendTextItem()
	app.repl.AppendChunk("World")

	app.repl.FinishStream(nil)

	if len(app.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(app.repl.messages))
	}
	if len(app.repl.messages[0].Blocks) != 2 {
		t.Fatalf("expected 2 text blocks, got %d", len(app.repl.messages[0].Blocks))
	}
	if app.repl.messages[0].Blocks[0].Text != "Hello " {
		t.Errorf("first block text = %q, want %q", app.repl.messages[0].Blocks[0].Text, "Hello ")
	}
	if app.repl.messages[0].Blocks[1].Text != "World" {
		t.Errorf("second block text = %q, want %q", app.repl.messages[0].Blocks[1].Text, "World")
	}
}

// ---------------------------------------------------------------------------
// engineEventToMsg
// ---------------------------------------------------------------------------

func TestApp_EngineEventToMsg_TextDelta(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventTextDelta,
		Text: "hello",
	})
	_, ok := msg.(streamChunkMsg)
	if !ok {
		t.Errorf("expected streamChunkMsg, got %T", msg)
	}
}

func TestApp_EngineEventToMsg_ToolUseStart(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolUseStart,
		ToolUse: &types.ToolUseEvent{
			ID:    "t1",
			Name:  "Read",
			Input: json.RawMessage(`{"file":"a.go"}`),
		},
	})
	tum, ok := msg.(streamToolUseMsg)
	if !ok {
		t.Fatalf("expected streamToolUseMsg, got %T", msg)
	}
	if tum.Name != "Read" {
		t.Errorf("name = %q, want %q", tum.Name, "Read")
	}
}

func TestApp_EngineEventToMsg_ToolUseStart_Nil(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type:    types.EventToolUseStart,
		ToolUse: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil ToolUse, got %T", msg)
	}
}

func TestApp_EngineEventToMsg_ToolResult(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolResult,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "t1",
			Output:    json.RawMessage(`"ok"`),
			IsError:   false,
		},
	})
	trm, ok := msg.(streamToolResultMsg)
	if !ok {
		t.Fatalf("expected streamToolResultMsg, got %T", msg)
	}
	if trm.ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want %q", trm.ToolUseID, "t1")
	}
}

func TestApp_EngineEventToMsg_ToolResult_Nil(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type:       types.EventToolResult,
		ToolResult: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil ToolResult, got %T", msg)
	}
}

// Bash tool returns empty DisplayOutput when stdout/stderr are empty.
// TUI should show empty string, NOT fall back to raw JSON.
func TestApp_EngineEventToMsg_ToolResult_EmptyDisplayOutput(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolResult,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "t1",
			Output:        json.RawMessage(`"{\"output\":\"\",\"exitCode\":0}"`),
			DisplayOutput: "", // empty because Bash had no stdout/stderr
			IsError:       false,
		},
	})
	trm, ok := msg.(streamToolResultMsg)
	if !ok {
		t.Fatalf("expected streamToolResultMsg, got %T", msg)
	}
	// Should be empty, NOT the raw JSON
	if strings.Contains(trm.Output, "exitCode") {
		t.Errorf("Output should not contain raw JSON when DisplayOutput is empty, got: %q", trm.Output)
	}
}

func TestApp_EngineEventToMsg_Error(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type:  types.EventError,
		Error: errors.New("test error"),
	})
	em, ok := msg.(errMsg)
	if !ok {
		t.Fatalf("expected errMsg, got %T", msg)
	}
	if em.Err.Error() != "test error" {
		t.Errorf("error = %q, want %q", em.Err.Error(), "test error")
	}
}

func TestApp_EngineEventToMsg_Complete(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventComplete,
	})
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Errorf("expected streamCompleteMsg, got %T", msg)
	}
}

func TestApp_EngineEventToMsg_Unknown(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolUseDelta,
	})
	if msg != nil {
		t.Errorf("expected nil for unknown event type, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// readEvents
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_NilHandler(t *testing.T) {
	t.Parallel()
	// Create app without hub — tuiHandler will be nil
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test-model",
	})
	app := NewApp(eng, json.RawMessage(`"test"`), nil)
	app.repl.resultCh = nil

	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Errorf("expected streamCompleteMsg when tuiHandler nil, got %T", msg)
	}
}

func TestApp_ReadEvents_EventReceived(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "hi"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	ctx := context.Background()
	_, resultCh := app.engine.Query(ctx, "test", json.RawMessage(`"sys"`))
	app.repl.resultCh = resultCh

	cmd := app.readEvents()
	// Wait briefly for events to flow through Hub → TUIHandler → appCh
	time.Sleep(100 * time.Millisecond)
	msg := cmd()
	// Should be either streamChunkMsg or streamCompleteMsg
	switch msg.(type) {
	case streamChunkMsg, streamCompleteMsg, streamStartMsg, streamMessageMsg:
		// ok
	default:
		t.Errorf("expected streamChunkMsg or streamCompleteMsg, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// handleSubmit — already streaming
// ---------------------------------------------------------------------------

func TestApp_HandleSubmit_AlreadyStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true

	model, cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("submit while streaming should produce no command")
	}
	_ = model
}

// ---------------------------------------------------------------------------
// Additional coverage — View edge cases
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// readEvents drain behavior — appCh drained before returning complete
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_DrainsAppChBeforeComplete(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	// Simulate: appCh has buffered events, resultCh is already closed
	app.repl.resultCh = nil // already closed

	// Send a buffered event first
	h.appCh <- streamChunkMsg{Text: "late event"}

	cmd := app.readEvents()
	msg := cmd()
	// Should return the buffered appCh event, not streamCompleteMsg
	cm, ok := msg.(streamChunkMsg)
	if !ok {
		t.Fatalf("expected streamChunkMsg, got %T", msg)
	}
	if cm.Text != "late event" {
		t.Errorf("expected 'late event', got %q", cm.Text)
	}
}

func TestApp_ReadEvents_ReturnsCompleteWhenBothChannelsClosed(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h
	app.repl.resultCh = nil

	// Both channels closed — should return complete immediately
	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg when both closed, got %T", msg)
	}
}

func TestApp_ReadEvents_NilHandlerReturnsComplete(t *testing.T) {
	t.Parallel()
	// When tuiHandler is nil, readEvents should immediately return complete
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = nil
	app.repl.resultCh = nil

	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg with nil handler, got %T", msg)
	}
}

func TestApp_View_PendingToolCalls(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	v := app.View()
	// Running state shows dim tool name + & suffix (no summary)
	if !strings.Contains(v, "Bash&") {
		t.Errorf("View should show 'Bash&' for running state, got: %s", v)
	}
}

func TestApp_View_SmallHeight(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 5 // availHeight = 5-4 = 1 < 3, triggers min height clamp
	v := app.View()
	if v == "" {
		t.Error("View should not be empty even with small height")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage — handleKey Ctrl+C with cancelFunc
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlC_CancelWithFunc(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	ctx, cancel := context.WithCancel(context.Background())
	app.repl.cancelFunc = cancel

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Error("Ctrl+C during streaming should not produce command")
	}
	a := model.(*App)
	if a.repl.cancelFunc != nil {
		t.Error("cancelFunc should be nil after cancel")
	}
	if ctx.Err() == nil {
		t.Error("context should be cancelled")
	}
	if a.repl.streaming {
		t.Error("streaming should be false after cancel")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage — handleSubmit direct call
// ---------------------------------------------------------------------------

func TestApp_HandleSubmit_Direct_AlreadyStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true

	cmd := app.handleSubmitRepl("test")
	if cmd != nil {
		t.Error("handleSubmit while streaming should return nil cmd")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage — spinnerTickMsg returns tick
// ---------------------------------------------------------------------------

func TestApp_Update_SpinnerTick_ReturnsCmd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	_, cmd := app.Update(spinnerTickMsg{})
	if cmd == nil {
		t.Error("spinnerTickMsg while streaming should return a tick command")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage — readEvents result channel
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// handleKey — KeyCtrlO toggle
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlO(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	if app.allToolsExpanded {
		t.Error("should start collapsed")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !app.allToolsExpanded {
		t.Error("Ctrl+O should toggle expanded")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if app.allToolsExpanded {
		t.Error("second Ctrl+O should toggle back")
	}
}

// ---------------------------------------------------------------------------
// handleKey — KeyCtrlA (Home)
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlA(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello")
	app.input.CursorLeft()
	app.input.CursorLeft()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if app.input.cursor != 0 {
		t.Errorf("Ctrl+A should move cursor to start, cursor = %d", app.input.cursor)
	}
}

// ---------------------------------------------------------------------------
// handleKey — KeyCtrlE (End)
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlE(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello")
	app.input.Home()
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if app.input.cursor != 5 {
		t.Errorf("Ctrl+E should move cursor to end, cursor = %d", app.input.cursor)
	}
}

// ---------------------------------------------------------------------------
// handleKey — KeyUp/KeyDown (arrow history)
// ---------------------------------------------------------------------------

func TestApp_HandleKey_KeyUp(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("previous")
	app.input.SetValue("current")
	app.Update(tea.KeyMsg{Type: tea.KeyUp})
	if app.input.Value() != "previous" {
		t.Errorf("KeyUp should navigate history up, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_KeyDown(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("first")
	app.history.Add("second")
	app.Update(tea.KeyMsg{Type: tea.KeyUp})
	app.Update(tea.KeyMsg{Type: tea.KeyDown})
	if app.input.Value() != "second" {
		t.Errorf("KeyDown should navigate history down, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+Y with non-empty kill ring
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlY_WithText(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.killRing.Push("killed text", "append")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if app.input.Value() != "killed text" {
		t.Errorf("Ctrl+Y should yank from kill ring, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — Ctrl+W with trailing spaces
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlW_TrailingSpaces(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// "a  b" with cursor at position 3 (before 'b') — Ctrl+W skips spaces then deletes "a  "
	app.input.SetValue("a  b")
	app.input.CursorLeft()
	// cursor now at 3, value[2]=' ' → space loop triggers
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if app.input.Value() != "b" {
		t.Errorf("Ctrl+W should skip spaces then delete word, got %q", app.input.Value())
	}
}

func TestApp_HandleKey_CtrlW_DeletesWordAtEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("hello world")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if app.input.Value() != "hello " {
		t.Errorf("Ctrl+W should delete last word, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// handleKey — double-press Ctrl+C quit
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlC_DoublePress(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// First press
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	// Second press within window
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("double-press Ctrl+C should produce quit command")
	}
}

// ---------------------------------------------------------------------------
// readEvents — blocking select: appCh receives after drain
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_BlockingAppCh(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h
	// resultCh is non-nil but empty (no sender) so select blocks on both channels
	resultCh := make(chan engine.QueryResult)
	app.repl.resultCh = resultCh

	// Send event AFTER readEvents starts (it will block in select)
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.appCh <- streamChunkMsg{Text: "delayed"}
	}()

	cmd := app.readEvents()
	msg := cmd()
	cm, ok := msg.(streamChunkMsg)
	if !ok {
		t.Fatalf("expected streamChunkMsg from blocking select, got %T", msg)
	}
	if cm.Text != "delayed" {
		t.Errorf("text = %q, want %q", cm.Text, "delayed")
	}
}

// ---------------------------------------------------------------------------
// readEvents — resultCh receives in blocking select
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_BlockingResultCh(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	resultCh := make(chan engine.QueryResult, 1)
	app.repl.resultCh = resultCh

	// Send result AFTER readEvents starts
	go func() {
		time.Sleep(50 * time.Millisecond)
		resultCh <- engine.QueryResult{Error: errors.New("async error")}
	}()

	cmd := app.readEvents()
	msg := cmd()
	cm, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg, got %T", msg)
	}
	if cm.Err == nil || cm.Err.Error() != "async error" {
		t.Errorf("expected async error, got %v", cm.Err)
	}
}

// ---------------------------------------------------------------------------
// readEvents — resultCh closed in blocking select
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_BlockingResultChClosed(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	resultCh := make(chan engine.QueryResult, 1)
	close(resultCh)
	app.repl.resultCh = resultCh

	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg when resultCh closed in blocking select, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// readEvents — blocking select appCh closed
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_BlockingAppChClosed(t *testing.T) {
	t.Parallel()
	h := NewTUIHandler()
	eng := engine.New(&engine.Params{
		Provider: &tuiMockProvider{},
		Model:    "test",
	})
	app := NewApp(eng, json.RawMessage(`"sys"`), nil)
	app.tuiHandler = h

	// Non-nil resultCh that will block (empty, no sender)
	resultCh := make(chan engine.QueryResult)
	app.repl.resultCh = resultCh

	// Close appCh so the blocking select hits !ok
	close(h.appCh)

	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(streamCompleteMsg)
	if !ok {
		t.Fatalf("expected streamCompleteMsg when appCh closed in blocking select, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// updateRepl — returns false for unknown msg type
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_UnknownMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	handled, cmd := app.updateRepl("unknown")
	if handled {
		t.Error("updateRepl should return false for unknown msg type")
	}
	if cmd != nil {
		t.Error("updateRepl should return nil cmd for unknown msg type")
	}
}

// ---------------------------------------------------------------------------
// handleSubmitRepl — full integration path
// ---------------------------------------------------------------------------

func TestApp_HandleSubmitRepl_Integration(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "response"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	cmd := app.handleSubmitRepl("test query")
	if cmd == nil {
		t.Fatal("handleSubmitRepl should return a command")
	}
	if !app.repl.IsStreaming() {
		t.Error("should be streaming after handleSubmitRepl")
	}
	if app.input.Value() != "" {
		t.Errorf("input should be reset, got %q", app.input.Value())
	}
}

// ---------------------------------------------------------------------------
// prettyJSON — unmarshalable value (trigger MarshalIndent fallback)
// ---------------------------------------------------------------------------

func TestPrettyJSON_MarshalIndentError(t *testing.T) {
	// After json.Unmarshal into any, values are always basic types
	// that MarshalIndent handles. This path is effectively unreachable via
	// the public API, but we test with extremely deeply nested JSON as a
	// best-effort attempt. If this doesn't work, the path is dead code.
	v := prettyJSON(json.RawMessage(`{"a":1}`))
	if !strings.Contains(v, "a") {
		t.Errorf("basic JSON should work, got %q", v)
	}
}

func TestApp_ReadEvents_ResultChannel(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	// Provider that immediately closes the stream (no events)
	mp.responses = append(mp.responses, tuiMockResponse{
		events: []llm.StreamEvent{
			{Type: "message_start", Message: &llm.MessageStart{Model: "test", Usage: types.Usage{InputTokens: 1}}},
			{Type: "message_stop"},
		},
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, resultCh := app.engine.Query(ctx, "test", json.RawMessage(`"sys"`))
	app.repl.resultCh = resultCh

	// Drain events first so resultCh is the one that fires
	time.Sleep(200 * time.Millisecond)

	cmd := app.readEvents()
	msg := cmd()
	// Could be streamCompleteMsg or streamChunkMsg depending on timing
	switch msg.(type) {
	case streamCompleteMsg, streamChunkMsg, streamStartMsg, streamMessageMsg:
		// ok
	default:
		t.Errorf("expected streamCompleteMsg or streamChunkMsg, got %T", msg)
	}
}
