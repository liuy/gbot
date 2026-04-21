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
	"slices"
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

// Compactor is the interface for auto-compact operations.
// The engine calls this when it detects token usage approaching limits
// (proactive) or when the API returns a context overflow error (reactive).
// TS align: autoCompact.ts + reactiveCompact.ts
type Compactor interface {
	Compact(ctx context.Context, messages []types.Message) ([]types.Message, error)
}

// AutoCompactConfig configures auto-compact behavior.
// TS align: autoCompact.ts configuration
type AutoCompactConfig struct {
	// Threshold is the ratio (0.0-1.0) of estimated context usage that triggers
	// proactive auto-compact. Default: 0.9 (90% of context window).
	Threshold float64
	// ContextWindow is the model's maximum context window in tokens.
	// If 0, proactive auto-compact is disabled.
	ContextWindow int
	// MaxConsecutiveFailures is the number of consecutive compact failures before
	// the circuit breaker trips and stops attempting proactive auto-compact. Default: 3.
	MaxConsecutiveFailures int
}

// Engine is the core agentic loop.
// Source: QueryEngine.ts — outer orchestrator + query.ts inner loop.
type Engine struct {
	provider       llm.Provider
	tools          map[string]tool.Tool
	toolOrder      []string
	toolsProvider  func() map[string]tool.Tool
	model          string
	maxTokens      int
	logger         *slog.Logger
	mu             sync.RWMutex
	messages       []types.Message
	sessionID      string
	tokenBudget    int
	turnCount      int
	dispatcher     EventDispatcher
	notifications  *notificationQueue
	systemPrompt   json.RawMessage // stored system prompt for fork agent access

	// isSubagent is true for sub-agent engines created by AgentTool.
	// Sub-agents bypass token budget exhaustion checks, matching TS behavior
	// where agentId presence disables budget tracking.
	// Source: tokenBudget.ts:45-53 — checkTokenBudget skips when agentId is set.
	isSubagent bool

	// agentType is the sub-agent type (e.g. "General", "Explore", "Plan").
	// Empty for the main engine. Set by NewSubEngine from SubEngineOptions.AgentType.
	agentType string

	// maxTurns is the maximum number of agentic turns before stopping.
	// Default: 50. Sub-engines may override via SubEngineOptions.
	maxTurns int

	// Auto-compact fields
	compactor   Compactor
	autoCompactConfig          AutoCompactConfig
	consecutiveCompactFailures int
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
	Compactor   Compactor
	AutoCompact AutoCompactConfig
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
		provider:         p.Provider,
		tools:            toolMap,
		toolOrder:        toolOrder,
		toolsProvider:    toolsProvider,
		model:            p.Model,
		maxTokens:        p.MaxTokens,
		logger:           p.Logger,
		tokenBudget:      p.TokenBudget,
		dispatcher:       p.Dispatcher,
		notifications:    &notificationQueue{},
		maxTurns:         50,
		compactor:        p.Compactor,
		autoCompactConfig: p.AutoCompact,
	}
}

