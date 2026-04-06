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
	"time"

	"github.com/user/gbot/pkg/llm"
	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

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
	tokenBudget int
	turnCount   int
}

// Config configures the engine.
type Config struct {
	Provider    llm.Provider
	Tools       []tool.Tool
	Model       string
	MaxTokens   int
	TokenBudget int
	Logger      *slog.Logger
}

// QueryResult is the final result of a query.
type QueryResult struct {
	Messages   []types.Message
	TurnCount  int
	TotalUsage types.Usage
	Terminal   types.TerminalReason
	Error      error
}

// New creates a new Engine.
func New(cfg *Config) *Engine {
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 16000
	}
	if cfg.TokenBudget == 0 {
		cfg.TokenBudget = 200000
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	toolMap := make(map[string]tool.Tool)
	var toolOrder []string
	for _, t := range cfg.Tools {
		toolMap[t.Name()] = t
		toolOrder = append(toolOrder, t.Name())
	}

	return &Engine{
		provider:    cfg.Provider,
		tools:       toolMap,
		toolOrder:   toolOrder,
		model:       cfg.Model,
		maxTokens:   cfg.MaxTokens,
		logger:      cfg.Logger,
		tokenBudget: cfg.TokenBudget,
	}
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
	eventCh <- types.QueryEvent{Type: types.EventMessage, Message: &userMsg}

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

		// Stage 14-15: API call streaming loop
		eventCh <- types.QueryEvent{Type: types.EventStreamStart}

		resp, err := e.callLLM(ctx, systemPrompt, eventCh)
		if err != nil {
			// Stage 16: Error handling
			action := e.handleStreamError(err)
			if !action.Continue {
				return QueryResult{
					Messages: e.messages,
					Terminal: e.classifyTerminalError(err),
					Error:    err,
				}
			}
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
		hasToolUse := false
		var toolUseBlocks []types.ContentBlock
		for _, block := range resp.Content {
			if block.Type == types.ContentTypeToolUse {
				hasToolUse = true
				toolUseBlocks = append(toolUseBlocks, block)
			}
		}

		if !hasToolUse {
			eventCh <- types.QueryEvent{Type: types.EventComplete}
			return QueryResult{
				Messages:   e.messages,
				TurnCount:  e.turnCount,
				TotalUsage: totalUsage,
				Terminal:   types.TerminalCompleted,
			}
		}

		// Stage 21: Tool execution (sequential in Phase 1)
		toolResultBlocks := e.executeTools(ctx, toolUseBlocks, eventCh)

		// Add tool results as user message
		e.messages = append(e.messages, types.Message{
			Role:    types.RoleUser,
			Content: toolResultBlocks,
		})

		// Stage 25-26: Turn counting and state transition
		e.turnCount++
		e.tokenBudget -= totalUsage.InputTokens + totalUsage.OutputTokens

		if e.tokenBudget <= 0 {
			e.logger.Warn("token budget exhausted")
			eventCh <- types.QueryEvent{Type: types.EventComplete}
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
func (e *Engine) callLLM(ctx context.Context, systemPrompt json.RawMessage, eventCh chan<- types.QueryEvent) (*types.Message, error) {
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
		return nil, fmt.Errorf("stream request: %w", err)
	}

	// Accumulate streaming response
	var contentBlocks []types.ContentBlock
	var currentText strings.Builder
	var currentToolInput strings.Builder
	var model string
	var stopReason string
	var usage types.Usage
	hasContent := false

	for event := range streamCh {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if event.Error != nil {
			return nil, event.Error
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				usage = event.Message.Usage
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				cb := *event.ContentBlock
				contentBlocks = append(contentBlocks, cb)
				if cb.Type == types.ContentTypeToolUse {
					currentToolInput.Reset()
					eventCh <- types.QueryEvent{
						Type: types.EventToolUseStart,
						ToolUse: &types.ToolUseEvent{
							ID:    cb.ID,
							Name:  cb.Name,
							Input: cb.Input,
						},
					}
				}
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					currentText.WriteString(event.Delta.Text)
					hasContent = true
					eventCh <- types.QueryEvent{
						Type: types.EventTextDelta,
						Text: event.Delta.Text,
					}
				case "input_json_delta":
					currentToolInput.WriteString(event.Delta.PartialJSON)
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
				}
			}

		case "message_delta":
			if event.DeltaMsg != nil {
				stopReason = event.DeltaMsg.StopReason
			}
			if event.Usage != nil {
				usage.OutputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			// Done

		case "ping":
			// Keepalive
		}
	}

	if hasContent && len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, types.NewTextBlock(currentText.String()))
	}

	return &types.Message{
		Role:       types.RoleAssistant,
		Content:    contentBlocks,
		Model:      model,
		StopReason: stopReason,
		Usage:      &usage,
		Timestamp:  time.Now(),
	}, nil
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
			eventCh <- types.QueryEvent{Type: types.EventToolResult, ToolResult: &evt}
			continue
		}

		outputJSON, _ := json.Marshal(result.Data)
		results = append(results, types.NewToolResultBlock(block.ID, outputJSON, false))
		eventCh <- types.QueryEvent{
			Type: types.EventToolResult,
			ToolResult: &types.ToolResultEvent{
				ToolUseID: block.ID,
				Output:    outputJSON,
				Timing:    elapsed,
			},
		}
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

// Reset clears the conversation history.
func (e *Engine) Reset() {
	e.messages = nil
	e.turnCount = 0
}
