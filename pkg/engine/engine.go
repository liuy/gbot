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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// EventDispatcher is the interface for routing events from Engine to consumers.
// Engine depends on this abstraction rather than a concrete Hub type,
// following the Dependency Inversion Principle.
// *hub.Hub satisfies this interface.
type EventDispatcher interface {
	Dispatch(event types.QueryEvent)
}

// Engine is the core agentic loop.
// Source: QueryEngine.ts — outer orchestrator + query.ts inner loop.
type Engine struct {
	provider    llm.Provider
	tools       map[string]tool.Tool
	toolOrder   []string
	model       string
	maxTokens   int
	logger      *slog.Logger
	messages    []types.Message
	tokenBudget    int
	turnCount      int
	dispatcher     EventDispatcher
	notifications  *notificationQueue
}

// Params holds the constructor arguments for Engine.
type Params struct {
	Provider    llm.Provider
	Tools       []tool.Tool
	Model       string
	MaxTokens   int
	TokenBudget int
	Logger      *slog.Logger
	Dispatcher  EventDispatcher
}

// QueryResult is the final result of a query.
type QueryResult struct {
	Messages   []types.Message
	TurnCount  int
	TotalUsage types.Usage
	Terminal   types.TerminalReason
	Error      error
}

// notificationQueue is a thread-safe FIFO of messages to be injected
// into the conversation on the next queryLoop iteration.
// Source: TS commandQueue with enqueuePendingNotification priority system.
type notificationQueue struct {
	mu       sync.Mutex
	messages []types.Message
}

func (q *notificationQueue) Enqueue(msg types.Message) {
	q.mu.Lock()
	q.messages = append(q.messages, msg)
	q.mu.Unlock()
}

func (q *notificationQueue) Drain() []types.Message {
	q.mu.Lock()
	pending := q.messages
	q.messages = nil
	q.mu.Unlock()
	return pending
}

// New creates a new Engine.
func New(p *Params) *Engine {
	if p.MaxTokens == 0 {
		p.MaxTokens = 16000
	}
	if p.TokenBudget == 0 {
		p.TokenBudget = 200000
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}

	toolMap := make(map[string]tool.Tool)
	var toolOrder []string
	for _, t := range p.Tools {
		toolMap[t.Name()] = t
		toolOrder = append(toolOrder, t.Name())
	}

	return &Engine{
		provider:      p.Provider,
		tools:         toolMap,
		toolOrder:     toolOrder,
		model:         p.Model,
		maxTokens:     p.MaxTokens,
		logger:        p.Logger,
		tokenBudget:   p.TokenBudget,
		dispatcher:    p.Dispatcher,
		notifications: &notificationQueue{},
	}
}

// EnqueueNotification adds a message to the notification queue.
// Thread-safe: may be called from any goroutine.
// The message will be injected at the start of the next queryLoop iteration.
func (e *Engine) EnqueueNotification(msg types.Message) {
	e.notifications.Enqueue(msg)
}

// Query executes the agentic loop for a user message.
// Source: query.ts:queryLoop() — the while(true) agentic loop.
// Returns a channel of streaming events and a channel for the final result.
func (e *Engine) Query(ctx context.Context, userMessage string, systemPrompt json.RawMessage) (<-chan types.QueryEvent, <-chan QueryResult) {
	eventCh := make(chan types.QueryEvent, 128)
	resultCh := make(chan QueryResult, 1)

	go func() {
		defer close(eventCh)
		defer close(resultCh)
		result := e.queryLoop(ctx, userMessage, systemPrompt, eventCh)
		resultCh <- result
	}()

	return eventCh, resultCh
}

// emitEvent sends an event via Hub (if set) or the event channel.
// When Hub is present, it is the authoritative path — eventCh is skipped
// to avoid unbounded buffering and potential deadlocks from undrained channels.
func (e *Engine) emitEvent(eventCh chan<- types.QueryEvent, event types.QueryEvent) {
	if e.dispatcher != nil {
		e.dispatcher.Dispatch(event)
		return
	}
	eventCh <- event
}

