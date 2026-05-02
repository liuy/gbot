package tui

import (
	"log/slog"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

// TUIHandler implements hub.EventHandler, bridging Hub events to bubbletea.
// Engine goroutine calls Hub.Dispatch which calls TUIHandler.Handle synchronously.
// Handle converts the event to a tea.Msg and writes to a buffered channel.
// readEvents() Cmd reads from this channel on the bubbletea side.
type TUIHandler struct {
	appCh   chan tea.Msg
	dropped atomic.Int64
}

// NewTUIHandler creates a TUIHandler with a 256-buffered channel.
func NewTUIHandler() *TUIHandler {
	return &TUIHandler{
		appCh: make(chan tea.Msg, 256),
	}
}

// Handle converts a Hub event to a bubbletea message and sends to appCh.
// Non-blocking: drops events if the buffer is full, incrementing the dropped counter.
// Exception: permission ask events use a blocking write with timeout (修正 12),
// because dropping them would leave the engine goroutine blocked forever.
func (h *TUIHandler) Handle(event hub.Event) {
	msg := h.convertEventToMsg(event)
	if msg == nil {
		return
	}

	// 修正 12: Permission ask events MUST be delivered.
	// Block with timeout; if TUI is unresponsive, auto-deny to unblock engine.
	if permMsg, ok := msg.(permissionAskMsg); ok {
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

	select {
	case h.appCh <- msg:
	default:
		h.dropped.Add(1)
		slog.Warn("TUIHandler: event dropped, buffer full", "eventType", event.Type)
	}
}

// Dropped returns the total number of events dropped due to a full buffer.
func (h *TUIHandler) Dropped() int64 {
	return h.dropped.Load()
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
