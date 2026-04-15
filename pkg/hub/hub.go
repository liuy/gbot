// Package hub provides callback-based synchronous event routing.
//
// Hub sits between Engine and UI clients (TUI, WebSocket, Discord, etc).
// Engine goroutine calls Dispatch synchronously for each event;
// Hub iterates all registered handlers and calls Handle in the same call stack.
// Hub has no goroutines and no channels — it is purely a routing layer.
package hub

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/types"
)

// Event is the event type dispatched by Engine through Hub.
type Event = types.QueryEvent

// EventHandler is implemented by UI clients to receive Engine events.
type EventHandler interface {
	Handle(event Event)
}

// Hub routes events from Engine to all subscribed handlers.
// It is safe for concurrent use (Dispatch and Subscribe are mutex-protected).
type Hub struct {
	mu       sync.Mutex
	handlers []EventHandler
}

// NewHub creates a new Hub with no handlers.
func NewHub() *Hub {
	return &Hub{}
}

// Subscribe adds a handler and returns an unsubscribe function.
// Calling the returned function removes the handler from future Dispatch calls.
func (h *Hub) Subscribe(handler EventHandler) func() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.handlers = append(h.handlers, handler)
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for i, hd := range h.handlers {
			if hd == handler {
				h.handlers = append(h.handlers[:i], h.handlers[i+1:]...)
				return
			}
		}
	}
}

// Dispatch sends an event to all registered handlers synchronously.
// Handlers are called in subscription order.
// If a handler panics, Dispatch recovers and continues to remaining handlers.
func (h *Hub) Dispatch(event Event) {
	logEngineEvent(event)

	h.mu.Lock()
	handlers := make([]EventHandler, len(h.handlers))
	copy(handlers, h.handlers)
	h.mu.Unlock()

	for _, handler := range handlers {
		func() {
			defer func() {
				_ = recover()
			}()
			handler.Handle(event)
		}()
	}
}

// Close removes all handlers.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers = nil
}

// logEngineEvent logs structured info about every engine event to /tmp/gbot-debug.log.
// This provides comprehensive observability for diagnosing token stats, event ordering,
// and rendering issues without needing verbose mode.
func logEngineEvent(event Event) {
	switch event.Type {
	case types.EventTextDelta:
		preview := truncateRunes(event.Text, 60)
		slog.Info("engine:text_delta", "text", preview)

	case types.EventToolStart:
		if event.ToolUse != nil {
			slog.Info("engine:tool_start", "id", event.ToolUse.ID, "name", event.ToolUse.Name, "summary", event.ToolUse.Summary)
		}

	case types.EventToolParamDelta:
		if event.PartialInput != nil {
			preview := event.PartialInput.Delta
			if len(preview) > 80 {
				preview = preview[:80] + "..."
			}
			slog.Info("engine:tool_param_delta", "id", event.PartialInput.ID, "delta", preview, "summary", event.PartialInput.Summary)
		}

	case types.EventToolOutputDelta:
		if event.ToolResult != nil {
			lines := strings.Count(event.ToolResult.DisplayOutput, "\n") + 1
			slog.Info("engine:tool_output_delta", "id", event.ToolResult.ToolUseID, "lines", lines)
		}

	case types.EventToolEnd:
		if event.ToolResult != nil {
			outputLen := len(event.ToolResult.DisplayOutput)
			slog.Info("engine:tool_end", "id", event.ToolResult.ToolUseID, "isError", event.ToolResult.IsError, "outputLen", outputLen, "timing", event.ToolResult.Timing)
		}

	case types.EventUsage:
		if event.Usage != nil {
			slog.Info("engine:usage", "inputTokens", event.Usage.InputTokens, "outputTokens", event.Usage.OutputTokens)
		}

	case types.EventTurnStart:
		slog.Info("engine:turn_start")

	case types.EventTurnEnd:
		slog.Info("engine:turn_end")

	case types.EventQueryEnd:
		slog.Info("engine:query_end")

	case types.EventQueryStart:
		if event.Message != nil {
			blockCount := len(event.Message.Content)
			slog.Info("engine:query_start", "role", string(event.Message.Role), "blocks", blockCount)
		}

	case types.EventThinkingStart:
		slog.Info("engine:thinking_start")

	case types.EventThinkingEnd:
		if event.Thinking != nil {
			slog.Info("engine:thinking_end", "duration", event.Thinking.Duration)
		}

	case types.EventError:
		errMsg := fmt.Sprintf("%v", event.Error)
		slog.Info("engine:error", "error", errMsg)

	default:
		slog.Info("engine:unknown", "type", event.Type)
	}
}

// truncateRunes truncates s to at most maxRunes runes, appending "..." if truncated.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
