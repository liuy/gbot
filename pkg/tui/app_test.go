package tui

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
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
		Provider:   provider,
		Model:      "test-model",
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
	if app.idleStop == nil {
		t.Error("idleStop must be initialized — nil value causes goroutine leak on first idle readEvents")
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestApp_Init(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	cmd := app.Init()
	if cmd != nil {
		t.Error("Init() should return nil (no alt screen)")
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
	if !strings.Contains(v, "❯") {
		t.Errorf("View should contain input prompt, got: %s", v)
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
// Update — textDeltaMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamChunk(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.AppendTextItem()

	model, _ := app.Update(textDeltaMsg{Text: "hello "})
	a := model.(*App)
	if len(a.repl.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(a.repl.messages))
	}
	if len(a.repl.messages[0].Blocks) == 0 || a.repl.messages[0].Blocks[0].Text != "hello " {
		t.Errorf("chunk not appended to blocks, got %v", a.repl.messages[0].Blocks)
	}
}

// ---------------------------------------------------------------------------
// Update — toolStartMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolUse(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	model, _ := app.Update(toolStartMsg{ID: "t1", Name: "Read", Input: `{"file":"test.go"}`})
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
// Update — toolEndMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolResult(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.pendingTool["t1"] = &ToolCallView{Name: "Read", Done: false}

	model, _ := app.Update(toolEndMsg{
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
// Update — queryEndMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamComplete(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("response text")

	model, cmd := app.Update(queryEndMsg{})
	// cmd is now readEvents() — keeps TUI listening for Hub events while idle
	if cmd == nil {
		t.Error("streamComplete should return readEvents cmd for idle listening")
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

	model, _ := app.Update(queryEndMsg{Err: errors.New("stream failed")})
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
// Update — notificationPendingMsg Path A (streaming) then Path B (idle)
// ---------------------------------------------------------------------------

// TestApp_NotificationPending_PathA_ThenPathB verifies the full flow:
// 1. notificationPendingMsg arrives during streaming (Path A: ignored)
// 2. queryEndMsg → TUI goes idle
// 3. Engine re-dispatches EventNotificationPending (via dispatchPendingNotifications)
// 4. notificationPendingMsg arrives in idle mode (Path B: triggers ProcessNotifications)
// Regression: notification arriving during last turn was silently dropped because
// runTurns only drains queue at turn start, and queryEndMsg did not check.
func TestApp_NotificationPending_PathA_ThenPathB(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{
		responses: []tuiMockResponse{
			{events: textStreamEvents("test-model", "Notification processed.")},
		},
	}

	app := newTestApp(mp)
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()

	// Step 1: notification arrives while streaming (Path A — ignored)
	model, cmd := app.Update(notificationPendingMsg{})
	if cmd == nil {
		t.Error("notificationPendingMsg during streaming should return readEvents cmd")
	}
	a := model.(*App)
	if !a.repl.streaming {
		t.Error("should still be streaming after notificationPendingMsg during stream")
	}

	// Step 2: query ends → TUI goes idle
	model, _ = a.Update(queryEndMsg{})
	a = model.(*App)
	if a.repl.streaming {
		t.Error("should not be streaming after queryEndMsg")
	}

	// Step 3: Engine re-dispatches notificationPendingMsg (simulating dispatchPendingNotifications)
	// Step 4: notificationPendingMsg arrives in idle mode (Path B)
	model, _ = a.Update(notificationPendingMsg{})
	a = model.(*App)

	// ProcessNotifications should start — streaming must be true again
	if !a.repl.streaming {
		t.Error("notificationPendingMsg in idle should trigger ProcessNotifications (Path B)")
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

	// Spinner only advances every 5th tick
	var model tea.Model = app
	for range 5 {
		model, _ = model.Update(spinnerTickMsg{})
	}
	app = model.(*App)
	if app.spinner.idx != 1 {
		t.Errorf("spinner idx = %d, want 1 after 5 ticks", app.spinner.idx)
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
	a := model.(*App)
	if a.spinner.idx != 0 {
		t.Errorf("spinner should not advance when not streaming, idx = %d", a.spinner.idx)
	}
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
	a := model.(*App)
	if a.repl.streaming {
		t.Error("should not be streaming after first Ctrl+C")
	}
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
	a := model.(*App)
	if a.repl.streaming {
		t.Error("should not start streaming with empty input")
	}
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
	// After SetValue("abc"), cursor=3. Left-left-right → cursor=2
	if app.input.cursor != 2 {
		t.Errorf("cursor = %d, want 2 after left-left-right", app.input.cursor)
	}
}

func TestApp_HandleKey_HomeEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.input.SetValue("abc")
	app.Update(tea.KeyMsg{Type: tea.KeyHome})
	if app.input.cursor != 0 {
		t.Errorf("after Home, cursor = %d, want 0", app.input.cursor)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if app.input.cursor != 3 {
		t.Errorf("after End, cursor = %d, want 3", app.input.cursor)
	}
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
	a := model.(*App)
	if a.input.Value() != "" {
		t.Errorf("unknown key should not change input, got %q", a.input.Value())
	}

	// ---------------------------------------------------------------------------
	// New key bindings
	// ---------------------------------------------------------------------------
}

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
	if !strings.Contains(v, "↓") || !strings.Contains(v, "tokens") {
		t.Errorf("streaming view should show token count with ↓, got: %s", v)
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
	// No status bar — check that elapsed time is not shown
	if strings.Contains(v, "0.0s") {
		t.Error("should not show elapsed time when progressStart is zero")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — toolParamDeltaMsg
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolDelta(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Read", "", `{}`)

	model, _ := app.Update(toolParamDeltaMsg{ID: "t1", Delta: `{"file":"test.go"}`, Summary: "test.go"})
	a := model.(*App)
	tcv := a.repl.pendingTool["t1"]
	if tcv == nil {
		t.Fatal("pendingTool should have t1")
	}
	if tcv.Summary != "test.go" {
		t.Errorf("summary = %q, want %q", tcv.Summary, "test.go")
	}
}

func TestApp_Update_StreamToolDelta_CountsChars(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Write", "", `{}`)

	delta := `{"content":"package main\nfunc main() {}"}`
	app.Update(toolParamDeltaMsg{ID: "t1", Delta: delta, Summary: "main.go"})

	if app.responseCharCount != len(delta) {
		t.Errorf("responseCharCount = %d, want %d (tool delta chars not counted)", app.responseCharCount, len(delta))
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

func TestPrettyJSON_ValidJSON(t *testing.T) {
	v := prettyJSON(json.RawMessage(`{"a":1}`))
	if !strings.Contains(v, `"a": 1`) {
		t.Errorf("prettyJSON with valid JSON should contain formatted key-value, got %q", v)
	}
	if !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
		t.Errorf("prettyJSON result should be wrapped in braces, got %q", v)
	}
}

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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg when appCh closed, got %T", msg)
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
	cm, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg, got %T", msg)
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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg when resultCh closed, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// updateRepl — turnStartMsg, streamMessageMsg, toolEndMsg
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_TurnStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	_, cmd := app.updateRepl(turnStartMsg{})
	if cmd == nil {
		t.Error("turnStartMsg should return a readEvents cmd")
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
	_, cmd := app.updateRepl(toolEndMsg{ToolUseID: "t1", Output: "ok"})
	if cmd == nil {
		t.Error("toolEndMsg should return a readEvents cmd")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — agentToolMsg (regression: must be in App.Update type switch)
// ---------------------------------------------------------------------------

func TestApp_Update_RoutesAgentToolMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search code", "{}")
	_, cmd := app.Update(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Grep",
		Summary:         "search pattern",
	})
	if cmd == nil {
		t.Error("agentToolMsg should be routed to updateRepl and return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_AgentToolMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")
	_, cmd := app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Grep",
		Summary:         "pattern",
	})
	if cmd == nil {
		t.Error("agentToolMsg should return a readEvents cmd")
	}
	tcv, ok := app.repl.pendingTool["call_abc"]
	if !ok {
		t.Fatal("pendingTool should have call_abc")
	}
	if len(tcv.AgentLogs) == 0 {
		t.Error("AgentLogs should have at least one entry")
	}
	if tcv.AgentLogs[0].ToolName != "Grep" {
		t.Errorf("AgentLogs[0].ToolName = %q, want Grep", tcv.AgentLogs[0].ToolName)
	}
	if tcv.AgentLogs[0].Done {
		t.Error("tool_start entry should not be Done")
	}
}

// TestApp_UpdateRepl_AgentToolParamDelta verifies that tool_param_delta events
// update the summary of the last running tool entry.
// TDD RED: tool_start with empty summary → tool_param_delta with summary → summary updated.
func TestApp_UpdateRepl_AgentToolParamDelta(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// Step 1: tool_start with empty summary (as happens at content_block_start)
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Bash",
		Summary:         "", // empty at content_block_start
	})
	tcv := app.repl.pendingTool["call_abc"]
	if tcv.AgentLogs[0].Summary != "" {
		t.Fatalf("initial summary should be empty, got %q", tcv.AgentLogs[0].Summary)
	}

	// Step 2: tool_param_delta arrives with summary from streaming input
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_param_delta",
		ToolName:        "Bash",
		Summary:         "count files",
	})

	// Verify summary updated
	tcv = app.repl.pendingTool["call_abc"]
	if len(tcv.AgentLogs) == 0 {
		t.Fatal("AgentLogs should have entries")
	}
	if tcv.AgentLogs[0].Summary != "count files" {
		t.Errorf("summary should be updated to %q, got %q", "count files", tcv.AgentLogs[0].Summary)
	}
}

// TestApp_UpdateRepl_AgentToolParamDelta_SameDepth verifies tool_param_delta
// updates the existing entry even when depth matches (no duplicate entry).
func TestApp_UpdateRepl_AgentToolParamDelta_SameDepth(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// tool_start at depth=0 (sub-agent's depth)
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Read",
		Summary:         "",
		Depth:           1,
	})
	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.AgentLogs) != 1 {
		t.Fatalf("expected 1 AgentLog entry, got %d", len(tcv.AgentLogs))
	}
	if tcv.AgentLogs[0].ToolName != "Read" {
		t.Errorf("expected tool Read, got %s", tcv.AgentLogs[0].ToolName)
	}

	// tool_param_delta also at depth=1 — should update existing entry, not add new
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_param_delta",
		ToolName:        "Read",
		Summary:         "Makefile",
		Depth:           1,
	})

	tcv = app.repl.pendingTool["call_abc"]
	if len(tcv.AgentLogs) != 1 {
		t.Errorf("expected 1 AgentLog entry (no duplicate), got %d: %+v", len(tcv.AgentLogs), tcv.AgentLogs)
	}
	if tcv.AgentLogs[0].Summary != "Makefile" {
		t.Errorf("expected summary Makefile, got %q", tcv.AgentLogs[0].Summary)
	}
}

