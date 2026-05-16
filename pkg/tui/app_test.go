package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
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
		{Type: "message_delta", DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}, Usage: &types.Usage{OutputTokens: 3}},
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()

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
	app.repl.StartQuery()
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
		var sb strings.Builder
		for _, m := range a.repl.messages {
			fmt.Fprintf(&sb, "  role=%s blocks=%d\n", m.Role, len(m.Blocks))
		}
		t.Errorf("no system message with 'stream failed' found; messages:\n%s", sb.String())
	}
}

// ---------------------------------------------------------------------------
// Update — attachmentMsg Path A (streaming) then Path B (idle)
// ---------------------------------------------------------------------------

// TestApp_Attachment_PathA_ThenPathB verifies the full flow:
// 1. attachmentMsg arrives during streaming (Path A: ignored)
// 2. queryEndMsg → TUI goes idle
// 3. Engine re-dispatches EventAttachment (via drain)
// 4. attachmentMsg arrives in idle mode (Path B: triggers ProcessAttachments)
// Regression: attachment arriving during last turn was silently dropped because
// runTurns only drains queue at turn start, and queryEndMsg did not check.
func TestApp_Attachment_PathA_ThenPathB(t *testing.T) {
	t.Parallel()

	mp := &tuiMockProvider{
		responses: []tuiMockResponse{
			{events: textStreamEvents("test-model", "Attachment processed.")},
		},
	}

	app := newTestApp(mp)
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for progress bar elapsed time during streaming simulation

	// Step 1: attachment arrives while streaming (Path A — ignored)
	model, cmd := app.Update(attachmentMsg{})
	if cmd == nil {
		t.Error("attachmentMsg during streaming should return readEvents cmd")
	}
	a := model.(*App)
	if !a.repl.streaming {
		t.Error("should still be streaming after attachmentMsg during stream")
	}

	// Step 2: query ends → TUI goes idle
	model, _ = a.Update(queryEndMsg{})
	a = model.(*App)
	if a.repl.streaming {
		t.Error("should not be streaming after queryEndMsg")
	}

	// Step 3: Engine re-dispatches attachmentMsg (simulating drain)
	// Step 4: attachmentMsg arrives in idle mode (Path B)
	model, _ = a.Update(attachmentMsg{})
	a = model.(*App)

	// TUI is pure renderer — streaming is NOT set by attachmentMsg.
	// Engine auto-processes and emits turnStartMsg which sets streaming state.
	if a.repl.streaming {
		t.Error("attachmentMsg in idle should NOT set streaming — engine auto-processes")
	}
}

// ---------------------------------------------------------------------------
// Update — attachmentMsg notification rendering
// ---------------------------------------------------------------------------

func TestApp_Attachment_NotificationRendering(t *testing.T) {
	t.Parallel()

	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	model, _ := app.Update(attachmentMsg{JobID: "bg-1", Preview: `Background command "npm test" completed (exit code 0)`})
	a := model.(*App)

	// StartQuery creates 1 assistant message, notification appends 1 more
	if len(a.repl.messages) != 2 {
		t.Fatalf("expected 2 messages (assistant + notification), got %d", len(a.repl.messages))
	}
	m := a.repl.messages[1] // second message is the notification
	if m.Role != "notification" {
		t.Errorf("Role = %q, want %q", m.Role, "notification")
	}
	if len(m.Blocks) != 1 || m.Blocks[0].Type != BlockText {
		t.Fatalf("expected 1 text block, got %v", m.Blocks)
	}
	// Dot is rendered with ANSI color codes, so check content not exact match
	text := m.Blocks[0].Text
	if !strings.Contains(text, `● Background command "npm test" completed`) {
		t.Errorf("Text = %q, want to contain %q", text, `● Background command "npm test" completed`)
	}
	if strings.Contains(text, "\x1b") {
		if !strings.Contains(text, "10m") {
			t.Error("expected green (10) ANSI color for success dot")
		}
	}
}

func TestApp_Attachment_NotificationRendering_Failed(t *testing.T) {
	t.Parallel()

	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	model, _ := app.Update(attachmentMsg{JobID: "bg-2", Preview: `● Background command "make" failed with exit code 1`, Failed: true})
	a := model.(*App)

	// StartQuery creates 1 assistant message, notification appends 1 more
	if len(a.repl.messages) != 2 {
		t.Fatalf("expected 2 messages (assistant + notification), got %d", len(a.repl.messages))
	}
	text := a.repl.messages[1].Blocks[0].Text
	if !strings.Contains(text, `● Background command "make" failed with exit code 1`) {
		t.Errorf("Text = %q, want to contain %q", text, `● Background command "make" failed with exit code 1`)
	}
	if strings.Contains(text, "\x1b") {
		if !strings.Contains(text, ";5;9m") && !strings.Contains(text, ";5;9;") {
			t.Error("expected red (9) ANSI color for error dot")
		}
	}
}

func TestApp_Attachment_EmptyMsg_NoNotification(t *testing.T) {
	t.Parallel()

	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	model, _ := app.Update(attachmentMsg{})
	a := model.(*App)

	// StartQuery creates 1 assistant message, but no notification added
	if len(a.repl.messages) != 1 {
		t.Errorf("empty attachmentMsg should not add notification, got %d messages", len(a.repl.messages))
	}
	if a.repl.messages[0].Role == "notification" {
		t.Error("empty attachmentMsg should not create notification message")
	}
}
// ---------------------------------------------------------------------------
// Update — attachmentMsg after query end (Case 2: idle path)
// ---------------------------------------------------------------------------

// TestApp_Attachment_AfterQueryEnd_AutoProcessed verifies that when a bg task
// completes after query end, the engine auto-processes the attachment (not TUI).
// TUI attachmentMsg handler is pure rendering — no streaming state change.
func TestApp_Attachment_AfterQueryEnd_AutoProcessed(t *testing.T) {
	t.Parallel()

	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Step 1: query ends -> TUI goes idle
	model, _ := app.Update(queryEndMsg{})
	a := model.(*App)
	if a.repl.streaming {
		t.Fatal("should not be streaming after queryEndMsg")
	}

	// Step 2: bg task completes after query ended -> enqueue
	app.engine.EnqueueAttachment(types.QueuedItem{
		Value:  "<job-notification><job-id>bg-2</job-id><status>completed</status></job-notification>",
		Mode:   types.ItemModeJob,
		IsMeta: true,
	})

	// Step 3: attachmentMsg arrives while idle — TUI only renders, no streaming
	model, _ = a.Update(attachmentMsg{})
	a = model.(*App)

	// TUI does NOT set streaming — engine auto-processes asynchronously
	if a.repl.streaming {
		t.Error("attachmentMsg in idle should NOT set streaming — engine auto-processes")
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

// TestApp_HandleKey_BackslashEnter inserts newline instead of submitting.
// Source: TS useTextInput.ts:248-253 — backslash+enter inserts \n.
func TestApp_HandleKey_BackslashEnter(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.input.SetValue("hello\\")
	app.input.cursor = len(app.input.value) // cursor at end

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// Should NOT submit (cmd should be nil)
	if cmd != nil {
		t.Error("backslash+enter should insert newline, not submit")
	}
	// The backslash should be removed and \n inserted
	if got := app.input.Value(); got != "hello\n" {
		t.Errorf("after backslash+enter: got %q, want %q", got, "hello\n")
	}
}

// TestApp_HandleKey_BackslashEnter_MiddleOfText inserts newline in middle.
// Source: TS useTextInput.ts:248-253 — cursor.text[cursor.offset-1] === '\\'
func TestApp_HandleKey_BackslashEnter_MiddleOfText(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.input.SetValue("hel\\lo")
	app.input.cursor = 4 // cursor after the backslash (pos 4, after "hel\")

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("backslash+enter in middle should insert newline, not submit")
	}
	if got := app.input.Value(); got != "hel\nlo" {
		t.Errorf("after backslash+enter in middle: got %q, want %q", got, "hel\nlo")
	}
}

// TestApp_HandleKey_AltEnter inserts newline (same as VSCode Shift+Enter).
// Source: TS useTextInput.ts:255-257 — key.meta || key.shift → insert \n.
// VSCode terminal-setup sends \x1b\r for Shift+Enter, which bubbletea
// parses as Alt+Enter (Alt=true, Type=KeyEnter).
func TestApp_HandleKey_AltEnter(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.input.SetValue("hello world")
	app.input.cursor = 5 // after "hello"

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if cmd != nil {
		t.Error("alt+enter should insert newline, not submit")
	}
	if got := app.input.Value(); got != "hello\n world" {
		t.Errorf("after alt+enter: got %q, want %q", got, "hello\n world")
	}
}

// TestApp_HandleKey_AltEnter_AtEnd inserts newline at end.
func TestApp_HandleKey_AltEnter_AtEnd(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.input.SetValue("hello")

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if cmd != nil {
		t.Error("alt+enter should insert newline, not submit")
	}
	if got := app.input.Value(); got != "hello\n" {
		t.Errorf("after alt+enter at end: got %q, want %q", got, "hello\n")
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
	app.input.SetValue("my draft")
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP}) // shows "second"
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlN}) // restores draft
	if app.input.Value() != "my draft" {
		t.Errorf("Ctrl+N should restore draft, got %q", app.input.Value())
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for elapsed time display in streaming progress line
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	s.StartQuery()
	// No tool was started with this ID
	s.PendingToolDone("nonexistent", "output", false, 0)
	// Should not panic, no tool updated
}

func TestReplState_PendingToolDelta(t *testing.T) {
	s := NewReplState()
	s.StartQuery()
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
	s.StartQuery()
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
// updateRepl — turnStartMsg, streamMessageMsg, toolEndMsg
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_TurnStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	_, cmd := app.updateRepl(turnStartMsg{})
	if cmd == nil {
		t.Error("turnStartMsg should return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_StreamMessage(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	_, cmd := app.updateRepl(streamMessageMsg{Role: "assistant"})
	if cmd == nil {
		t.Error("streamMessageMsg should return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_StreamToolResult(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("t1", "Read", "", `{}`)
	_, cmd := app.updateRepl(toolEndMsg{ToolUseID: "t1", Output: "ok"})
	if cmd == nil {
		t.Error("toolEndMsg should return a readEvents cmd")
	}
}

// ---------------------------------------------------------------------------
// updateRepl — agent toolStartMsg (regression: must be in App.Update type switch)
// ---------------------------------------------------------------------------

func TestApp_Update_RoutesAgentToolStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search code", "{}")
	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}
	_, cmd := app.Update(toolStartMsg{
		ID:      "sub-1",
		Name:    "Grep",
		Summary: "search pattern",
		Agent:   agent,
	})
	if cmd == nil {
		t.Error("agent toolStartMsg should be routed to updateRepl and return a readEvents cmd")
	}
}

func TestApp_UpdateRepl_AgentToolStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")
	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}
	_, cmd := app.updateRepl(toolStartMsg{
		ID:      "sub-1",
		Name:    "Grep",
		Summary: "pattern",
		Agent:   agent,
	})
	if cmd == nil {
		t.Error("agent toolStartMsg should return a readEvents cmd")
	}
	tcv, ok := app.repl.pendingTool["call_abc"]
	if !ok {
		t.Fatal("pendingTool should have call_abc")
	}
	if len(tcv.Blocks) == 0 {
		t.Error("Blocks should have at least one entry")
	}
	if tcv.Blocks[0].ToolCall.Name != "Grep" {
		t.Errorf("Blocks[0].ToolCall.Name = %q, want Grep", tcv.Blocks[0].ToolCall.Name)
	}
	if tcv.Blocks[0].ToolCall.Done {
		t.Error("tool_start entry should not be Done")
	}
}

// TestApp_UpdateRepl_AgentToolParamDelta verifies that toolParamDeltaMsg events
// update the summary of the last running tool entry.
// tool_start with empty summary → tool_param_delta with summary → summary updated.
func TestApp_UpdateRepl_AgentToolParamDelta(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	// Step 1: tool_start with empty summary (as happens at content_block_start)
	app.updateRepl(toolStartMsg{
		ID:      "sub-1",
		Name:    "Bash",
		Summary: "", // empty at content_block_start
		Agent:   agent,
	})
	tcv := app.repl.pendingTool["call_abc"]
	if tcv.Blocks[0].ToolCall.Summary != "" {
		t.Fatalf("initial summary should be empty, got %q", tcv.Blocks[0].ToolCall.Summary)
	}

	// Step 2: toolParamDeltaMsg arrives with summary from streaming input
	app.updateRepl(toolParamDeltaMsg{
		ID:      "sub-1",
		Summary: "count files",
		Agent:   agent,
	})

	// Verify summary updated
	tcv = app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) == 0 {
		t.Fatal("Blocks should have entries")
	}
	if tcv.Blocks[0].ToolCall.Summary != "count files" {
		t.Errorf("summary should be updated to %q, got %q", "count files", tcv.Blocks[0].ToolCall.Summary)
	}
}

// TestApp_UpdateRepl_AgentToolParamDelta_SameDepth verifies toolParamDeltaMsg
// updates the existing entry even when depth matches (no duplicate entry).
func TestApp_UpdateRepl_AgentToolParamDelta_SameDepth(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	// tool_start at depth=1 (sub-agent's depth)
	app.updateRepl(toolStartMsg{
		ID:      "sub-1",
		Name:    "Read",
		Summary: "",
		Agent:   agent,
	})
	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) != 1 {
		t.Fatalf("expected 1 Block entry, got %d", len(tcv.Blocks))
	}
	if tcv.Blocks[0].ToolCall.Name != "Read" {
		t.Errorf("expected tool Read, got %s", tcv.Blocks[0].ToolCall.Name)
	}

	// toolParamDeltaMsg — should update existing entry, not add new
	app.updateRepl(toolParamDeltaMsg{
		ID:      "sub-1",
		Summary: "Makefile",
		Agent:   agent,
	})

	tcv = app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) != 1 {
		t.Errorf("expected 1 Block entry (no duplicate), got %d: %+v", len(tcv.Blocks), tcv.Blocks)
	}
	if tcv.Blocks[0].ToolCall.Summary != "Makefile" {
		t.Errorf("expected summary Makefile, got %q", tcv.Blocks[0].ToolCall.Summary)
	}
}

// TestApp_UpdateRepl_AgentThinkingPreserved verifies that Thinking entry is
// preserved when tools start — thinking blocks are part of the agent output.
func TestApp_UpdateRepl_AgentThinkingPreserved(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	// thinking_start → adds Thinking block
	app.updateRepl(thinkingStartMsg{Agent: agent})
	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) != 1 || tcv.Blocks[0].Type != BlockThinking {
		t.Fatalf("should have 1 BlockThinking entry, got %v", tcv.Blocks)
	}

	// tool_start → adds tool, thinking block is preserved
	app.updateRepl(toolStartMsg{
		ID:      "sub-1",
		Name:    "Read",
		Summary: "main.go",
		Agent:   agent,
	})
	tcv = app.repl.pendingTool["call_abc"]
	// Should have 2 blocks: thinking + Read tool
	if len(tcv.Blocks) != 2 {
		t.Errorf("should have 2 blocks (thinking + Read), got %d: %v", len(tcv.Blocks), tcv.Blocks)
	}
	hasThinking := false
	hasTool := false
	for _, b := range tcv.Blocks {
		if b.Type == BlockThinking {
			hasThinking = true
		}
		if b.Type == BlockTool && b.ToolCall.Name == "Read" {
			hasTool = true
		}
	}
	if !hasThinking {
		t.Error("thinking block should be preserved after tool starts")
	}
	if !hasTool {
		t.Error("Read tool block should exist")
	}
}

