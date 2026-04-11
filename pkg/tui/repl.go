package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/engine"
)

// ---------------------------------------------------------------------------
// REPL State
// ---------------------------------------------------------------------------

// ReplState holds the interactive REPL session state embedded in App.
type ReplState struct {
	messages    []MessageView
	streaming   bool
	pendingTool map[string]*ToolCallView

	// Tracks partial input accumulation per tool ID for summary updates
	pendingInput map[string]string

	// Channel for the final query result (nil when idle)
	resultCh <-chan engine.QueryResult

	// Cancellation
	cancelFunc context.CancelFunc
}

// NewReplState creates a fresh REPL state.
func NewReplState() *ReplState {
	return &ReplState{
		messages:     []MessageView{},
		pendingTool:  make(map[string]*ToolCallView),
		pendingInput: make(map[string]string),
	}
}

// AddUserMessage appends a user message to the session history.
func (s *ReplState) AddUserMessage(text string) {
	s.messages = append(s.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: text}},
	})
}

// StartQuery begins a new streaming query, storing the result channel.
// Creates the assistant message immediately so blocks grow during streaming.
func (s *ReplState) StartQuery(resultCh <-chan engine.QueryResult) {
	s.resultCh = resultCh
	s.streaming = true
	s.pendingTool = make(map[string]*ToolCallView)
	s.pendingInput = make(map[string]string)
	s.messages = append(s.messages, MessageView{Role: "assistant", Blocks: nil})
}

// lastMsg returns a pointer to the last message, or nil.
func (s *ReplState) lastMsg() *MessageView {
	if len(s.messages) == 0 {
		return nil
	}
	return &s.messages[len(s.messages)-1]
}

// AppendChunk appends a streaming text delta to the last text block.
func (s *ReplState) AppendChunk(text string) {
	m := s.lastMsg()
	if m == nil {
		return
	}
	// Append to last text block if it exists, otherwise create one
	if len(m.Blocks) > 0 && m.Blocks[len(m.Blocks)-1].Type == BlockText {
		m.Blocks[len(m.Blocks)-1].Text += text
	} else {
		m.Blocks = append(m.Blocks, ContentBlock{Type: BlockText, Text: text})
	}
}

// AppendTextItem starts a new empty text block.
func (s *ReplState) AppendTextItem() {
	m := s.lastMsg()
	if m == nil {
		return
	}
	m.Blocks = append(m.Blocks, ContentBlock{Type: BlockText, Text: ""})
}

// PendingToolStarted records a new in-progress tool call.
func (s *ReplState) PendingToolStarted(id, name, summary, input string) {
	m := s.lastMsg()
	if m == nil {
		return
	}
	tcv := &ToolCallView{ID: id, Name: name, Summary: summary, Input: input, Done: false}
	s.pendingTool[id] = tcv
	m.Blocks = append(m.Blocks, ContentBlock{Type: BlockTool, ToolCall: *tcv})
}

// PendingToolDone updates a tool call with its result.
func (s *ReplState) PendingToolDone(id, output string, isError bool, elapsed time.Duration) {
	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}
	tcv.Output = output
	tcv.IsError = isError
	tcv.Done = true
	tcv.Elapsed = elapsed

	// Update the tool block in lastMsg
	m := s.lastMsg()
	if m == nil {
		return
	}
	for i := len(m.Blocks) - 1; i >= 0; i-- {
		if m.Blocks[i].Type == BlockTool && m.Blocks[i].ToolCall.ID == id {
			m.Blocks[i].ToolCall = *tcv
			return
		}
	}
}

// PendingToolDelta updates a pending tool's input and summary from engine.
func (s *ReplState) PendingToolDelta(id, delta, summary string) {
	s.pendingInput[id] += delta

	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}

	// Use summary pre-computed by engine (via tool.Description + fallback)
	if summary != "" {
		tcv.Summary = summary
	}
	inputStr := s.pendingInput[id]
	tcv.Input = prettyJSON(json.RawMessage(inputStr))

	// Update the tool block in lastMsg
	m := s.lastMsg()
	if m == nil {
		return
	}
	for i := len(m.Blocks) - 1; i >= 0; i-- {
		if m.Blocks[i].Type == BlockTool && m.Blocks[i].ToolCall.ID == id {
			m.Blocks[i].ToolCall = *tcv
			return
		}
	}
}

// FinishStream finalizes the streaming session.
// Blocks in s.messages are already built incrementally during streaming.
func (s *ReplState) FinishStream(err error) {
	s.streaming = false

	if err != nil {
		s.messages = append(s.messages, MessageView{
			Role:   "system",
			Blocks: []ContentBlock{{Type: BlockText, Text: fmt.Sprintf("Error: %v", err)}},
		})
	}

	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}
}

// CloseChannels clears the result channel.
func (s *ReplState) CloseChannels() {
	s.resultCh = nil
}

