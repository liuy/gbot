package tui

import (
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/hub"
	"github.com/liuy/gbot/pkg/types"
)

const (
	handlerBufSize = 1024 // channel buffer size
)

// TUIHandler implements hub.EventHandler, bridging Hub events to bubbletea.
// Engine goroutine calls Hub.Dispatch which calls TUIHandler.Handle synchronously.
// Handle converts the event to a tea.Msg and writes to a buffered channel.
// readEvents() Cmd reads from this channel on the bubbletea side.
//
// Two modes:
//   - Active engine: drainFn == nil. Events are written to appCh; App.readEvents
//     drains appCh on the bubbletea goroutine, producing tea.Msgs.
//   - Background engine: drainFn != nil. Events bypass appCh and call drainFn
//     synchronously on the engine goroutine. drainFn mutates the engine's own
//     ReplState without producing tea.Msgs, so the TUI doesn't redraw for
//     background activity (status bar polling reads it instead).
//
// Coalescing is done per-engine in Engine.emitEvent — this handler is a pure
// pass-through.
type TUIHandler struct {
	appCh    chan tea.Msg
	dropped  atomic.Int64
	engineID string

	// drainMu guards drainFn. switchEngine swaps the drain function under
	// this mutex so the engine goroutine never observes a half-flipped state.
	drainMu sync.RWMutex
	drainFn func(tea.Msg)
}

// NewTUIHandler creates a TUIHandler with a 1024-buffered channel for the
// active engine. Equivalent to NewTUIHandlerForEngine("", nil).
func NewTUIHandler() *TUIHandler {
	return NewTUIHandlerForEngine("", nil)
}

// NewTUIHandlerForEngine creates a TUIHandler tagged with an engine ID.
// drainFn != nil switches the handler into background-drain mode (see TUIHandler
// doc comment). Switching back to active mode happens via SetDrainFn(nil).
func NewTUIHandlerForEngine(engineID string, drainFn func(tea.Msg)) *TUIHandler {
	return &TUIHandler{
		appCh:    make(chan tea.Msg, handlerBufSize),
		engineID: engineID,
		drainFn:  drainFn,
	}
}

// EngineID returns the engine ID tag assigned at construction.
func (h *TUIHandler) EngineID() string { return h.engineID }

// SetDrainFn swaps the drain function. Called by switchEngine when the active
// engine changes: the previously-active engine gets a background drainFn, and
// the newly-active engine's drainFn is cleared so events flow to appCh again.
func (h *TUIHandler) SetDrainFn(fn func(tea.Msg)) {
	h.drainMu.Lock()
	h.drainFn = fn
	h.drainMu.Unlock()
}

// Drain backlog from appCh by passing each queued msg through fn. Used by
// switchEngine to flush the newly-active engine's stale events through the
// drain function before flipping it to nil (so no events are lost).
func (h *TUIHandler) DrainBacklog(fn func(tea.Msg)) {
	for {
		select {
		case m, ok := <-h.appCh:
			if !ok {
				return
			}
			fn(m)
		default:
			return
		}
	}
}

// Handle converts a Hub event to a bubbletea message and sends to appCh.
//
// All SSE events use blocking writes — this provides natural backpressure:
// if the TUI is slow, the engine goroutine waits for the channel to drain.
//
// The only exception is permission_ask: it uses a 5s timeout with auto-deny
// to avoid blocking the engine forever if the TUI is unresponsive.
func (h *TUIHandler) Handle(event hub.Event) {
	msg := h.convertEventToMsg(event)
	if msg == nil {
		return
	}

	// Background engine: bypass appCh entirely, drain synchronously.
	h.drainMu.RLock()
	drain := h.drainFn
	h.drainMu.RUnlock()
	if drain != nil {
		drain(msg)
		return
	}

	switch m := msg.(type) {
	case permissionAskMsg:
		select {
		case h.appCh <- m:
		case <-time.After(5 * time.Second):
			slog.Warn("TUIHandler: permission ask timed out, auto-denying")
			if m.event != nil && m.event.ResponseCh != nil {
				select {
				case m.event.ResponseCh <- types.AskResponse{Decision: types.DecisionDeny}:
				default:
				}
			}
		}
	case inputAskMsg:
		select {
		case h.appCh <- m:
		case <-time.After(60 * time.Second):
			slog.Warn("TUIHandler: input ask timed out, auto-aborting")
			if m.event != nil && m.event.ResponseCh != nil {
				select {
				case m.event.ResponseCh <- types.AskResponse{Aborted: true}:
				default:
				}
			}
		}
	default:
		h.appCh <- msg
	}
}