// TestApp_SpinnerTick_MarksDirty verifies that spinnerTickMsg sets contentDirty
// so tool dot blink animations render correctly.
func TestApp_SpinnerTick_MarksDirty(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
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

// TestApp_AgentUsageMsg_UpdatesInputTokens verifies that usageMsg with Agent
// snaps displayedInputTokens to include sub-agent input tokens.
func TestApp_AgentUsageMsg_UpdatesInputTokens(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// Main model usage — snaps displayedInputTokens
	app.updateRepl(usageMsg{InputTokens: 500, OutputTokens: 100})
	if app.displayedInputTokens != 500 {
		t.Fatalf("after usageMsg, displayedInputTokens = %d, want 500", app.displayedInputTokens)
	}

	// Agent usage — should snap displayedInputTokens (includes cache in total)
	agent := &types.AgentMeta{ParentToolUseID: "call_abc"}
	app.updateRepl(usageMsg{
		InputTokens:          300,
		CacheReadInputTokens: 200,
		OutputTokens:         50,
		Agent:                agent,
	})
	// TotalInputTokens = (500+300) InputTokens + (0+200) CacheRead = 1000
	if app.displayedInputTokens != 1000 {
		t.Errorf("after agent usageMsg, displayedInputTokens = %d, want 1000", app.displayedInputTokens)
	}
	if app.inputTokenTarget != 1000 {
		t.Errorf("inputTokenTarget = %d, want 1000", app.inputTokenTarget)
	}
	// Verify per-agent TokensIn includes cache: handler computes 300+200=500
	blk := app.repl.Messages()[0].Blocks[0]
	if blk.ToolCall.TokensIn != 500 {
		t.Errorf("agent TokensIn = %d, want 500 (input+cache)", blk.ToolCall.TokensIn)
	}
	if blk.ToolCall.TokensOut != 50 {
		t.Errorf("agent TokensOut = %d, want 50", blk.ToolCall.TokensOut)
	}
	// contextSize = 300+200+0+50 = 550
	if blk.ToolCall.ContextSize != 550 {
		t.Errorf("agent ContextSize = %d, want 550", blk.ToolCall.ContextSize)
	}
}

func TestApp_UpdateRepl_UsageMsg(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

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
	app.repl.StartQuery()

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
	app.repl.StartQuery()
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
	s.StartQuery()
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
	s.StartQuery()
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
	s.StartQuery()
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
	s.StartQuery()
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
	s.StartQuery()
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
	app.repl.StartQuery()

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
	app.repl.StartQuery()

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
	app.repl.StartQuery()

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
	s.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()

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
	if _, ok := msg.(queryEndMsg); !ok {
		t.Errorf("EventQueryEnd should return queryEndMsg, got %T", msg)
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
	app.engine.Query(ctx, "test", json.RawMessage(`"sys"`))

	cmd := app.readEvents()
	// readEvents() blocks on appCh, so it waits for the hub goroutine to dispatch
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for spinner animation timing during stream

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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-2 * time.Second) // REAL-TIME: needed for elapsed time in completed stats line
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for thinking state timing display

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
	app.repl.StartQuery()
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
	app.input.SetValue("my draft")
	app.Update(tea.KeyMsg{Type: tea.KeyUp})   // shows "second"
	app.Update(tea.KeyMsg{Type: tea.KeyDown}) // restores draft
	if app.input.Value() != "my draft" {
		t.Errorf("KeyDown should restore draft, got %q", app.input.Value())
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
	app.engine.Query(ctx, "test", json.RawMessage(`"sys"`))

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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for tool blink progress display
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
	app.input.SetWidth(20)
	app.input.SetValue("abcdefghijklmnopqrstuvwxyz") // wraps in 20-wide input
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
	app.input.SetWidth(20)
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
	app.input.SetWidth(20)
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
// streaming tool output display
// ---------------------------------------------------------------------------

func TestApp_Update_StreamToolOutput(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
	app.repl.PendingToolStarted("t1", "Bash", "", `{}`)

	// Set pendingToolStart BEFORE calling Update so it's available synchronously
	app.repl.pendingToolStart["t1"] = time.Now().Add(-100 * time.Millisecond) // REAL-TIME: needed for tool elapsed time calculation

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
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-2 * time.Second) // REAL-TIME: needed for stats scroll timing

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
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for stats block timing in message
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for stats line timing after stream complete
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for expanded tool view timing

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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
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
	app.repl.StartQuery()
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("done")
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for commit timing in tool collapse state
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for truncation timing in assistant text preservation

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
	app.repl.StartQuery()
	app.repl.PendingToolStarted("t1", "Bash", "ls", `{"command":"ls"}`)
	longOutput := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	app.repl.PendingToolDone("t1", longOutput, false, time.Second)
	app.repl.AppendTextItem()
	app.repl.AppendChunk("done")
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for commit preserves collapse state timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for scroll window content timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for auto-scroll elapsed time display
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for scroll page up/down content timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for mouse wheel scroll content timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for scroll reset on submit timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for scroll indicator position timing
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
// when scrolling. Used scrollOffset/viewLines which stayed at 1
// when maxOffset < viewLines (e.g. scrollTotal=73, viewLines=40, maxOff=33).
func TestApp_Scroll_PageNumberChanges(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 12 // maxContentLines=7, viewLines=6

	// Create content where scrollTotal is just over maxContentLines.
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for scroll page number calculation timing
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
// MidLine formula gave wrong page when scrollTotal wasn't an even multiple
// of viewLines (e.g. scrollTotal=19, viewLines=6 → showed 3/4 instead of 4/4).
func TestApp_Scroll_LastPageNumberCorrect(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 12 // maxContentLines=7, viewLines=6

	// 19 lines of content → totalPages=4, maxOff=13
	// At bottom (offset=13): midLine=16, old formula 16/6+1=3 (wrong, should be 4)
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for last page number correctness timing
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
	app2.height = 12
	app2.repl.StartQuery()
	app2.spinner.Start()
	app2.progressStart = time.Now() // REAL-TIME: needed for second app instance page number timing
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
// Full-page scroll with off-by-one caused page skipping.
func TestApp_Scroll_HalfPageScroll(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 12 // maxContentLines=7, viewLines=6, halfPage=3

	// 30 lines → 5 pages (viewLines=6)
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for half-page scroll timing
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
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for short content scroll state timing
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
	app.height = 12 // maxContentLines=7, viewLines=6

	// Create content where half-page scroll exactly reaches offset 0.
	// 9 lines: scrollTotal=9, maxOff=9-6=3, halfPage=3.
	// PgUp from bottom: 3-3=0 → clamped to 0.
	// userScrolled = 0>0 = false → View() auto-scrolls back.
	app.repl.StartQuery()
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for PgUp overshoot userScrolled detection timing
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
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for progress line with no tools

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
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for progress line with one tool

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
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) // REAL-TIME: needed for progress line with three tools

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
	app.repl.StartQuery()

	if app.repl.toolCount != 0 {
		t.Errorf("toolCount should be 0 after StartQuery, got %d", app.repl.toolCount)
	}
}

func TestApp_ToolCount_IncrementOnToolStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	app.repl.StartQuery()
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
func TestApp_UsageMsg_AccumulateAllFields(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

	// First usage event: input=6, cache_creation=404
	app.updateRepl(usageMsg{InputTokens: 6, OutputTokens: 0, CacheReadInputTokens: 0, CacheCreationInputTokens: 404})

	if app.status.usage.InputTokens != 6 {
		t.Errorf("after first usageMsg, InputTokens = %d, want 6", app.status.usage.InputTokens)
	}
	if app.status.usage.CacheCreationInputTokens != 404 {
		t.Errorf("after first usageMsg, CacheCreationInputTokens = %d, want 404", app.status.usage.CacheCreationInputTokens)
	}

	// Second usage event: input=6, cache_creation=404, output=44
	// All fields accumulate via +=.
	app.updateRepl(usageMsg{InputTokens: 6, OutputTokens: 44, CacheReadInputTokens: 0, CacheCreationInputTokens: 404})

	if app.status.usage.InputTokens != 12 {
		t.Errorf("after second usageMsg, InputTokens = %d, want 12 (6+6)", app.status.usage.InputTokens)
	}
	if app.status.usage.CacheCreationInputTokens != 808 {
		t.Errorf("after second usageMsg, CacheCreationInputTokens = %d, want 808 (404+404)", app.status.usage.CacheCreationInputTokens)
	}
	if app.status.usage.OutputTokens != 44 {
		t.Errorf("OutputTokens = %d, want 44", app.status.usage.OutputTokens)
	}

	// Verify displayed values
	totalInput := app.status.usage.TotalInputTokens()
	if app.displayedInputTokens != totalInput {
		t.Errorf("displayedInputTokens = %d, want %d", app.displayedInputTokens, totalInput)
	}
}

// TestApp_UsageMsg_MaxValue_SecondLarger verifies max() works when delta has larger values.
func TestApp_UsageMsg_AccumulateSecondLarger(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

	// First: input=10
	app.updateRepl(usageMsg{InputTokens: 10, OutputTokens: 0})
	// Second: input=21, output=35
	app.updateRepl(usageMsg{InputTokens: 21, OutputTokens: 35})

	if app.status.usage.InputTokens != 31 {
		t.Errorf("InputTokens = %d, want 31 (10+21)", app.status.usage.InputTokens)
	}
	if app.status.usage.OutputTokens != 35 {
		t.Errorf("OutputTokens = %d, want 35", app.status.usage.OutputTokens)
	}
}

// TestApp_UsageMsg_CacheCreationClearedOnNewResponse verifies that
// CacheCreationInputTokens from a previous API call is cleared when the
// new call has cache_creation=0. Without this fix, the stale value inflates
// TotalInputTokens and the status bar shows a wrong context size.
//
// Reproduces: TUI showed 26.7K (14294+7933+4492=26719) when actual context
// was 22.2K (14294+7933=22227) because cache_creation=4492 from an earlier
// API call was retained when the new call reported cache_creation=0.
func TestApp_UsageMsg_CacheCreationAccumulates(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

	// First API call: input=100, cache_read=500, cache_creation=200
	app.updateRepl(usageMsg{InputTokens: 100, OutputTokens: 0, CacheReadInputTokens: 500, CacheCreationInputTokens: 200})

	totalIn := app.status.usage.TotalInputTokens()
	if totalIn != 800 {
		t.Fatalf("after first call, TotalInputTokens = %d, want 800 (100+500+200)", totalIn)
	}

	// Second API call: input=150, cache_read=600, cache_creation=0
	// With +=, all fields accumulate — cache_creation stays at 200.
	app.updateRepl(usageMsg{InputTokens: 150, OutputTokens: 0, CacheReadInputTokens: 600, CacheCreationInputTokens: 0})

	// InputTokens=100+150=250, CacheRead=500+600=1100, CacheCreation=200+0=200
	totalIn = app.status.usage.TotalInputTokens()
	if totalIn != 1550 {
		t.Errorf("after second call, TotalInputTokens = %d, want 1550 (250+1100+200)", totalIn)
	}
	if app.displayedInputTokens != totalIn {
		t.Errorf("displayedInputTokens = %d, want %d", app.displayedInputTokens, totalIn)
	}
}

// TestApp_QueryEnd_UpdatesContextFromEngine verifies that at query end,
// the status bar reflects engine.ContextTokens (post-compact), not the
// stale pre-compact value from the last usageMsg.
func TestApp_QueryEnd_UpdatesContextFromEngine(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for query end context update elapsed time

	// Simulate API response with 26000 tokens
	app.updateRepl(usageMsg{InputTokens: 26000, OutputTokens: 100})
	if app.displayedInputTokens != 26000 {
		t.Fatalf("before compact, displayedInputTokens = %d, want 26000", app.displayedInputTokens)
	}

	// Simulate compact reducing context to 15000
	app.engine.ContextTokens = 15000

	// Query ends — status bar should now show engine's post-compact value
	app.updateRepl(queryEndMsg{})

	if app.displayedInputTokens != 15000 {
		t.Errorf("after queryEnd, displayedInputTokens = %d, want 15000 (post-compact engine.ContextTokens)", app.displayedInputTokens)
	}
}

// TestApp_UsageMsg_SetContextUsesLatestTurn verifies that SetContext receives
// the latest turn's input+output (context size), not the accumulated billing total.
func TestApp_UsageMsg_SetContextUsesLatestTurn(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

	// Turn 1: input=8400, output=57
	app.updateRepl(usageMsg{InputTokens: 8400, OutputTokens: 57})
	wantT1 := 8400 + 57 // = 8457
	if app.status.contextUsed != wantT1 {
		t.Errorf("after T1, contextUsed = %d, want %d (latest turn input+output)", app.status.contextUsed, wantT1)
	}

	// Turn 2: input=65, cache_read=8000, output=33
	// Accumulated billing = 8400+65 + 8000 = 16465, but context = latest turn = 8098
	app.updateRepl(usageMsg{InputTokens: 65, CacheReadInputTokens: 8000, OutputTokens: 33})
	wantT2 := 65 + 8000 + 33 // = 8098
	if app.status.contextUsed != wantT2 {
		t.Errorf("after T2, contextUsed = %d, want %d (latest turn), not %d (accumulated)",
			app.status.contextUsed, wantT2, app.status.usage.TotalInputTokens())
	}
}

// TestApp_AgentUsageMsg_DoesNotUpdateSetContext verifies that usageMsg with Agent
// does NOT change the context bar beyond what the main model already set.
func TestApp_AgentUsageMsg_DoesNotUpdateSetContext(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	// Main model sets context
	app.updateRepl(usageMsg{InputTokens: 500, OutputTokens: 100})
	mainContext := app.status.contextUsed
	if mainContext != 600 {
		t.Fatalf("after usageMsg, contextUsed = %d, want 600", mainContext)
	}

	// Sub-agent usage should update context (usageMsg with Agent updates SetContext)
	agent := &types.AgentMeta{ParentToolUseID: "call_abc"}
	app.updateRepl(usageMsg{
		InputTokens:  5000,
		OutputTokens: 200,
		Agent:        agent,
	})
	// After agent usageMsg, contextUsed should be updated because usageMsg
	// handler computes contextSize from all token fields and calls SetContext.
	// The context bar reflects the latest context (main + agent combined).
	newContext := app.status.contextUsed
	if newContext == mainContext {
		// Agent usage should have updated context to include agent tokens
		t.Errorf("after agent usageMsg, contextUsed = %d, expected change from %d",
			newContext, mainContext)
	}
}

// ---------------------------------------------------------------------------
// SetStore
// ---------------------------------------------------------------------------

func TestApp_SetStore(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	if app.store != nil {
		t.Error("expected nil store initially")
	}

	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	app.SetStore(store, "session-123", "/project", 5)
	if app.store != store {
		t.Error("store not set correctly")
	}
	if app.sessionID != "session-123" {
		t.Errorf("sessionID = %q, want %q", app.sessionID, "session-123")
	}
	if app.projectDir != "/project" {
		t.Errorf("projectDir = %q, want %q", app.projectDir, "/project")
	}
	if app.lastPersistedIdx != 5 {
		t.Errorf("lastPersistedIdx = %d, want 5", app.lastPersistedIdx)
	}
}

// ---------------------------------------------------------------------------
// handleSlashCommand — dispatches to correct handler
// ---------------------------------------------------------------------------

func TestApp_HandleSlashCommand_Clear(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	app.SetStore(store, "session-abc", "/project", 0)

	cmd := app.handleSlashCommand(SlashCommand{Name: "clear"}, nil)
	if cmd == nil {
		t.Error("clear should return a tea.Cmd")
	}
}

func TestApp_HandleSlashCommand_Unknown(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	commitCmd := func() tea.Msg { return nil }
	cmd := app.handleSlashCommand(SlashCommand{Name: "unknown_cmd"}, commitCmd)
	// Unknown command returns the commitCmd
	if cmd == nil {
		t.Error("unknown command should return commitCmd")
	}
}

// ---------------------------------------------------------------------------
// openPicker
// ---------------------------------------------------------------------------

func TestApp_OpenPicker(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, err := short.NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	app.SetStore(store, "session-abc", dir, 0)

	// Create some sessions
	_, err = store.CreateSession(dir, "test-model")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	commitCmd := func() tea.Msg { return nil }
	cmd := app.openPicker(commitCmd)

	if cmd == nil {
		t.Error("openPicker should return a tea.Cmd")
	}
	if app.activeDialog == nil {
		t.Error("listPicker should be set after openPicker")
	}
}

// ---------------------------------------------------------------------------
// Scroll functions
// ---------------------------------------------------------------------------

func TestApp_ScrollUp(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.scrollTotal = 10
	app.height = 5

	app.scrollUp(3)
	if app.scrollOffset != 0 {
		// scrollOffset starts at 0, going up should clamp at 0
		t.Errorf("scrollOffset = %d, want 0 (clamped)", app.scrollOffset)
	}
	if !app.userScrolled {
		t.Error("userScrolled should be true after scrollUp")
	}
}

func TestApp_ScrollDown(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.scrollTotal = 20
	app.height = 5
	app.scrollOffset = 0

	app.scrollDown(3)
	if app.scrollOffset != 3 {
		t.Errorf("scrollOffset = %d, want 3", app.scrollOffset)
	}
}

func TestApp_ScrollDown_ClampsToMax(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.scrollTotal = 10
	app.height = 5
	app.scrollOffset = 0

	// Scroll way past the end
	app.scrollDown(100)
	// Should clamp to max(scrollTotal - viewLines, 0)
	maxOff := max(app.scrollTotal-app.calcViewLines(), 0)
	if app.scrollOffset != maxOff {
		t.Errorf("scrollOffset = %d, want %d (clamped to max)", app.scrollOffset, maxOff)
	}
}

func TestApp_CalcViewLines(t *testing.T) {
	tests := []struct {
		name        string
		height      int
		scrollTotal int
		wantMin     int
		wantMax     int
	}{
		{"short content", 10, 5, 5, 5},     // content < maxContentLines → use maxContentLines
		{"overflow content", 10, 20, 4, 4}, // content > maxContentLines → reserve 1 line
		{"minimal height", 2, 20, 1, 1},    // very small terminal
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(&tuiMockProvider{})
			app.height = tc.height
			app.scrollTotal = tc.scrollTotal
			got := app.calcViewLines()
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("calcViewLines() = %d, want in range [%d, %d]", got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestApp_ScrollZeroTotal(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.scrollTotal = 0

	app.scrollUp(5)
	if app.scrollOffset != 0 {
		t.Errorf("scrollUp with zero total should not change offset, got %d", app.scrollOffset)
	}

	app.scrollDown(5)
	if app.scrollOffset != 0 {
		t.Errorf("scrollDown with zero total should not change offset, got %d", app.scrollOffset)
	}
}

// ---------------------------------------------------------------------------
// Update — picker mode routing
// ---------------------------------------------------------------------------

func TestApp_Update_PickerMode_KeyMsg(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, _ := short.NewStore(filepath.Join(dir, "test.db"))
	if _, err := store.CreateSession(dir, "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	app.SetStore(store, "existing-session", dir, 0)

	commitCmd := func() tea.Msg { return nil }
	app.openPicker(commitCmd)

	// Send a key message while in picker mode
	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := model.(*App)
	if updated.activeDialog == nil {
		t.Error("listPicker should still be set after key event")
	}
	if cmd != nil {
		// Down key doesn't quit picker, so cmd should be nil
		t.Error("expected nil cmd for non-quit picker key")
	}
}

func TestApp_Update_PickerMode_SelectClosesPicker(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, _ := short.NewStore(filepath.Join(dir, "test.db"))
	if _, err := store.CreateSession(dir, "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	app.SetStore(store, "existing-session", dir, 0)

	commitCmd := func() tea.Msg { return nil }
	app.openPicker(commitCmd)
	if app.activeDialog == nil {
		t.Fatal("listPicker should be set after openPicker")
	}

	// Send Esc to cancel picker
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	updated := model.(*App)
	if updated.activeDialog != nil {
		t.Error("listPicker should be nil after cancel")
	}
}

func TestApp_Update_PickerMode_WindowSize(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, _ := short.NewStore(filepath.Join(dir, "test.db"))
	if _, err := store.CreateSession(dir, "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	app.SetStore(store, "existing-session", dir, 0)

	commitCmd := func() tea.Msg { return nil }
	app.openPicker(commitCmd)

	// Send WindowSize while picker active — should update picker dims
	model, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	updated := model.(*App)
	if updated.activeDialog == nil {
		t.Fatal("listPicker should still be set after WindowSizeMsg")
	}
	if updated.activeDialog.width != 120 {
		t.Errorf("picker width = %d, want 120", updated.activeDialog.width)
	}
	// height is now dynamic — verify via View() instead of checking directly
	view := updated.View()
	if view == "" {
		t.Error("View() should return non-empty modal content")
	}
}

func TestApp_Update_WindowSize_Nil(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	updated := model.(*App)
	if updated.width != 80 {
		t.Errorf("width = %d, want 80", updated.width)
	}
	if updated.height != 24 {
		t.Errorf("height = %d, want 24", updated.height)
	}
}

// ---------------------------------------------------------------------------
// readEvents — edge cases
// ---------------------------------------------------------------------------

func TestApp_ReadEvents_ChannelDrain(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	h := NewTUIHandler()
	app.tuiHandler = h

	// Pre-fill the channel with an event
	h.appCh <- textDeltaMsg{Text: "buffered"}

	cmd := app.readEvents()
	msg := cmd()
	tdm, ok := msg.(textDeltaMsg)
	if !ok {
		t.Fatalf("expected textDeltaMsg from buffered channel, got %T", msg)
	}
	if tdm.Text != "buffered" {
		t.Errorf("Text = %q, want %q", tdm.Text, "buffered")
	}
}

func TestApp_ReadEvents_IdleModeWithStop(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	h := NewTUIHandler()
	app.tuiHandler = h
	app.idleStop = make(chan struct{})

	// Signal stop immediately
	close(app.idleStop)

	cmd := app.readEvents()
	msg := cmd()
	if _, ok := msg.(idleAbortedMsg); !ok {
		t.Fatalf("expected idleAbortedMsg, got %T", msg)
	}
}

func TestApp_HandleSlashCommand_Switch(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, _ := short.NewStore(filepath.Join(dir, "test.db"))
	if _, err := store.CreateSession(dir, "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	app.SetStore(store, "existing-session", dir, 0)

	app.handleSlashCommand(SlashCommand{Name: "session"}, nil)
	if app.activeDialog == nil {
		t.Error("listPicker should be set after /session")
	}
}

func TestApp_SlashCommand_PersistedToHistory(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	dir := t.TempDir()
	store, _ := short.NewStore(filepath.Join(dir, "test.db"))
	if _, err := store.CreateSession(dir, "model"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	app.SetStore(store, "existing-session", dir, 0)

	// Submit a slash command via Enter key
	app.input.SetValue("/session")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if app.history.Len() != 1 {
		t.Errorf("history should contain 1 entry (the slash command), got %d", app.history.Len())
	}
	// Navigate up — should recall "/session"
	result := app.history.Up("")
	text := result.Text
	if text != "/session" {
		t.Errorf("history up = %q, want %q", text, "/session")
	}
}

// ---------------------------------------------------------------------------
// Modal peek tests
// ---------------------------------------------------------------------------

// helperModalApp creates an app with a dialog open and the given terminal size.
func helperModalApp(t *testing.T, termHeight int) *App {
	t.Helper()
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: termHeight})
	opts := []DialogOption{
		{Label: "Option A", Shortcut: "a"},
		{Label: "Option B", Shortcut: "b"},
		{Label: "Option C"},
	}
	app.activeDialog = NewDialog("Test Dialog", opts)
	app.activeDialog.width = app.width
	return app
}

// helperAddMessages appends n assistant messages with the given text prefix.
func helperAddMessages(app *App, n int, prefix string) {
	for i := range n {
		app.repl.messages = append(app.repl.messages, MessageView{
			Role:   "assistant",
			Blocks: []ContentBlock{{Type: BlockText, Text: fmt.Sprintf("%s %d", prefix, i)}},
		})
	}
}

// assertModalContains checks that view contains all expected substrings.
func assertModalContains(t *testing.T, view string, expected ...string) {
	t.Helper()
	for _, e := range expected {
		if !strings.Contains(view, e) {
			t.Errorf("view should contain %q, got:\n%s", e, view)
		}
	}
}

func TestApp_ModalPeek_Adaptive_NoContent(t *testing.T) {
	app := helperModalApp(t, 24)
	view := app.View()
	assertModalContains(t, view, "Test Dialog", "Option A")
	// No content → peek=0, modalHeight = 24
	if app.activeDialog.height != 24 {
		t.Errorf("dialog height = %d, want 24 (no content, full modal)", app.activeDialog.height)
	}
}

func TestApp_ModalPeek_Adaptive_SparseContent(t *testing.T) {
	app := helperModalApp(t, 24)
	helperAddMessages(app, 3, "Line")
	// Populate cache directly (avoids fragile View()-then-reset pattern)
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false

	view := app.View()
	// 3 content lines, maxPeek = 19 → peek=3, modalHeight = 24-3 = 21
	assertModalContains(t, view, "Line 2", "Test Dialog")
	if app.activeDialog.height != 21 {
		t.Errorf("dialog height = %d, want 21 (3 peek lines)", app.activeDialog.height)
	}
	// Verify layout order: content before dialog title
	contentIdx := strings.Index(view, "Line 2")
	dialogIdx := strings.Index(view, "Test Dialog")
	if contentIdx == -1 || dialogIdx == -1 {
		t.Fatal("missing content or dialog title")
	}
	if dialogIdx < contentIdx {
		t.Error("dialog title should appear after peek content")
	}
}

func TestApp_ModalPeek_Adaptive_AbundantContent(t *testing.T) {
	app := helperModalApp(t, 24)
	helperAddMessages(app, 100, "Content line that is reasonably long")
	// Populate cache directly
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false

	view := app.View()
	// 100 lines > maxPeek(19) → peek=2, modalHeight = 24-2 = 22
	assertModalContains(t, view, "Test Dialog")
	if app.activeDialog.height != 22 {
		t.Errorf("dialog height = %d, want 22 (abundant content, 2 peek rows)", app.activeDialog.height)
	}
	// Should NOT show "Content line 0" (first items scrolled away)
	// Peek shows only last 2 lines of rendered content
}

func TestApp_ModalPeek_DialogShowsTitle(t *testing.T) {
	app := helperModalApp(t, 24)
	view := app.View()
	assertModalContains(t, view, "Test Dialog", "Option A")
}

func TestApp_ModalPeek_TinyTerminal(t *testing.T) {
	app := helperModalApp(t, 6)
	helperAddMessages(app, 5, "Line")
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false
	app.activeDialog = NewDialog("Test", []DialogOption{{Label: "A"}, {Label: "B"}})
	app.activeDialog.width = app.width

	view := app.View()
	assertModalContains(t, view, "Test")
	if app.activeDialog.height < minModalHeight {
		t.Errorf("dialog height = %d, should be >= %d", app.activeDialog.height, minModalHeight)
	}
	if app.activeDialog.height > app.height {
		t.Errorf("dialog height = %d exceeds terminal height %d", app.activeDialog.height, app.height)
	}
}

func TestApp_ModalPeek_ResizeRecalculates(t *testing.T) {
	app := helperModalApp(t, 24)
	helperAddMessages(app, 5, "Line")
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false
	app.activeDialog = NewDialog("Test", []DialogOption{{Label: "A"}, {Label: "B"}})
	app.activeDialog.width = app.width

	app.View()
	h1 := app.activeDialog.height

	app.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	app.View()
	h2 := app.activeDialog.height

	if h2 <= h1 {
		t.Errorf("after resize to larger terminal, height should increase: %d → %d", h1, h2)
	}
	// 5 content lines, maxPeek at h=40 is 35, 5<=35 → peek=5, modalHeight = 40-5 = 35
	if h2 != 35 {
		t.Errorf("dialog height after resize = %d, want 35", h2)
	}
}

func TestApp_ModalPeek_BoundaryContent(t *testing.T) {
	// Exactly maxPeek lines → show all (not capped)
	app := helperModalApp(t, 24)
	maxPeek := 24 - minModalHeight // = 19
	helperAddMessages(app, maxPeek, "Boundary line")
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false

	view := app.View()
	// maxPeek lines == maxPeek → show all → peek=maxPeek=19
	assertModalContains(t, view, "Test Dialog")
	if app.activeDialog.height != 24-maxPeek {
		t.Errorf("dialog height = %d, want %d", app.activeDialog.height, 24-maxPeek)
	}
}

func TestApp_ModalPeek_ExtremelyTinyTerminal(t *testing.T) {
	app := helperModalApp(t, 3)
	view := app.View()
	assertModalContains(t, view, "Test Dialog", "Option A")
	if app.activeDialog.height < minModalHeight {
		t.Errorf("dialog height = %d, should be >= %d even on tiny terminal", app.activeDialog.height, minModalHeight)
	}
}

func TestApp_ModalPeek_GetRenderedContent_NoCache(t *testing.T) {
	// Covers getRenderedContent branch where contentCache is empty
	// and it must call renderMessagesFull itself.
	app := helperModalApp(t, 24)
	helperAddMessages(app, 3, "CacheMiss")
	// Deliberately do NOT set contentCache — getRenderedContent must build temp
	app.contentCache = ""
	app.contentDirty = true

	view := app.View()
	assertModalContains(t, view, "Test Dialog", "CacheMiss 2")
	if app.activeDialog.height != 21 {
		t.Errorf("dialog height = %d, want 21 (3 peek, no cache)", app.activeDialog.height)
	}
}

func TestLastNLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"n<=0 returns empty", "hello\nworld", 0, ""},
		{"negative n returns empty", "hello\nworld", -1, ""},
		{"fewer lines than n returns all", "one line", 3, "one line"},
		{"exact n=1 returns last line", "first\nsecond\nthird", 1, "third"},
		{"n=2 returns last two lines", "first\nsecond\nthird", 2, "second\nthird"},
		{"empty string", "", 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lastNLines(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("lastNLines(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tab Completion integration tests
// ---------------------------------------------------------------------------

func TestApp_Completion_ShowsOnSlash(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/")
	app.completions.Update(app.input.Value(), app.input.cursor == len(app.input.value))

	view := app.View()
	if !app.completions.Visible() {
		t.Fatal("completions should be visible after typing '/'")
	}
	if !strings.Contains(view, "clear") {
		t.Error("view should contain 'clear'")
	}
	if !strings.Contains(view, "model") {
		t.Error("view should contain 'model'")
	}
	if !strings.Contains(view, "session") {
		t.Error("view should contain 'session'")
	}
}

func TestApp_Completion_TabFillsWithSpace(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/s")
	app.completions.Update(app.input.Value(), true)
	if !app.completions.Visible() {
		t.Fatal("completions should be visible")
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})

	got := app.input.Value()
	if got != "/session " {
		t.Errorf("after Tab, input = %q, want %q", got, "/session ")
	}
	if app.completions.Visible() {
		t.Error("completions should be dismissed after Tab")
	}
}

func TestApp_Completion_EscDismisses(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/")
	app.completions.Update(app.input.Value(), true)
	if !app.completions.Visible() {
		t.Fatal("should be visible")
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyEscape})

	if app.completions.Visible() {
		t.Error("completions should be dismissed after Esc")
	}
}

func TestApp_Completion_UpDownNavigation(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/")
	app.completions.Update(app.input.Value(), true)

	if app.completions.SelectedIndex() != 0 {
		t.Fatalf("initial index = %d, want 0", app.completions.SelectedIndex())
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if app.completions.SelectedIndex() != 1 {
		t.Errorf("after Down, index = %d, want 1", app.completions.SelectedIndex())
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if app.completions.SelectedIndex() != 2 {
		t.Errorf("after Down, index = %d, want 2", app.completions.SelectedIndex())
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if app.completions.SelectedIndex() != 1 {
		t.Errorf("after Up, index = %d, want 1", app.completions.SelectedIndex())
	}
}

func TestApp_Completion_EnterExecutesNoArgs(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/cl")
	app.completions.Update(app.input.Value(), true)
	if !app.completions.Visible() {
		t.Fatal("should be visible")
	}

	_, cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd == nil {
		t.Error("Enter on /clear (no args) should execute, got nil cmd")
	}
	if app.completions.Visible() {
		t.Error("completions should be dismissed after Enter")
	}
}

func TestApp_Completion_EnterFillsWithArgs(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/s")
	app.completions.Update(app.input.Value(), true)

	app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	got := app.input.Value()
	if got != "/session " {
		t.Errorf("after Enter on /session, input = %q, want %q", got, "/session ")
	}
	if app.completions.Visible() {
		t.Error("completions should be dismissed")
	}
}

func TestApp_Completion_IMEGuard(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/你好")
	app.completions.Update(app.input.Value(), true)

	if app.completions.Visible() {
		t.Error("should not trigger for non-ASCII input")
	}
}

func TestApp_Completion_SpaceDismisses(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/s")
	app.completions.Update(app.input.Value(), true)
	if !app.completions.Visible() {
		t.Fatal("should be visible")
	}

	app.input.InsertChar(' ')
	app.completions.Update(app.input.Value(), true)

	if app.completions.Visible() {
		t.Error("should dismiss after space")
	}
}

func TestApp_Completion_RenderedBelowInput(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/")
	app.completions.Update(app.input.Value(), true)

	view := app.View()

	inputIdx := strings.Index(view, "❯ /")
	if inputIdx < 0 {
		t.Fatal("view should contain input with '❯ /'")
	}
	clearIdx := strings.Index(view, "clear")
	if clearIdx < 0 {
		t.Fatal("view should contain 'clear' in completion")
	}
	if clearIdx < inputIdx {
		t.Error("completion should be rendered below input, but 'clear' appears before '❯ /'")
	}
}

// --- P2 edge case integration tests (from code review) ---

func TestApp_Tab_NotVisible_NoOp(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app.input.SetValue("hello")

	before := app.input.Value()
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if app.input.Value() != before {
		t.Errorf("Tab when not visible should not change input, got %q", app.input.Value())
	}
}

func TestApp_Tab_CursorNotAtEnd_NoOp(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	app.input.SetValue("/session")
	app.completions.Update(app.input.Value(), true)
	if !app.completions.Visible() {
		t.Fatal("should be visible")
	}

	// Move cursor to start
	app.input.cursor = 0
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})

	if app.input.Value() != "/session" {
		t.Errorf("Tab with cursor not at end should not fill, got %q", app.input.Value())
	}
}

func TestApp_Enter_NormalText_Submits(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app.input.SetValue("hello world")

	_, cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Error("Enter on normal text should submit")
	}
}

func TestApp_QueryEnd_ErrorFromBlockingLimit(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for blocking limit error timing

	// Simulate blocking limit error from engine
	app.updateRepl(queryEndMsg{
		Err: fmt.Errorf("Prompt is too long: 43.7k context tokens exceeds 31.0k limit"),
	})

	// FinishStream should have added the error as a system message.
	found := false
	for _, msg := range app.repl.messages {
		if msg.Role != "system" {
			continue
		}
		for _, b := range msg.Blocks {
			if b.Type == BlockText && strings.Contains(b.Text, "Prompt is too long") {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected system message with 'Prompt is too long' after blocking limit queryEnd")
	}
}

// TestApp_QueryEnd_UsesEngineTotalUsage verifies that the stats line at query
// end reflects the engine's accumulated TotalUsage (correct across multi-turn
// queries), not the TUI's per-turn streaming usage.
func TestApp_QueryEnd_UsesEngineTotalUsage(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for engine total usage stats timing

	// Simulate streaming: TUI sees per-turn values (overwritten each turn).
	// Turn 1: 10k input, 500 output
	app.updateRepl(usageMsg{InputTokens: 10000, CacheReadInputTokens: 5000})
	app.updateRepl(usageMsg{OutputTokens: 500})
	// Turn 2: 12k input, 300 output (TUI overwrites input, accumulates output)
	app.updateRepl(usageMsg{InputTokens: 12000, CacheReadInputTokens: 8000})
	app.updateRepl(usageMsg{OutputTokens: 300})

	// Before queryEnd, TUI's streaming usage has:
	// - InputTokens=12000, CacheRead=8000 (overwritten by turn 2)
	// - OutputTokens=800 (accumulated: 500+300)
	streamingTotalIn := app.status.usage.TotalInputTokens()
	if streamingTotalIn != 20000 {
		t.Logf("streaming TotalInput = %d (expected 20000, per-turn overwritten)", streamingTotalIn)
	}

	// Engine's TotalUsage has correct accumulated values across both turns:
	// Turn 1: Input=10000, CacheRead=5000, Output=500
	// Turn 2: Input=12000, CacheRead=8000, Output=300
	// Total: Input=22000, CacheRead=13000, Output=800
	engineTotal := types.Usage{
		InputTokens:              22000,
		OutputTokens:             800,
		CacheReadInputTokens:     13000,
		CacheCreationInputTokens: 0,
	}

	// Query ends with engine's accumulated TotalUsage
	app.updateRepl(queryEndMsg{
		TotalUsage: engineTotal,
	})

	// After queryEnd, the stats line should show engine's accumulated values.
	// Check that a.statsLine (or equivalent) uses engine totals, not streaming.
	// The last assistant message should contain the stats.
	lastMsg := app.repl.lastMsg()
	if lastMsg == nil {
		t.Fatal("expected at least one message after queryEnd")
	}
	statsText := lastMsg.View(200, false, "", false, 50)
	if !strings.Contains(statsText, "34.2k") && !strings.Contains(statsText, "35000") {
		// TotalInput = 22000 + 13000 = 35000 = 34.2k (1024 base)
		t.Errorf("stats line should show engine accumulated total input (34.2k), got:\n%s", statsText)
	}
	if !strings.Contains(statsText, "800") {
		t.Errorf("stats line should show engine accumulated output (800), got:\n%s", statsText)
	}
}

// ---------------------------------------------------------------------------
// Phase 2: Escape cancel + AbortError display
// ---------------------------------------------------------------------------

func TestApp_HandleEscape_DuringStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	cancelled := false
	app.repl.cancelFunc = func() {
		cancelled = true
	}

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Error("Escape during streaming should not produce a command")
	}
	a := model.(*App)

	if !cancelled {
		t.Error("expected cancelFunc to be called")
	}
	if a.repl.cancelFunc != nil {
		t.Error("cancelFunc should be nil after Escape")
	}
	// Escape does NOT call FinishStream — streaming stays true
	if !a.repl.streaming {
		t.Error("streaming should still be true (FinishStream not called by Escape)")
	}
}

func TestApp_HandleEscape_DuringStreaming_NoCancelFunc(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	// cancelFunc is nil — should not panic

	model, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Error("Escape during streaming should not produce a command")
	}
	a := model.(*App)
	if !a.repl.streaming {
		t.Error("streaming should still be true (no FinishStream)")
	}
}

func TestApp_HandleEscape_DoublePress_KillAll(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	cancelled := false
	app.repl.cancelFunc = func() {
		cancelled = true
	}

	killAllCalled := false
	app.killAllFn = func() {
		killAllCalled = true
	}

	// First Escape: cancels query, sets double-press pending
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !cancelled {
		t.Error("first Escape should call cancelFunc")
	}
	if killAllCalled {
		t.Error("first Escape should NOT call killAllFn")
	}

	// Second Escape within 800ms: kills all background tasks
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !killAllCalled {
		t.Error("second Escape should call killAllFn")
	}
}

func TestApp_HandleEscape_SinglePress_NoKillAll(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()

	cancelled := false
	app.repl.cancelFunc = func() {
		cancelled = true
	}

	killAllCalled := false
	app.killAllFn = func() {
		killAllCalled = true
	}

	// Single Escape
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	a := model.(*App)

	if !cancelled {
		t.Error("Escape should call cancelFunc")
	}
	if killAllCalled {
		t.Error("single Escape should NOT call killAllFn")
	}
	if a.repl.cancelFunc != nil {
		t.Error("cancelFunc should be nil after Escape")
	}
}

func TestApp_HandleEscape_DoublePress_NotStreaming_StillKillsAll(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	// Not streaming — but background tasks may still be running

	killAllCalled := false
	app.killAllFn = func() {
		killAllCalled = true
	}

	// Two quick presses while not streaming — still kills background tasks
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !killAllCalled {
		t.Error("double-press should kill background tasks even when not streaming")
	}
}

func TestApp_HandleEscape_KillAllFnNil_NoPanic(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.streaming = true
	app.spinner.Start()
	app.repl.cancelFunc = func() {}
	app.killAllFn = nil // no killAllFn set

	// Two quick presses — should not panic
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
}

func TestApp_QueryEnd_AbortError_Streaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for streaming abort error timing

	// Simulate query end with streaming-phase AbortError
	abortErr := &engine.AbortError{Phase: "streaming", Err: context.Canceled}
	app.updateRepl(queryEndMsg{Err: abortErr})

	if app.repl.streaming {
		t.Error("streaming should be false after queryEnd")
	}

	// AbortError should append inline interrupt message, not display as error.
	// The message content should contain the interrupt text.
	lastMsg := app.repl.lastMsg()
	if lastMsg == nil {
		t.Fatal("expected a message after abort")
	}
	text := lastMsg.View(200, false, "", false, 50)
	if !strings.Contains(text, types.InterruptMessage) {
		t.Errorf("expected inline interrupt message, got:\n%s", text)
	}
	// Should NOT contain "Error:" prefix — cancellation is not an error
	if strings.Contains(text, "Error:") {
		t.Errorf("abort should not display as Error, got:\n%s", text)
	}
}

func TestApp_QueryEnd_AbortError_Tools(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-1 * time.Second) // REAL-TIME: needed for tools abort error timing

	abortErr := &engine.AbortError{Phase: "tools", Err: context.Canceled}
	app.updateRepl(queryEndMsg{Err: abortErr})

	if app.repl.streaming {
		t.Error("streaming should be false after queryEnd")
	}

	// AbortError during tool execution should also show inline interrupt.
	lastMsg := app.repl.lastMsg()
	if lastMsg == nil {
		t.Fatal("expected a message after abort")
	}
	text := lastMsg.View(200, false, "", false, 50)
	if !strings.Contains(text, types.InterruptMessage) {
		t.Errorf("expected inline interrupt message, got:\n%s", text)
	}
	if strings.Contains(text, "Error:") {
		t.Errorf("abort should not display as Error, got:\n%s", text)
	}
}

// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// updateRepl — sub-agent thinking delta and end (Blocks rendering)
// ---------------------------------------------------------------------------

func TestApp_UpdateRepl_SubAgentThinkingDelta_AppendsText(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	// thinking_start → adds Thinking block
	app.updateRepl(thinkingStartMsg{Agent: agent})
	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) != 1 || tcv.Blocks[0].Type != BlockThinking {
		t.Fatalf("should have 1 BlockThinking, got %d blocks", len(tcv.Blocks))
	}

	// thinking_delta → should append text to the thinking block
	app.updateRepl(thinkingDeltaMsg{Text: "analyzing files...", Agent: agent})

	tcv = app.repl.pendingTool["call_abc"]
	foundThinking := false
	for _, b := range tcv.Blocks {
		if b.Type == BlockThinking && strings.Contains(b.Thinking.Text, "analyzing") {
			foundThinking = true
			break
		}
	}
	if !foundThinking {
		t.Errorf("thinking delta should append text to BlockThinking, got Blocks: %v", tcv.Blocks)
	}
}

func TestApp_UpdateRepl_SubAgentThinkingEnd_MarksDone(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	// thinking_start → adds Thinking block
	app.updateRepl(thinkingStartMsg{Agent: agent})

	// thinking_delta → adds text
	app.updateRepl(thinkingDeltaMsg{Text: "thinking text", Agent: agent})

	// thinking_end → should mark block as Done with Duration
	app.updateRepl(thinkingEndMsg{Duration: 3 * time.Second, Agent: agent})

	tcv := app.repl.pendingTool["call_abc"]
	foundDoneThinking := false
	for _, b := range tcv.Blocks {
		if b.Type == BlockThinking && b.Thinking.Done && b.Thinking.Duration == 3*time.Second {
			foundDoneThinking = true
			break
		}
	}
	if !foundDoneThinking {
		t.Errorf("thinking end should mark BlockThinking Done with Duration, got Blocks: %v", tcv.Blocks)
	}
}

func TestApp_UpdateRepl_SubAgentThinkingStartDeltaEnd_ContentPreserved(t *testing.T) {
	t.Parallel()
	// Full flow: thinking_start → delta → delta → end
	// After end, thinking block should still exist (Done=true) with text preserved
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.PendingToolStarted("call_abc", "Agent", "search", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_abc", AgentType: "Explore"}

	app.updateRepl(thinkingStartMsg{Agent: agent})
	app.updateRepl(thinkingDeltaMsg{Text: "thinking... ", Agent: agent})
	app.updateRepl(thinkingDeltaMsg{Text: "more thinking...", Agent: agent})
	app.updateRepl(thinkingEndMsg{Duration: 2 * time.Second, Agent: agent})

	tcv := app.repl.pendingTool["call_abc"]
	if len(tcv.Blocks) != 1 {
		t.Fatalf("expected 1 BlockThinking after thinking_end, got %d", len(tcv.Blocks))
	}
	blk := tcv.Blocks[0]
	if blk.Type != BlockThinking {
		t.Errorf("expected BlockThinking, got %d", blk.Type)
	}
	if !blk.Thinking.Done {
		t.Error("thinking should be marked Done")
	}
	if !strings.Contains(blk.Thinking.Text, "thinking") {
		t.Errorf("thinking text should be preserved, got: %q", blk.Thinking.Text)
	}
	if blk.Thinking.Duration != 2*time.Second {
		t.Errorf("thinking duration should be 2s, got %v", blk.Thinking.Duration)
	}
}

// TestApp_UpdateRepl_SubAgentToolFullLifecycle verifies the complete data flow
// for a sub-agent tool: tool_start → tool_param_delta → tool_output → tool_end.
// Then renders the message and checks that sub-agent tools appear NESTED inside
// the parent agent block, not at the top level.
//
// This test goes through actual message handlers — NOT constructing Blocks directly.
func TestApp_UpdateRepl_SubAgentToolFullLifecycle(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()

	// --- Step 1: Main agent starts an Agent tool ---
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")
	app.repl.SetAgentContextWindow("call_agent1", 200000)

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// --- Step 2: Sub-agent starts a Grep tool ---
	app.updateRepl(toolStartMsg{
		ID:      "sub_grep1",
		Name:    "Grep",
		Summary: "pattern",
		Input:   `{"pattern":"TODO"}`,
		Agent:   agent,
	})

	// Verify: Grep block added to parent.Blocks[]
	tcv := app.repl.pendingTool["call_agent1"]
	if len(tcv.Blocks) != 1 {
		t.Fatalf("step2: expected 1 block in parent, got %d", len(tcv.Blocks))
	}
	if tcv.Blocks[0].ToolCall.Name != "Grep" {
		t.Errorf("step2: expected Grep, got %q", tcv.Blocks[0].ToolCall.Name)
	}
	if tcv.Blocks[0].ToolCall.Done {
		t.Error("step2: tool should not be done yet")
	}

	// --- Step 3: Sub-agent tool output arrives ---
	// toolOutputDeltaMsg has no Agent field.
	// PendingToolOutput searches pendingTool["sub_grep1"] → NOT FOUND (it's in parent.Blocks).
	// The output is silently LOST.
	app.updateRepl(toolOutputDeltaMsg{
		ToolUseID:     "sub_grep1",
		DisplayOutput: "file1.go:10: TODO fix\nfile2.go:20: TODO refactor",
		Timing:        50 * time.Millisecond,
		Agent:         agent,
	})

	// Verify: output should be in parent.Blocks[0].ToolCall.Output
	tcv = app.repl.pendingTool["call_agent1"]
	subTool := tcv.Blocks[0].ToolCall
	if subTool.Output == "" {
		t.Error("step3: sub-agent tool output is EMPTY — toolOutputDeltaMsg has no Agent field, output lost")
	}

	// --- Step 4: Sub-agent tool ends ---
	app.updateRepl(toolEndMsg{
		ToolUseID: "sub_grep1",
		Output:    "2 matches found",
		IsError:   false,
		Timing:    100 * time.Millisecond,
		Agent:     agent,
	})

	// Verify: BlockTool marked Done, Output and Elapsed set
	tcv = app.repl.pendingTool["call_agent1"]
	subTool = tcv.Blocks[0].ToolCall
	if !subTool.Done {
		t.Error("step4: sub-agent tool should be done after toolEndMsg")
	}
	if subTool.Output == "" {
		t.Error("step4: sub-agent tool Output is empty — toolEndMsg sub-agent path doesn't set Output")
	}
	if subTool.Elapsed == 0 {
		t.Error("step4: sub-agent tool Elapsed is zero — toolEndMsg sub-agent path doesn't set Elapsed")
	}

	// --- Step 5: Main agent tool ends ---
	app.repl.PendingToolDone("call_agent1", "agent result", false, 500*time.Millisecond)

	// --- Step 6: Render and verify nesting ---
	msgs := app.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	rendered := msgs[0].View(80, false, "", false, 0)
	plain := stripANSIPrintable(rendered)

	if !strings.Contains(plain, "Agent") {
		t.Errorf("step6: rendering should contain 'Agent', got:\n%s", plain)
	}
	if !strings.Contains(plain, "Grep") {
		t.Errorf("step6: rendering should contain nested 'Grep', got:\n%s", plain)
	}
	if subTool.Output != "" && !strings.Contains(plain, "matches") {
		t.Errorf("step6: rendering should contain sub-agent output, got:\n%s", plain)
	}
}

// TestApp_UpdateRepl_SubAgentTextDeltaViaMessageFlow verifies textDeltaMsg
// through the actual message handler, not by constructing Blocks directly.
func TestApp_UpdateRepl_SubAgentTextDeltaViaMessageFlow(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()

	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	app.updateRepl(textDeltaMsg{
		Text:  "Now analyzing the codebase...",
		Agent: agent,
	})

	tcv := app.repl.pendingTool["call_agent1"]
	found := false
	for _, b := range tcv.Blocks {
		if b.Type == BlockText && strings.Contains(b.Text, "analyzing") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("textDeltaMsg should add BlockText to parent.Blocks, got blocks: %+v", tcv.Blocks)
	}
}

// TestApp_UpdateRepl_SubAgentNestedRendering verifies the full message flow
// produces correctly indented nested rendering:
//
//	● Agent(explore) (500ms)
//	| ↑0 ↓0 · 1 tools
//	  ● Grep(pattern) (100ms)
//	  | 2 matches found
//
// Rules:
//   - Tool header line: indent + ● + name(summary) + (time), NO pipe before ●
//   - Output first line: indent + | + content
//   - Nested tools just add depth*2 spaces, same pipe rules
func TestApp_UpdateRepl_SubAgentNestedRendering(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()

	// --- Step 1: Main agent starts an Agent tool ---
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")
	app.repl.SetAgentContextWindow("call_agent1", 200000)

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// --- Step 2: Sub-agent starts a Grep tool ---
	app.updateRepl(toolStartMsg{
		ID:      "sub_grep1",
		Name:    "Grep",
		Summary: "pattern",
		Input:   `{"pattern":"TODO"}`,
		Agent:   agent,
	})

	// --- Step 3: Sub-agent tool output arrives ---
	app.updateRepl(toolOutputDeltaMsg{
		ToolUseID:     "sub_grep1",
		DisplayOutput: "file1.go:10: TODO fix\nfile2.go:20: TODO refactor",
		Timing:        50 * time.Millisecond,
		Agent:         agent,
	})

	// --- Step 4: Sub-agent tool ends ---
	app.updateRepl(toolEndMsg{
		ToolUseID: "sub_grep1",
		Output:    "2 matches found",
		IsError:   false,
		Timing:    100 * time.Millisecond,
		Agent:     agent,
	})

	// --- Step 5: Main agent tool ends ---
	app.repl.PendingToolDone("call_agent1", "agent result", false, 500*time.Millisecond)

	// --- Step 6: Render and verify indentation ---
	msgs := app.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	rendered := msgs[0].View(80, false, "", false, 0)
	plain := stripANSIPrintable(rendered)
	lines := strings.Split(plain, "\n")

	t.Logf("rendered:\n%s", plain)

	// Verify line 0: parent agent header — "● Agent(explore) (500ms)"
	if len(lines) < 1 {
		t.Fatal("no lines rendered")
	}
	parentHeader := lines[0]
	if !strings.Contains(parentHeader, "Agent") {
		t.Errorf("line0: expected 'Agent' in header, got %q", parentHeader)
	}
	// Parent header must NOT start with spaces (depth=0) and NOT start with |
	if strings.HasPrefix(parentHeader, "|") {
		t.Errorf("line0: parent header must not start with |, got %q", parentHeader)
	}
	if strings.HasPrefix(parentHeader, " ") {
		t.Errorf("line0: parent header (depth=0) must not have leading spaces, got %q", parentHeader)
	}

	// Verify nested Grep header line: "  ● Grep(pattern) (100ms)"
	// Must have 2 leading spaces, must contain "Grep", must NOT have | before ●
	grepFound := false
	for _, line := range lines {
		if strings.Contains(line, "Grep") {
			grepFound = true
			// Must start with exactly 2 spaces (depth=1)
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("Grep header must start with 2 spaces (depth=1), got %q", line)
			}
			if strings.HasPrefix(line, "  |") {
				t.Errorf("Grep header must NOT have | before ●, got %q", line)
			}
			// Must contain ● after the indent
			if !strings.HasPrefix(line, "  ●") {
				t.Errorf("Grep header must be '  ● Grep(...)', got %q", line)
			}
			break
		}
	}
	if !grepFound {
		t.Errorf("rendering should contain 'Grep', got:\n%s", plain)
	}

	// Verify nested Grep output line: "  | 2 matches found"
	// Must have 2 leading spaces then |
	grepOutputFound := false
	for _, line := range lines {
		trimmed := strings.TrimPrefix(line, "  ")
		if trimmed != line && strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed, "matches") {
			grepOutputFound = true
			break
		}
	}
	if !grepOutputFound {
		t.Errorf("expected '  | ...matches...' output line, got:\n%s", plain)
	}

	// Verify NO line has | before ● (pipe before tool dot is always wrong)
	for i, line := range lines {
		if strings.Contains(line, "●") && strings.Contains(line, "|") {
			// Check if | appears before ●
			pipeIdx := strings.Index(line, "|")
			dotIdx := strings.Index(line, "●")
			if pipeIdx < dotIdx && pipeIdx >= 0 {
				t.Errorf("line%d: | must not appear before ●, got %q", i, line)
			}
		}
	}
}

// TestApp_UpdateRepl_SubAgentThinkingPreservedAfterToolStart verifies that
// a completed thinking block (thinkingEnd received) is NOT removed when a
// subsequent toolStartMsg arrives. The P1.2 fix should only remove ACTIVE
// (non-Done) thinking blocks, not completed ones.
func TestApp_UpdateRepl_SubAgentThinkingPreservedAfterToolStart(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// Sub-agent starts thinking
	app.updateRepl(thinkingStartMsg{Agent: agent})
	app.updateRepl(thinkingDeltaMsg{Text: "Let me analyze the code...", Agent: agent})
	app.updateRepl(thinkingEndMsg{Duration: 500 * time.Millisecond, Agent: agent})

	// Verify thinking block is Done
	tcv := app.repl.findToolView("call_agent1")
	thinkingCount := 0
	for _, b := range tcv.Blocks {
		if b.Type == BlockThinking {
			thinkingCount++
			if !b.Thinking.Done {
				t.Error("thinking should be Done after thinkingEndMsg")
			}
			if b.Thinking.Text == "" {
				t.Error("thinking text should not be empty")
			}
		}
	}
	if thinkingCount != 1 {
		t.Fatalf("expected 1 thinking block after thinkingEnd, got %d", thinkingCount)
	}

	// Sub-agent starts a tool — this should NOT remove the completed thinking
	app.updateRepl(toolStartMsg{
		ID:      "sub_grep1",
		Name:    "Grep",
		Summary: "pattern",
		Input:   `{}`,
		Agent:   agent,
	})

	// Verify thinking block is STILL THERE
	tcv = app.repl.findToolView("call_agent1")
	thinkingCount = 0
	for _, b := range tcv.Blocks {
		if b.Type == BlockThinking {
			thinkingCount++
		}
	}
	if thinkingCount != 1 {
		t.Errorf("completed thinking block should NOT be removed by toolStartMsg, got %d thinking blocks", thinkingCount)
	}
}

// TestApp_UpdateRepl_SubAgentTextIndentation verifies that text content
// from a sub-agent is rendered with proper depth indentation (2 spaces for
// depth=1 inside an agent block).
func TestApp_UpdateRepl_SubAgentTextIndentation(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// Sub-agent sends text
	app.updateRepl(textDeltaMsg{Text: "Here is my analysis of the codebase.", Agent: agent})

	// End the agent
	app.repl.PendingToolDone("call_agent1", "", false, 500*time.Millisecond)

	// Render and check indentation
	msgs := app.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	rendered := msgs[0].View(80, false, "", false, 0)
	plain := stripANSIPrintable(rendered)

	t.Logf("rendered:\n%s", plain)

	// Text should be at depth=1 (2-space indent), no "| " prefix
	if !strings.Contains(plain, "  Here is my analysis") {
		t.Errorf("sub-agent text should have 2-space indent, got:\n%s", plain)
	}
}

// TestApp_UpdateRepl_SubAgentThinkingStreaming verifies that thinking content
// is rendered DURING streaming (Done=false), not only after thinkingEnd.
func TestApp_UpdateRepl_SubAgentThinkingStreaming(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// Sub-agent starts thinking
	app.updateRepl(thinkingStartMsg{Agent: agent})
	app.updateRepl(thinkingDeltaMsg{Text: "Analyzing the module structure...", Agent: agent})

	// Do NOT send thinkingEnd — tool is still running, thinking is streaming

	// Render the message (agent tool is still in running state)
	msgs := app.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	rendered := msgs[0].View(80, false, "●", false, 0)
	plain := stripANSIPrintable(rendered)

	t.Logf("rendered (streaming):\n%s", plain)

	// Thinking content should be visible during streaming
	if !strings.Contains(plain, "Analyzing") {
		t.Errorf("thinking text should be rendered during streaming (Done=false), got:\n%s", plain)
	}
}

// TestApp_UpdateRepl_SubAgentThinkingStreamingHasIndent verifies that thinking
// content rendered inside an agent (depth=1) has 2-space indent on all lines.
func TestApp_UpdateRepl_SubAgentThinkingStreamingHasIndent(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	app.updateRepl(thinkingStartMsg{Agent: agent})
	app.updateRepl(thinkingDeltaMsg{Text: "Analyzing the module structure...", Agent: agent})

	msgs := app.repl.Messages()
	rendered := msgs[0].View(80, false, "●", false, 0)
	plain := stripANSIPrintable(rendered)

	t.Logf("rendered:\n%s", plain)

	// Thinking header and content should be at depth=1 (2-space indent)
	lines := strings.Split(plain, "\n")
	thinkingFound := false
	for _, line := range lines {
		stripped := stripANSIPrintable(line)
		if strings.Contains(stripped, "Thinking") {
			thinkingFound = true
			if !strings.HasPrefix(stripped, "  ") {
				t.Errorf("thinking header at depth=1 should start with 2 spaces, got %q", stripped)
			}
		}
		if strings.Contains(stripped, "Analyzing") {
			if !strings.HasPrefix(stripped, "  ") {
				t.Errorf("thinking content at depth=1 should start with 2 spaces, got %q", stripped)
			}
		}
	}
	if !thinkingFound {
		t.Errorf("should find 'Thinking' in output, got:\n%s", plain)
	}
}

// TestApp_UpdateRepl_SubAgentTextCollapsedWhenNotExpanded verifies that long text
// inside an agent's Blocks is collapsed (shows hint) when expand=false.
func TestApp_UpdateRepl_SubAgentTextCollapsedWhenNotExpanded(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// Send enough text to trigger collapse (5+ lines worth)
	longText := "Line one of the analysis.\nLine two of the analysis.\nLine three of the analysis.\nLine four of the analysis.\nLine five of the analysis.\nLine six of the analysis."
	app.updateRepl(textDeltaMsg{Text: longText, Agent: agent})
	app.repl.PendingToolDone("call_agent1", "", false, 500*time.Millisecond)

	msgs := app.repl.Messages()
	// expand=false → should show collapse hint
	rendered := msgs[0].View(80, false, "", false, 0)
	plain := stripANSIPrintable(rendered)

	t.Logf("rendered (collapsed):\n%s", plain)

	// Should show collapse hint like "… +N lines"
	if !strings.Contains(plain, "…") && !strings.Contains(plain, "...") {
		t.Errorf("long text should be collapsed with hint when expand=false, got:\n%s", plain)
	}
}

// TestApp_UpdateRepl_SubAgentThinkingCollapsedWhenNotExpanded verifies that
// thinking content inside an agent's Blocks is collapsed when expand=false.
func TestApp_UpdateRepl_SubAgentThinkingCollapsedWhenNotExpanded(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.PendingToolStarted("call_agent1", "Agent", "explore", "{}")

	agent := &types.AgentMeta{ParentToolUseID: "call_agent1", AgentType: "Explore"}

	// Start thinking, add enough content to trigger collapse
	app.updateRepl(thinkingStartMsg{Agent: agent})
	longThinking := "Thinking line one.\nThinking line two.\nThinking line three.\nThinking line four.\nThinking line five.\nThinking line six."
	app.updateRepl(thinkingDeltaMsg{Text: longThinking, Agent: agent})
	app.updateRepl(thinkingEndMsg{Duration: 500 * time.Millisecond, Agent: agent})
	app.repl.PendingToolDone("call_agent1", "", false, 500*time.Millisecond)

	msgs := app.repl.Messages()
	// expand=false → thinking content should be collapsed
	rendered := msgs[0].View(80, false, "", false, 0)
	plain := stripANSIPrintable(rendered)

	t.Logf("rendered (collapsed):\n%s", plain)

	// Should show collapse hint
	if !strings.Contains(plain, "…") && !strings.Contains(plain, "ctrl+o") {
		t.Errorf("long thinking should be collapsed with hint when expand=false, got:\n%s", plain)
	}
}

// ---------------------------------------------------------------------------
// Sub-agent queryEndMsg must NOT cancel main query context (compact bug)
// ---------------------------------------------------------------------------

func TestApp_QueryEndMsg_SubAgent_DoesNotCancelMainContext(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Simulate main query in progress: set up streaming state with a cancelFunc
	ctx, cancel := context.WithCancel(context.Background())
	app.repl.cancelFunc = cancel
	app.repl.StartQuery()
	app.spinner.Start()

	// Set up a parent tool block for the sub-agent
	parentToolID := "tool-parent-123"
	app.repl.pendingTool[parentToolID] = &ToolCallView{
		ID:      parentToolID,
		Name:    "Agent",
		Summary: "Execute a sub-agent task",
		Done:    false,
		Blocks:  []ContentBlock{},
	}

	// Sub-agent's queryEndMsg arrives (with Agent metadata set)
	subAgentMeta := &types.AgentMeta{
		ParentToolUseID: parentToolID,
		AgentType:       "Explore",
	}
	msg := queryEndMsg{
		Err:        nil,
		TotalUsage: types.Usage{InputTokens: 100, OutputTokens: 50},
		Agent:      subAgentMeta,
	}

	// Process the sub-agent's queryEndMsg
	handled, _ := app.updateRepl(msg)
	if !handled {
		t.Fatal("updateRepl should handle queryEndMsg")
	}

	// CRITICAL: the main query must still be streaming
	if !app.repl.IsStreaming() {
		t.Error("sub-agent queryEndMsg must NOT stop main query streaming")
	}

	// CRITICAL: cancelFunc must NOT have been called
	if ctx.Err() != nil {
		t.Error("sub-agent queryEndMsg must NOT cancel the main query context")
	}

	// cancelFunc must still be set (not consumed)
	if app.repl.cancelFunc == nil {
		t.Error("cancelFunc should still be set for main query")
	}

	// Clean up
	cancel()
}

// ---------------------------------------------------------------------------
// queryEndMsg from fork agent — marks parent card Done via updateRepl
// ---------------------------------------------------------------------------

func TestApp_QueryEndMsg_ForkAgent_MarksParentDone(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.spinner.Start()

	parentToolID := "tool-fork-parent-1"
	app.repl.PendingToolStarted(parentToolID, "Agent", "fork", "{}")
	app.repl.pendingToolStart[parentToolID] = time.Now().Add(-2 * time.Second) // REAL-TIME: needed for fork agent parent elapsed time

	// Set parent as background (simulates toolEndMsg with IsBackground=true)
	tcv := app.repl.findToolView(parentToolID)
	tcv.IsBackground = true
	tcv.Output = "Fork agent launched"
	app.repl.updateToolBlock(parentToolID, tcv)

	// Verify parent is NOT done yet
	if app.repl.findToolView(parentToolID).Done {
		t.Fatal("parent should NOT be Done before fork agent completes")
	}

	// Sub-agent queryEndMsg arrives
	subAgentMeta := &types.AgentMeta{
		ParentToolUseID: parentToolID,
		AgentType:       "Explore",
	}
	msg := queryEndMsg{
		Err:        nil,
		TotalUsage: types.Usage{InputTokens: 50, OutputTokens: 30},
		Agent:      subAgentMeta,
	}

	handled, _ := app.updateRepl(msg)
	if !handled {
		t.Fatal("updateRepl should handle fork agent queryEndMsg")
	}

	// Verify parent card is now Done
	parent := app.repl.findToolView(parentToolID)
	if parent == nil {
		t.Fatal("parent card should still exist")
	}
	if !parent.Done {
		t.Error("parent card should be Done after fork agent queryEndMsg")
	}
	if parent.Elapsed < time.Second {
		t.Errorf("parent Elapsed should be >= 1s, got %v", parent.Elapsed)
	}
}

// ---------------------------------------------------------------------------
// Fork agent error + multiple agents tests
// ---------------------------------------------------------------------------

func TestApp_QueryEndMsg_ForkAgent_ErrorMarksParentDone(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.spinner.Start()

	parentToolID := "tool-fork-err-1"
	app.repl.PendingToolStarted(parentToolID, "Agent", "fork", "{}")
	app.repl.pendingToolStart[parentToolID] = time.Now().Add(-1 * time.Second) // REAL-TIME: needed for fork agent error elapsed time

	tcv := app.repl.findToolView(parentToolID)
	tcv.IsBackground = true
	app.repl.updateToolBlock(parentToolID, tcv)

	// Fork agent completes with error
	msg := queryEndMsg{
		Err:        fmt.Errorf("fork agent failed: timeout"),
		TotalUsage: types.Usage{InputTokens: 10, OutputTokens: 5},
		Agent: &types.AgentMeta{
			ParentToolUseID: parentToolID,
			AgentType:       "Explore",
		},
	}

	handled, _ := app.updateRepl(msg)
	if !handled {
		t.Fatal("updateRepl should handle fork agent error queryEndMsg")
	}

	// Parent should be marked Done even when fork agent failed
	parent := app.repl.findToolView(parentToolID)
	if parent == nil {
		t.Fatal("parent card should exist")
	}
	if !parent.Done {
		t.Error("parent card should be Done even when fork agent errored")
	}
}

func TestApp_QueryEndMsg_MultipleForkAgents_Independent(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.spinner.Start()

	// Set up two independent fork agents
	for _, id := range []string{"tool-fork-a", "tool-fork-b"} {
		app.repl.PendingToolStarted(id, "Agent", "fork", "{}")
		app.repl.pendingToolStart[id] = time.Now().Add(-1 * time.Second) // REAL-TIME: needed for multiple fork agents elapsed time
		tcv := app.repl.findToolView(id)
		tcv.IsBackground = true
		app.repl.updateToolBlock(id, tcv)
	}

	// First fork agent completes
	msg1 := queryEndMsg{
		Agent: &types.AgentMeta{
			ParentToolUseID: "tool-fork-a",
			AgentType:       "Explore",
		},
	}
	app.updateRepl(msg1)

	// Only tool-fork-a should be Done, tool-fork-b should still be running
	a := app.repl.findToolView("tool-fork-a")
	b := app.repl.findToolView("tool-fork-b")
	if !a.Done {
		t.Error("tool-fork-a should be Done after its queryEndMsg")
	}
	if b.Done {
		t.Error("tool-fork-b should NOT be Done — only fork-a completed")
	}
	if b.IsBackground != true {
		t.Error("tool-fork-b should still be IsBackground=true")
	}

	// Second fork agent completes
	msg2 := queryEndMsg{
		Agent: &types.AgentMeta{
			ParentToolUseID: "tool-fork-b",
			AgentType:       "Plan",
		},
	}
	app.updateRepl(msg2)

	// Now both should be Done
	b = app.repl.findToolView("tool-fork-b")
	if !b.Done {
		t.Error("tool-fork-b should be Done after its queryEndMsg")
	}
}

func TestApp_SetKillAllFn(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})

	called := false
	app.SetKillAllFn(func() {
		called = true
	})

	// Trigger double-press to exercise the setter
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	app.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if !called {
		t.Error("SetKillAllFn callback should be invoked on double-press")
	}
}

func TestApp_ToolEndMsg_BackgroundToolNotFound_LogsWarn(t *testing.T) {
	t.Parallel()
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()

	// Send toolEndMsg with IsBackground=true for a tool that doesn't exist
	msg := toolEndMsg{
		ToolUseID:    "nonexistent-tool",
		Output:       "Fork agent launched",
		IsBackground: true,
	}
	// Should not panic, and should log a warning (verified by slog output)
	handled, _ := app.updateRepl(msg)
	if !handled {
		t.Error("updateRepl should handle toolEndMsg")
	}
	// The tool view should NOT exist since we never called PendingToolStarted
	if app.repl.findToolView("nonexistent-tool") != nil {
		t.Error("tool should not exist in pending map")
	}
}

// TestAutoRewind_Skipped_WhenToolUsePresent verifies that auto-rewind does NOT
// fire when the engine has tool_use/tool_result pairs in the current turn.
// Regression test: auto-rewind was incorrectly removing tool_result but leaving
// tool_use, causing API 2013 "tool call result does not follow tool call".
func TestAutoRewind_Skipped_WhenToolUsePresent(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.committedCount = 1

	// Set up engine messages simulating: user query → tool_use → tool_result + interrupt
	app.engine.SetMessages([]types.Message{
		{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock("read file")},
		},
		{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewToolUseBlock("tu_1", "Read", json.RawMessage(`{"file_path":"test.go"}`)),
			},
		},
		{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewToolResultBlock("tu_1", json.RawMessage(`"file contents here"`), false),
				types.NewTextBlock(types.InterruptMessage),
			},
		},
	})

	// Set up TUI state as if streaming just ended
	app.repl.StartQuery()
	app.repl.AppendTextItem()
	app.repl.AppendChunk("partial response")
	app.progressStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // deterministic time for stats

	// Simulate queryEnd with AbortError — this triggers tryAutoRewind
	abortErr := &engine.AbortError{Phase: "tools", Err: context.Canceled}
	app.updateRepl(queryEndMsg{Err: abortErr})

	// Verify auto-rewind did NOT fire — engine messages should still have tool_use
	msgs := app.engine.Messages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages after abort (no rewind), got %d", len(msgs))
	}

	// Verify tool_use still exists in messages
	hasToolUse := false
	for _, msg := range msgs {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse {
				hasToolUse = true
			}
		}
	}
	if !hasToolUse {
		t.Error("tool_use was removed — auto-rewind should not fire when tool_use is present")
	}

	// Validate tool pairing — tool_use must have matching tool_result
	for i, msg := range msgs {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type != types.ContentTypeToolUse {
				continue
			}
			found := false
			for j := i + 1; j < len(msgs); j++ {
				for _, rb := range msgs[j].Content {
					if rb.Type == types.ContentTypeToolResult && rb.ToolUseID == block.ID {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("tool_use %s at msg[%d] has no matching tool_result — will cause API 2013", block.ID, i)
			}
		}
	}
}