// queryLoop is the main agentic loop.
// Source: query.ts — the while(true) loop with 28 stages.
func (e *Engine) queryLoop(ctx context.Context, userMessage string, systemPrompt json.RawMessage, eventCh chan<- types.QueryEvent) QueryResult {
	// Stage 0: Process user input
	userMsg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock(userMessage),
		},
		Timestamp: time.Now(),
	}
	e.messages = append(e.messages, userMsg)
	e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &userMsg})

	var totalUsage types.Usage
	maxTurns := 50

	for e.turnCount < maxTurns {
		select {
		case <-ctx.Done():
			return QueryResult{
				Messages: e.messages,
				Terminal: types.TerminalAbortedStreaming,
				Error:    ctx.Err(),
			}
		default:
		}

		// Drain pending notifications (stall alerts, completion notifications
		// from background tasks). Source: TS drains commandQueue at query start.
		if pending := e.notifications.Drain(); len(pending) > 0 {
			e.messages = append(e.messages, pending...)
			for i := range pending {
				e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
			}
		}

		// Stage 14-15: API call streaming loop
		e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnStart})

		resp, streamingExecutor, err := e.callLLM(ctx, systemPrompt, eventCh)
		if err != nil {
			// Stage 16: Error handling
			action := e.handleStreamError(err)
			if !action.Continue {
				e.logger.Error("callLLM error (terminal)", "error", err, "turn", e.turnCount)
				return QueryResult{
					Messages: e.messages,
					Terminal: e.classifyTerminalError(err),
					Error:    err,
				}
			}
			e.logger.Warn("callLLM error (retryable)", "error", err, "turn", e.turnCount)
			continue
		}

		// Accumulate usage
		if resp.Usage != nil {
			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens
		}

		// Add assistant message to history
		e.messages = append(e.messages, *resp)

		// Stage 20: No-tool-use terminal path
		if streamingExecutor == nil {
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnEnd})
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryEnd})
			return QueryResult{
				Messages:   e.messages,
				TurnCount:  e.turnCount,
				TotalUsage: totalUsage,
				Terminal:   types.TerminalCompleted,
			}
		}

		// Stage 21: Wait for stream-started tools to complete, collect results.
		// Source: query.ts:1381 — getRemainingResults().
		toolResultBlocks := streamingExecutor.ExecuteAll(nil)

		// Add tool results as user message
		e.messages = append(e.messages, types.Message{
			Role:    types.RoleUser,
			Content: toolResultBlocks,
		})

		// End of this streaming round
		e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnEnd})

		// Stage 25-26: Turn counting and state transition
		e.turnCount++
		e.tokenBudget -= totalUsage.InputTokens + totalUsage.OutputTokens

		if e.tokenBudget <= 0 {
			e.logger.Warn("token budget exhausted")
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnEnd})
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryEnd})
			return QueryResult{
				Messages:   e.messages,
				TurnCount:  e.turnCount,
				TotalUsage: totalUsage,
				Terminal:   types.TerminalPromptTooLong,
			}
		}
	}

	return QueryResult{
		Messages:   e.messages,
		TurnCount:  e.turnCount,
		TotalUsage: totalUsage,
		Terminal:   types.TerminalCompleted,
	}
}

