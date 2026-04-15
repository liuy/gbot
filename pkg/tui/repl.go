package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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

	// Tracks when each tool call started streaming (for perceived elapsed)
	pendingToolStart map[string]time.Time

	// Channel for the final query result (nil when idle)
	resultCh <-chan engine.QueryResult

	// Cancellation
	cancelFunc context.CancelFunc

	// Tracks the index of the current thinking block in lastMsg().Blocks
	// so deltas can append to it. -1 when no thinking block is active.
	activeThinkingIdx int
}

// NewReplState creates a fresh REPL state.
func NewReplState() *ReplState {
	return &ReplState{
		messages:     []MessageView{},
		pendingTool:      make(map[string]*ToolCallView),
		pendingInput:     make(map[string]string),
		pendingToolStart: make(map[string]time.Time),
		activeThinkingIdx: -1,
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
	s.pendingToolStart[id] = time.Now()
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
	if start, ok := s.pendingToolStart[id]; ok {
		if perceived := time.Since(start); perceived > elapsed {
			tcv.Elapsed = perceived
		}
	}

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


// PendingToolOutput updates a streaming tool's output lines in real time.
func (s *ReplState) PendingToolOutput(id, output string, timing time.Duration) {
	tcv, ok := s.pendingTool[id]
	if !ok {
		return
	}

	// Track elapsed time (use perceived time for responsiveness)
	if start, ok := s.pendingToolStart[id]; ok {
		if perceived := time.Since(start); perceived > timing {
			tcv.Elapsed = perceived
		}
	}

	// Accumulate output lines (each event carries all current lines)
	tcv.Output = output

	// Mark tool as done so output is rendered (no "running..." anymore)
	tcv.Done = true

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

// PendingThinkingStarted appends a new thinking block to the last message.
func (s *ReplState) PendingThinkingStarted() {
	s.activeThinkingIdx = -1
	m := s.lastMsg()
	if m == nil {
		return
	}
	m.Blocks = append(m.Blocks, ContentBlock{
		Type:     BlockThinking,
		Thinking: ThinkingView{Done: false},
	})
	s.activeThinkingIdx = len(m.Blocks) - 1
}

// PendingThinkingDelta appends text to the active thinking block.
func (s *ReplState) PendingThinkingDelta(text string) {
	if s.activeThinkingIdx < 0 {
		return
	}
	m := s.lastMsg()
	if m == nil {
		return
	}
	if s.activeThinkingIdx >= len(m.Blocks) {
		return
	}
	m.Blocks[s.activeThinkingIdx].Thinking.Text += text
}

// PendingThinkingDone marks the active thinking block as done.
func (s *ReplState) PendingThinkingDone(duration time.Duration) {
	if s.activeThinkingIdx < 0 {
		return
	}
	m := s.lastMsg()
	if m == nil {
		return
	}
	if s.activeThinkingIdx >= len(m.Blocks) {
		return
	}
	blk := &m.Blocks[s.activeThinkingIdx].Thinking
	blk.Done = true
	blk.Duration = duration
	s.activeThinkingIdx = -1
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

	case textDeltaMsg:
		a.markViewportDirty()
		a.repl.AppendChunk(m.Text)
		a.responseCharCount += len(m.Text)
		return true, a.readEvents()

	case turnStartMsg:
		a.markViewportDirty()
		a.repl.AppendTextItem()
		return true, a.readEvents()

	case streamMessageMsg:
		a.markViewportDirty()
		return true, a.readEvents()

	case toolStartMsg:
		a.markViewportDirty()
		a.repl.PendingToolStarted(m.ID, m.Name, m.Summary, m.Input)
			slog.Info("tui:tool_start", "id", m.ID, "name", m.Name, "summary", m.Summary)
		return true, a.readEvents()

	case toolParamDeltaMsg:
		a.markViewportDirty()
		a.repl.PendingToolDelta(m.ID, m.Delta, m.Summary)
		a.responseCharCount += len(m.Delta)
		return true, a.readEvents()

	case toolOutputDeltaMsg:
		a.markViewportDirty()
		a.repl.PendingToolOutput(m.ToolUseID, m.DisplayOutput, m.Timing)
		return true, a.readEvents()

	case toolEndMsg:
		a.markViewportDirty()
		a.repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError, m.Timing)
			slog.Info("tui:tool_end", "id", m.ToolUseID, "isError", m.IsError, "outputLen", len(m.Output))
		return true, a.readEvents()

	case queryEndMsg:
		a.repl.FinishStream(m.Err)
		if !a.progressStart.IsZero() {
			elapsedStr := formatElapsed(a.progressStart)
			tokensStr := fmt.Sprintf("↑%s ↓%s tokens", formatTokenCount(a.status.inputTokens), formatTokenCount(a.status.outTokens))
			statsLine := styleDim.Render(tokensStr + " · " + elapsedStr)
			// Embed stats as a block in the last assistant message.
			// This is TUI-only — messages are not sent to the LLM.
			if msg := a.repl.lastMsg(); msg != nil {
				msg.Blocks = append(msg.Blocks, ContentBlock{Type: BlockStats, Text: statsLine})
				slog.Info("tui:query_end", "inputTokens", a.status.inputTokens, "outTokens", a.status.outTokens, "committedCount", a.committedCount, "totalMessages", len(a.repl.messages))
			}
		}
		a.progressStart = time.Time{}
		a.thinkingActive = false
		a.thinkingDuration = 0

		// Don't commit yet — keep current turn in Bubble Tea view so
		// Ctrl+O (expand/collapse tool output) remains interactive.
		// Commit happens when the user submits the next query.
		a.contentCache = ""
		a.contentDirty = false
		return true, nil

	case usageMsg:
		a.status.inputTokens += m.InputTokens
		a.status.outTokens += m.OutputTokens
		// Input tokens arrive all at once — snap immediately
		a.displayedInputTokens = a.status.inputTokens
		a.inputTokenTarget = a.status.inputTokens
		a.outputTokenTarget = a.status.outTokens
		slog.Info("tui:usage", "delta_in", m.InputTokens, "delta_out", m.OutputTokens, "total_in", a.status.inputTokens, "total_out", a.status.outTokens)
		return true, a.readEvents()

	case thinkingStartMsg:
		a.thinkingActive = true
		a.thinkingStart = time.Now()
		a.markViewportDirty()
		a.repl.PendingThinkingStarted()
		return true, a.readEvents()

	case thinkingDeltaMsg:
		a.markViewportDirty()
		a.repl.PendingThinkingDelta(m.Text)
		return true, a.readEvents()

	case thinkingEndMsg:
		a.thinkingActive = false
		a.thinkingDuration = m.Duration
		a.markViewportDirty()
		a.repl.PendingThinkingDone(m.Duration)
		return true, a.readEvents()

	case errMsg:
		a.status.SetError(m.Err.Error())
		a.repl.CloseChannels()
		// Commit uncommitted messages before resetting so error context
		// is preserved in terminal scrollback.
		var errCommitCmd tea.Cmd
		uncommitted := a.repl.messages[a.committedCount:]
		if len(uncommitted) > 0 {
// Suppress ctrl+o hints in scrollback (noHint=true) — preserve
			// user's expand/collapse state.
			rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", true, 0)
			errCommitCmd = tea.Println(rendered)
		}
		*a.repl = *NewReplState()
		a.committedCount = 0
		a.spinner.Stop()
		a.input.Focus()
		return true, errCommitCmd

	case submitMsg:
		return true, a.handleSubmitRepl(m.Text)

	// Periodic spinner tick while streaming
	case spinnerTickMsg:
		if a.repl.IsStreaming() {
			a.toolBlinkTick++
			if a.toolBlinkTick%5 == 0 {
				a.spinner.Tick()
			}
			a.toolBlink = (a.toolBlinkTick/5)%2 == 0
			// Animate displayed tokens toward actual values
			target := a.inputTokenTarget
			if a.status.inputTokens > target {
				target = a.status.inputTokens
			}
			a.displayedInputTokens = animateTokenValue(a.displayedInputTokens, target)
			outputTarget := a.outputTokenTarget
			if a.responseCharCount/4 > outputTarget {
				outputTarget = a.responseCharCount / 4
			}
			a.displayedOutputTokens = animateTokenValue(a.displayedOutputTokens, outputTarget)
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
	slog.Info("tui:query_start", "text", truncateRunes(text, 100), "text_len", len(text), "committedCount", a.committedCount, "totalMessages", len(a.repl.messages))
	if a.repl.IsStreaming() {
		return nil
	}

	// Commit previous turn's messages to scrollback before starting new turn.
	// This defers the commit so Ctrl+O stays interactive during the
	// completed turn, and scrolls up when user submits next query.
	var commitCmd tea.Cmd
	uncommitted := a.repl.messages[a.committedCount:]
	if len(uncommitted) > 0 {
// Suppress ctrl+o hints in scrollback (noHint=true) — preserve
		// user's expand/collapse state.
		rendered := renderMessagesFull(uncommitted, a.width, a.allToolsExpanded, "", true, 0)
		a.committedCount = len(a.repl.messages)
		commitCmd = tea.Println(rendered)
	}
	a.repl.AddUserMessage(text)
	a.history.Add(text)
	a.input.Reset()
	a.scrollOffset = 0
	a.scrollTotal = 0
	a.userScrolled = false
	a.markViewportDirty()

	ctx, cancel := context.WithCancel(context.Background())
	a.repl.cancelFunc = cancel

	// eventCh is discarded — events flow through Hub → TUIHandler → appCh
	_, resultCh := a.engine.Query(ctx, text, a.systemPrompt)
	a.repl.StartQuery(resultCh)
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.progressStart = time.Now()
	a.thinkingActive = false
	a.thinkingDuration = 0
	a.status.SetUsage(0, 0)
	a.responseCharCount = 0
	a.displayedInputTokens = 0
	a.displayedOutputTokens = 0
	// Estimate input tokens from context + user message text
	totalChars := len(a.systemPrompt) + len(text)
	for _, msg := range a.repl.Messages() {
		for _, blk := range msg.Blocks {
			if blk.Type == BlockText {
				totalChars += len(blk.Text)
			}
		}
	}
	a.inputTokenTarget = totalChars / 4

	return tea.Batch(
		commitCmd,
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
			return queryEndMsg{}
		}

		// Drain loop: prioritize appCh events over resultCh so that tool events
		// arriving just before resultCh closes are not missed.
		for {
			// First try non-blocking drain of any buffered appCh events.
			select {
			case msg, ok := <-a.tuiHandler.appCh:
				if !ok {
					a.repl.CloseChannels()
					return queryEndMsg{}
				}
				return msg
			default:
				// appCh empty — fall through to blocking select below.
			}

			// appCh is empty. Now block waiting for the next event from either
			// channel. resultCh may be nil (already closed) or closed.
			if a.repl.resultCh == nil {
				return queryEndMsg{}
			}

			select {
			case msg, ok := <-a.tuiHandler.appCh:
				if !ok {
					a.repl.CloseChannels()
					return queryEndMsg{}
				}
				return msg

			case result, ok := <-a.repl.resultCh:
				if !ok {
					return queryEndMsg{}
				}
				a.repl.CloseChannels()
				return queryEndMsg{Err: result.Error}
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

// renderMessagesFull renders the complete message history without height truncation.
// Terminal native scrollback handles scrolling — matching TS behavior.
func renderMessagesFull(messages []MessageView, width int, expandTools bool, toolDot string, noHint bool, maxOutputLines int) string {
	if len(messages) == 0 {
		welcomeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("246")).Italic(true)
		return welcomeStyle.Render("Welcome to gbot. Type a message to get started.")
	}

	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(msg.View(width, expandTools, toolDot, noHint, maxOutputLines))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// markViewportDirty marks the content cache as needing rebuild.
func (a *App) markViewportDirty() {
	a.contentDirty = true
}

// truncateRunes truncates s to at most maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