// IsStreaming returns whether a query is in progress.
func (s *ReplState) IsStreaming() bool { return s.streaming }

// Messages returns the session message history.
func (s *ReplState) Messages() []MessageView { return s.messages }

// ---------------------------------------------------------------------------
// REPL Update — handles all REPL-specific messages.
// Called from App.Update in app.go.
// ---------------------------------------------------------------------------

// updateRepl handles REPL-related messages on the App.
// Returns whether the message was handled, and any tea.Cmd to execute.
func (a *App) updateRepl(msg tea.Msg) (bool, tea.Cmd) {
	switch m := msg.(type) {

	case streamChunkMsg:
		a.repl.AppendChunk(m.Text)
		return true, a.readEvents()

	case streamStartMsg:
		a.repl.AppendTextItem()
		return true, a.readEvents()

	case streamMessageMsg:
		return true, a.readEvents()

	case streamToolUseMsg:
		a.repl.PendingToolStarted(m.ID, m.Name, m.Summary, m.Input)
		return true, a.readEvents()

	case streamToolDeltaMsg:
		a.repl.PendingToolDelta(m.ID, m.Delta, m.Summary)
		return true, a.readEvents()

	case streamToolResultMsg:
		a.repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError, m.Timing)
		return true, a.readEvents()

	case streamCompleteMsg:
		a.repl.FinishStream(m.Err)
		a.progressStart = time.Time{}
		return true, nil

	case errMsg:
		a.status.SetError(m.Err.Error())
		a.repl.CloseChannels()
		*a.repl = *NewReplState()
		a.spinner.Stop()
		a.input.Focus()
		return true, nil

	case submitMsg:
		return true, a.handleSubmitRepl(m.Text)

	// Periodic spinner tick while streaming
	case spinnerTickMsg:
		if a.repl.IsStreaming() {
			a.spinner.Tick()
			return true, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			})
		}
		return true, nil

	}
	return false, nil
}

// handleSubmitRepl initiates a streaming query and sets up the REPL state.
func (a *App) handleSubmitRepl(text string) tea.Cmd {
	if a.repl.IsStreaming() {
		return nil
	}
	a.repl.AddUserMessage(text)
	a.history.Add(text)
	a.input.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	a.repl.cancelFunc = cancel

	// eventCh is discarded — events flow through Hub → TUIHandler → appCh
	_, resultCh := a.engine.Query(ctx, text, a.systemPrompt)
	a.repl.StartQuery(resultCh)
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.progressStart = time.Now()

	return tea.Batch(
		tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
		a.readEvents(),
	)
}

// readEvents reads the next event from TUIHandler.appCh or the result channel.
// This is called as a tea.Cmd.
func (a *App) readEvents() tea.Cmd {
	return func() tea.Msg {
		if a.tuiHandler == nil {
			return streamCompleteMsg{}
		}

		// Drain loop: prioritize appCh events over resultCh so that tool events
		// arriving just before resultCh closes are not missed.
		for {
			// First try non-blocking drain of any buffered appCh events.
			select {
			case msg, ok := <-a.tuiHandler.appCh:
				if !ok {
					a.repl.CloseChannels()
					return streamCompleteMsg{}
				}
				return msg
			default:
				// appCh empty — fall through to blocking select below.
			}

			// appCh is empty. Now block waiting for the next event from either
			// channel. resultCh may be nil (already closed) or closed.
			if a.repl.resultCh == nil {
				return streamCompleteMsg{}
			}

			select {
			case msg, ok := <-a.tuiHandler.appCh:
				if !ok {
					a.repl.CloseChannels()
					return streamCompleteMsg{}
				}
				return msg

			case result, ok := <-a.repl.resultCh:
				if !ok {
					return streamCompleteMsg{}
				}
				a.repl.CloseChannels()
				return streamCompleteMsg{Err: result.Error}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// prettyJSON formats JSON for display.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	pretty, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		return string(raw)
	}
	return string(pretty)
}

// renderMessages renders the visible message list within the given bounds.
// expandTools controls whether tool output is shown fully or collapsed.
func renderMessages(messages []MessageView, width, maxHeight int, expandTools bool) string {
	if len(messages) == 0 {
		welcomeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
		return welcomeStyle.Render("Welcome to gbot. Type a message to get started.") + "\n"
	}

	var lines []string
	usedLines := 0

	for i := len(messages) - 1; i >= 0 && usedLines < maxHeight; i-- {
		rendered := messages[i].View(width, expandTools)
		msgLines := strings.Split(rendered, "\n")
		for j := len(msgLines) - 1; j >= 0 && usedLines < maxHeight; j-- {
			lines = append([]string{msgLines[j]}, lines...)
			usedLines++
		}
	}

	return strings.Join(lines, "\n")
}
