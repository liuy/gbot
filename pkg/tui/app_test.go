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
	app.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if app.input.Value() != "ab" {
		t.Errorf("Delete should call Backspace, Value() = %q", app.input.Value())
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