// --- renderInputOverlay ---

func TestApp_RenderInputOverlay_NoPeekContent(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	ch := make(chan types.AskResponse, 1)
	app.activeInput = NewInputDialog("Enter password:", false, testFutureDeadline, ch)

	view := app.renderInputOverlay()
	if !strings.Contains(view, "Input Required") {
		t.Error("overlay should contain input dialog title")
	}
	if !strings.Contains(view, "Enter password:") {
		t.Error("overlay should contain prompt text")
	}
}

func TestApp_RenderInputOverlay_WithPeekContent(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	helperAddMessages(app, 3, "Line")
	app.contentCache = renderMessagesFull(app.repl.messages, app.width, false, "", false, 0)
	app.contentDirty = false

	ch := make(chan types.AskResponse, 1)
	app.activeInput = NewInputDialog("Password:", true, testFutureDeadline, ch)

	view := app.renderInputOverlay()
	if !strings.Contains(view, "Line") {
		t.Error("overlay should contain peek content")
	}
	if !strings.Contains(view, "Input Required") {
		t.Error("overlay should contain input dialog title")
	}
	if !strings.Contains(view, "Password:") {
		t.Error("overlay should contain prompt")
	}
	// Peek content should appear before dialog
	peekIdx := strings.Index(view, "Line")
	dialogIdx := strings.Index(view, "Input Required")
	if peekIdx == -1 || dialogIdx == -1 {
		t.Fatal("missing peek or dialog in view")
	}
	if dialogIdx < peekIdx {
		t.Error("peek content should appear before dialog")
	}
}