// convertEventToMsg converts a types.QueryEvent to a bubbletea message.
// convertEventToMsg converts a types.QueryEvent to a bubbletea message.
// Returns nil for unhandled event types.
func (h *TUIHandler) convertEventToMsg(evt types.QueryEvent) tea.Msg {
	switch evt.Type {
	case types.EventAttachment:
		if evt.Message == nil || evt.Message.Attachment == nil {
			return nil
		}
		att := evt.Message.Attachment
		if att.Mode == types.ItemModePrompt {
			return attachmentMsg{
				UserText:   att.Prompt,
				SourceUUID: att.SourceUUID,
				Agent:      evt.Agent,
			}
		}
		xml := att.Prompt
		status := extractXMLField(xml, "status")
		return attachmentMsg{
			JobID:   extractXMLField(xml, "job-id"),
			Preview: extractXMLField(xml, "summary"),
			Failed:  status == "failed" || status == "killed",
			Agent:   evt.Agent,
		}

	case types.EventTurnStart:
		return turnStartMsg{Agent: evt.Agent}

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
				ID:       evt.ToolUse.ID,
				Name:     evt.ToolUse.Name,
				Summary:  evt.ToolUse.Summary,
				Input:    prettyJSON(evt.ToolUse.Input),
				Agent:    evt.Agent,
				IsSearch: evt.ToolUse.IsSearch,
				IsRead:   evt.ToolUse.IsRead,
				IsList:   evt.ToolUse.IsList,
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
				IsSearch:  evt.ToolResult.IsSearch,
				IsRead:    evt.ToolResult.IsRead,
				IsList:    evt.ToolResult.IsList,
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
				ID:       evt.PartialInput.ID,
				Delta:    evt.PartialInput.Delta,
				Summary:  evt.PartialInput.Summary,
				Agent:    evt.Agent,
				IsSearch: evt.PartialInput.IsSearch,
				IsRead:   evt.PartialInput.IsRead,
				IsList:   evt.PartialInput.IsList,
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

	case types.EventRetryAttempt:
		if evt.RetryAttempt != nil {
			return retryAttemptMsg{
				Attempt:    evt.RetryAttempt.Attempt,
				MaxRetries: evt.RetryAttempt.MaxRetries,
				RetryInMs:  evt.RetryAttempt.RetryInMs,
				Error:      evt.RetryAttempt.Error,
				ErrorType:  string(evt.RetryAttempt.ErrorType),
			}
		}

	case types.EventAsk:
		// Dispatch by Kind: permission vs interactive input.
		if evt.Ask != nil {
			switch evt.Ask.Kind {
			case types.AskPermission:
				return permissionAskMsg{event: evt.Ask}
			case types.AskInput:
				return inputAskMsg{event: evt.Ask}
			}
		}
		return nil
	}

	return nil
}

// extractXMLField extracts the text content of a simple XML tag and unescapes
// XML entities. E.g. extractXMLField("<job-id>bg-1</job-id>", "job-id") returns "bg-1".
func extractXMLField(xml, tag string) string {
	start := "<" + tag + ">"
	end := "</" + tag + ">"
	i := strings.Index(xml, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	j := strings.Index(xml[i:], end)
	if j < 0 {
		return ""
	}
	return unescapeXML(xml[i : i+j])
}

// unescapeXML reverses the escaping done by escapeXML in background.go.
func unescapeXML(s string) string {
	s = strings.ReplaceAll(s, "&apos;", "'")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return s
}