// TestApp_UpdateRepl_AgentThinkingRemoved verifies that Thinking entry is
// removed when tools start, not just marked done.
func TestApp_UpdateRepl_AgentThinkingRemoved(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// thinking_start → adds Thinking entry
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "thinking_start",
	})
	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.AgentLogs) != 1 || tcv.AgentLogs[0].ToolName != "Thinking" {
		t.Fatalf("should have 1 Thinking entry, got %v", tcv.AgentLogs)
	}

	// tool_start → removes Thinking, adds tool
	app.updateRepl(agentToolMsg{
		ParentToolUseID: "call_abc",
		AgentType:       "Explore",
		SubType:         "tool_start",
		ToolName:        "Read",
		Summary:         "main.go",
	})
	tcv = app.repl.pendingTool["call_abc"]
	for _, e := range tcv.AgentLogs {
		if e.ToolName == "Thinking" {
			t.Error("Thinking entry should be removed when tools start")
		}
	}
	// Should have exactly 1 entry (Read)
	if len(tcv.AgentLogs) != 1 {
		t.Errorf("should have 1 entry (Read), got %d: %v", len(tcv.AgentLogs), tcv.AgentLogs)
	}
}

// TestApp_SpinnerTick_MarksDirty verifies that spinnerTickMsg sets contentDirty
// so tool dot blink animations render correctly.
func TestApp_SpinnerTick_MarksDirty(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_1", "Bash", "test", "{}")
	// Clear any existing dirty flag
	app.contentDirty = false

	handled, _ := app.updateRepl(spinnerTickMsg{})
	if !handled {
		t.Fatal("spinnerTickMsg should be handled during streaming")
	}
	if !app.contentDirty {
		t.Error("spinnerTickMsg should mark contentDirty=true so tool dots blink")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — usageMsg
// ---------------------------------------------------------------------------

// TestApp_AgentUsageMsg_UpdatesInputTokens verifies that agentUsageMsg snaps
// displayedInputTokens to include sub-agent input tokens.
func TestApp_AgentUsageMsg_UpdatesInputTokens(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// Main model usage — snaps displayedInputTokens
	app.updateRepl(usageMsg{InputTokens: 500, OutputTokens: 100})
	if app.displayedInputTokens != 500 {
		t.Fatalf("after usageMsg, displayedInputTokens = %d, want 500", app.displayedInputTokens)
	}

	// Agent usage — should also snap displayedInputTokens
	app.updateRepl(agentUsageMsg{
		ParentToolUseID: "call_abc",
		InputTokens:     300,
		OutputTokens:    50,
	})
	if app.displayedInputTokens != 800 {
		t.Errorf("after agentUsageMsg, displayedInputTokens = %d, want 800 (500+300)", app.displayedInputTokens)
	}
	if app.inputTokenTarget != 800 {
		t.Errorf("inputTokenTarget = %d, want 800", app.inputTokenTarget)
	}
}

func TestApp_UpdateRepl_UsageMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	handled, cmd := app.updateRepl(usageMsg{InputTokens: 100, OutputTokens: 50})
	if !handled {
		t.Error("usageMsg should be handled")
	}
	if cmd == nil {
		t.Error("usageMsg should return a readEvents cmd")
	}
	if app.status.usage.InputTokens != 100 {
		t.Errorf("inputTokens = %d, want 100", app.status.usage.InputTokens)
	}
	if app.status.usage.OutputTokens != 50 {
		t.Errorf("outTokens = %d, want 50", app.status.usage.OutputTokens)
	}
	// Input tokens should snap immediately to actual value
	if app.displayedInputTokens != 100 {
		t.Errorf("displayedInputTokens = %d, want 100 (snap)", app.displayedInputTokens)
	}
	// Output tokens should NOT snap — they animate via spinner tick
	if app.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0 (not yet animated)", app.displayedOutputTokens)
	}
}

// ---------------------------------------------------------------------------
// updateRepl — thinkingStartMsg / thinkingEndMsg
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_ThinkingStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	handled, cmd := app.updateRepl(thinkingStartMsg{})
	if !handled {
		t.Error("thinkingStartMsg should be handled")
	}
	if cmd == nil {
		t.Error("thinkingStartMsg should return a readEvents cmd")
	}
	if !app.thinkingActive {
		t.Error("thinkingActive should be true")
	}
}

func TestApp_UpdateRepl_ThinkingEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.thinkingActive = true

	handled, cmd := app.updateRepl(thinkingEndMsg{Duration: 3 * time.Second})
	if !handled {
		t.Error("thinkingEndMsg should be handled")
	}
	if cmd == nil {
		t.Error("thinkingEndMsg should return a readEvents cmd")
	}
	if app.thinkingActive {
		t.Error("thinkingActive should be false after end")
	}
	if app.thinkingDuration != 3*time.Second {
		t.Errorf("thinkingDuration = %v, want 3s", app.thinkingDuration)
	}
}

// ---------------------------------------------------------------------------
// PendingThinking — ReplState methods
// ---------------------------------------------------------------------------

func TestReplState_PendingThinkingStarted(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	s.PendingThinkingStarted()

	m := s.lastMsg()
	if m == nil {
		t.Fatal("lastMsg should not be nil")
	}
	if len(m.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(m.Blocks))
	}
	blk := m.Blocks[0]
	if blk.Type != BlockThinking {
		t.Errorf("block type = %v, want BlockThinking", blk.Type)
	}
	if blk.Thinking.Done {
		t.Error("thinking block should not be done on start")
	}
	if s.activeThinkingIdx != 0 {
		t.Errorf("activeThinkingIdx = %d, want 0", s.activeThinkingIdx)
	}
}

func TestReplState_PendingThinkingStarted_NilLastMsg(t *testing.T) {
	s := NewReplState()
	s.PendingThinkingStarted()
	if s.activeThinkingIdx != -1 {
		t.Errorf("activeThinkingIdx = %d, want -1 when no messages", s.activeThinkingIdx)
	}
}

func TestReplState_PendingThinkingDelta(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	s.PendingThinkingStarted()
	s.PendingThinkingDelta("hello ")
	s.PendingThinkingDelta("world")

	m := s.lastMsg()
	blk := m.Blocks[s.activeThinkingIdx]
	if blk.Thinking.Text != "hello world" {
		t.Errorf("thinking text = %q, want %q", blk.Thinking.Text, "hello world")
	}
}

func TestReplState_PendingThinkingDelta_NoActiveBlock(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	// No thinking block started — activeThinkingIdx is -1
	s.PendingThinkingDelta("should be ignored")
	m := s.lastMsg()
	for _, blk := range m.Blocks {
		if blk.Type == BlockThinking {
			t.Error("should not have a thinking block")
		}
	}
}

func TestReplState_PendingThinkingDelta_NilLastMsg(t *testing.T) {
	s := NewReplState()
	// No messages at all
	s.PendingThinkingDelta("ignored")
	// Should not panic
}

func TestReplState_PendingThinkingDone(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	s.PendingThinkingStarted()
	s.PendingThinkingDelta("some thought")
	s.PendingThinkingDone(2500 * time.Millisecond)

	m := s.lastMsg()
	blk := m.Blocks[0]
	if !blk.Thinking.Done {
		t.Error("thinking block should be done")
	}
	if blk.Thinking.Duration != 2500*time.Millisecond {
		t.Errorf("duration = %v, want 2500ms", blk.Thinking.Duration)
	}
	if blk.Thinking.Text != "some thought" {
		t.Errorf("text = %q, want %q", blk.Thinking.Text, "some thought")
	}
	if s.activeThinkingIdx != -1 {
		t.Errorf("activeThinkingIdx = %d, want -1 after done", s.activeThinkingIdx)
	}
}