// callLLM sends the messages to the LLM and collects the full response.
// Source: query.ts — streaming API call accumulation.
func (e *Engine) callLLM(ctx context.Context, systemPrompt json.RawMessage, eventCh chan<- types.QueryEvent) (*types.Message, *StreamingToolExecutor, error) {
	// Build tool definitions for API
	var toolDefs []llm.ToolDef
	for _, name := range e.toolOrder {
		t, ok := e.tools[name]
		if !ok || !t.IsEnabled() {
			continue
		}
		schema := t.InputSchema()
		desc, err := t.Description(nil)
		if err != nil {
			desc = t.Name()
		}
		toolDefs = append(toolDefs, llm.ToolDef{
			Name:        t.Name(),
			Description: desc,
			InputSchema: schema,
		})
	}

	// Marshal messages for the API request
	apiMessages := e.marshalMessages()

	req := &llm.Request{
		Model:     e.model,
		MaxTokens: e.maxTokens,
		Messages:  apiMessages,
		System:    systemPrompt,
		Tools:     toolDefs,
		Stream:    true,
	}

	streamCh, err := e.provider.Stream(ctx, req)
	if err != nil {
		e.logger.Error("stream request failed", "error", err)
		return nil, nil, fmt.Errorf("stream request: %w", err)
	}

	// Accumulate streaming response
	var contentBlocks []types.ContentBlock
	var currentText strings.Builder
	var currentToolInput strings.Builder
	var currentToolID string
	var currentToolName string
	var model string
	var stopReason string
	var usage types.Usage
	var thinkingStart time.Time

	// StreamingToolExecutor — lazily created on first tool_use block.
	// Source: query.ts:562-568 — executor created before streaming.
	// Source: query.ts:841-843 — addTool called as each tool_use completes.
	var streamingExecutor *StreamingToolExecutor
	hasContent := false
	streamComplete := false

	for event := range streamCh {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		default:
		}

		if event.Error != nil {
			e.logger.Error("stream event error", "error", event.Error)
			return nil, nil, event.Error
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				usage = event.Message.Usage
				e.emitEvent(eventCh, types.QueryEvent{
					Type: types.EventUsage,
					Usage: &types.UsageEvent{
						InputTokens:  usage.InputTokens,
						OutputTokens: usage.OutputTokens,
					},
				})
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				cb := *event.ContentBlock
				contentBlocks = append(contentBlocks, cb)
				switch cb.Type {
				case types.ContentTypeToolUse:
					currentToolID = cb.ID
					currentToolName = cb.Name
					currentToolInput.Reset()
					summary := e.computeSummary(cb.Name, cb.Input)
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventToolStart,
						ToolUse: &types.ToolUseEvent{
							ID:      cb.ID,
							Name:    cb.Name,
							Input:   cb.Input,
							Summary: summary,
						},
					})
				case types.ContentTypeThinking:
					thinkingStart = time.Now()
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventThinkingStart,
					})
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					currentText.WriteString(event.Delta.Text)
					hasContent = true
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventTextDelta,
						Text: event.Delta.Text,
					})
				case "input_json_delta":
					currentToolInput.WriteString(event.Delta.PartialJSON)
					accumulated := currentToolInput.String()
					summary := e.computeSummary(currentToolName, json.RawMessage(accumulated))
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventToolParamDelta,
						PartialInput: &types.PartialInputEvent{
							ID:      currentToolID,
							Delta:   event.Delta.PartialJSON,
							Summary: summary,
						},
					})
				case "thinking_delta":
					currentText.WriteString(event.Delta.Thinking)
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventThinkingDelta,
						Thinking: &types.ThinkingEvent{
							Text: event.Delta.Thinking,
						},
					})
				}
			}

		case "content_block_stop":
			idx := event.Index
			if idx < len(contentBlocks) {
				cb := &contentBlocks[idx]
				switch cb.Type {
				case types.ContentTypeText:
					cb.Text = currentText.String()
					currentText.Reset()
				case types.ContentTypeToolUse:
					cb.Input = json.RawMessage(currentToolInput.String())
					currentToolInput.Reset()
					// Source: query.ts:841-843 — addTool as soon as input is complete.
					// Tools begin executing during LLM streaming, not after.
					if streamingExecutor == nil {
						streamingExecutor = NewStreamingToolExecutor(
							e.tools, nil,
							func(evt types.QueryEvent) { e.emitEvent(eventCh, evt) },
							ctx,
						)
					}
					streamingExecutor.AddTool(*cb)
				case types.ContentTypeThinking:
					cb.Text = currentText.String()
					currentText.Reset()
					elapsed := time.Since(thinkingStart)
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventThinkingEnd,
						Thinking: &types.ThinkingEvent{
							Duration: elapsed,
						},
					})
				}
			}

		case "message_delta":
			if event.DeltaMsg != nil {
				stopReason = event.DeltaMsg.StopReason
			}
			if event.Usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
				e.emitEvent(eventCh, types.QueryEvent{
					Type: types.EventUsage,
					Usage: &types.UsageEvent{
						InputTokens:  0, // omitted — already emitted in message_start
						OutputTokens: usage.OutputTokens,
					},
				})
			}

		case "message_stop":
			// Done
			streamComplete = true

		case "ping":
			// Keepalive
		}
	}

	if hasContent && len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, types.NewTextBlock(currentText.String()))
	}

	// Detect interrupted stream: content was received but stream never completed.
	if hasContent && !streamComplete {
		e.logger.Error("stream interrupted", "contentBlocks", len(contentBlocks), "model", model)
		return nil, nil, fmt.Errorf("stream interrupted: response incomplete (no stop_reason received)")
	}

	return &types.Message{
		Role:       types.RoleAssistant,
		Content:    contentBlocks,
		Model:      model,
		StopReason: stopReason,
		Usage:      &usage,
		Timestamp:  time.Now(),
	}, streamingExecutor, nil
}