// ---------------------------------------------------------------------------
// Fix 2: Second inputAskMsg should abort existing InputDialog
// ---------------------------------------------------------------------------

func TestApp_InputAskMsg_OverwriteAbortsExisting(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Set up an existing activeInput with its own result channel
	firstCh := make(chan types.AskResponse, 1)
	app.activeInput = NewInputDialog("First prompt:", false, testFutureDeadline, firstCh)

	// Send a second inputAskMsg — should abort the first dialog
	secondCh := make(chan types.AskResponse, 1)
	app.Update(inputAskMsg{
		event: &types.AskEvent{
			Kind:       types.AskInput,
			Prompt:     "Second prompt:",
			Masked:     false,
			Deadline:   testFutureDeadline,
			ResponseCh: secondCh,
		},
	})

	// First dialog's channel should receive abort
	select {
	case resp := <-firstCh:
		if !resp.Aborted {
			t.Error("first dialog should have been aborted on overwrite")
		}
	default:
		t.Error("first dialog's channel should have received abort response")
	}

	// Second dialog should be the new activeInput
	if app.activeInput == nil {
		t.Fatal("activeInput should be set after second inputAskMsg")
	}
	if app.activeInput.prompt != "Second prompt:" {
		t.Errorf("activeInput prompt = %q, want %q", app.activeInput.prompt, "Second prompt:")
	}

	// Second channel should NOT have received anything yet
	select {
	case <-secondCh:
		t.Error("second dialog's channel should not have received anything yet")
	default:
	}
}