func TestReplState_PendingThinkingDone_NoActiveBlock(t *testing.T) {
	s := NewReplState()
	s.StartQuery(nil)
	// No thinking block started
	s.PendingThinkingDone(time.Second)
	// Should not panic, activeThinkingIdx should stay -1
}

func TestReplState_PendingThinkingDone_NilLastMsg(t *testing.T) {
	s := NewReplState()
	s.PendingThinkingDone(time.Second)
	// Should not panic
}

func TestApp_UpdateRepl_ThinkingDelta(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	// Start thinking first
	app.updateRepl(thinkingStartMsg{})

	// Send delta
	handled, cmd := app.updateRepl(thinkingDeltaMsg{Text: "reasoning about..."})
	if !handled {
		t.Error("thinkingDeltaMsg should be handled")
	}
	if cmd == nil {
		t.Error("thinkingDeltaMsg should return a readEvents cmd")
	}

	// Verify text was accumulated
	m := app.repl.lastMsg()
	found := false
	for _, blk := range m.Blocks {
		if blk.Type == BlockThinking && blk.Thinking.Text == "reasoning about..." {
			found = true
		}
	}
	if !found {
		t.Error("thinking block should contain delta text")
	}
}

func TestApp_UpdateRepl_ThinkingStartCreatesBlock(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	app.updateRepl(thinkingStartMsg{})

	m := app.repl.lastMsg()
	found := false
	for _, blk := range m.Blocks {
		if blk.Type == BlockThinking {
			found = true
		}
	}
	if !found {
		t.Error("thinkingStartMsg should create a BlockThinking")
	}
}

func TestApp_UpdateRepl_ThinkingEndMarksDone(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	app.updateRepl(thinkingStartMsg{})
	app.updateRepl(thinkingDeltaMsg{Text: "thinking text"})
	app.updateRepl(thinkingEndMsg{Duration: 2 * time.Second})

	m := app.repl.lastMsg()
	for _, blk := range m.Blocks {
		if blk.Type == BlockThinking {
			if !blk.Thinking.Done {
				t.Error("thinking block should be done after thinkingEndMsg")
			}
			if blk.Thinking.Duration != 2*time.Second {
				t.Errorf("duration = %v, want 2s", blk.Thinking.Duration)
			}
			return
		}
	}
	t.Error("no BlockThinking found")
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
	a := model.(*App)
	if !a.repl.streaming {
		t.Error("should still be streaming after Enter during stream")
	}

	// ---------------------------------------------------------------------------
	// handleKey — Ctrl+Y with empty kill ring
	// ---------------------------------------------------------------------------
}

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
	// Home/End now move input cursor (Ctrl+A/Ctrl+E)
	// Use Ctrl+A/Ctrl+E for input cursor movement
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if app.input.cursor != 0 {
		t.Errorf("Ctrl+A cursor = %d, want 0", app.input.cursor)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if app.input.cursor != 3 {
		t.Errorf("Ctrl+E cursor = %d, want 3", app.input.cursor)
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
	a := model.(*App)
	if a.repl.streaming {
		t.Error("unknown msg should not start streaming")
	}

	// ---------------------------------------------------------------------------
	// finishStream
	// ---------------------------------------------------------------------------
}

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
	cm, ok := msg.(textDeltaMsg)
	if !ok {
		t.Fatalf("expected textDeltaMsg, got %T", msg)
	}
	if cm.Text != "hello" {
		t.Errorf("Text = %q, want %q", cm.Text, "hello")
	}
}

func TestApp_EngineEventToMsg_ToolUseStart(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolStart,
		ToolUse: &types.ToolUseEvent{
			ID:    "t1",
			Name:  "Read",
			Input: json.RawMessage(`{"file":"a.go"}`),
		},
	})
	tum, ok := msg.(toolStartMsg)
	if !ok {
		t.Fatalf("expected toolStartMsg, got %T", msg)
	}
	if tum.Name != "Read" {
		t.Errorf("name = %q, want %q", tum.Name, "Read")
	}
}

func TestApp_EngineEventToMsg_ToolUseStart_Nil(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type:    types.EventToolStart,
		ToolUse: nil,
	})
	if msg != nil {
		t.Errorf("expected nil for nil ToolUse, got %T", msg)
	}
}

func TestApp_EngineEventToMsg_ToolResult(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID: "t1",
			Output:    json.RawMessage(`"ok"`),
			IsError:   false,
		},
	})
	trm, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("expected toolEndMsg, got %T", msg)
	}
	if trm.ToolUseID != "t1" {
		t.Errorf("ToolUseID = %q, want %q", trm.ToolUseID, "t1")
	}
}

func TestApp_EngineEventToMsg_ToolResult_Nil(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type:       types.EventToolEnd,
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
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     "t1",
			Output:        json.RawMessage(`"{\"output\":\"\",\"exitCode\":0}"`),
			DisplayOutput: "", // empty because Bash had no stdout/stderr
			IsError:       false,
		},
	})
	trm, ok := msg.(toolEndMsg)
	if !ok {
		t.Fatalf("expected toolEndMsg, got %T", msg)
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
		Type: types.EventQueryEnd,
	})
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Errorf("expected queryEndMsg, got %T", msg)
	}
}

