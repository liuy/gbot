package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/user/gbot/pkg/engine"
	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// App — source: App.tsx → bubbletea root Model
// ---------------------------------------------------------------------------

// App is the root bubbletea Model.
// Source: App.tsx → bubbletea root Model
type App struct {
	width  int
	height int

	// Components
	input    *Input
	messages []MessageView
	status   StatusBar
	spinner  Spinner

	// State
	engine        *engine.Engine
	systemPrompt  json.RawMessage
	streaming     bool
	pendingTool   map[string]*ToolCallView // tool use ID -> in-progress tool call
	assistantBuf  strings.Builder          // accumulates streaming text

	// Cancellation
	cancelFunc context.CancelFunc

	// Channels from the current query (nil when idle)
	eventCh  <-chan types.QueryEvent
	resultCh <-chan engine.QueryResult
}

// NewApp creates a new App model.
func NewApp(eng *engine.Engine, systemPrompt json.RawMessage) *App {
	return &App{
		input:        NewInput(),
		messages:     []MessageView{},
		status:       NewStatusBar(),
		spinner:      NewSpinner(),
		engine:       eng,
		systemPrompt: systemPrompt,
		pendingTool:  make(map[string]*ToolCallView),
	}
}

// ---------------------------------------------------------------------------
// tea.Model interface
// ---------------------------------------------------------------------------

// Init initializes the TUI.
func (a *App) Init() tea.Cmd {
	return tea.EnterAltScreen
}

// Update handles bubbletea messages.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {

	case tea.WindowSizeMsg:
		a.width = m.Width
		a.height = m.Height
		a.input.SetWidth(a.width - 4)
		a.status.SetWidth(a.width)
		return a, nil

	case tea.KeyMsg:
		return a.handleKey(m)

	case streamChunkMsg:
		a.assistantBuf.WriteString(m.Text)
		return a, a.readEvents()

	case streamToolUseMsg:
		tcv := &ToolCallView{
			Name:  m.Name,
			Input: m.Input,
			Done:  false,
		}
		a.pendingTool[m.ID] = tcv
		return a, a.readEvents()

	case streamToolResultMsg:
		if tcv, ok := a.pendingTool[m.ToolUseID]; ok {
			tcv.Output = m.Output
			tcv.IsError = m.IsError
			tcv.Done = true
		}
		return a, a.readEvents()

	case streamCompleteMsg:
		a.finishStream(m.Err)
		return a, nil

	case errMsg:
		a.status.SetError(m.Err.Error())
		a.streaming = false
		a.spinner.Stop()
		a.input.Focus()
		return a, nil

	case submitMsg:
		return a.handleSubmit(m.Text)

	// Periodic spinner tick while streaming
	case spinnerTickMsg:
		if a.streaming {
			a.spinner.Tick()
			return a, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
				return spinnerTickMsg{}
			})
		}
		return a, nil
	}

	return a, nil
}

// View renders the entire TUI.
func (a *App) View() string {
	if a.width == 0 {
		return "Loading..."
	}

	var sb strings.Builder

	// Render messages (scroll region)
	availHeight := a.height - 4 // status bar + input + borders
	if availHeight < 3 {
		availHeight = 3
	}

	// Render visible messages from bottom
	rendered := renderMessages(a.messages, a.width, availHeight)
	sb.WriteString(rendered)

	// Show current assistant stream
	if a.streaming {
		text := a.assistantBuf.String()
		if text != "" || a.spinner.Active() {
			style := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
			sb.WriteString(style.Render("gbot: "))
			sb.WriteString(wordWrap(text, a.width-8))
			sb.WriteString(a.spinner.View())
			sb.WriteString("\n")
		}

		// Show pending tool calls
		for _, tcv := range a.pendingTool {
			if !tcv.Done {
				toolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
				sb.WriteString(toolStyle.Render(fmt.Sprintf("  [%s] running...", tcv.Name)))
				sb.WriteString("\n")
			}
		}
	}

	// Status bar
	sb.WriteString(a.status.View())
	sb.WriteString("\n")

	// Input
	sb.WriteString(a.input.View())

	return sb.String()
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {

	case tea.KeyCtrlC:
		if a.streaming {
			// Cancel current query
			if a.cancelFunc != nil {
				a.cancelFunc()
				a.cancelFunc = nil
			}
			a.finishStream(nil)
			return a, nil
		}
		// Not streaming: quit
		return a, tea.Quit

	case tea.KeyEnter:
		text := a.input.Value()
		if strings.TrimSpace(text) == "" {
			return a, nil
		}
		return a.handleSubmit(text)

	case tea.KeyBackspace:
		a.input.Backspace()
		return a, nil

	case tea.KeyDelete:
		a.input.Backspace() // simplified
		return a, nil

	case tea.KeyLeft:
		a.input.CursorLeft()
		return a, nil

	case tea.KeyRight:
		a.input.CursorRight()
		return a, nil

	case tea.KeyHome:
		a.input.Home()
		return a, nil

	case tea.KeyEnd:
		a.input.End()
		return a, nil

	case tea.KeySpace:
		a.input.InsertChar(' ')
		return a, nil

	case tea.KeyRunes:
		for _, ch := range msg.Runes {
			a.input.InsertChar(ch)
		}
		return a, nil

	case tea.KeyCtrlU:
		// Clear input line
		a.input.Reset()
		return a, nil

	case tea.KeyCtrlW:
		// Delete word
		a.input.DeleteWord()
		return a, nil
	}

	return a, nil
}