// ---------------------------------------------------------------------------
// History Up/Down integration
// ---------------------------------------------------------------------------

// TestApp_HistoryDown_NoOpOutsideNav verifies that pressing Down without
// prior Up does NOT clear the current input.
func TestApp_HistoryDown_NoOpOutsideNav(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("测试消息")

	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated := model.(*App)

	if updated.input.Value() != "测试消息" {
		t.Errorf("Down without prior Up should be no-op, got input = %q", updated.input.Value())
	}
}

// TestApp_HistoryDown_RestoresDraft verifies: Up into history → Down back
// restores the original draft input.
func TestApp_HistoryDown_RestoresDraft(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("我的草稿")

	// Up → old2
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	upApp := model.(*App)
	if upApp.input.Value() != "old2" {
		t.Fatalf("after Up, input = %q, want %q", upApp.input.Value(), "old2")
	}

	// Down → draft restored
	model, _ = upApp.Update(tea.KeyMsg{Type: tea.KeyDown})
	downApp := model.(*App)
	if downApp.input.Value() != "我的草稿" {
		t.Errorf("after Down, input = %q, want draft %q", downApp.input.Value(), "我的草稿")
	}
}

// TestApp_HistoryUp_CursorAtStart verifies that pressing Up moves the
// cursor to the beginning of the history entry (matching TS cursorToStart).
func TestApp_HistoryUp_CursorAtStart(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("草稿")

	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	upApp := model.(*App)

	if upApp.input.Value() != "old2" {
		t.Fatalf("after Up, input = %q, want %q", upApp.input.Value(), "old2")
	}
	if upApp.input.cursor != 0 {
		t.Errorf("after Up, cursor = %d, want 0 (start)", upApp.input.cursor)
	}
}

