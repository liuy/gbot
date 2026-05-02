package tui

import (
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

const (
	handlerBufSize    = 1024           // channel buffer size
	coalesceWindow    = 100 * time.Millisecond // flush accumulated text every 100ms (matches TS)
)

// TUIHandler implements hub.EventHandler, bridging Hub events to bubbletea.
// Engine goroutine calls Hub.Dispatch which calls TUIHandler.Handle synchronously.
// Handle converts the event to a tea.Msg and writes to a buffered channel.
// readEvents() Cmd reads from this channel on the bubbletea side.
//
// High-frequency streaming events (text_delta, thinking_delta) are coalesced:
// small chunks accumulate in a buffer and flush when either:
//   - 100ms has elapsed since the last flush (time window, matching TS behavior)
//   - a non-streaming event arrives (preserves ordering)
type TUIHandler struct {
	appCh   chan tea.Msg
	dropped atomic.Int64

	// Coalescing buffers. Handle() is called from a single engine goroutine,
	// so no mutex is needed.
	textBuf     strings.Builder
	textAgent   *types.AgentMeta
	lastTextFlush time.Time
	thinkBuf    strings.Builder
	thinkAge    *types.AgentMeta
	lastThinkFlush time.Time
}

// NewTUIHandler creates a TUIHandler with a 1024-buffered channel.
func NewTUIHandler() *TUIHandler {
	now := time.Now()
	return &TUIHandler{
		appCh:         make(chan tea.Msg, handlerBufSize),
		lastTextFlush: now,
		lastThinkFlush: now,
	}
}

// Handle converts a Hub event to a bubbletea message and sends to appCh.
//
// All SSE events use blocking writes — this provides natural backpressure:
// if the TUI is slow, the engine goroutine waits for the channel to drain.
// Coalescing reduces channel writes by ~512x, so blocking is cheap.
//
// The only exception is permission_ask: it uses a 5s timeout with auto-deny
// to avoid blocking the engine forever if the TUI is unresponsive.
func (h *TUIHandler) Handle(event hub.Event) {
	msg := h.convertEventToMsg(event)
	if msg == nil {
		return
	}

	// --- Permission ask: blocking with timeout + auto-deny ---
	if permMsg, ok := msg.(permissionAskMsg); ok {
		h.flushAll()
		select {
		case h.appCh <- permMsg:
		case <-time.After(5 * time.Second):
			slog.Warn("TUIHandler: permission ask timed out, auto-denying")
			if permMsg.event != nil && permMsg.event.ResponseCh != nil {
				select {
				case permMsg.event.ResponseCh <- types.UserDecisionDeny:
				default:
				}
			}
		}
		return
	}

	// --- Coalesce text_delta ---
	if td, ok := msg.(textDeltaMsg); ok {
		h.textBuf.WriteString(td.Text)
		h.textAgent = td.Agent
		if time.Since(h.lastTextFlush) >= coalesceWindow {
			h.flushText()
		}
		return
	}

	// --- Coalesce thinking_delta ---
	if td, ok := msg.(thinkingDeltaMsg); ok {
		h.thinkBuf.WriteString(td.Text)
		h.thinkAge = td.Agent
		if time.Since(h.lastThinkFlush) >= coalesceWindow {
			h.flushThinking()
		}
		return
	}

	// --- All other SSE events: flush + blocking write (no timeout) ---
	h.flushAll()
	h.appCh <- msg
}

// Flush forces any accumulated text/thinking to be written to the channel.
// Useful for benchmarks and cleanup. No-op if nothing is buffered.
func (h *TUIHandler) Flush() {
	h.flushAll()
}

// Dropped returns the total number of events dropped due to a full buffer.
func (h *TUIHandler) Dropped() int64 {
	return h.dropped.Load()
}

func (h *TUIHandler) flushAll() {
	h.flushText()
	h.flushThinking()
}

func (h *TUIHandler) flushText() {
	if h.textBuf.Len() == 0 {
		return
	}
	text := h.textBuf.String()
	h.textBuf.Reset()
	agent := h.textAgent
	h.textAgent = nil
	h.lastTextFlush = time.Now()
	h.appCh <- textDeltaMsg{Text: text, Agent: agent}
}

func (h *TUIHandler) flushThinking() {
	if h.thinkBuf.Len() == 0 {
		return
	}
	text := h.thinkBuf.String()
	h.thinkBuf.Reset()
	agent := h.thinkAge
	h.thinkAge = nil
	h.lastThinkFlush = time.Now()
	h.appCh <- thinkingDeltaMsg{Text: text, Agent: agent}
}

// convertEventToMsg converts a types.QueryEvent to a bubbletea message.
// Returns nil for unhandled event types.
func (h *TUIHandler) convertEventToMsg(evt types.QueryEvent) tea.Msg {
	switch evt.Type {
	case types.EventNotificationPending:
		return notificationPendingMsg{}

	case types.EventTurnStart:
		return turnStartMsg{}

	case types.EventTextStart:
		return textStartMsg{Agent: evt.Agent}

	case types.EventTextDelta:
		return textDeltaMsg{Text: evt.Text, Agent: evt.Agent}

	case types.EventTextEnd:
		return textEndMsg{Agent: evt.Agent}

	case types.EventQueryStart:
		if evt.Message != nil {
			return streamMessageMsg{Role: string(evt.Message.Role)}
		}
		return nil

	case types.EventToolStart:
		if evt.ToolUse != nil {
			return toolStartMsg{
				ID:      evt.ToolUse.ID,
				Name:    evt.ToolUse.Name,
				Summary: evt.ToolUse.Summary,
				Input:   prettyJSON(evt.ToolUse.Input),
				Agent:   evt.Agent,
			}
		}

	case types.EventToolRun:
		if evt.ToolUse != nil {
			return toolRunMsg{
				ID:    evt.ToolUse.ID,
				Name:  evt.ToolUse.Name,
				Agent: evt.Agent,
			}
		}

	case types.EventToolEnd:
		if evt.ToolResult != nil {
			return toolEndMsg{
				ToolUseID: evt.ToolResult.ToolUseID,
					IsBackground: evt.ToolResult.IsBackground,
				Output:    evt.ToolResult.DisplayOutput,
				IsError:   evt.ToolResult.IsError,
				Timing:    evt.ToolResult.Timing,
				Agent:     evt.Agent,
			}
		}

	case types.EventError:
		return errMsg{Err: evt.Error}

	case types.EventUsage:
		if evt.Usage != nil {
			return usageMsg{
				InputTokens:              evt.Usage.InputTokens,
				OutputTokens:             evt.Usage.OutputTokens,
				CacheReadInputTokens:     evt.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: evt.Usage.CacheCreationInputTokens,
				Agent:                    evt.Agent,
			}
		}

	case types.EventThinkingStart:
		return thinkingStartMsg{Agent: evt.Agent}

	case types.EventThinkingDelta:
		if evt.Thinking != nil && evt.Thinking.Text != "" {
			return thinkingDeltaMsg{Text: evt.Thinking.Text, Agent: evt.Agent}
		}

	case types.EventThinkingEnd:
		if evt.Thinking != nil {
			return thinkingEndMsg{Duration: evt.Thinking.Duration, Agent: evt.Agent}
		}

	case types.EventQueryEnd:
		var totalUsage types.Usage
		if evt.Usage != nil {
			totalUsage = types.Usage{
				InputTokens:              evt.Usage.InputTokens,
				OutputTokens:             evt.Usage.OutputTokens,
				CacheReadInputTokens:     evt.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: evt.Usage.CacheCreationInputTokens,
			}
		}
		return queryEndMsg{Err: evt.Error, TotalUsage: totalUsage, Agent: evt.Agent}

	case types.EventToolParamDelta:
		// LLM streaming JSON input delta
		if evt.PartialInput != nil {
			return toolParamDeltaMsg{
				ID:      evt.PartialInput.ID,
				Delta:   evt.PartialInput.Delta,
				Summary: evt.PartialInput.Summary,
				Agent:   evt.Agent,
			}
		}
		return nil

	case types.EventToolOutputDelta:
		// Tool streaming output lines during execution
		if evt.ToolResult != nil && evt.ToolResult.DisplayOutput != "" {
			return toolOutputDeltaMsg{
				ToolUseID:     evt.ToolResult.ToolUseID,
				DisplayOutput: evt.ToolResult.DisplayOutput,
				Timing:        evt.ToolResult.Timing,
				Agent:         evt.Agent,
			}
		}
		return nil

	case types.EventTurnEnd:
		// Per-round end; TUI doesn't need to act on this currently.
		return nil

	case types.EventPermissionAsk:
		// Permission ask from any engine (main or sub).
		if evt.PermissionAsk != nil {
			return permissionAskMsg{event: evt.PermissionAsk}
		}
		return nil
	}

	return nil
}