// ---------------------------------------------------------------------------
// Submit / streaming
// ---------------------------------------------------------------------------

func (a *App) handleSubmit(text string) (tea.Model, tea.Cmd) {
	if a.streaming {
		// Already streaming, ignore
		return a, nil
	}

	// Add user message view
	a.messages = append(a.messages, MessageView{
		Role:    "user",
		Content: text,
	})

	a.input.Reset()

	// Start streaming
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFunc = cancel

	eventCh, resultCh := a.engine.Query(ctx, text, a.systemPrompt)
	a.eventCh = eventCh
	a.resultCh = resultCh
	a.streaming = true
	a.assistantBuf.Reset()
	a.pendingTool = make(map[string]*ToolCallView)
	a.status.SetStreaming(true)
	a.spinner.Start()

	// Start spinner tick and read first events
	return a, tea.Batch(
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
		if a.eventCh == nil {
			return streamCompleteMsg{}
		}

		// Try to read an event
		select {
		case evt, ok := <-a.eventCh:
			if !ok {
				// Event channel closed, read result
				a.eventCh = nil
				return streamCompleteMsg{}
			}
			return a.engineEventToMsg(evt)

		case result, ok := <-a.resultCh:
			if !ok {
				return streamCompleteMsg{}
			}
			a.resultCh = nil
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
				Timing:    evt.ToolResult.Timing.String(),
			}
		}

	case types.EventError:
		return errMsg{Err: evt.Error}

	case types.EventComplete:
		return streamCompleteMsg{}
	}

	// Unknown event type: read next
	return a.readEvents()()
}

// finishStream finalizes the current streaming response.
func (a *App) finishStream(err error) {
	a.streaming = false
	a.status.SetStreaming(false)
	a.spinner.Stop()
	a.input.Focus()

	if a.cancelFunc != nil {
		a.cancelFunc()
		a.cancelFunc = nil
	}

	// Build assistant message view
	text := a.assistantBuf.String()
	var toolCalls []ToolCallView
	for _, tcv := range a.pendingTool {
		toolCalls = append(toolCalls, *tcv)
	}

	if text != "" || len(toolCalls) > 0 {
		a.messages = append(a.messages, MessageView{
			Role:      "assistant",
			Content:   text,
			ToolCalls: toolCalls,
		})
	}

	a.assistantBuf.Reset()
	a.pendingTool = make(map[string]*ToolCallView)

	if err != nil {
		a.messages = append(a.messages, MessageView{
			Role:    "system",
			Content: fmt.Sprintf("Error: %v", err),
		})
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

	// Render from the end (most recent) to fill available height
	var lines []string
	usedLines := 0

	for i := len(messages) - 1; i >= 0 && usedLines < maxHeight; i-- {
		rendered := messages[i].View(width)
		msgLines := strings.Split(rendered, "\n")
		// Add lines in reverse order (bottom-up)
		for j := len(msgLines) - 1; j >= 0 && usedLines < maxHeight; j-- {
			if msgLines[j] != "" {
				lines = append([]string{msgLines[j]}, lines...)
				usedLines++
			}
		}
	}

	return strings.Join(lines, "\n") + "\n"
}

// spinnerTickMsg is an internal message to animate the spinner.
type spinnerTickMsg struct{}