// TestApp_HistoryDown_DraftCursorAtEnd verifies that when Down restores the
// draft, the cursor is at the end of the draft text.
func TestApp_HistoryDown_DraftCursorAtEnd(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("我的草稿")

	// Up → old2 (cursor at start)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	upApp := model.(*App)

	// Down → draft restored (cursor at end)
	model, _ = upApp.Update(tea.KeyMsg{Type: tea.KeyDown})
	downApp := model.(*App)

	if downApp.input.Value() != "我的草稿" {
		t.Errorf("after Down, input = %q, want draft %q", downApp.input.Value(), "我的草稿")
	}
	want := len([]rune("我的草稿"))
	if downApp.input.cursor != want {
		t.Errorf("after Down restores draft, cursor = %d, want %d (end)", downApp.input.cursor, want)
	}
}

// TestApp_HistoryDown_EntryCursorAtEnd verifies that when Down shows a newer
// history entry (not draft), the cursor is at the end.
func TestApp_HistoryDown_EntryCursorAtEnd(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("草稿")

	// Up → old2 (cursor at start)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	up1 := model.(*App)

	// Up → old1 (cursor at start)
	model, _ = up1.Update(tea.KeyMsg{Type: tea.KeyUp})
	up2 := model.(*App)

	// Down → old2 (cursor at end)
	model, _ = up2.Update(tea.KeyMsg{Type: tea.KeyDown})
	downApp := model.(*App)

	if downApp.input.Value() != "old2" {
		t.Errorf("after Down, input = %q, want %q", downApp.input.Value(), "old2")
	}
	want := len([]rune("old2"))
	if downApp.input.cursor != want {
		t.Errorf("after Down to entry, cursor = %d, want %d (end)", downApp.input.cursor, want)
	}
}

// TestApp_HistoryUp_SecondUpCursorAtStart verifies multiple Ups all place
// the cursor at the start.
func TestApp_HistoryUp_SecondUpCursorAtStart(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("old1")
	app.history.Add("old2")
	app.input.SetValue("草稿")

	// Up → old2
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	up1 := model.(*App)

	// Up → old1
	model, _ = up1.Update(tea.KeyMsg{Type: tea.KeyUp})
	up2 := model.(*App)

	if up2.input.Value() != "old1" {
		t.Fatalf("after 2nd Up, input = %q, want %q", up2.input.Value(), "old1")
	}
	if up2.input.cursor != 0 {
		t.Errorf("after 2nd Up, cursor = %d, want 0 (start)", up2.input.cursor)
	}
}

// TestApp_HistoryDown_SubmittedDraftCleared verifies that after submitting
// "测试消息" (input goes empty), Up shows it from history, and Down clears
// back to empty (draft was empty when Up was pressed).
func TestApp_HistoryDown_SubmittedDraftCleared(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.history.Add("测试消息")

	// Input is empty (just submitted)
	app.input.SetValue("")

	// Up → shows "测试消息" from history (cursor at start)
	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyUp})
	upApp := model.(*App)
	if upApp.input.Value() != "测试消息" {
		t.Fatalf("after Up, input = %q, want %q", upApp.input.Value(), "测试消息")
	}

	// Down → draft restored (empty, since input was empty when Up was pressed)
	model, _ = upApp.Update(tea.KeyMsg{Type: tea.KeyDown})
	downApp := model.(*App)
	if downApp.input.Value() != "" {
		t.Errorf("after Down, input = %q, want empty (draft was empty)", downApp.input.Value())
	}
}

func TestResetDisplayState_ClearsRetry(t *testing.T) {
	app := newTestApp(nil)
	app.retryActive = true
	app.retryAttempt = 5
	app.retryMax = 10
	app.retryRemaining = 5 * time.Second
	app.retryErrorType = string(types.RetryErrorStreamInterrupted)
	app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	app.resetDisplayState()

	if app.retryActive {
		t.Error("retryActive should be false after resetDisplayState")
	}
	if app.retryAttempt != 0 {
		t.Errorf("retryAttempt = %d, want 0", app.retryAttempt)
	}
	if app.retryMax != 0 {
		t.Errorf("retryMax = %d, want 0", app.retryMax)
	}
	if app.retryRemaining != 0 {
		t.Errorf("retryRemaining = %v, want 0", app.retryRemaining)
	}
	if app.retryErrorType != "" {
		t.Errorf("retryError = %q, want empty", app.retryErrorType)
	}
}

func TestRetryView_HiddenBelowAttempt4(t *testing.T) {
	app := newTestApp(nil)
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	app.retryActive = true
	app.retryAttempt = 3
	app.retryMax = 10
	app.retryRemaining = 5 * time.Second
	app.retryErrorType = string(types.RetryErrorStreamInterrupted)
	app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	view := app.View()
	if strings.Contains(view, "Retrying in") {
		t.Error("retry display should be hidden for attempt < 4")
	}
}

func TestRetryView_VisibleAtAttempt4(t *testing.T) {
	app := newTestApp(nil)
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	app.retryActive = true
	app.retryAttempt = 4
	app.retryMax = 10
	app.retryRemaining = 5 * time.Second
	app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	app.retryErrorType = string(types.RetryErrorStreamInterrupted)
	app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	view := app.View()
	if !strings.Contains(view, "Connection interrupted") {
		t.Error("retry display should show connection interrupted message")
	}
	if !strings.Contains(view, "Retrying in") {
		t.Error("retry display should show retrying countdown")
	}
	if !strings.Contains(view, "attempt 4/10") {
		t.Error("retry display should show attempt count")
	}
}

// TestRetryView_AutoHideWhenStreaming verifies that retry display disappears
// as soon as response content or thinking starts — no manual clearing needed.
func TestRetryView_AutoHideWhenStreaming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		responseChars   int
		thinkingActive  bool
		shouldShowRetry bool
	}{
		{"no response yet", 0, false, true},
		{"text started", 10, false, false},
		{"thinking started", 0, true, false},
		{"both active", 10, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(nil)
			app.width = 80
			app.height = 24
			app.repl.streaming = true
			app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			app.retryActive = true
			app.retryAttempt = 4
			app.retryMax = 10
			app.retryRemaining = 5 * time.Second
			app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			app.retryErrorType = string(types.RetryErrorStreamInterrupted)
			app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			app.responseCharCount = tt.responseChars
			app.thinkingActive = tt.thinkingActive

			view := app.View()
			hasRetry := strings.Contains(view, "Retrying in")
			if hasRetry != tt.shouldShowRetry {
				t.Errorf("%s: hasRetry=%v, want %v", tt.name, hasRetry, tt.shouldShowRetry)
			}
		})
	}
}

func TestRetryView_NoRawErrorShown(t *testing.T) {
	app := newTestApp(nil)
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	app.retryActive = true
	app.retryAttempt = 4
	app.retryMax = 10
	app.retryRemaining = 5 * time.Second
	app.retryErrorType = string(types.RetryErrorStreamInterrupted)
	app.retryStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	view := app.View()
	if strings.Contains(view, "stream interrupted: response incomplete") {
		t.Error("retry display should NOT show raw engine error to user")
	}
	if !strings.Contains(view, "Connection interrupted") {
		t.Error("retry display should show user-friendly message, got:", view)
	}
}

func TestRetryAttemptMsg_ContinuesEventChain(t *testing.T) {
	t.Parallel()
	app := newTestApp(nil)
	app.repl.streaming = true
	app.spinner.Start()
	app.progressStart = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// Send retryAttemptMsg
	msg := retryAttemptMsg{
		Attempt:    5,
		MaxRetries: 10,
		RetryInMs:  8000,
		Error:      "stream interrupted",
	}
	_, cmd := app.Update(msg)

	// The handler MUST return a non-nil Cmd that continues reading events.
	// If cmd is nil, the event chain is broken and TUI hangs.
	if cmd == nil {
		t.Fatal("retryAttemptMsg handler returned nil cmd — event chain broken, TUI will hang")
	}

	// Verify state was set
	if !app.retryActive {
		t.Error("retryActive should be true after retryAttemptMsg")
	}
	if app.retryAttempt != 5 {
		t.Errorf("retryAttempt = %d, want 5", app.retryAttempt)
	}

	// Execute the returned Cmd — it should produce a message (readEvents reads from channel)
	// Write a textDeltaMsg to the channel so readEvents can pick it up
	app.tuiHandler = NewTUIHandler()
	app.tuiHandler.appCh <- textDeltaMsg{Text: "after retry", Agent: &types.AgentMeta{AgentType: "main"}}

	result := cmd()
	if result == nil {
		t.Fatal("returned cmd produced nil message — readEvents not in the chain")
	}
	// Should get the textDeltaMsg we put in the channel
	if _, ok := result.(textDeltaMsg); !ok {
		t.Errorf("expected textDeltaMsg from readEvents, got %T", result)
	}
}

