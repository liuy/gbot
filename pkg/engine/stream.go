// Package engine implements the core agentic loop for gbot.
//
// Source reference: query.ts (~1730 lines), QueryEngine.ts
// Phase 1 simplifications:
//   - No context compression (hard token limit, oldest messages dropped)
//   - No session persistence across restarts
//   - Simple permission model (allow/deny/ask, no passthrough or classifier)
//   - No tool grouping or deferral (sequential execution only)
package engine

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// StreamAccumulator collects SSE streaming events into a complete Message.
// Source: query.ts — streaming API call accumulation inside the while(true) loop.
// TS accumulates content blocks, text deltas, tool input deltas, and usage
// across multiple SSE event types (message_start, content_block_start/delta/stop,
// message_delta, message_stop).
type StreamAccumulator struct {
	contentBlocks    []types.ContentBlock
	currentText      strings.Builder
	currentToolInput strings.Builder
	model            string
	stopReason       string
	usage            types.Usage
	thinkingStart    time.Time
	hasContent       bool
}

// NewStreamAccumulator creates a new accumulator.
func NewStreamAccumulator() *StreamAccumulator {
	return &StreamAccumulator{}
}

// ProcessEvent handles a single SSE event and returns true if the caller
// should emit it as a QueryEvent.
// Source: query.ts — the event switch inside the streaming accumulation loop.
func (a *StreamAccumulator) ProcessEvent(event llm.StreamEvent) (emitEvent *types.QueryEvent, err error) {
	if event.Error != nil {
		return nil, event.Error
	}

	switch event.Type {
	case "message_start":
		// Source: query.ts — message_start captures model and initial usage.
		if event.Message != nil {
			a.model = event.Message.Model
			a.usage = event.Message.Usage
			emitEvent = &types.QueryEvent{
				Type: types.EventUsage,
				Usage: &types.UsageEvent{
					InputTokens:  a.usage.InputTokens,
					OutputTokens: a.usage.OutputTokens,
				},
			}
		}

	case "content_block_start":
		// Source: query.ts — content_block_start appends a new content block
		// and resets the appropriate accumulator.
		if event.ContentBlock != nil {
			cb := *event.ContentBlock
			a.contentBlocks = append(a.contentBlocks, cb)
			switch cb.Type {
			case types.ContentTypeToolUse:
				a.currentToolInput.Reset()
				emitEvent = &types.QueryEvent{
					Type: types.EventToolUseStart,
					ToolUse: &types.ToolUseEvent{
						ID:    cb.ID,
						Name:  cb.Name,
						Input: cb.Input,
					},
				}
			case types.ContentTypeThinking:
				a.thinkingStart = time.Now()
				emitEvent = &types.QueryEvent{
					Type: types.EventThinkingStart,
				}
			}
		}

	case "content_block_delta":
		// Source: query.ts — text_delta appends to currentText and emits
		// EventTextDelta; input_json_delta appends to currentToolInput.
		if event.Delta != nil {
			switch event.Delta.Type {
			case "text_delta":
				a.currentText.WriteString(event.Delta.Text)
				a.hasContent = true
				emitEvent = &types.QueryEvent{
					Type: types.EventTextDelta,
					Text: event.Delta.Text,
				}
			case "input_json_delta":
				a.currentToolInput.WriteString(event.Delta.PartialJSON)
			case "thinking_delta":
				a.currentText.WriteString(event.Delta.Thinking)
			}
		}

	case "content_block_stop":
		// Source: query.ts — content_block_stop finalizes the block at
		// event.Index, writing accumulated text or tool input.
		idx := event.Index
		if idx < len(a.contentBlocks) {
			cb := &a.contentBlocks[idx]
			switch cb.Type {
			case types.ContentTypeText:
				cb.Text = a.currentText.String()
				a.currentText.Reset()
			case types.ContentTypeToolUse:
				cb.Input = json.RawMessage(a.currentToolInput.String())
				a.currentToolInput.Reset()
			case types.ContentTypeThinking:
				cb.Text = a.currentText.String()
				a.currentText.Reset()
				elapsed := time.Since(a.thinkingStart)
				emitEvent = &types.QueryEvent{
					Type: types.EventThinkingEnd,
					Thinking: &types.ThinkingEvent{
						Duration: elapsed,
					},
				}
			}
		}

	case "message_delta":
		// Source: query.ts — message_delta carries stop_reason and output usage.
		if event.DeltaMsg != nil {
			a.stopReason = event.DeltaMsg.StopReason
		}
		if event.Usage != nil {
			a.usage.OutputTokens = event.Usage.OutputTokens
			emitEvent = &types.QueryEvent{
				Type: types.EventUsage,
				Usage: &types.UsageEvent{
					InputTokens:  a.usage.InputTokens,
					OutputTokens: a.usage.OutputTokens,
				},
			}
		}

	case "message_stop":
		// Source: query.ts — message_stop signals end of this response.

	case "ping":
		// Source: query.ts — keepalive, ignored.

	}
	return emitEvent, nil
}

// BuildMessage constructs the final Message from accumulated state.
// Source: query.ts — after the streaming loop ends, the accumulated blocks
// and metadata are assembled into a Message.
func (a *StreamAccumulator) BuildMessage() *types.Message {
	if a.hasContent && len(a.contentBlocks) == 0 {
		a.contentBlocks = append(a.contentBlocks, types.NewTextBlock(a.currentText.String()))
	}

	return &types.Message{
		Role:       types.RoleAssistant,
		Content:    a.contentBlocks,
		Model:      a.model,
		StopReason: a.stopReason,
		Usage:      &a.usage,
	}
}

// HasToolUse checks if any accumulated content block is a tool_use.
// Source: query.ts — after streaming, check if the response contains tool calls
// to decide whether to enter the tool execution path (Stage 21).
func (a *StreamAccumulator) HasToolUse() bool {
	for _, cb := range a.contentBlocks {
		if cb.Type == types.ContentTypeToolUse {
			return true
		}
	}
	return false
}

// ToolUseBlocks returns all tool_use content blocks.
func (a *StreamAccumulator) ToolUseBlocks() []types.ContentBlock {
	var blocks []types.ContentBlock
	for _, cb := range a.contentBlocks {
		if cb.Type == types.ContentTypeToolUse {
			blocks = append(blocks, cb)
		}
	}
	return blocks
}
