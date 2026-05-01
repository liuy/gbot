package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
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

	// Total tool calls in the current query (for progress display)
	toolCount int

	// Cancellation
	cancelFunc context.CancelFunc

	// Tracks the index of the current thinking block in lastMsg().Blocks
	// so deltas can append to it. -1 when no thinking block is active.
	activeThinkingIdx int
}

// NewReplState creates a fresh REPL state.
func NewReplState() *ReplState {
	return &ReplState{
		messages:          []MessageView{},
		pendingTool:       make(map[string]*ToolCallView),
		pendingInput:      make(map[string]string),
		pendingToolStart:  make(map[string]time.Time),
		activeThinkingIdx: -1,
	}
}

// updateToolBlock finds the tool block with the given ID in the last message
// (searching in reverse) and replaces its ToolCallView. Returns false if not found.
func (s *ReplState) updateToolBlock(id string, tcv *ToolCallView) bool {
	m := s.lastMsg()
	if m == nil {
		return false
	}
	for i := len(m.Blocks) - 1; i >= 0; i-- {
		if m.Blocks[i].Type == BlockTool && m.Blocks[i].ToolCall.ID == id {
			m.Blocks[i].ToolCall = *tcv
			return true
		}
	}
	return false
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
func (s *ReplState) StartQuery() {
	s.streaming = true
	s.pendingTool = make(map[string]*ToolCallView)
	s.pendingInput = make(map[string]string)
	s.toolCount = 0
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
	s.toolCount++
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

	// Add sub-agent tool count to global stats
	if tcv.ToolCount > 0 {
		s.toolCount += tcv.ToolCount
	}

	// Update the tool block in lastMsg
	s.updateToolBlock(id, tcv)
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
	s.updateToolBlock(id, tcv)
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
	s.updateToolBlock(id, tcv)
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

// UpdateAgentProgress handles sub-agent events (thinking, tool_start, tool_end).
func (s *ReplState) UpdateAgentProgress(msg agentToolMsg) {
	tcv, ok := s.pendingTool[msg.ParentToolUseID]
	if !ok {
		slog.Info("tui:agent_progress:unknown_parent", "parentID", msg.ParentToolUseID, "agentType", msg.AgentType, "toolName", msg.ToolName)
		return
	}

	switch msg.SubType {
	case "thinking_start":
		// Add a Thinking entry if none exists
		for _, e := range tcv.AgentLogs {
			if e.ToolName == "Thinking" && !e.Done {
				return // already thinking
			}
		}
		tcv.AgentLogs = append(tcv.AgentLogs, AgentLogEntry{
			AgentType: msg.AgentType,
			Depth:     msg.Depth,
			ToolName:  "Thinking",
			Done:      false,
		})

	case "thinking_end":
		// Remove Thinking entry - only shown during active thinking phase
		for i := range tcv.AgentLogs {
			if tcv.AgentLogs[i].ToolName == "Thinking" && !tcv.AgentLogs[i].Done {
				tcv.AgentLogs = append(tcv.AgentLogs[:i], tcv.AgentLogs[i+1:]...)
				break
			}
		}

	case "tool_start":
		// Remove any Thinking entry - thinking done, tools starting
		for i := range tcv.AgentLogs {
			if tcv.AgentLogs[i].ToolName == "Thinking" {
				tcv.AgentLogs = append(tcv.AgentLogs[:i], tcv.AgentLogs[i+1:]...)
				break
			}
		}
		// Mark previous entries at same depth as done
		for i := range tcv.AgentLogs {
			if tcv.AgentLogs[i].Depth == msg.Depth && !tcv.AgentLogs[i].Done {
				tcv.AgentLogs[i].Done = true
			}
		}
		tcv.AgentLogs = append(tcv.AgentLogs, AgentLogEntry{
			AgentType: msg.AgentType,
			Depth:     msg.Depth,
			ToolName:  msg.ToolName,
			Summary:   msg.Summary,
			Done:      false,
		})
		tcv.ToolCount++

	case "tool_param_delta":
		// Update summary of last tool entry at this depth (streaming input).
		// Match by ToolName to avoid updating a different tool at same depth.
		if msg.Summary != "" {
			for i := len(tcv.AgentLogs) - 1; i >= 0; i-- {
				if tcv.AgentLogs[i].Depth == msg.Depth && tcv.AgentLogs[i].ToolName == msg.ToolName && tcv.AgentLogs[i].ToolName != "Thinking" {
					tcv.AgentLogs[i].Summary = msg.Summary
					break
				}
			}
		}

	case "tool_end":
		// Mark the last non-done tool entry at this depth as done
		for i := len(tcv.AgentLogs) - 1; i >= 0; i-- {
			if tcv.AgentLogs[i].Depth == msg.Depth && !tcv.AgentLogs[i].Done && tcv.AgentLogs[i].ToolName != "Thinking" {
				tcv.AgentLogs[i].Done = true
				tcv.AgentLogs[i].IsError = msg.IsError
				break
			}
		}
	}

	// Keep last 50 entries
	if len(tcv.AgentLogs) > 50 {
		tcv.AgentLogs = tcv.AgentLogs[len(tcv.AgentLogs)-50:]
	}

	// Update the block in lastMsg
	s.updateToolBlock(msg.ParentToolUseID, tcv)
}

// UpdateAgentUsage accumulates sub-agent token usage into both global and per-agent counters.
func (s *ReplState) UpdateAgentUsage(parentID string, inputTokens, outputTokens, contextSize int) {
	tcv, ok := s.pendingTool[parentID]
	if !ok {
		return
	}
	tcv.TokensIn += inputTokens
	tcv.TokensOut += outputTokens
	tcv.ContextSize = contextSize

	s.updateToolBlock(parentID, tcv)
}

// SetAgentContextWindow sets the context window for a sub-agent tool call.
// Called once at tool start so the TUI can display "8.7k/30.0k" usage ratios.
func (s *ReplState) SetAgentContextWindow(parentID string, window int) {
	tcv, ok := s.pendingTool[parentID]
	if !ok {
		return
	}
	tcv.ContextWindow = window
	s.updateToolBlock(parentID, tcv)
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
	case textStartMsg:
		// No-op: text content block started. Future use: viewport transitions.
		return true, a.readEvents()

	case textEndMsg:
		// No-op: text content block finished.
		return true, a.readEvents()

	case toolRunMsg:
		// No-op: tool execution started after input accumulation.
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
		if m.Name == "Agent" {
			a.repl.SetAgentContextWindow(m.ID, a.engine.ContextWindow())
		}
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
		a.taskListDirty = true
		slog.Info("tui:tool_end", "id", m.ToolUseID, "isError", m.IsError, "outputLen", len(m.Output))
		return true, a.readEvents()

	case agentToolMsg:
		a.markViewportDirty()
		a.repl.UpdateAgentProgress(m)
		return true, a.readEvents()

	case agentUsageMsg:
		a.status.usage.InputTokens += m.InputTokens
		a.status.usage.OutputTokens += m.OutputTokens
		a.status.usage.CacheReadInputTokens += m.CacheReadInputTokens
		a.status.usage.CacheCreationInputTokens += m.CacheCreationInputTokens
		totalIn := a.status.usage.TotalInputTokens()
		a.displayedInputTokens = totalIn
		a.inputTokenTarget = totalIn
		a.outputTokenTarget = a.status.usage.OutputTokens
		slog.Info("tui:usage", "delta_in", m.InputTokens, "delta_out", m.OutputTokens, "total_in", a.status.usage.TotalInputTokens(), "total_out", a.status.usage.OutputTokens, "cache_read", a.status.usage.CacheReadInputTokens, "cache_creation", a.status.usage.CacheCreationInputTokens)
		totalAgentIn := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
		contextSize := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens + m.OutputTokens
		slog.Info("tui:agent_usage", "delta_in", m.InputTokens, "delta_out", m.OutputTokens, "total_in", a.status.usage.TotalInputTokens(), "total_out", a.status.usage.OutputTokens, "context_size", contextSize)
		a.repl.UpdateAgentUsage(m.ParentToolUseID, totalAgentIn, m.OutputTokens, contextSize)
		return true, a.readEvents()

	case queryEndMsg:
		// Display user-friendly message for abort errors
		displayErr := m.Err
		if displayErr != nil {
			if ae, ok := errors.AsType[*engine.AbortError](displayErr); ok {
				switch ae.Phase {
				case "streaming":
					displayErr = fmt.Errorf("query cancelled during streaming")
				case "tools":
					displayErr = fmt.Errorf("query cancelled during tool execution")
				default:
					displayErr = fmt.Errorf("query cancelled (%s)", ae.Phase)
				}
			}
		}
		slog.Info("tui:queryEnd", "err", displayErr)
		a.repl.FinishStream(displayErr)

		// Sync status bar with engine's final ContextTokens (post-compact).
		// During streaming the bar showed the API-reported value; compact may
		// have reduced the context after that.
		if a.engine.ContextTokens > 0 {
			a.displayedInputTokens = a.engine.ContextTokens
			a.inputTokenTarget = a.engine.ContextTokens
			a.status.SetContext(a.engine.ContextTokens, a.engine.ContextWindow())
		}

		if !a.progressStart.IsZero() {
			// Use engine's accumulated TotalUsage for stats line (correct
			// across multi-turn queries). Fall back to streaming usage if
			// engine didn't provide accumulated data.
			queryUsage := m.TotalUsage
			if queryUsage.InputTokens == 0 && queryUsage.OutputTokens == 0 {
				queryUsage = a.status.usage
			}
			elapsedStr := formatElapsed(a.progressStart)
			tokensStr := fmt.Sprintf("↑%s ↓%s tokens", formatTokenCount(queryUsage.TotalInputTokens()), formatTokenCount(queryUsage.OutputTokens))
			var cachePart string
			if queryUsage.CacheReadInputTokens > 0 || queryUsage.CacheCreationInputTokens > 0 {
				total := queryUsage.CacheReadInputTokens + queryUsage.CacheCreationInputTokens + queryUsage.InputTokens
				if total > 0 {
					if queryUsage.CacheReadInputTokens > 0 {
						pct := queryUsage.CacheReadInputTokens * 100 / total
						cachePart = fmt.Sprintf(" · %d%% cached", pct)
					} else {
						cachePart = fmt.Sprintf(" · %s warmed", formatTokenCount(queryUsage.CacheCreationInputTokens))
					}
				}
			} else {
				cachePart = " · cache missed"
			}
			var toolsPart string
			if tc := a.repl.toolCount; tc > 0 {
				if tc == 1 {
					toolsPart = " · 1 tool"
				} else {
					toolsPart = fmt.Sprintf(" · %d tools", tc)
				}
			}
			statsLine := styleDim.Render(tokensStr + cachePart + toolsPart + " · " + elapsedStr)
			// Embed stats as a block in the last assistant message.
			// This is TUI-only — messages are not sent to the LLM.
			if msg := a.repl.lastMsg(); msg != nil {
				msg.Blocks = append(msg.Blocks, ContentBlock{Type: BlockStats, Text: statsLine})
				slog.Info("tui:query_end", "total_in", queryUsage.TotalInputTokens(), "total_out", queryUsage.OutputTokens, "cache_read", queryUsage.CacheReadInputTokens, "cache_creation", queryUsage.CacheCreationInputTokens, "committedCount", a.committedCount, "totalMessages", len(a.repl.messages))
			}
		}
		a.progressStart = time.Time{}
		a.thinkingActive = false
		a.thinkingDuration = 0

		// Persist successful turn to short-term memory.
		// Only when err==nil — Ctrl+C and error paths do NOT persist,
		// ensuring no partial/interrupted state is stored.
		if m.Err == nil {
			a.persistTurn()
		}

		// Don't commit yet — keep current turn in Bubble Tea view so
		// Ctrl+O (expand/collapse tool output) remains interactive.
		// Commit happens when the user submits the next query.
		a.contentCache = ""
		a.contentDirty = false
		// Keep listening for Hub events while idle (Path B: fork agent
		// notifications). readEvents blocks on appCh.
		return true, a.readEvents()

	case usageMsg:
		// Accumulate billing cost from each turn's message_delta.
		a.status.usage.InputTokens += m.InputTokens
		a.status.usage.OutputTokens += m.OutputTokens
		a.status.usage.CacheReadInputTokens += m.CacheReadInputTokens
		a.status.usage.CacheCreationInputTokens += m.CacheCreationInputTokens
		// Input tokens arrive all at once — snap immediately
		totalIn := a.status.usage.TotalInputTokens()
		a.displayedInputTokens = totalIn
		a.inputTokenTarget = totalIn
		a.outputTokenTarget = a.status.usage.OutputTokens
		contextSize := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens + m.OutputTokens
		a.status.SetContext(contextSize, a.engine.ContextWindow())
		slog.Info("tui:usage", "delta_in", m.InputTokens, "delta_out", m.OutputTokens, "context_size", contextSize, "total_in", a.status.usage.TotalInputTokens(), "total_out", a.status.usage.OutputTokens, "cache_read", a.status.usage.CacheReadInputTokens, "cache_creation", a.status.usage.CacheCreationInputTokens)
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

	case permissionAskMsg:
		if m.event != nil {
			detail := extractDetail(m.event.ToolName, m.event.Input)
			ch := m.event.ResponseCh
			a.activeDialog = NewPermissionDialog(m.event, detail)
			a.activeDialog.width = a.width
			a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
				dialogDonePermission(d, ch)
				return a, a.readEvents()
			}
		}
		return true, a.readEvents()

	case notificationPendingMsg:
		if a.repl.IsStreaming() {
			// Path A handles it — runTurns drains queue each iteration
			return true, a.readEvents()
		}
		// Path B: idle — trigger ProcessNotifications
		ctx, cancel := context.WithCancel(context.Background())
		a.repl.cancelFunc = cancel
		a.engine.ProcessNotifications(ctx, a.systemPrompt)
		a.repl.StartQuery()
		a.status.SetStreaming(true)
		a.spinner.Start()
		a.progressStart = time.Now()
		a.thinkingActive = false
		a.thinkingDuration = 0
		a.status.SetUsage(types.Usage{})
		return true, a.readEvents()

	case idleAbortedMsg:
		// No-op: user submitted, new query already started
		return true, nil

	case infoMsg:
		a.status.SetInfo(string(m))
		return true, nil

	case errMsg:
		a.status.SetError(m.Err.Error())
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
			a.markViewportDirty()
			a.toolBlinkTick++
			if a.toolBlinkTick%3 == 0 {
				a.spinner.Tick()
			}
			a.toolBlink = (a.toolBlinkTick/5)%2 == 0
			// Animate displayed tokens toward actual values
			target := max(a.status.usage.TotalInputTokens(), a.inputTokenTarget)
			a.displayedInputTokens = animateTokenValue(a.displayedInputTokens, target)
			outputTarget := max(a.responseCharCount/4, a.outputTokenTarget)
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
	slog.Info("tui:query_start", "text", tool.TruncateRunes(text, 100), "text_len", len(text), "committedCount", a.committedCount, "totalMessages", len(a.repl.messages))
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
	// Check for slash commands before adding user message to engine.
	if cmd, ok := LookupSlashCommand(text); ok {
		a.history.Add(text)
		a.input.Reset()
		return a.handleSlashCommand(cmd, commitCmd)
	}

	a.repl.AddUserMessage(text)
	a.history.Add(text)
	a.input.Reset()
	a.scrollOffset = 0
	a.scrollTotal = 0
	a.userScrolled = false
	a.markViewportDirty()

	// Cancel any idle readEvents goroutine to prevent goroutine leak.
	if a.idleStop != nil {
		close(a.idleStop)
	}
	a.idleStop = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	a.repl.cancelFunc = cancel

	// events flow through Hub → TUIHandler → appCh
	a.engine.Query(ctx, text, a.systemPrompt)
	a.repl.StartQuery()
	a.status.SetStreaming(true)
	a.spinner.Start()
	a.progressStart = time.Now()
	a.thinkingActive = false
	a.thinkingDuration = 0
	a.status.SetUsage(types.Usage{})
	a.responseCharCount = 0
	a.displayedInputTokens = 0
	a.displayedOutputTokens = 0
	a.cacheReadTokens = 0
	a.cacheCreationTokens = 0
	// Estimate input tokens from context + user message text
	estimate := types.EstimateTokens(string(a.systemPrompt)) + types.EstimateTokens(text)
	for _, msg := range a.repl.Messages() {
		for _, blk := range msg.Blocks {
			if blk.Type == BlockText {
				estimate += types.EstimateTokens(blk.Text)
			}
		}
	}
	a.inputTokenTarget = estimate

	return tea.Batch(
		commitCmd,
		tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return spinnerTickMsg{}
		}),
		a.readEvents(),
	)
}

// readEvents reads the next event from TUIHandler.appCh.
// This is called as a tea.Cmd.
func (a *App) readEvents() tea.Cmd {
	return func() tea.Msg {
		if a.tuiHandler == nil {
			return queryEndMsg{}
		}
		select {
		case msg, ok := <-a.tuiHandler.appCh:
			if !ok {
				return queryEndMsg{}
			}
			return msg
		case <-a.idleStop:
			return idleAbortedMsg{}
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