// executeTools runs tool calls sequentially.
// Source: query.ts:runTools() + StreamingToolExecutor.ts
func (e *Engine) executeTools(ctx context.Context, toolUseBlocks []types.ContentBlock, eventCh chan<- types.QueryEvent) []types.ContentBlock {
	var results []types.ContentBlock

	for _, block := range toolUseBlocks {
		if block.Type != types.ContentTypeToolUse {
			continue
		}

		t, ok := e.tools[block.Name]
		if !ok {
			errJSON, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("unknown tool: %s", block.Name)})
			results = append(results, types.NewToolResultBlock(block.ID, errJSON, true))
			continue
		}

		start := time.Now()

		// Try streaming execution first (ToolWithStreaming interface).
		// Source: StreamingToolExecutor.ts — ExecuteStream with onProgress callbacks.
		if streamer, ok := t.(tool.ToolWithStreaming); ok {
			var lastDisplayOutput string
			result, err := streamer.ExecuteStream(ctx, block.Input, nil, func(u tool.ProgressUpdate) {
				// Emit streaming output to TUI as each progress update arrives.
				// Source: StreamingToolExecutor.ts:onProgress callback → EventToolOutputDelta.
				if len(u.Lines) > 0 {
					display := strings.Join(u.Lines, "\n")
					lastDisplayOutput = display
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventToolOutputDelta,
						ToolResult: &types.ToolResultEvent{
							ToolUseID:     block.ID,
							DisplayOutput: display,
							Timing:        time.Since(start),
						},
					})
				}
			})
			elapsed := time.Since(start)

			if err != nil {
				errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
				evt := types.ToolResultEvent{
					ToolUseID: block.ID,
					Output:    errJSON,
					IsError:   true,
					Timing:    elapsed,
				}
				results = append(results, types.NewToolResultBlock(block.ID, errJSON, true))
				evt.DisplayOutput = err.Error()
				e.emitEvent(eventCh, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &evt})
				continue
			}

			rawJSON, _ := json.Marshal(result.Data)
			outputJSON, _ := json.Marshal(string(rawJSON))
			results = append(results, types.NewToolResultBlock(block.ID, outputJSON, false))
			displayOutput := t.RenderResult(result.Data)
			// If the tool produced streaming output, include it in the final event too.
			if displayOutput == "" && lastDisplayOutput != "" {
				displayOutput = lastDisplayOutput
			}
			e.emitEvent(eventCh, types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     block.ID,
					Output:        outputJSON,
					DisplayOutput: displayOutput,
					Timing:        elapsed,
				},
			})
			continue
		}

		// Fallback: non-streaming Call().
		// Source: StreamingToolExecutor.ts — fallback path for tools without ExecuteStream.
		result, err := t.Call(ctx, block.Input, nil)
		elapsed := time.Since(start)

		if err != nil {
			errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
			evt := types.ToolResultEvent{
				ToolUseID: block.ID,
				Output:    errJSON,
				IsError:   true,
				Timing:    elapsed,
			}
			results = append(results, types.NewToolResultBlock(block.ID, errJSON, true))
			evt.DisplayOutput = err.Error()
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventToolEnd, ToolResult: &evt})
			continue
		}

		rawJSON, _ := json.Marshal(result.Data)
		outputJSON, _ := json.Marshal(string(rawJSON))
		results = append(results, types.NewToolResultBlock(block.ID, outputJSON, false))
		displayOutput := t.RenderResult(result.Data)
		e.emitEvent(eventCh, types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     block.ID,
				Output:        outputJSON,
				DisplayOutput: displayOutput,
				Timing:        elapsed,
			},
		})
	}

	return results
}