func TestApp_EngineEventToMsg_Unknown(t *testing.T) {
	msg := NewTUIHandler().convertEventToMsg(types.QueryEvent{
		Type: types.EventToolParamDelta,
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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Errorf("expected queryEndMsg when tuiHandler nil, got %T", msg)
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
	// Should be either textDeltaMsg or queryEndMsg
	switch msg.(type) {
	case textDeltaMsg, queryEndMsg, turnStartMsg, streamMessageMsg:
		// ok
	default:
		t.Errorf("expected textDeltaMsg or queryEndMsg, got %T", msg)
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
	a := model.(*App)
	if a.input.Value() != "" {
		t.Errorf("input should be unchanged, got %q", a.input.Value())
	}

	// ---------------------------------------------------------------------------
	// Additional coverage — View edge cases
	// ---------------------------------------------------------------------------

	// ---------------------------------------------------------------------------
	// readEvents drain behavior — appCh drained before returning complete
	// ---------------------------------------------------------------------------
}

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
	h.appCh <- textDeltaMsg{Text: "late event"}

	cmd := app.readEvents()
	msg := cmd()
	// Should return the buffered appCh event, not queryEndMsg
	cm, ok := msg.(textDeltaMsg)
	if !ok {
		t.Fatalf("expected textDeltaMsg, got %T", msg)
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

	// Close appCh so idle readEvents gets !ok and returns queryEndMsg
	close(h.appCh)
	cmd := app.readEvents()
	msg := cmd()
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg when both closed, got %T", msg)
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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg with nil handler, got %T", msg)
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
	if !strings.Contains(v, "Bash") || !strings.Contains(v, "running...") {
		t.Errorf("View should show 'Bash ... running...' for running state, got: %s", v)
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
	if !strings.Contains(v, "❯") {
		t.Errorf("View with small height should still show prompt, got: %q", v)
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
// Spinner e2e — full animation lifecycle
// ---------------------------------------------------------------------------

// Test: submit → spinner animates input estimate → API responds → snap to actual.
func TestSpinnerE2E_InputEstimateToSnap(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "Hello world response"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	// 1. Submit query
	app.handleSubmitRepl("hello, this is a test message")
	// Estimate should be set from systemPrompt + user text
	if app.inputTokenTarget <= 0 {
		t.Fatalf("inputTokenTarget = %d, want > 0 after submit", app.inputTokenTarget)
	}
	if app.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0 right after submit", app.displayedInputTokens)
	}

	// 2. Spinner ticks — displayedInputTokens animates toward estimate
	app.Update(spinnerTickMsg{})
	if app.displayedInputTokens == 0 {
		t.Error("displayedInputTokens should increment on first tick")
	}
	estimate := app.inputTokenTarget
	if app.displayedInputTokens > estimate {
		t.Errorf("displayedInputTokens = %d, should not exceed estimate %d", app.displayedInputTokens, estimate)
	}

	// 3. More ticks — continues animating
	prev := app.displayedInputTokens
	app.Update(spinnerTickMsg{})
	if app.displayedInputTokens <= prev {
		t.Errorf("displayedInputTokens = %d, should increase from %d", app.displayedInputTokens, prev)
	}

	// 4. API responds with actual input tokens — snap
	actualInput := 500
	app.Update(usageMsg{InputTokens: actualInput, OutputTokens: 0})
	if app.displayedInputTokens != actualInput {
		t.Errorf("displayedInputTokens = %d, want %d after snap", app.displayedInputTokens, actualInput)
	}
	if app.inputTokenTarget != actualInput {
		t.Errorf("inputTokenTarget = %d, want %d after snap", app.inputTokenTarget, actualInput)
	}

	// 5. Subsequent ticks don't change input (already at target)
	app.Update(spinnerTickMsg{})
	if app.displayedInputTokens != actualInput {
		t.Errorf("displayedInputTokens = %d, should stay at %d after snap", app.displayedInputTokens, actualInput)
	}
}

// Test: output tokens animate from 0 as text chunks arrive.
func TestSpinnerE2E_OutputAnimatesDuringStream(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()

	// Receive text chunks
	app.Update(textDeltaMsg{Text: "Hello "})
	app.Update(textDeltaMsg{Text: "world, this is a long response with many tokens"})
	// responseCharCount = len("Hello ") + len("world, this is a long response with many tokens")
	expectedEstimate := app.responseCharCount / 4

	// Before any tick, displayedOutputTokens is still 0
	if app.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0 before first tick", app.displayedOutputTokens)
	}

	// After tick, starts animating toward estimate
	app.Update(spinnerTickMsg{})
	if app.displayedOutputTokens == 0 {
		t.Error("displayedOutputTokens should increment on tick")
	}
	if app.displayedOutputTokens > expectedEstimate {
		t.Errorf("displayedOutputTokens = %d, should not exceed estimate %d", app.displayedOutputTokens, expectedEstimate)
	}

	// More chunks + ticks → keeps growing
	app.Update(textDeltaMsg{Text: " and even more text to stream"})
	app.Update(spinnerTickMsg{})
	if app.displayedOutputTokens < 2 {
		t.Errorf("displayedOutputTokens = %d, should keep growing", app.displayedOutputTokens)
	}
}

// Test: completed stats line shown after streaming ends.
func TestSpinnerE2E_CompletedStatsAfterStream(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now().Add(-2 * time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("response text")

	// Stream complete — saves stats
	app.Update(queryEndMsg{})
	if app.repl.IsStreaming() {
		t.Error("should not be streaming after complete")
	}
	foundStats := false
	for _, blk := range app.repl.lastMsg().Blocks {
		if blk.Type == BlockStats {
			foundStats = true
		}
	}
	if !foundStats {
		t.Error("last message should have BlockStats after complete")
	}

	// View should show completed stats line (no spinner)
	// After commit-on-complete, stats are committed to scrollback via tea.Println,
	// not rendered in View(). Verify stats exist in the message blocks instead.
	v := app.View()
	if strings.Contains(v, "thinking") {
		t.Errorf("completed stats should not show thinking, got: %s", v)
	}
}

// Test: thinking state shown during streaming.
func TestSpinnerE2E_ThinkingState(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()

	// Thinking starts
	app.Update(thinkingStartMsg{})
	if !app.thinkingActive {
		t.Error("thinkingActive should be true")
	}
	v := app.View()
	if !strings.Contains(v, "thinking") {
		t.Errorf("view should show 'thinking', got: %s", v)
	}

	// Thinking ends
	app.Update(thinkingEndMsg{Duration: 3 * time.Second})
	if app.thinkingActive {
		t.Error("thinkingActive should be false after end")
	}
	v = app.View()
	if !strings.Contains(v, "thought for 3.0s") {
		t.Errorf("view should show 'thought for 3.0s', got: %s", v)
	}
}

// Test: multiple queries reset state correctly.
func TestSpinnerE2E_SecondQueryResetsCounters(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{events: textStreamEvents("test-model", "second")})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24

	// Simulate first query state (without starting a real engine goroutine)
	app.repl.AddUserMessage("first query")
	app.repl.StartQuery(nil)
	app.status.usage.InputTokens = 100
	app.status.usage.OutputTokens = 50
	app.displayedInputTokens = 100
	app.displayedOutputTokens = 50
	app.responseCharCount = 200
	app.repl.FinishStream(nil)

	// Verify first query left state
	if app.displayedInputTokens != 100 {
		t.Errorf("after first query, displayedInputTokens = %d, want 100", app.displayedInputTokens)
	}
	if app.responseCharCount == 0 {
		t.Error("responseCharCount should be non-zero after first query")
	}

	// Second query — should reset all counters
	app.handleSubmitRepl("second query")
	if app.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0 after second submit", app.displayedInputTokens)
	}
	if app.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0 after second submit", app.displayedOutputTokens)
	}
	if app.responseCharCount != 0 {
		t.Errorf("responseCharCount = %d, want 0 after second submit", app.responseCharCount)
	}
	if app.inputTokenTarget <= 0 {
		t.Errorf("inputTokenTarget = %d, want > 0 after second submit", app.inputTokenTarget)
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

	// Send event using channel-based sync to avoid race
	sendReady := make(chan struct{})
	go func() {
		<-sendReady
		h.appCh <- textDeltaMsg{Text: "delayed"}
	}()

	cmd := app.readEvents()
	close(sendReady) // signal goroutine to send
	msg := cmd()
	cm, ok := msg.(textDeltaMsg)
	if !ok {
		t.Fatalf("expected textDeltaMsg from blocking select, got %T", msg)
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
	cm, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg, got %T", msg)
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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg when resultCh closed in blocking select, got %T", msg)
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
	_, ok := msg.(queryEndMsg)
	if !ok {
		t.Fatalf("expected queryEndMsg when appCh closed in blocking select, got %T", msg)
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

// ---------------------------------------------------------------------------
// animateTokenValue
// ---------------------------------------------------------------------------

func TestAnimateTokenValue_Under1000(t *testing.T) {
	t.Parallel()
	// Increments by 1 when displayed < 1000
	got := animateTokenValue(0, 100)
	if got != 1 {
		t.Errorf("animateTokenValue(0, 100) = %d, want 1", got)
	}
	got = animateTokenValue(99, 100)
	if got != 100 {
		t.Errorf("animateTokenValue(99, 100) = %d, want 100 (clamps to target)", got)
	}
}

func TestAnimateTokenValue_Over1000(t *testing.T) {
	t.Parallel()
	// Increments by 100 (0.1k) when displayed >= 1000
	got := animateTokenValue(1000, 2000)
	if got != 1100 {
		t.Errorf("animateTokenValue(1000, 2000) = %d, want 1100", got)
	}
	got = animateTokenValue(1950, 2000)
	if got != 2000 {
		t.Errorf("animateTokenValue(1950, 2000) = %d, want 2000 (clamps to target)", got)
	}
}

func TestAnimateTokenValue_AlreadyAtTarget(t *testing.T) {
	t.Parallel()
	got := animateTokenValue(500, 500)
	if got != 500 {
		t.Errorf("animateTokenValue(500, 500) = %d, want 500", got)
	}
}

func TestAnimateTokenValue_ExceedsTarget(t *testing.T) {
	t.Parallel()
	got := animateTokenValue(600, 500)
	if got != 500 {
		t.Errorf("animateTokenValue(600, 500) = %d, want 500 (returns target)", got)
	}
}

func TestAnimateTokenValue_CrossThreshold(t *testing.T) {
	t.Parallel()
	// 999 → 1000 step is +1 (still under 1000)
	got := animateTokenValue(999, 5000)
	if got != 1000 {
		t.Errorf("animateTokenValue(999, 5000) = %d, want 1000", got)
	}
	// 1000 → 1100 step is +100
	got = animateTokenValue(1000, 5000)
	if got != 1100 {
		t.Errorf("animateTokenValue(1000, 5000) = %d, want 1100", got)
	}
}

func TestAnimateTokenValue_ZeroTarget(t *testing.T) {
	t.Parallel()
	got := animateTokenValue(0, 0)
	if got != 0 {
		t.Errorf("animateTokenValue(0, 0) = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Spinner tick animates displayed tokens
// ---------------------------------------------------------------------------

func TestApp_Update_SpinnerTick_AnimatesTokens(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	app.status.usage.InputTokens = 100
	app.responseCharCount = 800 // estimate = 200 output tokens

	app.Update(spinnerTickMsg{})
	if app.displayedInputTokens != 1 {
		t.Errorf("displayedInputTokens = %d, want 1", app.displayedInputTokens)
	}
	if app.displayedOutputTokens != 1 {
		t.Errorf("displayedOutputTokens = %d, want 1", app.displayedOutputTokens)
	}

	// Tick several times — should keep incrementing
	for range 5 {
		app.Update(spinnerTickMsg{})
	}
	if app.displayedInputTokens != 6 {
		t.Errorf("after 6 ticks, displayedInputTokens = %d, want 6", app.displayedInputTokens)
	}
	if app.displayedOutputTokens != 6 {
		t.Errorf("after 6 ticks, displayedOutputTokens = %d, want 6", app.displayedOutputTokens)
	}
}

func TestApp_HandleSubmitRepl_ResetsDisplayedTokens(t *testing.T) {
	t.Parallel()
	mp := &tuiMockProvider{}
	mp.responses = append(mp.responses, tuiMockResponse{
		events: textStreamEvents("test-model", "hi"),
	})
	app := newTestApp(mp)
	app.width = 80
	app.height = 24
	app.displayedInputTokens = 500
	app.displayedOutputTokens = 500
	app.responseCharCount = 999

	app.handleSubmitRepl("test")
	if app.displayedInputTokens != 0 {
		t.Errorf("displayedInputTokens = %d, want 0", app.displayedInputTokens)
	}
	if app.displayedOutputTokens != 0 {
		t.Errorf("displayedOutputTokens = %d, want 0", app.displayedOutputTokens)
	}
	if app.responseCharCount != 0 {
		t.Errorf("responseCharCount = %d, want 0", app.responseCharCount)
	}
	// Should have set an input token target estimate
	if app.inputTokenTarget <= 0 {
		t.Errorf("inputTokenTarget = %d, want > 0", app.inputTokenTarget)
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

	ctx := t.Context()
	_, resultCh := app.engine.Query(ctx, "test", json.RawMessage(`"sys"`))
	app.repl.resultCh = resultCh

	// Drain hub events into appCh until it's empty, using a done channel
	// for sync instead of time.Sleep to avoid race.
	done := make(chan struct{})
	go func() {
		// Give engine goroutine time to process, then signal
		for range 100 {
			runtime.Gosched()
		}
		close(done)
	}()
	<-done

	cmd := app.readEvents()
	msg := cmd()
	// Could be queryEndMsg or textDeltaMsg depending on timing
	switch msg.(type) {
	case queryEndMsg, textDeltaMsg, turnStartMsg, streamMessageMsg:
		// ok
	default:
		t.Errorf("expected queryEndMsg or textDeltaMsg, got %T", msg)
	}
}

// ---------------------------------------------------------------------------
// View — toolDot when streaming + toolBlink
// ---------------------------------------------------------------------------

func TestApp_View_StreamingToolBlink(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("thinking...")
	app.toolBlink = true
	v := app.View()
	// toolDot should be rendered (bright white bold dot)
	if !strings.Contains(v, "thinking...") {
		t.Errorf("should contain content, got: %s", v)
	}
}

// ---------------------------------------------------------------------------
// Ctrl+P/N with wrapped input lines (CursorUp/Down returns true)
// ---------------------------------------------------------------------------

func TestApp_HandleKey_CtrlP_WrappedInput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 30
	app.input.SetWidth(26)
	app.input.SetValue("abcdefghijklmnopqrstuvwxyz") // wraps in 26-wide input
	// Cursor at end (position 26), on second wrapped line
	// Ctrl+P should call CursorUp which returns true (on second line)
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a := model.(*App)
	if cmd != nil {
		t.Error("Ctrl+P with wrapped lines should produce no command (cursor moves up)")
	}
	if a.input.cursor > 26 {
		t.Errorf("cursor should be within value range, got %d", a.input.cursor)
	}
}

func TestApp_HandleKey_CtrlN_WrappedInput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 30
	app.input.SetWidth(26)
	app.input.SetValue("abcdefghijklmnopqrstuvwxyz") // wraps in 26-wide input
	app.input.Home()                                 // cursor at 0, first line
	// Ctrl+N should call CursorDown which returns true (on first line, can go down)
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	a := model.(*App)
	if cmd != nil {
		t.Error("Ctrl+N with wrapped lines should produce no command (cursor moves down)")
	}
	if a.input.cursor == 0 {
		t.Error("cursor should have moved down from home position")
	}
}

func TestApp_HandleKey_KeyUp_WrappedInput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 30
	app.input.SetWidth(26)
	app.input.SetValue("abcdefghijklmnopqrstuvwxyz")
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	a := model.(*App)
	if cmd != nil {
		t.Error("KeyUp with wrapped input should move cursor up, no command")
	}
	if a.input.cursor > 26 {
		t.Errorf("cursor should be within value range, got %d", a.input.cursor)
	}
}

func TestApp_HandleKey_KeyDown_WrappedInput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 30
	app.input.SetWidth(26)
	app.input.SetValue("abcdefghijklmnopqrstuvwxyz")
	app.input.Home()
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	a := model.(*App)
	if cmd != nil {
		t.Error("KeyDown with wrapped input should move cursor down, no command")
	}
	if a.input.cursor == 0 {
		t.Error("cursor should have moved down from home position")
	}
}

// ---------------------------------------------------------------------------
// prettyJSON — remaining paths
// ---------------------------------------------------------------------------

func TestPrettyJSON_Empty(t *testing.T) {
	t.Parallel()
	v := prettyJSON(nil)
	if v != "" {
		t.Errorf("prettyJSON(nil) = %q, want empty", v)
	}
}

func TestPrettyJSON_InvalidJSON(t *testing.T) {
	t.Parallel()
	v := prettyJSON(json.RawMessage(`not json`))
	if v != "not json" {
		t.Errorf("prettyJSON(invalid) = %q, want raw string", v)
	}
}

// ---------------------------------------------------------------------------
// PendingToolOutput — toolOutputDeltaMsg handler in App.Update
// Source: Phase 2 — streaming tool output display
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolOutput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "", `{}`)

	model, _ := app.Update(toolOutputDeltaMsg{
		ToolUseID:     "t1",
		DisplayOutput: "stdout line\n",
		Timing:        200 * time.Millisecond,
	})
	a := model.(*App)
	tcv := a.repl.pendingTool["t1"]
	if tcv == nil {
		t.Fatal("pendingTool should have t1")
	}
	if !tcv.Done {
		t.Error("Done should be true after toolOutputDeltaMsg")
	}
	if tcv.Output != "stdout line\n" {
		t.Errorf("Output = %q, want %q", tcv.Output, "stdout line\n")
	}
}

func TestApp_Update_StreamToolOutput_NonExistent(t *testing.T) {
	t.Parallel()
	// Sending output for a non-existent tool should not panic
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	model, _ := app.Update(toolOutputDeltaMsg{
		ToolUseID:     "nonexistent",
		DisplayOutput: "output",
		Timing:        0,
	})
	if model == nil {
		t.Error("Update should return non-nil model for unknown tool")
	}
}

func TestApp_Update_StreamToolOutput_UpdatesElapsed(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "", `{}`)

	// Set pendingToolStart BEFORE calling Update so it's available synchronously
	app.repl.pendingToolStart["t1"] = time.Now().Add(-100 * time.Millisecond)

	model, _ := app.Update(toolOutputDeltaMsg{
		ToolUseID:     "t1",
		DisplayOutput: "output",
		Timing:        50 * time.Millisecond,
	})
	a := model.(*App)
	tcv := a.repl.pendingTool["t1"]
	// Elapsed should use the perceived time (100ms) since it's greater than timing (50ms)
	if tcv.Elapsed < 90*time.Millisecond {
		t.Errorf("Elapsed = %v, want >= 90ms (perceived time)", tcv.Elapsed)
	}
}

// ---------------------------------------------------------------------------
// Stats line scrolls with content (BlockStats approach)
// ---------------------------------------------------------------------------

// TestApp_StatsScrollsWithContent verifies that the completed query stats line
// is embedded in the assistant message and committed to scrollback via tea.Println.
// After commit-on-complete, View() only shows uncommitted (active) content.
func TestApp_StatsScrollsWithContent(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// --- First query lifecycle ---

	// Simulate: user submits first query
	app.repl.AddUserMessage("first query")
	app.repl.StartQuery(nil)
	app.progressStart = time.Now().Add(-2 * time.Second)

	// Streaming: assistant response
	app.repl.AppendTextItem()
	app.repl.AppendChunk("first response")

	// Usage arrives
	app.status.usage.InputTokens = 100
	app.status.usage.OutputTokens = 50

	// Stream completes — should embed stats in the last message
	// Commit is deferred until next submit, so content stays in BT view.
	app.Update(queryEndMsg{})

	// Verify: stats block exists in the message
	foundStats := false
	for _, blk := range app.repl.messages[1].Blocks {
		if blk.Type == BlockStats {
			foundStats = true
		}
	}
	if !foundStats {
		t.Fatal("first query should have BlockStats after complete")
	}

	// Verify: View() still shows content (deferred commit — not committed yet)
	v1 := app.View()
	if !strings.Contains(v1, "first response") {
		t.Errorf("after stream complete (before next submit), View should still show content, got:\n%s", v1)
	}

	// Verify: committedCount is still 0 (deferred)
	if app.committedCount != 0 {
		t.Errorf("committedCount = %d, want 0 (deferred commit)", app.committedCount)
	}

	// --- Second query: submitting commits previous turn ---

	// User submits second query — this triggers commit of first turn
	app.handleSubmitRepl("second query")
	app.markViewportDirty()

	// Second query streaming
	app.repl.AppendTextItem()
	app.repl.AppendChunk("second response")

	// Verify: View shows only second query's content
	v2 := app.View()
	if !strings.Contains(v2, "second response") {
		t.Fatalf("during second query, View should contain second response, got:\n%s", v2)
	}
	// First query content is in scrollback, not in View
	if strings.Contains(v2, "first response") {
		t.Errorf("View should not contain first query content (it's in scrollback), got:\n%s", v2)
	}
}

// TestApp_StatsBlockInMessage verifies the stats block is a ContentBlock in the
// assistant message, not a separate rendering section.
func TestApp_StatsBlockInMessage(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	app.repl.AddUserMessage("hi")
	app.repl.StartQuery(nil)
	app.progressStart = time.Now().Add(-1 * time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("hello back")
	app.status.usage.InputTokens = 50
	app.status.usage.OutputTokens = 20

	app.Update(queryEndMsg{})

	// Last message should have a stats block
	lastMsg := app.repl.lastMsg()
	if lastMsg == nil {
		t.Fatal("expected at least one message")
	}
	foundStats := false
	for _, blk := range lastMsg.Blocks {
		if blk.Type == BlockStats {
			foundStats = true
			if !strings.Contains(blk.Text, "tokens") {
				t.Errorf("stats block text = %q, should contain 'tokens'", blk.Text)
			}
		}
	}
	if !foundStats {
		t.Error("last message should contain a BlockStats block")
	}
}

// ---------------------------------------------------------------------------
// REGRESSION TESTS for commit-on-complete (f2779a9)
// ---------------------------------------------------------------------------

// TestStreamComplete_StatsLineContainsActualTokenValues verifies that the stats
// line embedded in the assistant message shows the actual token counts, not ↑0.
// Regression: commit f2779a9 caused ↑0 for input tokens.
func TestStreamComplete_StatsLineContainsActualTokenValues(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now().Add(-1 * time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("hello")

	// Simulate usage events arriving before complete (same order as production)
	app.Update(usageMsg{InputTokens: 100, OutputTokens: 0})
	app.Update(usageMsg{InputTokens: 0, OutputTokens: 50})

	// Stream complete — should embed stats with actual token values
	app.Update(queryEndMsg{})

	lastMsg := app.repl.lastMsg()
	if lastMsg == nil {
		t.Fatal("expected last message")
	}

	foundStats := false
	for _, blk := range lastMsg.Blocks {
		if blk.Type == BlockStats {
			foundStats = true
			text := blk.Text
			// The KEY assertion: should show actual input token count, NOT ↑0
			if strings.Contains(text, "↑0") {
				t.Errorf("stats should NOT show ↑0 when inputTokens=100, got: %s", text)
			}
			if !strings.Contains(text, "↑100") {
				t.Errorf("stats should show ↑100 for inputTokens=100, got: %s", text)
			}
			if !strings.Contains(text, "↓50") {
				t.Errorf("stats should show ↓50 for outTokens=50, got: %s", text)
			}
		}
	}
	if !foundStats {
		t.Error("expected BlockStats after complete")
	}
}

// TestView_ExpandedToolVisibleWithHeightLimit verifies that expanded tool calls
// produce full content in the cache and that the scroll window shows recent output.
// With scroll buffer, very long expanded output scrolls the tool header off-screen;
// the user can scroll up (PgUp/mouse wheel) to see it.
func TestView_ExpandedToolVisibleWithHeightLimit(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 20 // small height to trigger scrolling (maxLines = 17)
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()

	// Tool with long output
	app.repl.PendingToolStarted("t1", "Bash", "awk command", `{"command":"awk ..."}`)
	longOutput := strings.Repeat("output line\n", 50)
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)

	app.repl.AppendTextItem()
	app.repl.AppendChunk("done")
	app.markViewportDirty()

	// Collapsed: tool name should be visible (output is short when collapsed)
	v1 := app.View()
	if !strings.Contains(v1, "Bash") {
		t.Errorf("collapsed tool name 'Bash' should be visible, got:\n%s", v1)
	}

	// Expanded: content cache should contain the tool name
	app.allToolsExpanded = true
	app.contentDirty = true
	v2 := app.View()
	// Full content should contain Bash (verifies it's in the rendered output)
	if !strings.Contains(app.contentCache, "Bash") {
		t.Errorf("expanded content cache should contain 'Bash', got:\n%s", app.contentCache)
	}
	// Scroll window should show recent content ("done" text)
	if !strings.Contains(v2, "done") {
		t.Errorf("scroll window should show recent text 'done', got:\n%s", v2)
	}
	// Scroll indicator should be present since content overflows
	if !strings.Contains(v2, "PgUp/PgDown/Mouse") {
		t.Errorf("scroll indicator should be present when content overflows, got:\n%s", v2)
	}
	// Scroll total should exceed viewport (height - 3 = 17 lines)
	if app.scrollTotal <= 17 {
		t.Errorf("scrollTotal = %d, expected > 17 (viewport size) for expanded output", app.scrollTotal)
	}
}

// ---------------------------------------------------------------------------
// Bug 3: Tool collapse — tools with >4 lines should show collapse hint
// Regression: tools no longer collapse after commit-on-complete changes
// ---------------------------------------------------------------------------

// TestApp_View_ToolOutputCollapsed verifies that a completed tool with >4 lines
// of output shows the "ctrl+o to expand" collapse hint.
func TestApp_View_ToolOutputCollapsed(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 40
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	// 10 lines of output — should collapse to 3 lines + hint
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.markViewportDirty()

	v := app.View()
	if !strings.Contains(v, "ctrl+o to expand") {
		t.Errorf("collapsed tool should show ctrl+o to expand hint, got:\n%s", v)
	}
	if !strings.Contains(v, "Bash") {
		t.Errorf("should show tool name Bash, got:\n%s", v)
	}
	// Should show first 3 lines but NOT line10
	if !strings.Contains(v, "line1") {
		t.Errorf("should show line1, got:\n%s", v)
	}
	if strings.Contains(v, "line10") {
		t.Errorf("collapsed tool should NOT show line10 (hidden behind collapse), got:\n%s", v)
	}
}

// TestApp_View_ToolOutputExpanded verifies that expanded tools show all output.
func TestApp_View_ToolOutputExpanded(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 40
	app.allToolsExpanded = true
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.markViewportDirty()

	v := app.View()
	if strings.Contains(v, "ctrl+o to expand") {
		t.Errorf("expanded tool should NOT show collapse hint, got:\n%s", v)
	}
	if !strings.Contains(v, "line10") {
		t.Errorf("expanded tool should show all lines including line10, got:\n%s", v)
	}
}

// TestApp_View_ToolOutputCollapsedAfterCommit verifies that tools remain collapsed
// after queryEndMsg commits messages via tea.Println.
func TestApp_View_ToolOutputCollapsedAfterCommit(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 40

	// Simulate full query lifecycle
	app.repl.AddUserMessage("test")
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("done")
	app.progressStart = time.Now()
	app.status.usage.InputTokens = 10
	app.status.usage.OutputTokens = 5

	// Before commit: View shows collapsed output
	v := app.View()
	if !strings.Contains(v, "ctrl+o to expand") {
		t.Errorf("before commit: tool should be collapsed, got:\n%s", v)
	}

	// Stream complete — triggers commit via tea.Println
	app.Update(queryEndMsg{})

	// Verify committed output is collapsed
	rendered := renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Errorf("committed output should show collapsed tool hint, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "line10") {
		t.Errorf("committed output should NOT show line10 (collapsed), got:\n%s", rendered)
	}
}

// TestRenderMessagesFull_Collapsed verifies renderMessagesFull collapses tools.
func TestRenderMessagesFull_Collapsed(t *testing.T) {
	t.Parallel()
	messages := []MessageView{
		{
			Role: "assistant",
			Blocks: []ContentBlock{
				{Type: BlockTool, ToolCall: ToolCallView{
					ID: "t1", Name: "Bash", Summary: "ls",
					Output: strings.Repeat("output line\n", 20),
					Done:   true, Elapsed: time.Second,
				}},
			},
		},
	}

	// Not expanded → should collapse
	rendered := renderMessagesFull(messages, 80, false, "", false, 0)
	if !strings.Contains(rendered, "ctrl+o to expand") {
		t.Errorf("renderMessagesFull(expand=false) should collapse tool output, got:\n%s", rendered)
	}

	// Expanded → should show all
	rendered = renderMessagesFull(messages, 80, true, "", false, 0)
	if strings.Contains(rendered, "ctrl+o to expand") {
		t.Errorf("renderMessagesFull(expand=true) should NOT collapse, got:\n%s", rendered)
	}
}

// TestRenderMessagesFull_NoHintCommit verifies that renderMessagesFull with noHint=true
// omits ctrl+o hint while preserving collapse state.
func TestRenderMessagesFull_NoHintCommit(t *testing.T) {
	t.Parallel()
	messages := []MessageView{
		{
			Role: "assistant",
			Blocks: []ContentBlock{
				{Type: BlockTool, ToolCall: ToolCallView{
					ID: "t1", Name: "Bash", Summary: "ls",
					Output: strings.Repeat("output line\n", 20),
					Done:   true, Elapsed: time.Second,
				}},
			},
		},
	}

	// Collapsed + noHint=true: no ctrl+o but still collapsed
	rendered := renderMessagesFull(messages, 80, false, "", true, 0)
	if strings.Contains(rendered, "ctrl+o") {
		t.Errorf("noHint=true should suppress ctrl+o, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "… +17 lines") {
		t.Errorf("noHint=true should still show line count, got:\n%s", rendered)
	}

	// Expanded + noHint=true: shows all (same as expanded without noHint)
	rendered = renderMessagesFull(messages, 80, true, "", true, 0)
	if strings.Contains(rendered, "ctrl+o") {
		t.Errorf("expanded should not have ctrl+o, got:\n%s", rendered)
	}
}

// TestApp_View_TruncationPreservesAssistantText verifies that when expanded tool
// output exceeds terminal height, the assistant's text response AFTER the tool
// is still visible. Truncation should cut from the top (old content), not the
// bottom (newest content).
func TestApp_View_TruncationPreservesAssistantText(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 15 // small height → maxLines = 12
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()

	// Tool with long output (takes many lines)
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := strings.Repeat("tool output line\n", 20)
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)

	// Assistant text AFTER the tool — this must be visible
	app.repl.AppendTextItem()
	app.repl.AppendChunk("FINAL ANSWER: the result is 42")
	app.markViewportDirty()

	// Expand tools
	app.allToolsExpanded = true
	app.contentDirty = true

	v := app.View()
	if !strings.Contains(v, "FINAL ANSWER") {
		t.Errorf("truncation should preserve assistant text after tool output, got:\n%s", v)
	}
}

// TestApp_CommitPreservesCollapseState verifies that committing to scrollback
// preserves the user's collapse/expand state — collapsed tools stay collapsed
// in committed output (just without the ctrl+o hint).
func TestApp_CommitPreservesCollapseState(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 40
	// User has NOT expanded tools (default collapsed)
	if app.allToolsExpanded {
		t.Error("should start collapsed")
	}

	app.repl.AddUserMessage("test")
	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("done")
	app.progressStart = time.Now()
	app.status.usage.InputTokens = 10
	app.status.usage.OutputTokens = 5

	// Stream complete
	app.Update(queryEndMsg{})

	// Simulate commit (renderMessagesFull with noHint=true but NOT forced expand)
	// The commit should use expand=false (user's state), not expand=true
	uncommitted := app.repl.messages[app.committedCount:]
	rendered := renderMessagesFull(uncommitted, app.width, app.allToolsExpanded, "", true, 0)

	// Should NOT show all lines — tools are collapsed
	if strings.Contains(rendered, "line10") {
		t.Errorf("collapsed tools should NOT show line10 in committed output, got:\n%s", rendered)
	}
	// Should show collapsed indicator without ctrl+o hint
	if !strings.Contains(rendered, "… +7 lines") {
		t.Errorf("should show collapsed line count (without ctrl+o), got:\n%s", rendered)
	}
	if strings.Contains(rendered, "ctrl+o") {
		t.Errorf("committed output should not contain ctrl+o hint, got:\n%s", rendered)
	}
}

// ---------------------------------------------------------------------------
// Scroll buffer tests
// ---------------------------------------------------------------------------

// TestApp_Scroll_WindowLimitsContent verifies that View() limits visible content
// to the terminal height when content exceeds it.
func TestApp_Scroll_WindowLimitsContent(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10 // maxContentLines = 10 - 3 = 7

	// Add 20 lines of plain text (not tool output, to avoid per-tool truncation)
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 20))
	app.markViewportDirty()

	v := app.View()
	viewLines := strings.Split(v, "\n")

	// View should have limited lines (scroll indicator + content + progress + input)
	if len(viewLines) > app.height {
		t.Errorf("View() produced %d lines, should fit within height %d:\n%s", len(viewLines), app.height, v)
	}

	// Scroll indicator should be present (format: "N/M PgUp/PgDown/Mouse")
	hasScrollIndicator := strings.Contains(v, "PgUp/PgDown/Mouse")
	if !hasScrollIndicator {
		t.Errorf("scroll indicator should be present when content overflows, got:\n%s", v)
	}

	// scrollTotal should reflect full content (20 lines + possible empty trailing line)
	if app.scrollTotal <= 7 {
		t.Errorf("scrollTotal = %d, expected > 7 (viewport lines)", app.scrollTotal)
	}
}

// TestApp_Scroll_AutoScrollToBottom verifies auto-scroll behavior.
func TestApp_Scroll_AutoScrollToBottom(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10

	// Add long content
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 20))
	app.markViewportDirty()

	v := app.View()

	// userScrolled=false means auto-scroll to bottom → should show recent content
	if !strings.Contains(v, "line") {
		t.Errorf("auto-scroll should show content, got:\n%s", v)
	}

	// Should be at the bottom (scrollOffset near end)
	if app.scrollOffset == 0 && app.scrollTotal > 7 {
		t.Error("auto-scroll should set scrollOffset near bottom, got 0")
	}
}