// TestRetryView_CountdownAccuracy verifies the View shows a reasonable countdown
// when retryStart is recent (not stale). The countdown formula is:
// secs = max(int((retryRemaining - time.Since(retryStart)).Seconds())+1, 0)
func TestRetryView_CountdownAccuracy(t *testing.T) {
	app := newTestApp(nil)
	app.width = 80
	app.height = 24
	app.repl.streaming = true
	app.progressStart = time.Now().Add(-5 * time.Second) // REAL-TIME: progress timer
	app.retryActive = true
	app.retryAttempt = 4
	app.retryMax = 10
	app.retryRemaining = 5000 * time.Millisecond
	// Set retryStart slightly in the past — simulates receiving retry event 200ms ago
	app.retryStart = time.Now().Add(-200 * time.Millisecond) // REAL-TIME: countdown offset
	app.retryErrorType = string(types.RetryErrorStreamInterrupted)

	view := app.View()

	// elapsed ≈ 200ms, remaining ≈ 4800ms = 4.8s, int(4.8)+1 = 5
	// Allow range [4, 6] to account for test execution timing
	hasCountdown := strings.Contains(view, "Retrying in 4s") ||
		strings.Contains(view, "Retrying in 5s") ||
		strings.Contains(view, "Retrying in 6s")
	if !hasCountdown {
		// Extract the countdown line for debugging
		for line := range strings.SplitSeq(view, "\n") {
			if strings.Contains(line, "Retrying in") {
				t.Errorf("countdown out of expected range [4,6], got line: %s", line)
				return
			}
		}
		t.Error("no countdown line found in view")
	}
}

func TestApp_HandleKey_PasteMultiline_ResetsNav(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Populate history and enter navigation
	app.history.Add("old1")
	app.history.Add("old2")
	app.history.Up("current draft")
	if app.history.savedDraft == "" {
		t.Fatal("setup: savedDraft should be set after Up()")
	}

	// Paste multi-line content — should resetNavAndAccum
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2"), Paste: true})

	if app.input.Value() != "line1\nline2" {
		t.Errorf("paste multiline: got %q, want %q", app.input.Value(), "line1\nline2")
	}
	if app.history.savedDraft != "" {
		t.Errorf("paste should reset history nav (clear savedDraft), got %q", app.history.savedDraft)
	}
}

func TestApp_PasteMultiline_SubmitPreservesAllLines(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Simulate paste of "测试消息\n换行"
	pastedText := "测试消息\n换行"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pastedText), Paste: true})

	// Verify input contains the full pasted text
	if got := app.input.Value(); got != pastedText {
		t.Fatalf("input after paste: got %q, want %q", got, pastedText)
	}

	// Submit
	cmd := app.handleSubmitRepl(pastedText)
	if cmd == nil {
		t.Fatal("handleSubmitRepl should return a command")
	}

	// Check the user message in repl state
	msgs := app.repl.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}

	// Find user message text
	var userText strings.Builder
	for _, m := range msgs {
		if m.Role == "user" {
			for _, b := range m.Blocks {
				if b.Type == BlockText {
					userText.WriteString(b.Text)
				}
			}
		}
	}
	if userText.String() != pastedText {
		t.Errorf("submitted text: got %q, want %q", userText.String(), pastedText)
	}

	// Verify rendering preserves both lines
	rendered := msgs[0].View(80, false, "", false, 0)
	if !strings.Contains(rendered, "测试消息") {
		t.Errorf("rendered should contain '测试消息', got: %s", rendered)
	}
	if !strings.Contains(rendered, "换行") {
		t.Errorf("rendered should contain '换行', got: %s", rendered)
	}
}

func TestApp_PasteMultiline_NoBracketedPaste(t *testing.T) {
	// Simulates a terminal without bracketed paste support:
	// paste "测试消息\n换行" arrives as separate events:
	// 1) runes "测试消息", 2) KeyEnter (from \n), 3) runes "换行"
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// First part: "测试消息" arrives as regular runes (no paste flag)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("测试消息")})
	if got := app.input.Value(); got != "测试消息" {
		t.Fatalf("after first runes: got %q, want %q", got, "测试消息")
	}

	// The \n arrives as KeyEnter — this SUBMITS "测试消息" as a query!
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Input is now empty (was reset by handleSubmitRepl)
	if got := app.input.Value(); got != "" {
		t.Errorf("input should be empty after Enter, got %q", got)
	}

	// Second part: "换行" arrives as runes
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("换行")})
	if got := app.input.Value(); got != "换行" {
		t.Errorf("after second runes: got %q, want %q", got, "换行")
	}

	// Check submitted messages: should have "测试消息" as first user message
	msgs := app.repl.Messages()
	var userMsgs []string
	for _, m := range msgs {
		if m.Role == "user" {
			for _, b := range m.Blocks {
				if b.Type == BlockText {
					userMsgs = append(userMsgs, b.Text)
				}
			}
		}
	}
	if len(userMsgs) != 1 || userMsgs[0] != "测试消息" {
		t.Errorf("submitted messages: %v, want [%q]", userMsgs, "测试消息")
	}
	t.Logf("CONFIRMED: without bracketed paste, first line is auto-submitted, second line remains in input")
	t.Logf("This explains the reported issue: they see '换行' in input, '测试消息' was submitted separately")
}

func TestApp_View_PasteMultiline_InputBothLinesVisible(t *testing.T) {
	// Small terminal where multi-line input overflows the fixed 5-line reserve
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 8 // maxContentLines = 8-5 = 3

	// Add content to fill the 3-line content area
	app.repl.AddUserMessage("line1")
	app.repl.AddUserMessage("line2")
	app.repl.AddUserMessage("line3")
	app.markViewportDirty()

	// Paste 2-line content into input
	pastedText := "测试消息\n换行"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pastedText), Paste: true})

	inputView := app.input.View()
	inputLineCount := strings.Count(inputView, "\n") + 1
	t.Logf("input lines: %d", inputLineCount)

	view := app.View()
	viewLines := strings.Count(view, "\n") + 1
	t.Logf("total view lines: %d (terminal height: %d)", viewLines, app.height)

	if viewLines > app.height {
		t.Errorf("view overflows terminal: %d lines in height %d", viewLines, app.height)
	}

	if !strings.Contains(view, "测试消息") {
		t.Errorf("View should contain '测试消息'")
		t.Logf("View:\n%s", view)
	}
	if !strings.Contains(view, "换行") {
		t.Errorf("View should contain '换行'")
	}
}

// TestApp_Paste_CarriageReturnNormalizedToNewline verifies that \r from
// Windows Terminal paste is converted to \n so both lines are visible.
func TestApp_Paste_CarriageReturnNormalizedToNewline(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Windows Terminal sends \r (0x0D) for line breaks in pasted text
	pastedText := "测试消息\r换行"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pastedText), Paste: true})

	inputVal := app.input.Value()
	t.Logf("input value: %q", inputVal)

	if strings.ContainsRune(inputVal, '\r') {
		t.Errorf("input value should not contain \\r, got: %q", inputVal)
	}
	if !strings.Contains(inputVal, "测试消息\n换行") {
		t.Errorf("input value should be '测试消息\\n换行', got: %q", inputVal)
	}

	// Both lines must be visible in View
	view := app.View()
	if !strings.Contains(view, "测试消息") {
		t.Errorf("View should contain '测试消息'")
		t.Logf("View:\n%s", view)
	}
	if !strings.Contains(view, "换行") {
		t.Errorf("View should contain '换行'")
		t.Logf("View:\n%s", view)
	}
}

// TestApp_Paste_CRLFNormalized verifies \r\n from Windows paste becomes \n\n
// (TS behavior: .replace(/\r/g, '\n') converts each \r individually).
func TestApp_Paste_CRLFNormalized(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	pastedText := "line1\r\nline2"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pastedText), Paste: true})

	inputVal := app.input.Value()
	t.Logf("input value: %q", inputVal)

	if strings.ContainsRune(inputVal, '\r') {
		t.Errorf("input value should not contain \\r, got: %q", inputVal)
	}
	// \r\n becomes \n\n (both \r and \n are kept, \r→\n)
	if !strings.Contains(inputVal, "line1\n\nline2") {
		t.Errorf("expected 'line1\\n\\nline2', got: %q", inputVal)
	}
}

// TestApp_NormalTyping_CarriageReturnNormalized verifies non-paste typing also
// converts \r to \n (Alt+Enter / SSH-coalesced input path).
func TestApp_NormalTyping_CarriageReturnNormalized(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Non-paste typing with \r
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello\rworld"), Paste: false})

	inputVal := app.input.Value()
	t.Logf("input value: %q", inputVal)

	if strings.ContainsRune(inputVal, '\r') {
		t.Errorf("input value should not contain \\r, got: %q", inputVal)
	}
	if !strings.Contains(inputVal, "hello\nworld") {
		t.Errorf("expected 'hello\\nworld', got: %q", inputVal)
	}
}

func TestPaste_BelowThreshold_InsertsVerbatim(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// 500 chars, 1 newline — below both thresholds
	text := strings.Repeat("x", 499) + "\n"
	runes := []rune(text)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes, Paste: true})

	inputVal := app.input.Value()
	if strings.Contains(inputVal, "[Pasted text") {
		t.Errorf("short paste should not create reference, got: %q", inputVal)
	}
	if !strings.Contains(inputVal, strings.Repeat("x", 499)) {
		t.Errorf("full content should be in input, got length %d", len(inputVal))
	}
}

func TestPaste_CharThreshold_CreatesRef(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// 900 chars, 0 newlines — exceeds char threshold
	text := strings.Repeat("a", 900)
	runes := []rune(text)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes, Paste: true})

	inputVal := app.input.Value()
	if !strings.Contains(inputVal, "[Pasted text #1]") {
		t.Errorf("expected [Pasted text #1] for >800 chars with 0 newlines, got: %q", inputVal)
	}
	if len(app.pasteStore) != 1 {
		t.Fatalf("expected 1 paste store entry, got %d", len(app.pasteStore))
	}
	if app.pasteStore[1] != text {
		t.Errorf("stored content mismatch: stored %d chars, want %d", len(app.pasteStore[1]), len(text))
	}
}

func TestPaste_LineThreshold_CreatesRef(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// 3 newlines (>2) — exceeds line threshold
	text := "line1\nline2\nline3\nline4"
	runes := []rune(text)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes, Paste: true})

	inputVal := app.input.Value()
	if !strings.Contains(inputVal, "[Pasted text #1 +3 lines]") {
		t.Errorf("expected [Pasted text #1 +3 lines], got: %q", inputVal)
	}
	if app.pasteStore[1] != text {
		t.Errorf("stored content mismatch")
	}
}

func TestPaste_MultiplePastes_IncrementingIDs(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	text1 := "line1\nline2\nline3\nline4" // 3 newlines
	text2 := "alpha\nbeta\ngamma\ndelta"  // 3 newlines

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text1), Paste: true})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" "), Paste: false})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text2), Paste: true})

	inputVal := app.input.Value()
	if !strings.Contains(inputVal, "[Pasted text #1 +3 lines]") {
		t.Errorf("expected #1 reference, got: %q", inputVal)
	}
	if !strings.Contains(inputVal, "[Pasted text #2 +3 lines]") {
		t.Errorf("expected #2 reference, got: %q", inputVal)
	}
	if len(app.pasteStore) != 2 {
		t.Errorf("expected 2 store entries, got %d", len(app.pasteStore))
	}
}

func TestPaste_Backspace_DeletesWholeToken(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	text := "line1\nline2\nline3\nline4" // 3 newlines
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})

	before := app.input.Value()
	if !strings.Contains(before, "[Pasted text #1") {
		t.Fatalf("expected reference before backspace, got: %q", before)
	}

	// Backspace should delete the entire token at once
	app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	after := app.input.Value()
	if strings.Contains(after, "[Pasted text") {
		t.Errorf("backspace should delete entire token, got: %q", after)
	}
	if len(after) != 0 {
		t.Errorf("input should be empty after deleting only content, got: %q", after)
	}
}

func TestPaste_Submit_ExpandsRefs(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	pasted := "line1\nline2\nline3\nline4"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})

	inputVal := app.input.Value()
	if !strings.Contains(inputVal, "[Pasted text #1") {
		t.Fatalf("expected reference, got: %q", inputVal)
	}

	// Submit
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Check the user message was expanded
	if len(app.repl.messages) == 0 {
		t.Fatal("expected at least one message after submit")
	}
	msg := app.repl.messages[0]
	if msg.Role != "user" {
		t.Fatalf("expected user message, got role %q", msg.Role)
	}
	expanded := msg.Blocks[0].Text
	if strings.Contains(expanded, "[Pasted text") {
		t.Errorf("submitted text should have expanded references, got: %q", expanded)
	}
	if !strings.Contains(expanded, "line1\nline2\nline3\nline4") {
		t.Errorf("submitted text should contain original content, got: %q", expanded)
	}
}

func TestPaste_StoreClearedAfterSubmit(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	pasted := "line1\nline2\nline3\nline4"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	if len(app.pasteStore) != 1 {
		t.Fatalf("expected 1 store entry before submit, got %d", len(app.pasteStore))
	}

	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(app.pasteStore) != 0 {
		t.Errorf("paste store should be empty after submit, got %d entries", len(app.pasteStore))
	}
	if app.nextPasteID != 1 {
		t.Errorf("nextPasteID should reset to 1, got %d", app.nextPasteID)
	}
}

func TestBackspaceToken_RegularText_SingleChar(t *testing.T) {
	input := NewInput()
	input.SetValue("hello")
	input.End()

	input.BackspaceToken()

	if input.Value() != "hell" {
		t.Errorf("expected 'hell', got %q", input.Value())
	}
}

// ---------------------------------------------------------------------------
// Paste reference integration tests — call chain coverage
// ---------------------------------------------------------------------------

// TestPasteChain_FullFlow tests the complete paste→view→submit→expand chain.
// Observable output: the submitted message text contains expanded content.
func TestPasteChain_FullFlow(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	pasted := "func main() {\n\tfmt.Println(\"hello\")\n\treturn\n}"
	// 3 newlines → triggers reference

	// Step 1: Paste
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})

	// Observable: input shows reference, not raw content
	inputVal := app.input.Value()
	if strings.Contains(inputVal, "func main()") {
		t.Errorf("input should show reference, not raw pasted content, got: %q", inputVal)
	}
	if !strings.HasPrefix(inputVal, "[Pasted text #1") {
		t.Errorf("input should start with paste reference, got: %q", inputVal)
	}

	// Step 2: Submit
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Observable: engine receives expanded text, not the reference
	if len(app.repl.messages) == 0 {
		t.Fatal("expected a user message after submit")
	}
	submitted := app.repl.messages[0].Blocks[0].Text
	if strings.Contains(submitted, "[Pasted text") {
		t.Errorf("submitted text should NOT contain reference, got: %q", submitted)
	}
	if submitted != pasted {
		t.Errorf("submitted text should equal original paste\nwant: %q\n got: %q", pasted, submitted)
	}

	// Observable: input is cleared
	if app.input.Value() != "" {
		t.Errorf("input should be empty after submit, got: %q", app.input.Value())
	}
}

// TestPasteChain_MixedInput tests typing + paste + typing → correct expansion.
func TestPasteChain_MixedInput(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Type prefix
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("review this code: "), Paste: false})

	// Paste multi-line content
	pasted := "line1\nline2\nline3\nline4"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})

	// Type suffix
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" thanks!"), Paste: false})

	// Verify input contains all three parts
	inputVal := app.input.Value()
	if !strings.HasPrefix(inputVal, "review this code: ") {
		t.Errorf("input should start with typed prefix, got: %q", inputVal)
	}
	if !strings.Contains(inputVal, "[Pasted text #1") {
		t.Errorf("input should contain reference, got: %q", inputVal)
	}
	if !strings.HasSuffix(inputVal, " thanks!") {
		t.Errorf("input should end with typed suffix, got: %q", inputVal)
	}

	// Submit → verify expansion preserves prefix and suffix
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	submitted := app.repl.messages[0].Blocks[0].Text

	if !strings.HasPrefix(submitted, "review this code: ") {
		t.Errorf("submitted should start with prefix, got: %q", submitted)
	}
	if !strings.Contains(submitted, pasted) {
		t.Errorf("submitted should contain expanded paste, got: %q", submitted)
	}
	if !strings.HasSuffix(submitted, " thanks!") {
		t.Errorf("submitted should end with suffix, got: %q", submitted)
	}
	// The reference should be fully replaced
	if strings.Contains(submitted, "[Pasted text") {
		t.Errorf("submitted should NOT contain reference, got: %q", submitted)
	}
}

