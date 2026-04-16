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
	"sort"
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
	tools         map[string]tool.Tool
	toolOrder     []string
	toolsProvider func() map[string]tool.Tool
	model       string
	maxTokens   int
	logger      *slog.Logger
	messages    []types.Message
	tokenBudget    int
	turnCount      int
	dispatcher     EventDispatcher
	notifications  *notificationQueue

	// isSubagent is true for sub-agent engines created by AgentTool.
	// Sub-agents bypass token budget exhaustion checks, matching TS behavior
	// where agentId presence disables budget tracking.
	// Source: tokenBudget.ts:45-53 — checkTokenBudget skips when agentId is set.
	isSubagent bool

	// maxTurns is the maximum number of agentic turns before stopping.
	// Default: 50. Sub-engines may override via SubEngineOptions.
	maxTurns int
}

// Params holds the constructor arguments for Engine.
type Params struct {
	Provider    llm.Provider
	Tools       []tool.Tool                    // static tool list (ignored if ToolsProvider is set)
	ToolsProvider func() map[string]tool.Tool  // dynamic tool resolution — called each turn
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

	// Resolve initial tools: prefer dynamic provider, fall back to static slice.
	var toolMap map[string]tool.Tool
	var toolsProvider func() map[string]tool.Tool
	if p.ToolsProvider != nil {
		toolsProvider = p.ToolsProvider
		toolMap = p.ToolsProvider()
	} else {
		toolMap = make(map[string]tool.Tool)
		for _, t := range p.Tools {
			toolMap[t.Name()] = t
		}
	}
	var toolOrder []string
	for name := range toolMap {
		toolOrder = append(toolOrder, name)
	}
	sort.Strings(toolOrder)

	return &Engine{
		provider:      p.Provider,
		tools:         toolMap,
		toolOrder:     toolOrder,
		toolsProvider: toolsProvider,
		model:         p.Model,
		maxTokens:     p.MaxTokens,
		logger:        p.Logger,
		tokenBudget:   p.TokenBudget,
		dispatcher:    p.Dispatcher,
		notifications: &notificationQueue{},
		maxTurns:      50,
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
	if eventCh != nil {
		eventCh <- event
	}
	// Both nil (sub-engine): silently discard — result returned via QueryResult
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

	for e.turnCount < e.maxTurns {
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

		if e.tokenBudget <= 0 && !e.isSubagent {
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
	e.refreshTools()
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

	e.logger.Info("callLLM tools registered", "count", len(toolDefs), "names", func() string {
		var names []string
		for _, td := range toolDefs {
			names = append(names, td.Name)
		}
		return strings.Join(names, ",")
	}())

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
			if streamingExecutor != nil {
				streamingExecutor.Discard()
			}
			return nil, nil, ctx.Err()
		default:
		}

		if event.Error != nil {
			e.logger.Error("stream event error", "error", event.Error)
			if streamingExecutor != nil {
				streamingExecutor.Discard()
			}
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
							Name:    currentToolName,
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
						InputTokens:  event.Usage.InputTokens,
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
		if streamingExecutor != nil {
			streamingExecutor.Discard()
		}
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

// refreshTools rebuilds the tool map and order from the provider if set.
// Called at the start of each callLLM so late-registered tools are visible.
func (e *Engine) refreshTools() {
	if e.toolsProvider == nil {
		return
	}
	e.tools = e.toolsProvider()
	e.toolOrder = make([]string, 0, len(e.tools))
	for name := range e.tools {
		e.toolOrder = append(e.toolOrder, name)
	}
	sort.Strings(e.toolOrder)
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

// ---------------------------------------------------------------------------
// TaggedDispatcher — wraps parent dispatcher to inject AgentMeta into sub-agent events
// ---------------------------------------------------------------------------

// taggedDispatcher wraps an EventDispatcher and injects AgentMeta into every event.
// Used by sub-engines so their tool events reach the parent TUI with agent context.
type taggedDispatcher struct {
	parent EventDispatcher
	meta   *types.AgentMeta
}

func (d *taggedDispatcher) Dispatch(event types.QueryEvent) {
	event.Agent = d.meta
	d.parent.Dispatch(event)
}

// ---------------------------------------------------------------------------
// Sub-engine support — source: tools/AgentTool/runAgent.ts:330-500
// ---------------------------------------------------------------------------

// SubEngineOptions configures the creation of a sub-engine for agent execution.
type SubEngineOptions struct {
	SystemPrompt    string               // sub-agent's system prompt
	Tools           map[string]tool.Tool  // filtered tool set
	MaxTurns        int                   // 0 = default 50
	Model           string               // "" = inherit from parent
	ParentToolUseID string               // parent Agent tool call ID for event tagging
	AgentType       string               // "general-purpose", "Explore", "Plan"
}

// NewSubEngine creates a new Engine that shares the Provider and Logger
// with the parent but has fully independent state (messages, tools, budget).
// Source: runAgent.ts:330-500 — runAgent setup phase
func (e *Engine) NewSubEngine(opts SubEngineOptions) *Engine {
	model := e.model
	if opts.Model != "" {
		model = opts.Model
	}

	// Build toolOrder from the filtered tool set
	var toolOrder []string
	for name := range opts.Tools {
		toolOrder = append(toolOrder, name)
	}
	sort.Strings(toolOrder)

	// If parent has a dispatcher, wrap it to tag sub-agent events.
	var dispatcher EventDispatcher
	if e.dispatcher != nil && opts.ParentToolUseID != "" {
		parentDepth := 0 // TODO: track depth for nested agents
		dispatcher = &taggedDispatcher{
			parent: e.dispatcher,
			meta: &types.AgentMeta{
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
				Depth:           parentDepth,
			},
		}
	}

	return &Engine{
		provider:      e.provider,
		tools:         opts.Tools,
		toolOrder:     toolOrder,
		model:         model,
		maxTokens:     e.maxTokens,
		logger:        e.logger,
		messages:      []types.Message{},
		tokenBudget:   0, // sub-agents bypass budget checks via isSubagent
		turnCount:     0,
		dispatcher:    dispatcher,
		notifications: &notificationQueue{},
		isSubagent:    true,
		maxTurns:      subMaxTurns(opts.MaxTurns),
	}
}

// QuerySync executes the agentic loop synchronously (no goroutine, no channels).
// Used by sub-agents created via AgentTool. EventCh is nil — events are silently discarded.
// Source: TS sync sub-agents execute runAgent() directly in the caller's context.
func (e *Engine) QuerySync(ctx context.Context, userMessage string, systemPrompt json.RawMessage) QueryResult {
	return e.queryLoop(ctx, userMessage, systemPrompt, nil)
}

// subMaxTurns returns the max turns for a sub-engine.
// 0 or negative means use parent default (50).
func subMaxTurns(n int) int {
	if n <= 0 {
		return 50
	}
	return n
}