// handleStreamError determines the action for a streaming error.
func (e *Engine) handleStreamError(err error) types.LoopAction {
	if llm.IsRetryable(err) {
		return types.LoopAction{Continue: true, Reason: types.ContinueNextTurn}
	}
	return types.LoopAction{Continue: false}
}

// computeSummary returns a human-readable summary for a tool invocation.
// Uses tool.Description() when available, falls back to partial JSON extraction.
func (e *Engine) computeSummary(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	// Try tool.Description() first (works for complete JSON)
	if t, ok := e.tools[name]; ok {
		if desc, err := t.Description(input); err == nil && desc != "" {
			return desc
		}
	}
	// Fallback: extract from partial JSON via string matching
	return extractSummaryFromPartial(name, string(input))
}

// extractSummaryFromPartial extracts a summary from partial JSON using string matching.
// Handles incomplete JSON where full unmarshal fails.
func extractSummaryFromPartial(name, partial string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", "")
	switch normalized {
	case "Bash", "shell":
		return extractJSONStringField(partial, "command", "", 30)
	case "Read", "Write", "Edit", "fileread", "filewrite", "fileedit":
		return extractJSONStringField(partial, "file_path", "", 40)
	case "Glob", "Grep", "fileglob", "searchcode":
		return extractJSONStringField(partial, "pattern", "", 40)
	}
	return ""
}

// extractJSONStringField extracts a string field value from potentially incomplete JSON.
func extractJSONStringField(jsonStr, fieldName, prefix string, maxLen int) string {
	key := `"` + fieldName + `"`
	idx := strings.Index(jsonStr, key)
	if idx < 0 {
		return ""
	}
	rest := jsonStr[idx+len(key):]
	colonIdx := strings.Index(rest, ":")
	if colonIdx < 0 {
		return ""
	}
	valueStart := colonIdx + 1
	for valueStart < len(rest) && (rest[valueStart] == ' ' || rest[valueStart] == '\n' || rest[valueStart] == '\t') {
		valueStart++
	}
	if valueStart >= len(rest) || rest[valueStart] != '"' {
		return ""
	}
	valueStart++
	valueEnd := valueStart
	for valueEnd < len(rest) && rest[valueEnd] != '"' && rest[valueEnd] != ',' && rest[valueEnd] != '}' {
		valueEnd++
	}
	value := rest[valueStart:valueEnd]
	if value == "" {
		return ""
	}
	if len(value) > maxLen {
		value = value[:maxLen] + "..."
	}
	return prefix + value
}

// classifyTerminalError maps an error to a terminal reason.
func (e *Engine) classifyTerminalError(err error) types.TerminalReason {
	if llm.IsContextOverflow(err) {
		return types.TerminalPromptTooLong
	}
	if llm.IsRateLimit(err) {
		return types.TerminalBlockingLimit
	}
	return types.TerminalModelError
}

// marshalMessages converts internal messages to API format.
func (e *Engine) marshalMessages() []types.Message {
	return e.messages
}

// EscapeJSONString escapes a string for JSON embedding.
func EscapeJSONString(s string) string {
	var buf bytes.Buffer
	json.HTMLEscape(&buf, []byte(s))
	result := buf.String()
	if len(result) >= 2 && result[0] == '"' && result[len(result)-1] == '"' {
		return result[1 : len(result)-1]
	}
	return result
}

// AddSystemMessage adds a system message to the conversation.
func (e *Engine) AddSystemMessage(content string) {
	e.messages = append(e.messages, types.Message{
		Role: types.RoleSystem,
		Content: []types.ContentBlock{
			types.NewTextBlock(content),
		},
		Timestamp: time.Now(),
	})
}

// Messages returns the current message history.
func (e *Engine) Messages() []types.Message {
	return e.messages
}

// Tools returns the tool map used by the engine.
func (e *Engine) Tools() map[string]tool.Tool {
	return e.tools
}

// MaxTokens returns the max tokens setting.
func (e *Engine) MaxTokens() int {
	return e.maxTokens
}

// TokenBudget returns the token budget setting.
func (e *Engine) TokenBudget() int {
	return e.tokenBudget
}

// Reset clears the conversation history.
func (e *Engine) Reset() {
	e.messages = nil
	e.turnCount = 0
}
