package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/user/gbot/pkg/engine"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// REPL State
// ---------------------------------------------------------------------------

// ReplState holds the interactive REPL session state embedded in App.
type ReplState struct {
	messages    []MessageView
	streaming   bool
	pendingTool map[string]*ToolCallView
	assistantBuf strings.Builder

	// Channels from the current query (nil when idle)
	eventCh  <-chan types.QueryEvent
	resultCh <-chan engine.QueryResult

	// Cancellation
	cancelFunc context.CancelFunc
}

// NewReplState creates a fresh REPL state.
func NewReplState() *ReplState {
	return &ReplState{
		messages:    []MessageView{},
		pendingTool: make(map[string]*ToolCallView),
	}
}

// AddUserMessage appends a user message to the session history.
func (s *ReplState) AddUserMessage(text string) {
	s.messages = append(s.messages, MessageView{Role: "user", Content: text})
}

// StartQuery begins a new streaming query, populating channels.
func (s *ReplState) StartQuery(eventCh <-chan types.QueryEvent, resultCh <-chan engine.QueryResult) {
	s.eventCh = eventCh
	s.resultCh = resultCh
	s.streaming = true
	s.assistantBuf.Reset()
	s.pendingTool = make(map[string]*ToolCallView)
}

// AppendChunk appends a streaming text delta.
func (s *ReplState) AppendChunk(text string) {
	s.assistantBuf.WriteString(text)
}

// PendingToolStarted records a new in-progress tool call.
func (s *ReplState) PendingToolStarted(id, name, input string) {
	s.pendingTool[id] = &ToolCallView{Name: name, Input: input, Done: false}
}

// PendingToolDone marks a tool call as completed.
func (s *ReplState) PendingToolDone(id, output string, isError bool) {
	if tcv, ok := s.pendingTool[id]; ok {
		tcv.Output = output
		tcv.IsError = isError
		tcv.Done = true
	}
}

// FinishStream finalizes the streaming session, appending the assistant message.
func (s *ReplState) FinishStream(err error) {
	s.streaming = false

	text := s.assistantBuf.String()
	var toolCalls []ToolCallView
	for _, tcv := range s.pendingTool {
		toolCalls = append(toolCalls, *tcv)
	}

	if text != "" || len(toolCalls) > 0 {
		s.messages = append(s.messages, MessageView{
			Role:      "assistant",
			Content:   text,
			ToolCalls: toolCalls,
		})
	}

	s.assistantBuf.Reset()
	s.pendingTool = make(map[string]*ToolCallView)

	if err != nil {
		s.messages = append(s.messages, MessageView{
			Role:    "system",
			Content: fmt.Sprintf("Error: %v", err),
		})
	}

	// Cancel and clear the query context
	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}
}

// CloseChannels clears the event and result channels.
func (s *ReplState) CloseChannels() {
	s.eventCh = nil
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

	case streamToolUseMsg:
		a.repl.PendingToolStarted(m.ID, m.Name, m.Input)
		return true, a.readEvents()

	case streamToolResultMsg:
		a.repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError)
		return true, a.readEvents()

	case streamCompleteMsg:
		a.repl.FinishStream(m.Err)
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
	a.input.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	a.repl.cancelFunc = cancel

	eventCh, resultCh := a.engine.Query(ctx, text, a.systemPrompt)
	a.repl.StartQuery(eventCh, resultCh)
	a.status.SetStreaming(true)
	a.spinner.Start()

	return tea.Batch(
		tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
		a.readEvents(),
	)
}

// readEvents reads the next event from the engine channels and converts
// it to a bubbletea message. This is called as a tea.Cmd.
func (a *App) readEvents() tea.Cmd {
	return func() tea.Msg {
		if a.repl.eventCh == nil {
			return streamCompleteMsg{}
		}

		select {
		case evt, ok := <-a.repl.eventCh:
			if !ok {
				a.repl.CloseChannels()
				return streamCompleteMsg{}
			}
			return a.engineEventToMsg(evt)

		case result, ok := <-a.repl.resultCh:
			if !ok {
				return streamCompleteMsg{}
			}
			a.repl.CloseChannels()
			return streamCompleteMsg{Err: result.Error}
		}
	}
}

// engineEventToMsg converts a types.QueryEvent to a bubbletea message.
func (a *App) engineEventToMsg(evt types.QueryEvent) tea.Msg {
	switch evt.Type {
	case types.EventTextDelta:
		return streamChunkMsg{Text: evt.Text}

	case types.EventToolUseStart:
		if evt.ToolUse != nil {
			input := prettyJSON(evt.ToolUse.Input)
			return streamToolUseMsg{
				ID:    evt.ToolUse.ID,
				Name:  evt.ToolUse.Name,
				Input: input,
			}
		}

	case types.EventToolResult:
		if evt.ToolResult != nil {
			return streamToolResultMsg{
				ToolUseID: evt.ToolResult.ToolUseID,
				Output:    prettyJSON(evt.ToolResult.Output),
				IsError:   evt.ToolResult.IsError,
			}
		}

	case types.EventError:
		return errMsg{Err: evt.Error}

	case types.EventComplete:
		return streamCompleteMsg{}
	}

	return a.readEvents()()
}

// ---------------------------------------------------------------------------
// REPL View — renders the streaming assistant output and pending tools.
// Called from App.View in app.go.
// ---------------------------------------------------------------------------

// replView renders the in-progress streaming assistant output and pending tool calls.
func (a *App) replView(sb *strings.Builder) {
	if !a.repl.IsStreaming() {
		return
	}

	text := a.repl.assistantBuf.String()
	if text != "" || a.spinner.Active() {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
		sb.WriteString(style.Render("gbot: "))
		sb.WriteString(RenderWidth(text, a.width-8))
		sb.WriteString(a.spinner.View())
		sb.WriteString("\n")
	}

	for _, tcv := range a.repl.pendingTool {
		if !tcv.Done {
			toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
			sb.WriteString(toolStyle.Render(fmt.Sprintf("  [%s] running...", tcv.Name)))
			sb.WriteString("\n")
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
	var v interface{}
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
func renderMessages(messages []MessageView, width, maxHeight int) string {
	if len(messages) == 0 {
		welcomeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
		return welcomeStyle.Render("Welcome to gbot. Type a message to get started.") + "\n"
	}

	var lines []string
	usedLines := 0

	for i := len(messages) - 1; i >= 0 && usedLines < maxHeight; i-- {
		rendered := messages[i].View(width)
		msgLines := strings.Split(rendered, "\n")
		for j := len(msgLines) - 1; j >= 0 && usedLines < maxHeight; j-- {
			lines = append([]string{msgLines[j]}, lines...)
			usedLines++
		}
	}

	return strings.Join(lines, "\n")
}