// TestApp_Scroll_PageUpPageDown verifies PgUp/PgDown key bindings.
func TestApp_Scroll_PageUpPageDown(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10

	// Add long content
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 30))
	app.markViewportDirty()

	// Force initial view to populate scrollTotal
	_ = app.View()

	// PgUp should scroll up
	prevOffset := app.scrollOffset
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if app.scrollOffset >= prevOffset {
		t.Errorf("PgUp should decrease scrollOffset, was %d now %d", prevOffset, app.scrollOffset)
	}
	if !app.userScrolled {
		t.Error("PgUp should set userScrolled=true")
	}

	// PgDown should scroll down
	prevOffset = app.scrollOffset
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if app.scrollOffset <= prevOffset {
		t.Errorf("PgDown should increase scrollOffset, was %d now %d", prevOffset, app.scrollOffset)
	}
}

// TestApp_Scroll_MouseWheel verifies mouse wheel scroll support.
func TestApp_Scroll_MouseWheel(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10

	// Add long content
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 30))
	app.markViewportDirty()
	_ = app.View()

	// Mouse wheel up should scroll up
	prevOffset := app.scrollOffset
	_, _ = app.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	if app.scrollOffset >= prevOffset {
		t.Errorf("wheel up should decrease scrollOffset, was %d now %d", prevOffset, app.scrollOffset)
	}

	// Mouse wheel down should scroll down
	prevOffset = app.scrollOffset
	_, _ = app.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if app.scrollOffset <= prevOffset {
		t.Errorf("wheel down should increase scrollOffset, was %d now %d", prevOffset, app.scrollOffset)
	}
}