// EnqueueNotification adds a message to the notification queue.
// Thread-safe: may be called from any goroutine.
// The message will be injected at the start of the next queryLoop iteration.
func (e *Engine) EnqueueNotification(msg types.Message) {
	e.notifications.Enqueue(msg)
	// Signal TUI: notification available (Path B — between-turn re-query).
	// Mid-turn: ignored by TUI (runTurns drains queue, Path A).
	// Between-turn: triggers ProcessNotifications via notificationPendingMsg.
	if e.dispatcher != nil {
		e.dispatcher.Dispatch(types.QueryEvent{
			Type: types.EventNotificationPending,
		})
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

// ProcessNotifications drains pending notifications and runs the turn loop.
// This is Path B — equivalent to TS's between-turn new query() invocation.
// Unlike Query(), no userMessage is injected; the notifications themselves
// are the user-role messages fed into the conversation.
// Returns the same channels as Query() so the TUI can reuse the streaming pipeline.
func (e *Engine) ProcessNotifications(ctx context.Context, systemPrompt json.RawMessage) (<-chan types.QueryEvent, <-chan QueryResult) {
	eventCh := make(chan types.QueryEvent, 128)
	resultCh := make(chan QueryResult, 1)

	go func() {
		defer close(eventCh)
		defer close(resultCh)

		pending := e.notifications.Drain()
		if len(pending) == 0 {
			// Race guard: Hub event fired but queue already drained
			resultCh <- QueryResult{Terminal: types.TerminalCompleted}
			return
		}

		e.appendMessages(pending)
		for i := range pending {
			e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
		}

		result := e.runTurns(ctx, systemPrompt, eventCh)
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
	e.appendMessage(userMsg)
	e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &userMsg})

	return e.runTurns(ctx, systemPrompt, eventCh)
}

// runTurns executes the agentic turn loop. Shared by queryLoop (normal path)
// and QueryWithExistingMessages (fork agent path).
func (e *Engine) runTurns(ctx context.Context, systemPrompt json.RawMessage, eventCh chan<- types.QueryEvent) QueryResult {
	var totalUsage types.Usage
	// Log query summary on every exit path.
	defer func() {
		if totalUsage.InputTokens > 0 || totalUsage.OutputTokens > 0 {
			total := totalUsage.InputTokens + totalUsage.CacheReadInputTokens + totalUsage.CacheCreationInputTokens
			cacheStatus := "miss"
			if totalUsage.CacheReadInputTokens > 0 && total > 0 {
				pct := totalUsage.CacheReadInputTokens * 100 / total
				cacheStatus = fmt.Sprintf("hit %d%%", pct)
			} else if totalUsage.CacheCreationInputTokens > 0 {
				cacheStatus = "warm"
			}
			e.logger.Info("engine:query_summary",
				"input", totalUsage.InputTokens,
				"output", totalUsage.OutputTokens,
				"cache_read", totalUsage.CacheReadInputTokens,
				"cache_creation", totalUsage.CacheCreationInputTokens,
				"turns", e.turnCount,
				"cache", cacheStatus,
			)
		}
	}()
	reactiveCompactDone := false

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
			e.appendMessages(pending)
			for i := range pending {
				e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
			}
		}

		// Proactive auto-compact: check before API call.
		// TS align: query.ts:453-468 — deps.autocompact() before API.
		if e.shouldAutoCompact() {
			compacted, err := e.compactor.Compact(ctx, e.Messages())
			if err != nil {
				e.mu.Lock()
				e.consecutiveCompactFailures++
				failures := e.consecutiveCompactFailures
				e.mu.Unlock()
				e.logger.Warn("proactive auto-compact failed",
					"error", err,
					"consecutiveFailures", failures)
			} else {
				e.mu.Lock()
				e.consecutiveCompactFailures = 0
				e.mu.Unlock()
				e.setMessages(compacted)
				e.logger.Info("proactive auto-compact succeeded",
					"messages", len(compacted))
			}
		}

		// Stage 14-15: API call streaming loop
		e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnStart})

		resp, streamingExecutor, err := e.callLLM(ctx, systemPrompt, eventCh)
		if err != nil {
			// Reactive compact: try compact + retry on context overflow.
			// TS align: query.ts:1119-1175 — reactiveCompact.tryReactiveCompact()
			if e.compactor != nil && llm.IsContextOverflow(err) && !reactiveCompactDone {
				compacted, compactErr := e.compactor.Compact(ctx, e.Messages())
				if compactErr == nil {
					e.setMessages(compacted)
					reactiveCompactDone = true
					e.logger.Info("reactive auto-compact succeeded, retrying")
					continue
				}
				e.logger.Warn("reactive auto-compact failed", "error", compactErr)
			}

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
			totalUsage.CacheReadInputTokens += resp.Usage.CacheReadInputTokens
			totalUsage.CacheCreationInputTokens += resp.Usage.CacheCreationInputTokens
		}

		// Add assistant message to history
		e.appendMessage(*resp)

		// Populate conversation history on the executor so tools
		// (e.g. Agent tool) can access the full parent conversation.
		if streamingExecutor != nil {
			streamingExecutor.SetMessages(e.messages)
		}

		// Stage 20: No-tool-use terminal path
		if streamingExecutor == nil {
			// Before exiting, check if notifications arrived during this
			// turn. If so, inject them and continue the loop instead of
			// returning. Source: TS queryLoop checks commandQueue at each
			// iteration start; notifications arriving on the last turn are
			// handled by draining here and continuing.
			if pending := e.notifications.Drain(); len(pending) > 0 {
				e.appendMessages(pending)
				for i := range pending {
					e.emitEvent(eventCh, types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
				}
				e.emitEvent(eventCh, types.QueryEvent{Type: types.EventTurnEnd})
				continue
			}
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
		e.appendMessage(types.Message{
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

	e.logger.Info("callLLM:tools", "count", len(toolDefs), "names", func() string {
		var names []string
		for _, td := range toolDefs {
			names = append(names, td.Name)
		}
		return strings.Join(names, ",")
	}())

	// Marshal messages for the API request
	apiMessages := e.marshalMessages()

	// Enable prompt caching: wrap system prompt into structured blocks
	// so applyCacheControlToSystem can inject cache_control markers.
	// Source: claude.ts:1374-1376 — always on by default.
	var systemBlocks []llm.SystemBlockParam
	var cacheControl *llm.CacheControlConfig
	var promptStateKey *llm.PromptStateKey
	if len(systemPrompt) > 0 {
		var promptText string
		if err := json.Unmarshal(systemPrompt, &promptText); err == nil && promptText != "" {
			systemBlocks = []llm.SystemBlockParam{
				{Type: "text", Text: promptText},
			}
			if e.isSubagent {
				// Sub-agent: 5m TTL, agent-specific QuerySource
				// Source: promptCategory.ts:16-28 — getQuerySourceForAgent
				querySource := "agent:custom"
				if isBuiltInAgent(e.agentType) {
					querySource = "agent:builtin:" + e.agentType
				}
				cacheControl = &llm.CacheControlConfig{Type: "ephemeral", TTL: "5m"}
				promptStateKey = &llm.PromptStateKey{
					QuerySource: querySource,
					AgentID:     e.agentType,
				}
			} else {
				cacheControl = &llm.CacheControlConfig{Type: "ephemeral", TTL: "1h"}
				promptStateKey = &llm.PromptStateKey{QuerySource: "repl_main_thread"}
			}
		}
	}

	req := &llm.Request{
		Model:          e.model,
		MaxTokens:      e.maxTokens,
		Messages:       apiMessages,
		System:         systemPrompt,
		SystemBlocks:   systemBlocks,
		Tools:          toolDefs,
		Stream:         true,
		CacheControl:   cacheControl,
		PromptStateKey: promptStateKey,
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
						InputTokens:              usage.InputTokens,
						OutputTokens:             usage.OutputTokens,
						CacheReadInputTokens:     usage.CacheReadInputTokens,
						CacheCreationInputTokens: usage.CacheCreationInputTokens,
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
				case types.ContentTypeText:
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventTextStart,
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
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventTextEnd,
					})
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
					e.emitEvent(eventCh, types.QueryEvent{
						Type: types.EventToolRun,
						ToolUse: &types.ToolUseEvent{
							ID:   cb.ID,
							Name: cb.Name,
						},
					})
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
				// Align with TS updateUsage (claude.ts:2924-2946):
				// input/cache: overwrite if > 0, else keep start value.
				// output: direct set (like TS ??).
				if event.Usage.InputTokens > 0 {
					usage.InputTokens = event.Usage.InputTokens
				}
				usage.OutputTokens = event.Usage.OutputTokens
				if event.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
				}
				if event.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
				}
				e.emitEvent(eventCh, types.QueryEvent{
					Type: types.EventUsage,
					Usage: &types.UsageEvent{
						InputTokens:              usage.InputTokens,
						OutputTokens:             usage.OutputTokens,
						CacheReadInputTokens:     usage.CacheReadInputTokens,
						CacheCreationInputTokens: usage.CacheCreationInputTokens,
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

// estimateTokens returns a rough token count estimate for the current messages.
// Uses the 4 chars/token heuristic, matching TS's roughTokenCount.
func (e *Engine) estimateTokens() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	total := 0
	for _, msg := range e.messages {
		for _, block := range msg.Content {
			total += len(block.Text)
			total += len(block.Input)
			total += len(block.Content)
		}
	}
	if total == 0 {
		return 0
	}
	return total / 4
}

// shouldAutoCompact returns true if proactive auto-compact should be triggered.
// TS align: autoCompact.ts:shouldAutoCompact()
func (e *Engine) shouldAutoCompact() bool {
	// Estimate tokens first (takes its own RLock) to avoid nested locking.
	tokens := e.estimateTokens()

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.compactor == nil {
		return false
	}
	cfg := e.autoCompactConfig
	if cfg.ContextWindow <= 0 || cfg.Threshold <= 0 {
		return false
	}
	if e.isSubagent {
		return false
	}
	// Circuit breaker
	maxFail := cfg.MaxConsecutiveFailures
	if maxFail <= 0 {
		maxFail = 3
	}
	if e.consecutiveCompactFailures >= maxFail {
		return false
	}
	threshold := float64(cfg.ContextWindow) * cfg.Threshold
	return float64(tokens) > threshold
}

// marshalMessages converts internal messages to API format.
// Strips response-only fields (Timestamp, Model, StopReason, Usage) that
// the Anthropic Messages API does not accept in request messages.
// Source: TS normalizeMessagesForAPI — simplified for Phase 1 (no attachments,
// tool references, or virtual messages).
func (e *Engine) marshalMessages() []types.Message {
	result := make([]types.Message, len(e.messages))
	for i, msg := range e.messages {
		contentCopy := make([]types.ContentBlock, len(msg.Content))
		copy(contentCopy, msg.Content)
		result[i] = types.Message{
			Role:    msg.Role,
			Content: contentCopy,
		}
	}

	// Add cache_control to the last block of the last message for incremental caching.
	// This mirrors TS Claude Code's addCacheBreakpoints() which marks only
	// messages[messages.length - 1] with cache_control on its last block.
	// Source: claude.ts:3089-3106 (addCacheBreakpoints)
	if len(result) > 0 {
		last := &result[len(result)-1]
		if len(last.Content) > 0 {
			lastBlock := &last.Content[len(last.Content)-1]
			lastBlock.CacheControl = &types.CacheControlConfig{Type: "ephemeral"}
		}
	}

	return result
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
	e.appendMessage(types.Message{
		Role: types.RoleSystem,
		Content: []types.ContentBlock{
			types.NewTextBlock(content),
		},
		Timestamp: time.Now(),
	})
}

// Messages returns a copy of the current message history.
// Thread-safe: acquires RLock and returns a clone so callers can read
// without holding the lock (safe for concurrent access from TUI).
func (e *Engine) Messages() []types.Message {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Clone(e.messages)
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
	e.mu.Lock()
	e.messages = nil
	e.turnCount = 0
	e.mu.Unlock()
}

// appendMessage adds a message to the history under Lock.
func (e *Engine) appendMessage(msg types.Message) {
	e.mu.Lock()
	e.messages = append(e.messages, msg)
	e.mu.Unlock()
}

// appendMessages adds multiple messages to the history under Lock.
func (e *Engine) appendMessages(msgs []types.Message) {
	e.mu.Lock()
	e.messages = append(e.messages, msgs...)
	e.mu.Unlock()
}

// setMessages replaces the message history under Lock.
func (e *Engine) setMessages(msgs []types.Message) {
	e.mu.Lock()
	e.messages = msgs
	e.mu.Unlock()
}

// SetMessages replaces the engine's message history under Lock.
func (e *Engine) SetMessages(msgs []types.Message) {
	e.setMessages(msgs)
}

// SetSessionID sets the session ID for this engine.
func (e *Engine) SetSessionID(id string) {
	e.mu.Lock()
	e.sessionID = id
	e.mu.Unlock()
}

// SetCompactor configures the auto-compact compactor and threshold.
// Call after engine construction when the store is available.
func (e *Engine) SetCompactor(c Compactor, cfg AutoCompactConfig) {
	e.mu.Lock()
	e.compactor = c
	e.autoCompactConfig = cfg
	e.mu.Unlock()
}

// SessionID returns the current session ID.
func (e *Engine) SessionID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessionID
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
		agentType:     opts.AgentType,
		maxTurns:      subMaxTurns(opts.MaxTurns),
	}
}

// isBuiltInAgent returns true for the three built-in agent types.
// Source: builtInAgents.ts — General, Explore, Plan
func isBuiltInAgent(agentType string) bool {
	switch agentType {
	case "General", "Explore", "Plan":
		return true
	}
	return false
}

// QuerySync executes the agentic loop synchronously (no goroutine, no channels).
// Used by sub-agents created via AgentTool. EventCh is nil — events are silently discarded.
// Source: TS sync sub-agents execute runAgent() directly in the caller's context.
func (e *Engine) QuerySync(ctx context.Context, userMessage string, systemPrompt json.RawMessage) QueryResult {
	return e.queryLoop(ctx, userMessage, systemPrompt, nil)
}

// QueryWithExistingMessages executes the agentic turn loop starting from
// pre-constructed messages (no user message injection). Used by fork agents
// that build their own conversation history.
func (e *Engine) QueryWithExistingMessages(ctx context.Context, messages []types.Message, systemPrompt json.RawMessage) QueryResult {
	e.setMessages(messages)
	return e.runTurns(ctx, systemPrompt, nil)
}

// Model returns the engine's model name.
func (e *Engine) Model() string { return e.model }

// SystemPrompt returns the stored system prompt bytes.
func (e *Engine) SystemPrompt() json.RawMessage { return e.systemPrompt }

// SetSystemPrompt stores the system prompt for later access by fork agents.
func (e *Engine) SetSystemPrompt(sp json.RawMessage) { e.systemPrompt = sp }

// subMaxTurns returns the max turns for a sub-engine.
// 0 or negative means use parent default (50).
func subMaxTurns(n int) int {
	if n <= 0 {
		return 50
	}
	return n
}
