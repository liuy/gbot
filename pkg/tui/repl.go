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
	assistantBuf strings.Builder

	// Channel for the final query result (nil when idle)
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
	s.messages = append(s.messages, MessageView{
		Role:   "user",
		Blocks: []ContentBlock{{Type: BlockText, Text: text}},
	})
}

// StartQuery begins a new streaming query, storing the result channel.
func (s *ReplState) StartQuery(resultCh <-chan engine.QueryResult) {
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
func (s *ReplState) PendingToolDone(id, output string, isError bool, elapsed time.Duration) {
	if tcv, ok := s.pendingTool[id]; ok {
		tcv.Output = output
		tcv.IsError = isError
		tcv.Done = true
		tcv.Elapsed = elapsed
	}
}

// FinishStream finalizes the streaming session, appending the assistant message.
func (s *ReplState) FinishStream(err error) {
	s.streaming = false

	text := s.assistantBuf.String()
	var toolBlocks []ContentBlock
	for _, tcv := range s.pendingTool {
		toolBlocks = append(toolBlocks, ContentBlock{Type: BlockTool, ToolCall: *tcv})
	}

	if text != "" || len(toolBlocks) > 0 {
		s.messages = append(s.messages, MessageView{
			Role:   "assistant",
			Blocks: []ContentBlock{{Type: BlockText, Text: text}},
		})
		// Append tool blocks as separate message
		for _, tb := range toolBlocks {
			lastIdx := len(s.messages) - 1
			s.messages[lastIdx].Blocks = append(s.messages[lastIdx].Blocks, tb)
		}
	}

	s.assistantBuf.Reset()
	s.pendingTool = make(map[string]*ToolCallView)

	if err != nil {
		s.messages = append(s.messages, MessageView{
			Role:   "system",
			Blocks: []ContentBlock{{Type: BlockText, Text: fmt.Sprintf("Error: %v", err)}},
		})
	}

	// Cancel and clear the query context
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
		return true, a.readEvents()

	case streamMessageMsg:
		return true, a.readEvents()

	case streamToolUseMsg:
		a.repl.PendingToolStarted(m.ID, m.Name, m.Input)
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
	if text != "" {
		sb.WriteString(RenderWidth(text, a.width-2))
		sb.WriteString("\n")
	}

	// Pending tool calls: show ● dot in dim color (in-progress) — per TS ToolUseLoader
	for _, tcv := range a.repl.pendingTool {
		if !tcv.Done {
			dimDot := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true).Render(dot)
			dimName := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true).Render(tcv.Name)
			fmt.Fprintf(sb, "%s %s running...", dimDot, dimName)
			if tcv.Input != "" && len(tcv.Input) < 200 {
				sb.WriteString("\n  " + wordWrap(tcv.Input, a.width-4))
			}
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