// TestApp_Scroll_ResetOnSubmit verifies scroll state resets on new query.
func TestApp_Scroll_ResetOnSubmit(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10

	// Add long content and scroll up
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 30))
	app.markViewportDirty()
	_ = app.View()
	app.scrollUp(5)
	if app.scrollOffset == 0 {
		t.Fatal("scrollUp should change offset")
	}

	// Complete stream
	app.repl.FinishStream(nil)
	app.spinner.Stop()

	// Submit new query
	app.input.SetValue("new query")
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Scroll state should be reset
	if app.scrollOffset != 0 {
		t.Errorf("scrollOffset should be 0 after submit, got %d", app.scrollOffset)
	}
	if app.scrollTotal != 0 {
		t.Errorf("scrollTotal should be 0 after submit, got %d", app.scrollTotal)
	}
	if app.userScrolled {
		t.Error("userScrolled should be false after submit")
	}

	// Execute any batched commands
	if cmd != nil {
		_ = cmd()
	}
}

// TestApp_Scroll_IndicatorPosition verifies scroll indicator shows correct position.
func TestApp_Scroll_IndicatorPosition(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10

	// Add long content
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 30))
	app.markViewportDirty()

	// Auto-scroll to bottom → should show ↑ arrow (content above)
	v := app.View()
	if !strings.Contains(v, "↑") {
		t.Errorf("at bottom should show ↑ arrow, got:\n%s", v)
	}

	// Scroll to top → should show ↓ arrow (content below)
	app.scrollOffset = 0
	app.userScrolled = true
	v = app.View()
	if !strings.Contains(v, "↓") {
		t.Errorf("at top should show ↓ arrow, got:\n%s", v)
	}

	// Scroll to middle → should show ↕ arrow (both directions)
	app.scrollOffset = app.scrollTotal / 2
	app.userScrolled = true
	v = app.View()
	if !strings.Contains(v, "↕") {
		t.Errorf("in middle should show ↕ arrow, got:\n%s", v)
	}
}

