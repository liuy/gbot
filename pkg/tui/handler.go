package tui

import (
	"log/slog"
	"sync/atomic"

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
func (h *TUIHandler) Handle(event hub.Event) {
	msg := h.convertEventToMsg(event)
	if msg == nil {
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
	case types.EventTurnStart:
		return turnStartMsg{}

	case types.EventTextDelta:
		return textDeltaMsg{Text: evt.Text}

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
			}
		}

		case types.EventToolEnd:
			if evt.ToolResult != nil {
				return toolEndMsg{
					ToolUseID: evt.ToolResult.ToolUseID,
					Output:    evt.ToolResult.DisplayOutput,
					IsError:   evt.ToolResult.IsError,
					Timing:    evt.ToolResult.Timing,
				}
			}

	case types.EventError:
		return errMsg{Err: evt.Error}

	case types.EventUsage:
		if evt.Usage != nil {
			return usageMsg{
				InputTokens:  evt.Usage.InputTokens,
				OutputTokens: evt.Usage.OutputTokens,
			}
		}

	case types.EventThinkingStart:
		return thinkingStartMsg{}

	case types.EventThinkingDelta:
		if evt.Thinking != nil && evt.Thinking.Text != "" {
			return thinkingDeltaMsg{Text: evt.Thinking.Text}
		}

	case types.EventThinkingEnd:
		if evt.Thinking != nil {
			return thinkingEndMsg{Duration: evt.Thinking.Duration}
		}

	case types.EventQueryEnd:
		return queryEndMsg{}

	case types.EventToolParamDelta:
		// LLM streaming JSON input delta
		if evt.PartialInput != nil {
			return toolParamDeltaMsg{
				ID:      evt.PartialInput.ID,
				Delta:   evt.PartialInput.Delta,
				Summary: evt.PartialInput.Summary,
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
			}
		}
		return nil

	case types.EventTurnEnd:
		// Per-round end; TUI doesn't need to act on this currently.
		return nil
	}

	return nil
}