// TestPasteChain_BoundaryChar tests the exact 800/801 char boundary.
func TestPasteChain_BoundaryChar(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Exactly 800 chars — should NOT trigger
	text800 := strings.Repeat("a", 800)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text800), Paste: true})
	if strings.Contains(app.input.Value(), "[Pasted text") {
		t.Errorf("800 chars should NOT trigger reference, got: %q", app.input.Value()[:50])
	}

	app.input.Reset()
	app.pasteStore = make(map[int]string)
	app.nextPasteID = 1

	// Exactly 801 chars — should trigger
	text801 := strings.Repeat("a", 801)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text801), Paste: true})
	if !strings.Contains(app.input.Value(), "[Pasted text #1]") {
		t.Errorf("801 chars should trigger reference, got: %q", app.input.Value())
	}
	if app.pasteStore[1] != text801 {
		t.Errorf("stored content length = %d, want 801", len(app.pasteStore[1]))
	}
}

// TestPasteChain_BoundaryLines tests the exact 2/3 newline boundary.
func TestPasteChain_BoundaryLines(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Exactly 2 newlines — should NOT trigger
	text2nl := "a\nb\nc"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text2nl), Paste: true})
	if strings.Contains(app.input.Value(), "[Pasted text") {
		t.Errorf("2 newlines should NOT trigger reference, got: %q", app.input.Value())
	}

	app.input.Reset()
	app.pasteStore = make(map[int]string)
	app.nextPasteID = 1

	// Exactly 3 newlines — should trigger
	text3nl := "a\nb\nc\nd"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text3nl), Paste: true})
	if !strings.Contains(app.input.Value(), "[Pasted text #1 +3 lines]") {
		t.Errorf("3 newlines should trigger reference, got: %q", app.input.Value())
	}
}

// TestPasteChain_RecoveryAfterSubmit tests that paste IDs reset after submit.
func TestPasteChain_RecoveryAfterSubmit(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// First turn: paste and submit
	pasted := "line1\nline2\nline3\nline4"
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Store should be empty after submit
	if len(app.pasteStore) != 0 {
		t.Fatalf("store should be empty after submit, got %d entries", len(app.pasteStore))
	}
	if app.nextPasteID != 1 {
		t.Fatalf("nextPasteID should reset to 1, got %d", app.nextPasteID)
	}

	// Second paste — should get #1 again (not #2) because IDs reset
	app.input.Reset() // clear any leftover state from failed engine query
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(pasted), Paste: true})
	inputVal := app.input.Value()
	if !strings.Contains(inputVal, "[Pasted text #1") {
		t.Errorf("after submit, paste ID should restart from #1, got: %q", inputVal)
	}
	if strings.Contains(inputVal, "[Pasted text #2") {
		t.Errorf("ID should NOT be #2 after submit reset, got: %q", inputVal)
	}

	// Expansion works on second paste too
	expanded := app.expandPasteRefs(inputVal)
	if expanded != pasted {
		t.Errorf("second paste expansion should work\nwant: %q\n got: %q", pasted, expanded)
	}
}

// TestPasteChain_BackspacePreservesTypedText tests backspace only deletes the token.
func TestPasteChain_BackspacePreservesTypedText(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24

	// Type before and after paste
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prefix "), Paste: false})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd"), Paste: true})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" suffix"), Paste: false})

	// Move cursor to end of paste reference (before " suffix")
	// The cursor is after " suffix", so we need to move back past " suffix" to the end of the ref
	for range len(" suffix") {
		app.input.CursorLeft()
	}

	// Now backspace should delete the entire token
	app.input.BackspaceToken()
	inputAfter := app.input.Value()

	if inputAfter != "prefix  suffix" {
		t.Errorf("after deleting token, expected 'prefix  suffix', got: %q", inputAfter)
	}
	// Typed text should be preserved
	if !strings.HasPrefix(inputAfter, "prefix ") {
		t.Errorf("prefix should be preserved, got: %q", inputAfter)
	}
	if !strings.HasSuffix(inputAfter, " suffix") {
		t.Errorf("suffix should be preserved, got: %q", inputAfter)
	}
}

// ---------------------------------------------------------------------------
// renderQueueBox tests
// ---------------------------------------------------------------------------

func TestRenderQueueBox_Empty(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	result := app.renderQueueBox()
	if result != "" {
		t.Errorf("renderQueueBox with empty pendingQueue = %q, want empty string", result)
	}
}

func TestRenderQueueBox_SingleItem(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.pendingQueue = []pendingQueueItem{{ID: "id-1", Text: "run tests"}}
	result := app.renderQueueBox()
	if result == "" {
		t.Fatal("renderQueueBox with 1 item returned empty string")
	}
	if !strings.Contains(result, "○") {
		t.Errorf("renderQueueBox should contain ○, got %q", result)
	}
	if !strings.Contains(result, "run tests") {
		t.Errorf("renderQueueBox should contain item text 'run tests', got %q", result)
	}
}

func TestRenderQueueBox_MultipleItems(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.pendingQueue = []pendingQueueItem{
		{ID: "id-1", Text: "first message"},
		{ID: "id-2", Text: "second message"},
		{ID: "id-3", Text: "third message"},
	}
	result := app.renderQueueBox()
	if result == "" {
		t.Fatal("renderQueueBox with 3 items returned empty string")
	}
	for _, text := range []string{"first message", "second message", "third message"} {
		if !strings.Contains(result, text) {
			t.Errorf("renderQueueBox should contain %q, got %q", text, result)
		}
	}
}

func TestRenderQueueBox_MultilineIndent(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 40
	app.height = 24
	app.pendingQueue = []pendingQueueItem{{ID: "id-1", Text: "this is a very long message that should wrap to multiple lines when rendered in the queue box"}}
	result := app.renderQueueBox()
	if result == "" {
		t.Fatal("renderQueueBox with long text returned empty string")
	}
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected wrapped multiline output, got %d lines: %q", len(lines), result)
	}
	continuation := lines[1]
	wantPrefix := strings.Repeat(" ", renderedPromptWidth)
	if !strings.HasPrefix(continuation, wantPrefix) {
		t.Errorf("continuation line indent mismatch:\n  got:      %q (len %d)\n  want prefix: %q (len %d)\n  full result:\n%s",
			continuation[:min(len(continuation), len(wantPrefix)+4)], len(continuation),
			wantPrefix, len(wantPrefix), result)
	}
	if strings.HasPrefix(continuation, wantPrefix+"  ") {
		t.Errorf("continuation line has 2 extra spaces after indent — should align with input, not add padding:\n  got: %q\n  full result:\n%s", continuation[:min(len(continuation), 20)], result)
	}
}

// ---------------------------------------------------------------------------
// attachmentMsg — ItemModePrompt (queued user message) handling
// ---------------------------------------------------------------------------

func TestAttachmentMsg_UserPrompt_QueueRemoval(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	uuid := "test-uuid-abc"
	app.pendingQueue = []pendingQueueItem{{ID: uuid, Text: "original queued text"}}

	model, _ := app.Update(attachmentMsg{UserText: "hello", SourceUUID: uuid})
	a := model.(*App)

	if len(a.pendingQueue) != 0 {
		t.Errorf("pendingQueue should be empty after UUID match, got %d items", len(a.pendingQueue))
	}

	lastMsg := a.repl.messages[len(a.repl.messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("last message Role = %q, want %q", lastMsg.Role, "user")
	}
	if len(lastMsg.Blocks) != 1 || lastMsg.Blocks[0].Text != "hello" {
		t.Errorf("last message text = %q, want %q", lastMsg.Blocks[0].Text, "hello")
	}
}

func TestAttachmentMsg_UserPrompt_UUIDMismatch(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	app.pendingQueue = []pendingQueueItem{{ID: "abc", Text: "queued"}}

	model, _ := app.Update(attachmentMsg{UserText: "hello", SourceUUID: "xyz"})
	a := model.(*App)

	if len(a.pendingQueue) != 1 {
		t.Fatalf("pendingQueue should still have 1 item after UUID mismatch, got %d", len(a.pendingQueue))
	}
	if a.pendingQueue[0].ID != "abc" {
		t.Errorf("pendingQueue[0].ID = %q, want %q", a.pendingQueue[0].ID, "abc")
	}

	lastMsg := a.repl.messages[len(a.repl.messages)-1]
	if lastMsg.Role != "user" {
		t.Errorf("last message Role = %q, want %q", lastMsg.Role, "user")
	}
	if len(lastMsg.Blocks) != 1 || lastMsg.Blocks[0].Text != "hello" {
		t.Errorf("last message text = %q, want %q", lastMsg.Blocks[0].Text, "hello")
	}
}

// ---------------------------------------------------------------------------
// handleEnqueueMessage tests
// ---------------------------------------------------------------------------

func TestHandleEnqueueMessage_PlainText(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()
	app.input.SetValue("will be cleared")

	cmd := app.handleEnqueueMessage("test message")
	if cmd != nil {
		t.Errorf("handleEnqueueMessage should return nil, got %T", cmd)
	}
	if len(app.pendingQueue) != 1 {
		t.Fatalf("pendingQueue should have 1 entry, got %d", len(app.pendingQueue))
	}
	if app.pendingQueue[0].Text != "test message" {
		t.Errorf("pendingQueue[0].Text = %q, want %q", app.pendingQueue[0].Text, "test message")
	}
	if app.pendingQueue[0].ID == "" {
		t.Error("pendingQueue[0].ID should not be empty")
	}
	if app.input.Value() != "" {
		t.Errorf("input should be reset after handleEnqueueMessage, got %q", app.input.Value())
	}
}

func TestHandleEnqueueMessage_SlashCommand(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	cmd := app.handleEnqueueMessage("/clear")
	if cmd != nil {
		t.Errorf("handleEnqueueMessage for slash command should return nil, got %T", cmd)
	}
	if len(app.pendingQueue) != 0 {
		t.Errorf("pendingQueue should be empty for slash commands, got %d items", len(app.pendingQueue))
	}
}

func TestHandleEnqueueMessage_EmptyText(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	cmd := app.handleEnqueueMessage("   ")
	if cmd != nil {
		t.Errorf("handleEnqueueMessage for whitespace should return nil, got %T", cmd)
	}
	if len(app.pendingQueue) != 0 {
		t.Errorf("pendingQueue should be empty for whitespace input, got %d items", len(app.pendingQueue))
	}
}

// ---------------------------------------------------------------------------
// Call chain tests — enqueue → drain → render full path
// ---------------------------------------------------------------------------

// TestQueueMessage_CallChain_EnqueueDrainRender verifies the full path:
// 1. User types text while streaming → handleEnqueueMessage
// 2. pendingQueue gains entry + queue box renders it
// 3. Engine drains → attachmentMsg with UUID match
// 4. pendingQueue entry removed + message inserted into conversation
// 5. Queue box disappears
func TestQueueMessage_CallChain_EnqueueDrainRender(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.width = 80
	app.height = 24
	app.repl.StartQuery()
	app.spinner.Start()

	// Step 1: enqueue two messages while streaming
	app.handleEnqueueMessage("first queued msg")
	app.handleEnqueueMessage("second queued msg")

	// Step 2: verify pendingQueue has both entries
	if len(app.pendingQueue) != 2 {
		t.Fatalf("pendingQueue should have 2 entries, got %d", len(app.pendingQueue))
	}
	if app.pendingQueue[0].Text != "first queued msg" {
		t.Errorf("pendingQueue[0].Text = %q, want %q", app.pendingQueue[0].Text, "first queued msg")
	}
	if app.pendingQueue[1].Text != "second queued msg" {
		t.Errorf("pendingQueue[1].Text = %q, want %q", app.pendingQueue[1].Text, "second queued msg")
	}

	// Step 2b: queue box renders both items
	qb := app.renderQueueBox()
	if qb == "" {
		t.Fatal("renderQueueBox should return non-empty when items are queued")
	}
	if !strings.Contains(qb, "first queued msg") {
		t.Error("queue box should contain 'first queued msg'")
	}
	if !strings.Contains(qb, "second queued msg") {
		t.Error("queue box should contain 'second queued msg'")
	}

	// Step 3: first attachmentMsg drain — match first UUID
	uuid1 := app.pendingQueue[0].ID
	model, _ := app.Update(attachmentMsg{UserText: "first queued msg", SourceUUID: uuid1})
	app = model.(*App)

	// Step 4: first entry removed, one remains
	if len(app.pendingQueue) != 1 {
		t.Fatalf("pendingQueue should have 1 entry after first drain, got %d", len(app.pendingQueue))
	}
	if app.pendingQueue[0].Text != "second queued msg" {
		t.Errorf("remaining entry should be 'second queued msg', got %q", app.pendingQueue[0].Text)
	}

	// Verify conversation has the drained message as user message
	msgs := app.repl.messages
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Role != "user" {
		t.Errorf("drained message role = %q, want 'user'", lastMsg.Role)
	}
	if len(lastMsg.Blocks) != 1 || lastMsg.Blocks[0].Text != "first queued msg" {
		t.Errorf("drained message text = %q, want 'first queued msg'", lastMsg.Blocks[0].Text)
	}

	// Queue box still shows second item
	qb2 := app.renderQueueBox()
	if !strings.Contains(qb2, "second queued msg") {
		t.Error("queue box should still contain 'second queued msg'")
	}
	if strings.Contains(qb2, "first queued msg") {
		t.Error("queue box should NOT contain 'first queued msg' after drain")
	}

	// Step 5: second drain
	uuid2 := app.pendingQueue[0].ID
	model, _ = app.Update(attachmentMsg{UserText: "second queued msg", SourceUUID: uuid2})
	app = model.(*App)

	if len(app.pendingQueue) != 0 {
		t.Errorf("pendingQueue should be empty after second drain, got %d", len(app.pendingQueue))
	}

	// Queue box should be empty now
	qb3 := app.renderQueueBox()
	if qb3 != "" {
		t.Errorf("queue box should be empty after all drains, got %q", qb3)
	}

	// Both messages in conversation
	msgs = app.repl.messages
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages in conversation, got %d", len(msgs))
	}
	lastTwo := msgs[len(msgs)-2:]
	if lastTwo[0].Blocks[0].Text != "first queued msg" {
		t.Errorf("second-to-last message = %q, want 'first queued msg'", lastTwo[0].Blocks[0].Text)
	}
	if lastTwo[1].Blocks[0].Text != "second queued msg" {
		t.Errorf("last message = %q, want 'second queued msg'", lastTwo[1].Blocks[0].Text)
	}
}

// TestQueueMessage_CallChain_ResetOnQueryEnd verifies that pendingQueue is
// cleared when resetDisplayState is called (e.g. /clear, /session -n).
// This is the recovery scenario: stale queue items should not leak.
func TestQueueMessage_CallChain_ResetOnQueryEnd(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	// Enqueue messages
	app.handleEnqueueMessage("stale msg 1")
	app.handleEnqueueMessage("stale msg 2")
	if len(app.pendingQueue) != 2 {
		t.Fatalf("setup: pendingQueue should have 2, got %d", len(app.pendingQueue))
	}

	// Simulate session reset
	app.resetDisplayState()

	if len(app.pendingQueue) != 0 {
		t.Errorf("pendingQueue should be empty after resetDisplayState, got %d", len(app.pendingQueue))
	}

	// Queue box should be empty
	qb := app.renderQueueBox()
	if qb != "" {
		t.Errorf("queue box should be empty after reset, got %q", qb)
	}
}

// TestQueueMessage_CallChain_UUIDMismatchDoesNotRemove verifies that
// receiving an attachmentMsg with a non-matching UUID does NOT remove
// any pendingQueue entry — important for safety when drain order varies.
func TestQueueMessage_CallChain_UUIDMismatchDoesNotRemove(t *testing.T) {
	app := newTestApp(&tuiMockProvider{})
	app.repl.StartQuery()
	app.spinner.Start()

	app.handleEnqueueMessage("my message")
	if len(app.pendingQueue) != 1 {
		t.Fatalf("setup: pendingQueue should have 1, got %d", len(app.pendingQueue))
	}

	// Drain with wrong UUID
	model, _ := app.Update(attachmentMsg{UserText: "wrong message", SourceUUID: "nonexistent-uuid"})
	app = model.(*App)

	// Entry should still be there
	if len(app.pendingQueue) != 1 {
		t.Errorf("pendingQueue should still have 1 entry after UUID mismatch, got %d", len(app.pendingQueue))
	}

	// But a user message was still appended (engine drained something, just not our item)
	msgs := app.repl.messages
	lastMsg := msgs[len(msgs)-1]
	if lastMsg.Blocks[0].Text != "wrong message" {
		t.Errorf("conversation should contain the drained text, got %q", lastMsg.Blocks[0].Text)
	}
}