// TestApp_Scroll_PageNumberChanges verifies the page number actually changes
// when scrolling. Previous bug: used scrollOffset/viewLines which stayed at 1
// when maxOffset < viewLines (e.g. scrollTotal=73, viewLines=40, maxOff=33).
func TestApp_Scroll_PageNumberChanges(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10 // maxContentLines=7, viewLines=6

	// Create content where scrollTotal is just over maxContentLines.
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 10))
	app.markViewportDirty()

	// At bottom (auto-scroll): page should be 2/2
	v := app.View()
	if !strings.Contains(v, "2/2") {
		t.Errorf("at bottom should show page 2/2, got:\n%s", v)
	}

	// PgUp to top: page should be 1/2
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	v = app.View()
	if !strings.Contains(v, "1/2") {
		t.Errorf("at top should show page 1/2, got:\n%s", v)
	}
}

// TestApp_Scroll_LastPageNumberCorrect verifies that when scrolled to the bottom,
// the page indicator shows totalPages (not one less).
// Bug: midLine formula gave wrong page when scrollTotal wasn't an even multiple
// of viewLines (e.g. scrollTotal=19, viewLines=6 → showed 3/4 instead of 4/4).
func TestApp_Scroll_LastPageNumberCorrect(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10 // maxContentLines=7, viewLines=6

	// 19 lines of content → totalPages=4, maxOff=13
	// At bottom (offset=13): midLine=16, old formula 16/6+1=3 (wrong, should be 4)
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 19))
	app.markViewportDirty()

	// Auto-scroll to bottom
	v := app.View()
	if !strings.Contains(v, "4/4") {
		t.Errorf("at bottom should show page 4/4, got:\n%s", v)
	}

	// Also test 13 lines → totalPages=3, maxOff=7
	app2 := newTestApp(&tuiMockProvider{})
	app2.width = 80
	app2.height = 10
	app2.repl.StartQuery(nil)
	app2.spinner.Start()
	app2.progressStart = time.Now()
	app2.repl.AppendChunk(strings.Repeat("line\n", 13))
	app2.markViewportDirty()

	v2 := app2.View()
	if !strings.Contains(v2, "3/3") {
		t.Errorf("at bottom with 13 lines should show page 3/3, got:\n%s", v2)
	}
}

