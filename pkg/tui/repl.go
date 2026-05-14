package tui

import (
	"bytes"
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

// updateToolBlock finds the tool block with the given ID across all messages
// (not just lastMsg) and replaces its ToolCallView. Returns false if not found.
func (s *ReplState) updateToolBlock(id string, tcv *ToolCallView) bool {
	for i := len(s.messages) - 1; i >= 0; i-- {
		if updateToolBlockInBlocks(&s.messages[i].Blocks, id, tcv) {
			return true
		}
	}
	return false
}

// updateToolBlockInBlocks recursively searches and updates a ToolCallView by ID.
func updateToolBlockInBlocks(blocks *[]ContentBlock, id string, tcv *ToolCallView) bool {
	for i := len(*blocks) - 1; i >= 0; i-- {
		blk := &(*blocks)[i]
		if blk.Type == BlockTool {
			if blk.ToolCall.ID == id {
				blk.ToolCall = *tcv
				return true
			}
			if len(blk.ToolCall.Blocks) > 0 {
				if updateToolBlockInBlocks(&blk.ToolCall.Blocks, id, tcv) {
					return true
				}
			}
		}
	}
	return false
}

// findToolView returns the ToolCallView for the given tool ID.
// Searches pendingTool first, then all messages backwards (most recent first).
// Recursively searches nested Blocks within agent tool calls.
func (s *ReplState) findToolView(id string) *ToolCallView {
	// 1. Top-level pending tools
	if tcv, ok := s.pendingTool[id]; ok {
		return tcv
	}
	// 2. Search pending tools' nested Blocks (child agent tools)
	for _, tcv := range s.pendingTool {
		if found := findToolViewInBlocks(tcv.Blocks, id); found != nil {
			return found
		}
	}
	// 3. Search messages recursively
	for i := len(s.messages) - 1; i >= 0; i-- {
		if found := findToolViewInBlocks(s.messages[i].Blocks, id); found != nil {
			return found
		}
	}
	return nil
}

// findToolViewInBlocks recursively searches for a ToolCallView by ID within ContentBlocks.
func findToolViewInBlocks(blocks []ContentBlock, id string) *ToolCallView {
	for i := len(blocks) - 1; i >= 0; i-- {
		blk := &blocks[i]
		if blk.Type == BlockTool {
			if blk.ToolCall.ID == id {
				return &blk.ToolCall
			}
			if len(blk.ToolCall.Blocks) > 0 {
				if found := findToolViewInBlocks(blk.ToolCall.Blocks, id); found != nil {
					return found
				}
			}
		}
	}
	return nil
}

// trimBlocks keeps the last 50 non-thinking blocks.
// Thinking blocks do not count toward the limit.
func (s *ReplState) trimBlocks(tcv *ToolCallView) {
	const maxBlocks = 50
	if len(tcv.Blocks) <= maxBlocks {
		return
	}
	tcv.Blocks = tcv.Blocks[len(tcv.Blocks)-maxBlocks:]
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

// formatToolDisplayName converts raw tool names to user-facing display names.
// MCP tools: "mcp__fetch__get_raw_text" → "fetch - get_raw_text (MCP)"
// Built-in tools: returned as-is.
func formatToolDisplayName(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 3 {
		return name
	}
	return parts[1] + " - " + parts[2] + " (MCP)"
}

// PendingToolStarted records a new in-progress tool call.
func (s *ReplState) PendingToolStarted(id, name, summary, input string) {
	m := s.lastMsg()
	if m == nil {
		return
	}
	tcv := &ToolCallView{ID: id, Name: formatToolDisplayName(name), Summary: summary, Input: input, Done: false}
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
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				if len(parent.Blocks) > 0 && parent.Blocks[len(parent.Blocks)-1].Type == BlockText {
					parent.Blocks[len(parent.Blocks)-1].Text += m.Text
				} else {
					parent.Blocks = append(parent.Blocks, ContentBlock{Type: BlockText, Text: m.Text})
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.AppendChunk(m.Text)
		}
		a.responseCharCount += len(m.Text)
		return true, a.readEvents()
	case textStartMsg:
		return true, a.readEvents()

	case textEndMsg:
		// No-op: text content block finished.
		return true, a.readEvents()

	case toolRunMsg:
		// No-op: tool execution started after input accumulation.
		// Sub-agent tool_run is informational; tool_start already added the entry.
		return true, a.readEvents()

	case turnStartMsg:
		a.markViewportDirty()
		a.repl.AppendTextItem()
		return true, a.readEvents()

	case retryAttemptMsg:
		a.retryActive = true
		a.retryAttempt = m.Attempt
		a.retryMax = m.MaxRetries
		a.retryRemaining = time.Duration(m.RetryInMs) * time.Millisecond
		a.retryStart = time.Now()
		a.retryErrorType = m.ErrorType
		a.markViewportDirty()
		return true, a.readEvents()

	case streamMessageMsg:
		a.markViewportDirty()
		return true, a.readEvents()

	case toolStartMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				parent.Blocks = append(parent.Blocks, ContentBlock{
					Type: BlockTool,
					ToolCall: ToolCallView{
						ID: m.ID, Name: m.Name, Summary: m.Summary,
						Input: m.Input, Done: false,
					},
				})
				parent.ToolCount++
				if parent.AgentType == "" && m.Agent.AgentType != "" {
					parent.AgentType = m.Agent.AgentType
				}
				a.repl.trimBlocks(parent)
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingToolStarted(m.ID, m.Name, m.Summary, m.Input)
			if m.Name == "Agent" {
				a.repl.SetAgentContextWindow(m.ID, a.engine.ContextWindow())
			}
		}
		slog.Info("tui:tool_start", "id", m.ID, "name", m.Name, "summary", m.Summary, "agent", m.Agent != nil)
		return true, a.readEvents()

	case toolParamDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil && m.Summary != "" {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && !parent.Blocks[i].ToolCall.Done {
						parent.Blocks[i].ToolCall.Summary = m.Summary
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingToolDelta(m.ID, m.Delta, m.Summary)
		}
		a.responseCharCount += len(m.Delta)
		return true, a.readEvents()

	case toolOutputDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && parent.Blocks[i].ToolCall.ID == m.ToolUseID {
						parent.Blocks[i].ToolCall.Output = m.DisplayOutput
						if m.Timing > parent.Blocks[i].ToolCall.Elapsed {
							parent.Blocks[i].ToolCall.Elapsed = m.Timing
						}
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
		a.repl.PendingToolOutput(m.ToolUseID, m.DisplayOutput, m.Timing)
		}
		return true, a.readEvents()

	case toolEndMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockTool && !parent.Blocks[i].ToolCall.Done {
						parent.Blocks[i].ToolCall.Done = true
						parent.Blocks[i].ToolCall.IsError = m.IsError
						parent.Blocks[i].ToolCall.Output = m.Output
						parent.Blocks[i].ToolCall.Elapsed = m.Timing
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			if m.IsBackground {
				// Fork agent: keep running state, just set IsBackground flag.
				// The card stays Done=false until the fork agent's sub-engine
				// dispatches queryEndMsg, which marks it Done.
				tcv := a.repl.findToolView(m.ToolUseID)
				if tcv != nil {
					tcv.IsBackground = true
					tcv.Output = m.Output
					a.repl.updateToolBlock(m.ToolUseID, tcv)
				} else {
					slog.Warn("tui:background_tool_not_found", "id", m.ToolUseID)
				}
			} else {
				a.repl.PendingToolDone(m.ToolUseID, m.Output, m.IsError, m.Timing)
			}
			a.taskListDirty = true
		}
		if m.IsError {
			slog.Error("tui:tool_error", "id", m.ToolUseID, "output", m.Output)
		}
		slog.Info("tui:tool_end", "id", m.ToolUseID, "isError", m.IsError, "outputLen", len(m.Output), "agent", m.Agent != nil)
		return true, a.readEvents()

	case queryEndMsg:
		// Sub-agent queryEnd: do NOT finish the main query stream.
		// Only the main engine's queryEnd (Agent == nil) should trigger FinishStream.
		// sub-engine's EventQueryEnd was flowing through without agent metadata,
		// causing FinishStream to cancel the main query's context mid-loop.
		if m.Agent != nil {
			// Fork agent completed: mark parent card as Done.
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil && parent.IsBackground && !parent.Done {
				parent.Done = true
				if start, ok := a.repl.pendingToolStart[m.Agent.ParentToolUseID]; ok {
					parent.Elapsed = time.Since(start)
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
			return true, a.readEvents()
		}

		// For abort errors, append inline interrupt message and finish cleanly.
		// The engine already added [Request interrupted by user] to the
		// assistant message content; we mirror it in the TUI display.
		displayErr := m.Err
		if displayErr != nil {
			if _, ok := errors.AsType[*engine.AbortError](displayErr); ok {
				a.repl.AppendChunk(types.InterruptMessage)
				displayErr = nil

				// Auto-rewind: if no meaningful response was produced, restore state
				if m.Agent == nil {
					if a.tryAutoRewind() {
						slog.Info("tui:auto_rewind", "reason", "no_meaningful_response")
					}
				}
			}
		}
		slog.Info("tui:queryEnd", "err", displayErr)
		a.repl.FinishStream(displayErr)

		// Sync status bar with engine's final ContextTokens (post-compact).
		// During streaming the bar showed the API-reported value; compact may
		// have reduced the context after that.
		if ct := a.engine.GetContextTokens(); ct > 0 {
			a.displayedInputTokens = ct
			a.inputTokenTarget = ct
			a.status.SetContext(ct, a.engine.ContextWindow())
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
			tokensStr := fmt.Sprintf("↑%s ↓%s tokens", types.FormatTokenCount(queryUsage.TotalInputTokens()), types.FormatTokenCount(queryUsage.OutputTokens))
			var cachePart string
			if queryUsage.CacheReadInputTokens > 0 || queryUsage.CacheCreationInputTokens > 0 {
				total := queryUsage.CacheReadInputTokens + queryUsage.CacheCreationInputTokens + queryUsage.InputTokens
				if total > 0 {
					if queryUsage.CacheReadInputTokens > 0 {
						pct := queryUsage.CacheReadInputTokens * 100 / total
						cachePart = fmt.Sprintf(" · %d%% cached", pct)
					} else {
						cachePart = fmt.Sprintf(" · %s warmed", types.FormatTokenCount(queryUsage.CacheCreationInputTokens))
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
		// Sub-agent: also accumulate into parent tool call stats
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				totalAgentIn := m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
				parent.TokensIn += totalAgentIn
				parent.TokensOut += m.OutputTokens
				parent.ContextSize = contextSize
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		}
		return true, a.readEvents()

	case thinkingStartMsg:
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				parent.Blocks = append(parent.Blocks, ContentBlock{
					Type:     BlockThinking,
				})
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.thinkingActive = true
			a.thinkingStart = time.Now()
			a.markViewportDirty()
			a.repl.PendingThinkingStarted()
		}
		return true, a.readEvents()

	case thinkingDeltaMsg:
		a.markViewportDirty()
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockThinking && !parent.Blocks[i].Thinking.Done {
						parent.Blocks[i].Thinking.Text += m.Text
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.repl.PendingThinkingDelta(m.Text)
		}
		return true, a.readEvents()

	case thinkingEndMsg:
		if m.Agent != nil {
			parent := a.repl.findToolView(m.Agent.ParentToolUseID)
			if parent != nil {
				for i := len(parent.Blocks) - 1; i >= 0; i-- {
					if parent.Blocks[i].Type == BlockThinking && !parent.Blocks[i].Thinking.Done {
						parent.Blocks[i].Thinking.Done = true
						parent.Blocks[i].Thinking.Duration = m.Duration
						break
					}
				}
				a.repl.updateToolBlock(m.Agent.ParentToolUseID, parent)
			}
		} else {
			a.thinkingActive = false
			a.thinkingDuration = m.Duration
			a.markViewportDirty()
			a.repl.PendingThinkingDone(m.Duration)
		}
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

	case inputAskMsg:
		if m.event != nil {
			if a.activeInput != nil && !a.activeInput.done {
				sendDecision(a.activeInput.result, types.AskResponse{Aborted: true})
			}
			a.activeInput = NewInputDialog(
				m.event.Prompt,
				m.event.Masked,
				m.event.Deadline,
				m.event.ResponseCh,
			)
			return true, a.activeInput.Init()
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
		a.pasteStore = make(map[int]string)
		a.nextPasteID = 1
		return a.handleSlashCommand(cmd, commitCmd)
	}

	a.repl.AddUserMessage(text)
	a.history.Add(text)
	a.input.Reset()
	a.pasteStore = make(map[int]string)
	a.nextPasteID = 1
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
	// Estimate input tokens: use engine's precise ContextTokens (from last
	// API usage) as the base, only estimate the new user message text.
	// Falls back to system prompt estimation on the first turn (cold start).
	base := a.engine.GetContextTokens()
	if base == 0 {
		base = types.EstimateTokens(string(a.systemPrompt))
	}
	a.inputTokenTarget = base + types.EstimateTokens(text)

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
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "  ", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
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