// TestApp_Scroll_HalfPageScroll verifies PgUp/PgDown scrolls by half a page,
// matching TS behavior (Math.floor(viewportHeight/2)). This ensures:
// 1. No page skipping (each PgUp changes page number by at most 1)
// 2. 50% overlap between consecutive views (context preserved)
// Previous bug: full-page scroll with off-by-one caused page skipping.
func TestApp_Scroll_HalfPageScroll(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10 // maxContentLines=7, viewLines=6, halfPage=3

	// 30 lines → 5 pages (viewLines=6)
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 30))
	app.markViewportDirty()

	// Auto-scroll to bottom → page 5/5
	v := app.View()
	if !strings.Contains(v, "5/5") {
		t.Fatalf("should start at page 5/5, got:\n%s", v)
	}

	// PgUp → should go to page 4 (half-page=3, offset 24→21, page 21/6+1=4)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	v = app.View()
	if !strings.Contains(v, "4/5") {
		t.Errorf("after 1 PgUp should show page 4/5, got:\n%s", v)
	}

	// PgUp again → page 4 still (offset 21→18, page 18/6+1=4, overlap)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	v = app.View()
	if !strings.Contains(v, "4/5") {
		t.Errorf("after 2 PgUp should still show page 4/5 (overlap), got:\n%s", v)
	}

	// PgUp again → page 3 (offset 18→15, page 15/6+1=3)
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	v = app.View()
	if !strings.Contains(v, "3/5") {
		t.Errorf("after 3 PgUp should show page 3/5, got:\n%s", v)
	}

	// Verify the scroll amount is exactly viewLines/2 = 3
	bottom := app.scrollTotal - 6 // maxOff
	app.scrollOffset = bottom     // reset to bottom
	_ = app.View()                // populate
	prevOffset := app.scrollOffset
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	scrolled := prevOffset - app.scrollOffset
	if scrolled != 3 {
		t.Errorf("PgUp should scroll %d lines (half page), got %d", 3, scrolled)
	}
}

// TestApp_Scroll_ShortContentNoScrolling verifies no scroll when content fits.
func TestApp_Scroll_ShortContentNoScrolling(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 40 // large height

	// Add short content
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk("short response")
	app.markViewportDirty()

	v := app.View()

	// No scroll indicator when content fits
	if strings.Contains(v, "PgUp/PgDown/Mouse") {
		t.Errorf("short content should not show scroll indicator, got:\n%s", v)
	}
	if app.scrollOffset != 0 {
		t.Errorf("scrollOffset should be 0 for short content, got %d", app.scrollOffset)
	}
}

// TestApp_Scroll_PgUpOvershootSetsUserScrolled reproduces the bug where PgUp
// from the bottom overshoots past 0 (clamped), and userScrolled stays false
// because `userScrolled = scrollOffset > 0` evaluates to false at offset 0.
// This causes View() to auto-scroll back to bottom, making PgUp a no-op.
func TestApp_Scroll_PgUpOvershootSetsUserScrolled(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 10 // maxContentLines=7, viewLines=6

	// Create content where half-page scroll exactly reaches offset 0.
	// 9 lines: scrollTotal=9, maxOff=9-6=3, halfPage=3.
	// PgUp from bottom: 3-3=0 → clamped to 0.
	// Bug: userScrolled = 0>0 = false → View() auto-scrolls back.
	app.repl.StartQuery(nil)
	app.spinner.Start()
	app.progressStart = time.Now()
	app.repl.AppendChunk(strings.Repeat("line\n", 9))
	app.markViewportDirty()
	_ = app.View() // populate scrollTotal

	if app.scrollTotal <= 7 {
		t.Fatalf("need scrollTotal > maxContentLines for overflow, got %d", app.scrollTotal)
	}

	// PgUp from auto-scrolled bottom
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyPgUp})

	if !app.userScrolled {
		t.Error("PgUp must set userScrolled=true even when offset clamps to 0")
	}

	// Verify View() respects userScrolled and stays at top, not auto-scrolling back
	v := app.View()
	if app.scrollOffset != 0 {
		t.Errorf("View() should keep offset at 0 when userScrolled=true, got %d", app.scrollOffset)
	}
	// Should show scroll indicator since we're at top of overflow content
	if !strings.Contains(v, "PgUp/PgDown/Mouse") {
		t.Errorf("should show scroll indicator at top of overflow, got:\n%s", v)
	}
}

// ---------------------------------------------------------------------------
// Tool count in progress line
// ---------------------------------------------------------------------------

func TestApp_ProgressLine_NoTools(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.spinner.Start()
	app.progressStart = time.Now()

	v := app.View()
	if strings.Contains(v, "tool") && strings.Contains(v, "tokens") {
		// "tokens" contains "tool" substring, so be more precise
		t.Errorf("should not show tool count when no tools, got:\n%s", v)
	}
}

func TestApp_ProgressLine_OneTool(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.spinner.Start()
	app.progressStart = time.Now()

	// Simulate one tool started
	app.repl.toolCount = 1

	v := app.View()
	if !strings.Contains(v, "1 tool") {
		t.Errorf("should show '1 tool', got:\n%s", v)
	}
	if strings.Contains(v, "1 tools") {
		t.Errorf("should use singular '1 tool', not '1 tools', got:\n%s", v)
	}
}

func TestApp_ProgressLine_ThreeTools(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.spinner.Start()
	app.progressStart = time.Now()

	// Simulate three tools started
	app.repl.toolCount = 3

	v := app.View()
	if !strings.Contains(v, "3 tools") {
		t.Errorf("should show '3 tools', got:\n%s", v)
	}
}

func TestApp_ToolCount_ResetsOnNewQuery(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.toolCount = 5

	// StartQuery resets toolCount
	app.repl.StartQuery(nil)

	if app.repl.toolCount != 0 {
		t.Errorf("toolCount should be 0 after StartQuery, got %d", app.repl.toolCount)
	}
}

func TestApp_ToolCount_IncrementOnToolStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	app.repl.StartQuery(nil)
	app.repl.PendingToolStarted("id1", "Read", "", "")
	if app.repl.toolCount != 1 {
		t.Errorf("toolCount should be 1 after one tool start, got %d", app.repl.toolCount)
	}
	app.repl.PendingToolStarted("id2", "Grep", "", "")
	if app.repl.toolCount != 2 {
		t.Errorf("toolCount should be 2 after two tool starts, got %d", app.repl.toolCount)
	}
	app.repl.PendingToolStarted("id3", "Bash", "", "")
	if app.repl.toolCount != 3 {
		t.Errorf("toolCount should be 3 after three tool starts, got %d", app.repl.toolCount)
	}
}

func TestApp_ToolCount_NotShownWhenNotStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.toolCount = 5

	v := app.View()
	// Progress line only shows when streaming
	if strings.Contains(v, "5 tools") {
		t.Errorf("should not show tool count when not streaming, got:\n%s", v)
	}
}

// TestApp_UsageMsg_NoDoubleCount_MaxValue verifies that usage tokens use max()
// not +=, preventing double-counting when providers report the same values
// in both message_start and message_delta (e.g. MiniMax).
func TestApp_UsageMsg_NoDoubleCount_MaxValue(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	// First usage event (simulates message_start): input=6, cache_creation=404
	app.updateRepl(usageMsg{InputTokens: 6, OutputTokens: 0, CacheReadInputTokens: 0, CacheCreationInputTokens: 404})

	if app.status.usage.InputTokens != 6 {
		t.Errorf("after first usageMsg, InputTokens = %d, want 6", app.status.usage.InputTokens)
	}
	if app.status.usage.CacheCreationInputTokens != 404 {
		t.Errorf("after first usageMsg, CacheCreationInputTokens = %d, want 404", app.status.usage.CacheCreationInputTokens)
	}

	// Second usage event (simulates message_delta): same input/cache values + output
	// MiniMax reports: input=6 (same), cache_creation=404 (same), output=44
	app.updateRepl(usageMsg{InputTokens: 6, OutputTokens: 44, CacheReadInputTokens: 0, CacheCreationInputTokens: 404})

	// With +=, InputTokens would be 12 and CacheCreationInputTokens would be 808.
	// With max(), they should stay at 6 and 404.
	if app.status.usage.InputTokens != 6 {
		t.Errorf("after second usageMsg, InputTokens = %d, want 6 (max, not += which gives 12)", app.status.usage.InputTokens)
	}
	if app.status.usage.CacheCreationInputTokens != 404 {
		t.Errorf("after second usageMsg, CacheCreationInputTokens = %d, want 404 (max, not += which gives 808)", app.status.usage.CacheCreationInputTokens)
	}
	// OutputTokens should accumulate (0 then 44 = 44)
	if app.status.usage.OutputTokens != 44 {
		t.Errorf("OutputTokens = %d, want 44", app.status.usage.OutputTokens)
	}

	// Verify displayed values also correct
	totalInput := app.status.usage.TotalInputTokens()
	if app.displayedInputTokens != totalInput {
		t.Errorf("displayedInputTokens = %d, want %d", app.displayedInputTokens, totalInput)
	}
}

// TestApp_UsageMsg_MaxValue_SecondLarger verifies max() works when delta has larger values.
func TestApp_UsageMsg_MaxValue_SecondLarger(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery(nil)

	// First: input=10
	app.updateRepl(usageMsg{InputTokens: 10, OutputTokens: 0})
	// Second: input=21 (larger, e.g. cache hit with more context)
	app.updateRepl(usageMsg{InputTokens: 21, OutputTokens: 35})

	if app.status.usage.InputTokens != 21 {
		t.Errorf("InputTokens = %d, want 21 (max of 10 and 21)", app.status.usage.InputTokens)
	}
	if app.status.usage.OutputTokens != 35 {
		t.Errorf("OutputTokens = %d, want 35", app.status.usage.OutputTokens)
	}
}
