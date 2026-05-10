// Package engine implements the core agentic loop for gbot.
//
// Source reference: query.ts (~1730 lines), QueryEngine.ts
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/mcp"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	mcpresource "github.com/liuy/gbot/pkg/tool/mcp"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/tool/toolsearch"
	"github.com/liuy/gbot/pkg/types"

	"github.com/liuy/gbot/pkg/memory/session"

	"github.com/google/uuid"
)

// Compactor is the interface for auto-compact operations.
// The engine calls this when it detects token usage approaching limits
// (proactive) or when the API returns a context overflow error (reactive).
// TS align: autoCompact.ts + reactiveCompact.ts
type Compactor interface {
	Compact(ctx context.Context, messages []types.Message) (*CompactResult, error)
}

// PostTurnHook is called after each successful turn in the agentic loop.
// TS source: utils/hooks/postSamplingHooks.ts — PostSamplingHook.
// Only fires on the main engine (not sub-agents), and only when auto-compact
// is enabled (ContextWindow > 0), matching TS behavior where session memory
// depends on auto-compact.
type PostTurnHook func(ctx context.Context, messages []types.Message, currentTokens int, querySource string)

// TS auto-compact constants.
// Source: services/compact/autoCompact.ts
const (
	maxOutputTokensForSummary = 20_000 // MAX_OUTPUT_TOKENS_FOR_SUMMARY: reserve for compact output
	manualCompactBufferTokens = 3_000  // MANUAL_COMPACT_BUFFER_TOKENS: blocking limit buffer
)

// autoCompactBuffer returns the dynamic buffer for the auto-compact threshold.
// TS uses a fixed 13K, but that's too aggressive for small windows (50% of 30K).
// Dynamic: 7% of effectiveWindow, minimum 3K. At 200K window this ≈ 12.6K (≈ TS's 13K).
func autoCompactBuffer(effectiveWindow int) int {
	buf := max(effectiveWindow*7/100, 3000)
	return buf
}

// AutoCompactConfig configures auto-compact behavior.
// TS align: autoCompact.ts configuration
type AutoCompactConfig struct {
	// ContextWindow is the model's maximum context window in tokens.
	// If 0, proactive auto-compact is disabled.
	ContextWindow int
	// MaxConsecutiveFailures is the number of consecutive compact failures before
	// the circuit breaker trips and stops attempting proactive auto-compact. Default: 3.
	MaxConsecutiveFailures int
}

// subEngineSeq generates unique suffixes for sub-engine session IDs.
// Plan Issue 2: each sub-engine must have unique SessionID to isolate REPLTool sessions.
var subEngineSeq atomic.Int64

// Engine is the core agentic loop.
// Source: QueryEngine.ts — outer orchestrator + query.ts inner loop.
type Engine struct {
	provider      llm.Provider
	tools         map[string]tool.Tool
	toolOrder     []string
	toolsProvider func() map[string]tool.Tool
	model         string
	maxTokens     int
	logger        *slog.Logger
	mu            sync.RWMutex
	messages      []types.Message
	sessionID     string
	tokenBudget   int
	turnCount     int
	dispatcher    types.EventDispatcher
	workingDir    string
	notifications *notificationQueue
	systemPrompt  json.RawMessage // stored system prompt for fork agent access

	// isSubagent is true for sub-agent engines created by AgentTool.
	// Sub-agents bypass token budget exhaustion checks, matching TS behavior
	// where agentId presence disables budget tracking.
	// Source: tokenBudget.ts:45-53 — checkTokenBudget skips when agentId is set.
	isSubagent bool

	// agentType is the sub-agent type (e.g. "General", "Explore", "Plan").
	// Empty for the main engine. Set by NewSubEngine from SubEngineOptions.AgentType.
	agentType string

	// maxTurns is the maximum number of agentic turns before stopping.
	// 0 means no limit — aligns with TS built-in agents (undefined maxTurns).
	maxTurns int

	// Auto-compact fields
	compactor                  Compactor
	autoCompactConfig          AutoCompactConfig
	consecutiveCompactFailures int

	// postTurnHooks are called after each successful agentic turn.
	// TS source: utils/hooks/postSamplingHooks.ts — postSamplingHooks array.
	// Session memory extraction registers here.
	postTurnHooks []PostTurnHook

	// sessionMemory manages background session notes extraction.
	// TS source: services/SessionMemory/sessionMemory.ts.
	// nil when session memory is disabled (auto-compact off or not configured).
	sessionMemory *session.SessionMemory

	// ContextTokens stores the context token count from the last API response.
	// Value = TotalInputTokens() + OutputTokens (input + cache + output of last turn).
	// Persisted to sessions table so resume can restore the exact value,
	// avoiding heuristic overestimation that triggers false compacts.
	// After compact, decremented by message delta (heuristic) and corrected
	// on next API response.
	ContextTokens int

	// mcpRegistry manages MCP server connections and tool discovery.
	mcpRegistry *mcp.Registry

	// hooks is the user-configurable lifecycle hooks system.
	// Nil when no hooks are configured.
	hooks *hooks.Hooks

	// agentMetaDepth tracks nesting depth for sub-agent rendering.
	// 0 = main engine, 1 = direct child, 2 = grandchild, etc.
	agentMetaDepth int

	// onCloseFn is called during Close() so callers (main.go) can wire
	// session-scoped cleanup (e.g. REPL CleanSession) without Engine
	// knowing about REPLTool directly.
	onCloseFn func(sessionID string)

	// fileHistory tracks file backups for rewind/restore.
	// Set by TUI layer via SetFileHistory after session init.
	// Sub-engines share the same Tracker (sub-agent edits are also tracked).
	fileHistory *filehistory.Tracker

	// permissionChecker evaluates permission rules for tool invocations.
	// Nil when no permission rules are configured (default allow).
	// Source: permissionsLoader.ts — loadAllPermissionRulesFromDisk.
	permissionChecker permission.PermissionChecker

	// contentReplacementState tracks per-message tool result budget decisions
	// across turns for prompt cache stability.
	// TS: ContentReplacementState (toolResultStorage.ts:390-393)
	contentReplacementState *toolresult.ContentReplacementState

	// toolSearch tracks which deferred tools have been discovered via ToolSearch.
	// When ToolSearch is active (any deferred tools exist),
	// undiscovered deferred tools are omitted from the API request and listed by name
	// in a synthetic user message prefix.
	// Source: utils/toolSearch.ts — discoveredTools
	toolSearch *toolSearchState

	// recordWriter persists ContentReplacementRecords to transcript storage.
	// Set via SetRecordWriter after engine construction when the store is available.
	// TS: writeToTranscript callback in applyToolResultBudget (toolResultStorage.ts:924-936).
	recordWriter func([]toolresult.ContentReplacementRecord)
}

// Params holds the constructor arguments for Engine.
type Params struct {
	Provider          llm.Provider
	Tools             []tool.Tool                 // static tool list (ignored if ToolsProvider is set)
	ToolsProvider     func() map[string]tool.Tool // dynamic tool resolution — called each turn
	Model             string
	MaxTokens         int
	MaxTurns          int // 0 = no limit
	TokenBudget       int
	Logger            *slog.Logger
	Dispatcher        types.EventDispatcher
	Compactor         Compactor
	AutoCompact       AutoCompactConfig
	MCPRegistry       *mcp.Registry
	Hooks             *hooks.Hooks
	PermissionChecker permission.PermissionChecker
	WorkingDir        string // working directory for file history snapshots
}

// QueryResult is the final result of a query.
type QueryResult struct {
	Messages   []types.Message
	TurnCount  int
	TotalUsage types.Usage
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
	toolOrder := slices.Sorted(maps.Keys(toolMap))

	return &Engine{
		provider:                p.Provider,
		tools:                   toolMap,
		toolOrder:               toolOrder,
		toolsProvider:           toolsProvider,
		model:                   p.Model,
		maxTokens:               p.MaxTokens,
		logger:                  p.Logger,
		tokenBudget:             p.TokenBudget,
		dispatcher:              p.Dispatcher,
		notifications:           &notificationQueue{},
		maxTurns:                p.MaxTurns,
		compactor:               p.Compactor,
		autoCompactConfig:       p.AutoCompact,
		mcpRegistry:             p.MCPRegistry,
		hooks:                   p.Hooks,
		permissionChecker:       p.PermissionChecker,
		workingDir:              p.WorkingDir,
		contentReplacementState: toolresult.NewContentReplacementState(),
		agentMetaDepth:         0,
		toolSearch:             newToolSearchState(),
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
func (e *Engine) Query(ctx context.Context, userMessage string, systemPrompt json.RawMessage) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("engine: panic in queryLoop", "error", r, "stack", string(debug.Stack()))
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: fmt.Errorf("internal error: %v", r)})
			}
		}()
		e.queryLoop(ctx, userMessage, systemPrompt)
	}()
}

// ProcessNotifications drains pending notifications and runs the turn loop.
// This is Path B — equivalent to TS's between-turn new query() invocation.
func (e *Engine) ProcessNotifications(ctx context.Context, systemPrompt json.RawMessage) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("engine: panic in ProcessNotifications", "error", r, "stack", string(debug.Stack()))
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: fmt.Errorf("internal error: %v", r)})
			}
		}()
		pending := e.notifications.Drain()
		if len(pending) == 0 {
			return
		}
		e.appendMessages(pending)
		for i := range pending {
			e.emitEvent(types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
		}
		e.runTurns(ctx, systemPrompt)
	}()
}

// ProcessNotificationsSync drains pending notifications and runs the turn loop synchronously.
// Used by tests that need to wait for the result.
func (e *Engine) ProcessNotificationsSync(ctx context.Context, systemPrompt json.RawMessage) QueryResult {
	pending := e.notifications.Drain()
	if len(pending) == 0 {
		return QueryResult{}
	}
	e.appendMessages(pending)
	for i := range pending {
		e.emitEvent(types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
	}
	return e.runTurns(ctx, systemPrompt)
}

// emitEvent sends an event via the dispatcher (Hub).
// When no dispatcher is set (sub-engine), events are silently discarded
// and results are returned via the function return value.
func (e *Engine) emitEvent(event types.QueryEvent) {
	if e.dispatcher != nil {
		e.dispatcher.Dispatch(event)
	}
	// No dispatcher (sub-engine): silently discard
}

// queryLoop is the main agentic loop.
// Source: query.ts — the while(true) loop with 28 stages.
func (e *Engine) queryLoop(ctx context.Context, userMessage string, systemPrompt json.RawMessage) QueryResult {
	// Stage 0: Process user input
	userMsg := types.Message{
		Role: types.RoleUser,
		Content: []types.ContentBlock{
			types.NewTextBlock(userMessage),
		},
		Timestamp: time.Now(),
	}
	e.appendMessage(userMsg)
	e.emitEvent(types.QueryEvent{Type: types.EventQueryStart, Message: &userMsg})

	return e.runTurns(ctx, systemPrompt)
}

// runTurns executes the agentic turn loop. Shared by queryLoop (normal path)
// and QueryWithExistingMessages (fork agent path).
func (e *Engine) runTurns(ctx context.Context, systemPrompt json.RawMessage) QueryResult {
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

	// Microcompact: shrink prompt before the turn loop.
	// Source: query.ts:413-419 — runs once per query, before autocompact.
	// Sub-agents use agent-specific querySource so isMainThreadSource excludes them.
	mcQuerySource := e.querySource()
	mcResult := MicrocompactMessages(e.messages, mcQuerySource, e.logger)
	e.setMessages(mcResult.Messages)

	reactiveCompactDone := false

	for e.maxTurns == 0 || e.turnCount < e.maxTurns {
		compactSucceeded := false

		// Stage 4: Loop-top abort check.
		// Source: query.ts — context cancellation check at loop top.
		if err := ShouldAbort(ctx, "streaming"); err != nil {
			e.appendInlineInterruptMessage()
			e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: err})
			return QueryResult{
				Messages: e.messages,
				Error:    err,
			}
		}

		// Drain pending notifications (stall alerts, completion notifications
		// from background tasks). Source: TS drains commandQueue at query start.
		if pending := e.notifications.Drain(); len(pending) > 0 {
			e.appendMessages(pending)
			for i := range pending {
				e.emitEvent(types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
			}
		}

		// Pre-turn auto-compact: check before API call, like TS.
		// TS align: query.ts auto-compact runs before blocking limit.
		// Uses ContextTokens from previous turn (set after API response).
		if e.shouldAutoCompact() {
			compactID := "compact-auto-" + uuid.New().String()[:8]
			e.emitEvent(types.QueryEvent{
				Type: types.EventToolStart,
				ToolUse: &types.ToolUseEvent{
					ID:      compactID,
					Name:    "Compact",
					Summary: "Compacting conversation...",
				},
			})
			e.emitEvent(types.QueryEvent{
				Type:    types.EventToolRun,
				ToolUse: &types.ToolUseEvent{ID: compactID, Name: "Compact"},
			})
			e.fireCompactHooks(ctx, "auto", "pre")
			result, compactErr := e.runCompact(ctx, "pre-turn compact")
			if compactErr != nil {
				e.emitEvent(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     compactID,
						DisplayOutput: fmt.Sprintf("Compact failed: %v", compactErr),
						IsError:       true,
					},
				})
				e.mu.Lock()
				e.consecutiveCompactFailures++
				failures := e.consecutiveCompactFailures
				e.mu.Unlock()
				e.logger.Warn("pre-turn auto-compact failed",
					"error", compactErr,
					"consecutiveFailures", failures)
			} else {
				// Only count as success if compact actually reduced tokens.
				// AutoCompactor can return a no-op result (BeforeTokens == AfterTokens)
				// when head messages have no extractable text. In that case, don't
				// skip the blocking limit — it serves as a safety net.
				if result.BeforeTokens > result.AfterTokens {
					compactSucceeded = true
				}
				e.mu.Lock()
				if compactSucceeded {
					e.consecutiveCompactFailures = 0
				} else {
					e.consecutiveCompactFailures++
				}
				e.mu.Unlock()
				e.emitEvent(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     compactID,
						DisplayOutput: formatCompactOutput(result),
					},
				})
				e.logger.Info("pre-turn auto-compact succeeded",
					"messages", len(result.Messages))
				e.fireCompactHooks(ctx, "auto", "post")
			}
		}

		// Token-based pruning: when auto-compact failed and context is at blocking
		// limit, try clearing old compactable tool result content as last resort.
		// gbot equivalent of TS Cached Microcompact (which uses Anthropic cache_editing).
		if !e.isSubagent && !compactSucceeded && e.autoCompactConfig.ContextWindow > 0 {
			if pruned := e.maybeTokenPrune(); pruned != nil {
				e.logger.Info("engine:token_prune",
					"cleared", pruned.Cleared,
					"tokens_saved", pruned.TokensSaved)
				e.mu.Lock()
				e.ContextTokens -= pruned.TokensSaved
				if e.ContextTokens < 0 {
					e.ContextTokens = 0
				}
				e.mu.Unlock()
			}
		}

		// Blocking limit: refuse API call if context exceeds safe threshold.
		// TS align: query.ts:628-636 — skip blocking limit when compact
		// produced a result (!compactionResult). Only block when compact
		// didn't succeed this turn.
		// Sub-agents exempt to prevent deadlock (compact/session_memory need large context).
		if !e.isSubagent && !compactSucceeded && e.autoCompactConfig.ContextWindow > 0 {
			reservedTokens := min(e.maxTokens, maxOutputTokensForSummary)
			effectiveWindow := e.autoCompactConfig.ContextWindow - reservedTokens
			blockingLimit := effectiveWindow - manualCompactBufferTokens
			if blockingLimit > 0 && e.currentInputTokens() >= blockingLimit {
				tokens := e.currentInputTokens()
				e.logger.Warn("blocking limit exceeded, refusing API call",
					"tokens", tokens,
					"limit", blockingLimit)
				blockErr := fmt.Errorf("Prompt is too long: %s context tokens exceeds %s limit", types.FormatTokenCount(tokens), types.FormatTokenCount(blockingLimit))
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: blockErr})
				return QueryResult{
					Messages:   e.messages,
					TurnCount:  e.turnCount,
					TotalUsage: totalUsage,
					Error:      blockErr,
				}
			}
		}

		// Stage 14-15: API call streaming loop
		e.emitEvent(types.QueryEvent{Type: types.EventTurnStart})

		resp, streamingExecutor, err := e.callLLM(ctx, systemPrompt)
		if err != nil {
			// Abort check: if ctx was cancelled during callLLM, return *AbortError
			// with inline interrupt message. This handles the common case where
			// the user presses Escape during streaming (text, thinking, or tool_use).
			if abortErr := ShouldAbort(ctx, "streaming"); abortErr != nil {
				e.appendInlineInterruptMessage()
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr, Usage: &types.UsageEvent{
					InputTokens:              totalUsage.InputTokens,
					OutputTokens:             totalUsage.OutputTokens,
					CacheReadInputTokens:     totalUsage.CacheReadInputTokens,
					CacheCreationInputTokens: totalUsage.CacheCreationInputTokens,
				}})
				return QueryResult{
					Messages:   e.messages,
					TurnCount:  e.turnCount,
					TotalUsage: totalUsage,
					Error:      abortErr,
				}
			}
			// Reactive compact: try compact + retry on context overflow.
			// TS align: query.ts:1119-1175 — reactiveCompact.tryReactiveCompact()
			if e.compactor != nil && llm.IsContextOverflow(err) && !reactiveCompactDone {
				// Recursion guard: compact/session_memory forked agents
				// must not retry on overflow — they'd deadlock.
				src := e.querySource()
				if src != QuerySourceCompact && src != QuerySourceSessionMemory {
					compactID := "compact-reactive-" + uuid.New().String()[:8]
					e.emitEvent(types.QueryEvent{
						Type: types.EventToolStart,
						ToolUse: &types.ToolUseEvent{
							ID:      compactID,
							Name:    "Compact",
							Summary: "Compacting after context overflow...",
						},
					})
					e.emitEvent(types.QueryEvent{
						Type:    types.EventToolRun,
						ToolUse: &types.ToolUseEvent{ID: compactID, Name: "Compact"},
					})
					e.fireCompactHooks(ctx, "auto", "pre")
					result, compactErr := e.runCompact(ctx, "reactive compact")
					if compactErr == nil {
						e.fireCompactHooks(ctx, "auto", "post")
						e.emitEvent(types.QueryEvent{
							Type: types.EventToolEnd,
							ToolResult: &types.ToolResultEvent{
								ToolUseID:     compactID,
								DisplayOutput: formatCompactOutput(result),
							},
						})
						reactiveCompactDone = true
						e.logger.Info("reactive auto-compact succeeded, retrying")
						continue
					}
					e.emitEvent(types.QueryEvent{
						Type: types.EventToolEnd,
						ToolResult: &types.ToolResultEvent{
							ToolUseID:     compactID,
							DisplayOutput: fmt.Sprintf("Compact failed: %v", compactErr),
							IsError:       true,
						},
					})
					e.logger.Warn("reactive auto-compact failed", "error", compactErr)
				}
			}

			// Stage 15.5: Abort check after reactive compact attempt.
			// If ctx was cancelled during compact, return *AbortError instead
			// of the original API error (overflow, rate limit, etc.).
			if abortErr := ShouldAbort(ctx, "streaming"); abortErr != nil {
				e.appendInlineInterruptMessage()
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: abortErr})
				return QueryResult{
					Messages:   e.messages,
					TurnCount:  e.turnCount,
					TotalUsage: totalUsage,
					Error:      abortErr,
				}
			}

			// Stage 16: Error handling
			action := e.handleStreamError(err)
			if !action.Continue {
				e.logger.Error("callLLM error (terminal)", "error", err, "turn", e.turnCount)
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: err})
				return QueryResult{
					Messages: e.messages,
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
			// Store context size estimate for currentInputTokens().
			// API InputTokens is non-cached input only; TotalInputTokens()
			// adds cache read/creation. OutputTokens included because the
			// response will be part of the next request context.
			// Aligns with TS tokenCountWithEstimation (input + output).
			e.mu.Lock()
			e.ContextTokens = resp.Usage.TotalInputTokens() + resp.Usage.OutputTokens
			e.mu.Unlock()
		}

		// Add assistant message to history
		e.appendMessage(*resp)

		// Populate conversation history on the executor so tools
		// (e.g. Agent tool) can access the full parent conversation.
		if streamingExecutor != nil {
			streamingExecutor.SetMessages(e.messages)

			// Stage 18: Post-streaming abort check.
			// Source: query.ts:1015-1029 — consume getRemainingResults or yieldMissingToolResultBlocks.
			// appendInlineInterruptMessage now also generates synthetic tool_results.
			if err := ShouldAbort(ctx, "streaming"); err != nil {
				e.appendInlineInterruptMessage()
				streamingExecutor.Discard()
				e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
				e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: err, Usage: &types.UsageEvent{
					InputTokens:              totalUsage.InputTokens,
					OutputTokens:             totalUsage.OutputTokens,
					CacheReadInputTokens:     totalUsage.CacheReadInputTokens,
					CacheCreationInputTokens: totalUsage.CacheCreationInputTokens,
				}})
				return QueryResult{
					Messages:   e.messages,
					TurnCount:  e.turnCount,
					TotalUsage: totalUsage,
					Error:      err,
				}
			}
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
					e.emitEvent(types.QueryEvent{Type: types.EventQueryStart, Message: &pending[i]})
				}
				e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
				continue
			}

			// Stop/SubagentStop hook — blocking result gives LLM another turn.
			// Source: stopHooks.ts — handleStopHooks.
			if blockResult := e.runStopHook(ctx); blockResult != nil {
				e.logger.Info("stop hook blocked, continuing turn")
				e.appendMessage(types.Message{
					Role: types.RoleUser,
					Content: []types.ContentBlock{
						types.NewTextBlock("[hook] " + blockResult.Stderr),
					},
				})
				e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
				e.turnCount++
				e.firePostTurnHooks(ctx)
				continue
			}
			e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
			e.turnCount++
			e.firePostTurnHooks(ctx)
			e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Usage: &types.UsageEvent{
				InputTokens:              totalUsage.InputTokens,
				OutputTokens:             totalUsage.OutputTokens,
				CacheReadInputTokens:     totalUsage.CacheReadInputTokens,
				CacheCreationInputTokens: totalUsage.CacheCreationInputTokens,
			}})
			return QueryResult{
				Messages:   e.messages,
				TurnCount:  e.turnCount,
				TotalUsage: totalUsage,
			}
		}

		// Stage 21: Wait for stream-started tools to complete, collect results.
		// Source: query.ts:1381 — getRemainingResults().
		execResult := streamingExecutor.ExecuteAll(nil)

		// ToolSearch: register any tools discovered by ToolSearch execution.
		// Source: utils/toolSearch.ts — discovered tools are extracted from results
		// and added to the active set for subsequent API calls.
		if _, tsActive := e.tools[ToolSearchToolName]; tsActive {
			streamingExecutor.mu.Lock()
			for _, tt := range streamingExecutor.tools {
				if tt.Name == ToolSearchToolName && tt.Result != nil && tt.Err == nil {
					names := ExtractDiscoveredToolNamesFromResult(tt.Result.Data)
					if len(names) > 0 {
						e.toolSearch.DiscoverTools(names)
						e.logger.Info("toolSearch:discovered",
							"count", len(names),
							"names", strings.Join(names, ","))
					}
				}
			}
			streamingExecutor.mu.Unlock()
		}

		// Add tool results as user message (MUST come before NewMessages).
		// The Anthropic API requires tool_result to directly follow the
		// assistant's tool_use block without intermediate user messages.
		// TS reference: toolExecution.ts — addToolResult() line 1456 first,
		// then newMessages line 1566 after.
		if len(execResult.ToolResultBlocks) > 0 {
			e.appendMessage(types.Message{
				Role:    types.RoleUser,
				Content: execResult.ToolResultBlocks,
			})
		}

		// Stage 23: Post-tool-execution abort check.
		// Source: query.ts:1485-1516 — tool execution complete, check abort.
		if err := ShouldAbort(ctx, "tools"); err != nil {
			e.appendInlineInterruptMessage()
			e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})
			e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Error: err, Usage: &types.UsageEvent{
				InputTokens:              totalUsage.InputTokens,
				OutputTokens:             totalUsage.OutputTokens,
				CacheReadInputTokens:     totalUsage.CacheReadInputTokens,
				CacheCreationInputTokens: totalUsage.CacheCreationInputTokens,
			}})
			return QueryResult{
				Messages:   e.messages,
				TurnCount:  e.turnCount,
				TotalUsage: totalUsage,
				Error:      err,
			}
		}

		// Append NewMessages AFTER tool_result.
		// Tool-provided messages (e.g., skill content) follow the tool_result.
		if len(execResult.NewMessages) > 0 {
			e.appendMessages(execResult.NewMessages)
		}

		// End of this streaming round
		e.emitEvent(types.QueryEvent{Type: types.EventTurnEnd})

		// Stage 25-26: Turn counting
		e.turnCount++

		// Post-turn hooks (session memory extraction, etc.)
		// TS: executePostSamplingHooks in query.ts after each sampling step.
		e.firePostTurnHooks(ctx)
	}

	e.emitEvent(types.QueryEvent{Type: types.EventQueryEnd, Usage: &types.UsageEvent{
		InputTokens:              totalUsage.InputTokens,
		OutputTokens:             totalUsage.OutputTokens,
		CacheReadInputTokens:     totalUsage.CacheReadInputTokens,
		CacheCreationInputTokens: totalUsage.CacheCreationInputTokens,
	}})
	return QueryResult{
		Messages:   e.messages,
		TurnCount:  e.turnCount,
		TotalUsage: totalUsage,
	}
}

// runStopHook calls the Stop or SubagentStop hook.
// Returns non-nil if any hook blocks (exit 2), giving the LLM another turn.
// Source: stopHooks.ts — handleStopHooks.
func (e *Engine) runStopHook(ctx context.Context) *hooks.HookResult {
	if e.hooks == nil {
		return nil
	}
	input := &hooks.HookInput{
		HookEventName: string(hooks.HookStop),
		SessionID:     e.sessionID,
	}
	if e.isSubagent {
		input.HookEventName = string(hooks.HookSubagentStop)
		input.AgentType = e.agentType
		return e.hooks.SubagentStop(ctx, input)
	}
	return e.hooks.Stop(ctx, input)
}

// callLLM sends the messages to the LLM and collects the full response.
// Source: query.ts — streaming API call accumulation.

// fireCompactHooks fires PreCompact and PostCompact hooks around compaction.
// Hooks are best-effort — errors are logged but don't prevent compact.
func (e *Engine) fireCompactHooks(ctx context.Context, trigger string, phase string) {
	if e.hooks == nil {
		return
	}
	input := &hooks.HookInput{
		SessionID: e.sessionID,
		Trigger:   trigger,
	}
	switch phase {
	case "pre":
		input.HookEventName = string(hooks.HookPreCompact)
		e.hooks.PreCompact(ctx, input)
	case "post":
		input.HookEventName = string(hooks.HookPostCompact)
		e.hooks.PostCompact(ctx, input)
	}
}

// buildToolDefs converts a slice of tools into LLM tool definitions.
func buildToolDefs(tools []tool.Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(tools))
	for _, t := range tools {
		if !t.IsEnabled() {
			continue
		}
		schema := t.InputSchema()
		desc, err := t.Description(nil)
		if err != nil {
			desc = t.Name()
		}
		defs = append(defs, llm.ToolDef{
			Name:        t.Name(),
			Description: desc,
			InputSchema: schema,
		})
	}
	return defs
}
func (e *Engine) callLLM(ctx context.Context, systemPrompt json.RawMessage) (*types.Message, *StreamingToolExecutor, error) {
	e.refreshTools()

	// ToolSearch: check if filtering is active.
	// Source: utils/toolSearch.ts — isToolSearchEnabled + extractDiscoveredToolNames
	_, toolSearchActive := e.tools[ToolSearchToolName]
	var deferredAnnouncement string
	var activeTools []tool.Tool

	if toolSearchActive {
		var deferredTools []tool.Tool
		activeTools, deferredTools, _ = FilterToolsForRequest(e.tools, e.toolSearch, e.toolOrder)
		deferredAnnouncement = DeferredToolsAnnouncement(deferredTools)
	}

	// Build tool definitions for API (filtered if ToolSearch is active).
	var toolsToBuild []tool.Tool
	if toolSearchActive && len(activeTools) > 0 {
		toolsToBuild = activeTools
	} else {
		for _, name := range e.toolOrder {
			if t, ok := e.tools[name]; ok && t.IsEnabled() {
				toolsToBuild = append(toolsToBuild, t)
			}
		}
	}
	toolDefs := buildToolDefs(toolsToBuild)

	e.logger.Info("callLLM:tools", "count", len(toolDefs), "names", func() string {
		var names []string
		for _, td := range toolDefs {
			names = append(names, td.Name)
		}
		return strings.Join(names, ",")
	}())

	// Marshal messages for the API request
	apiMessages := e.marshalMessages()

	// Apply per-message tool result budget (TS: applyToolResultBudget).
	// Replaces large tool results with previews when aggregate per-message
	// size exceeds 200K. Budget decisions are cached for prompt cache stability.
	apiMessages = e.applyBudget(apiMessages)

	// ToolSearch: prepend deferred tools announcement to first user message.
	// Source: utils/toolSearch.ts — <available-deferred-tools> user message prefix
	if toolSearchActive && deferredAnnouncement != "" && len(apiMessages) > 0 {
		// Prepend as a synthetic user message at the beginning of the conversation
		// so it doesn't disrupt the conversation flow.
		prefixMsg := types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock(deferredAnnouncement),
			},
		}
		apiMessages = append([]types.Message{prefixMsg}, apiMessages...)
	}

	// Enable prompt caching: wrap system prompt into structured blocks
	// so applyCacheControlToSystem can inject cache_control markers.
	// Source: claude.ts:1374-1376 — always on by default.
	var systemBlocks []llm.SystemBlockParam
	var cacheControl *types.CacheControlConfig
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
				cacheControl = &types.CacheControlConfig{Type: "ephemeral", TTL: "5m"}
				promptStateKey = &llm.PromptStateKey{
					QuerySource: e.querySource(),
					AgentID:     e.agentType,
				}
			} else {
				cacheControl = &types.CacheControlConfig{Type: "ephemeral", TTL: "1h"}
				promptStateKey = &llm.PromptStateKey{QuerySource: e.querySource()}
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
	var model string
	var stopReason string
	var usage types.Usage
	var thinkingStart time.Time

	// Per-index accumulator for parallel tool calls.
	// OpenAI SSE can interleave input_json_delta across multiple tool_use
	// blocks; each block's state must be isolated by event.Index.
	type blockAccumulator struct {
		toolInput strings.Builder
		toolID    string
		toolName  string
	}
	var blockAcc []*blockAccumulator

	// StreamingToolExecutor — lazily created on first tool_use block.
	// Source: query.ts:562-568 — executor created before streaming.
	// Source: query.ts:841-843 — addTool called as each tool_use completes.
	var streamingExecutor *StreamingToolExecutor
	hasContent := false
	streamComplete := false

	for event := range streamCh {
		select {
		case <-ctx.Done():
			// Source: query.ts Stage 2+3 — streaming interrupted:
			// 1. Generate synthetic tool_results for ALL tool_use blocks.
			//    ExecuteAll never runs after mid-stream abort, so even started tools
			//    need synthetic results to prevent orphaned tool_use blocks.
			// 2. Append partial assistant message for conversation consistency
			var orphanedBlocks []types.ContentBlock
			if streamingExecutor != nil {
				orphanedBlocks = SyntheticToolResultsForBlocks(
					contentBlocks, nil, AbortReasonStreamingFallback)
				streamingExecutor.Discard()
			}
			if len(contentBlocks) > 0 {
				e.appendMessage(types.Message{
					Role:       types.RoleAssistant,
					Content:    contentBlocks,
					Model:      model,
					StopReason: stopReason,
					Usage:      &usage,
					Timestamp:  time.Now(),
				})
				if len(orphanedBlocks) > 0 {
					e.appendMessage(types.Message{
						Role:    types.RoleUser,
						Content: orphanedBlocks,
					})
				}
			}
			return nil, nil, ShouldAbort(ctx, "streaming")
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
				// NOTE: do NOT emit EventUsage from message_start.
				// MiniMax message_start usage is unreliable (often all-zero).
				// Only emit from message_delta which is consistently accurate.
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				cb := *event.ContentBlock
				contentBlocks = append(contentBlocks, cb)
				switch cb.Type {
				case types.ContentTypeToolUse:
					acc := &blockAccumulator{toolID: cb.ID, toolName: cb.Name}
					bidx := event.Index
					for len(blockAcc) <= bidx {
						blockAcc = append(blockAcc, nil)
					}
					blockAcc[bidx] = acc
					summary := e.computeSummary(cb.Name, cb.Input)
					e.emitEvent(types.QueryEvent{
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
					e.emitEvent(types.QueryEvent{
						Type: types.EventThinkingStart,
					})
				case types.ContentTypeText:
					e.emitEvent(types.QueryEvent{
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
					e.emitEvent(types.QueryEvent{
						Type: types.EventTextDelta,
						Text: event.Delta.Text,
					})
				case "input_json_delta":
					if event.Index < len(blockAcc) && blockAcc[event.Index] != nil {
						acc := blockAcc[event.Index]
						acc.toolInput.WriteString(event.Delta.PartialJSON)
						summary := e.computeSummary(acc.toolName, json.RawMessage(acc.toolInput.String()))
						e.emitEvent(types.QueryEvent{
							Type: types.EventToolParamDelta,
							PartialInput: &types.PartialInputEvent{
								ID:      acc.toolID,
								Name:    acc.toolName,
								Delta:   event.Delta.PartialJSON,
								Summary: summary,
							},
						})
					}
				case "thinking_delta":
					currentText.WriteString(event.Delta.Thinking)
					e.emitEvent(types.QueryEvent{
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
					e.emitEvent(types.QueryEvent{
						Type: types.EventTextEnd,
					})
				case types.ContentTypeToolUse:
					if idx < len(blockAcc) && blockAcc[idx] != nil {
						cb.Input = json.RawMessage(blockAcc[idx].toolInput.String())
					}
					// Source: query.ts:841-843 — addTool as soon as input is complete.
					// Tools begin executing during LLM streaming, not after.
					if streamingExecutor == nil {
						// Build ToolUseContext with all tools and pending MCP servers.
						// Previously passed nil, which forced each tool to create a
						// minimal context without access to the full tool map.
						// When ToolSearch is active, use filtered tool map so undiscovered
						// deferred tools return "No such tool available" instead of executing.
						executorToolMap := e.tools
						if toolSearchActive && len(activeTools) > 0 {
							executorToolMap = make(map[string]tool.Tool, len(activeTools))
							for _, t := range activeTools {
								executorToolMap[t.Name()] = t
							}
						}
						baseTctx := &tool.ToolUseContext{
							Ctx: ctx,
							WorkingDir: e.workingDir,
							Options: tool.ToolUseOptions{
								Tools:             e.tools,
								PendingMCPServers: e.pendingMCPServerNames(),
								SessionID:         e.sessionID,
							},
						}
						streamingExecutor = NewStreamingToolExecutor(
							executorToolMap, baseTctx,
							func(evt types.QueryEvent) { e.emitEvent(evt) },
							ctx,
						)
						streamingExecutor.SetHooks(e.hooks, e.sessionID)
						streamingExecutor.SetPermissionChecker(e.permissionChecker)
						streamingExecutor.SetFileHistory(e.fileHistory)
						if e.isSubagent {
							streamingExecutor.SetSubEngine(true)
						}
					}
					e.emitEvent(types.QueryEvent{
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
					e.emitEvent(types.QueryEvent{
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
				e.emitEvent(types.QueryEvent{
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
	} else if currentText.Len() > 0 {
		// Finalize text blocks that didn't get content_block_stop.
		for i := range contentBlocks {
			if contentBlocks[i].Type == types.ContentTypeText && contentBlocks[i].Text == "" {
				contentBlocks[i].Text = currentText.String()
				break
			}
		}
	}

	// Post-loop: stream ended without message_stop — determine why.
	//
	// Go channel semantics create a control flow gap: when ctx is cancelled,
	// the provider closes streamCh, causing for-range to exit naturally
	// without triggering the select <-ctx.Done() guard inside the loop.
	if !streamComplete {
		if ctx.Err() != nil {
			// User cancelled — provider closed channel after detecting ctx cancellation.
			var orphanedBlocks []types.ContentBlock
			if streamingExecutor != nil {
				orphanedBlocks = SyntheticToolResultsForBlocks(contentBlocks, nil, AbortReasonStreamingFallback)
				streamingExecutor.Discard()
			}
			if len(contentBlocks) > 0 {
				e.appendMessage(types.Message{
					Role:       types.RoleAssistant,
					Content:    contentBlocks,
					Model:      model,
					StopReason: stopReason,
					Usage:      &usage,
					Timestamp:  time.Now(),
				})
				if len(orphanedBlocks) > 0 {
					e.appendMessage(types.Message{
						Role:    types.RoleUser,
						Content: orphanedBlocks,
					})
				}
			}
			return nil, nil, ShouldAbort(ctx, "streaming")
		}
		if hasContent {
			// Genuine stream failure: content received but no stop signal.
			if streamingExecutor != nil {
				streamingExecutor.Discard()
			}
			e.logger.Error("stream interrupted", "contentBlocks", len(contentBlocks), "model", model)
			return nil, nil, fmt.Errorf("stream interrupted: response incomplete (no stop_reason received)")
		}
		// No content and no cancel — stream ended immediately without explanation.
		e.logger.Error("stream ended without content or completion signal")
		return nil, nil, fmt.Errorf("stream ended without content or completion signal")
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
// Fallback chain: ToolWithSummary → Description() → extractSummaryFromPartial.
func (e *Engine) computeSummary(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	if t, ok := e.tools[name]; ok {
		// 1. Try ToolWithSummary (MCP tools with SearchHint or param extraction)
		if ts, ok := t.(tool.ToolWithSummary); ok {
			if s := ts.Summary(input); s != "" {
				return s
			}
		}
		// 2. Fallback: Description() for built-in tools
		if desc, err := t.Description(input); err == nil && desc != "" {
			return desc
		}
	}
	// 3. Final: extract from partial JSON
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
	// MCP tools and unknown tools: try common param names
	for _, field := range []string{"url", "query", "file_path", "pattern", "command", "path"} {
		if v := extractJSONStringField(partial, field, "", 60); v != "" {
			return v
		}
	}
	return ""
}

// extractJSONStringField extracts a string field value from potentially incomplete JSON.
func extractJSONStringField(jsonStr, fieldName, prefix string, maxLen int) string {
	key := `"` + fieldName + `"`
	_, after, ok := strings.Cut(jsonStr, key)
	if !ok {
		return ""
	}
	rest := after
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

// currentInputTokens estimates current context token count.
// TS align: tokenCountWithEstimation (utils/tokens.ts:226).
//
// When ContextTokens > 0 (normal case: restored from DB or set after API call),
// it holds the precise context size from the last API response. We add an
// estimated delta for messages added since that response (tool results, user
// queries) by scanning backward to find the last assistant message and
// estimating everything after it.
//
// When ContextTokens <= 0 (abnormal: DB restore failed or pre-first-call),
// logs an error and falls back to full message estimation.
func (e *Engine) currentInputTokens() int {
	e.mu.RLock()
	last := e.ContextTokens
	msgs := e.messages
	e.mu.RUnlock()

	if last <= 0 {
		// Abnormal state — DB persistence should have restored a value,
		// and the first API call sets it. Log and fall back.
		e.logger.Error("currentInputTokens: ContextTokens <= 0",
			"ContextTokens", last,
			"message_count", len(msgs))
		return EstimateMessagesTokens(msgs)
	}

	// Find last assistant message — everything after it is the delta
	// (tool results, user queries) not yet counted in ContextTokens.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == types.RoleAssistant {
			delta := EstimateMessagesTokens(msgs[i+1:])
			return last + delta
		}
	}

	// No assistant message found — ContextTokens already accounts for
	// all messages in history. In real operation this only happens when
	// messages were loaded from DB without an assistant response (rare).
	// Can't determine delta boundary, so return the precise value as-is.
	return last
}

// maybeTokenPrune attempts token-based tool result pruning when the context
// is approaching the blocking limit and auto-compact could not help.
// Returns nil if pruning is not needed or not possible.
func (e *Engine) maybeTokenPrune() *TokenPruneResult {
	config := getTokenPruneConfig()
	if !config.Enabled {
		return nil
	}

	// Compute token budget (same as blocking limit threshold)
	reservedTokens := min(e.maxTokens, maxOutputTokensForSummary)
	effectiveWindow := e.autoCompactConfig.ContextWindow - reservedTokens
	tokenBudget := effectiveWindow - manualCompactBufferTokens

	currentTokens := e.currentInputTokens()
	if currentTokens <= tokenBudget {
		return nil
	}

	result := maybeTokenBasedMicrocompact(
		e.Messages(),
		currentTokens,
		tokenBudget,
		config,
		e.querySource(),
		e.logger,
	)
	if result == nil {
		return nil
	}

	// Apply pruned messages to engine state
	e.setMessages(result.Messages)
	return result
}

// shouldAutoCompact returns true if proactive auto-compact should be triggered.
// TS align: autoCompact.ts:shouldAutoCompact()
func (e *Engine) shouldAutoCompact() bool {
	// Estimate tokens first (takes its own RLock) to avoid nested locking.
	tokens := e.currentInputTokens()

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.compactor == nil {
		return false
	}
	cfg := e.autoCompactConfig
	if cfg.ContextWindow <= 0 {
		return false
	}
	// Recursion guard: compact and session_memory are forked agents
	// that would deadlock if they triggered another compact.
	// Source: TS autoCompact.ts:169-172
	src := e.querySource()
	if src == QuerySourceCompact || src == QuerySourceSessionMemory || src == QuerySourceAutoDream {
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
	// TS formula: effectiveWindow = contextWindow - min(maxTokens, 20K)
	// threshold = effectiveWindow - 13K
	reservedTokens := min(e.maxTokens, maxOutputTokensForSummary)
	effectiveWindow := cfg.ContextWindow - reservedTokens
	threshold := effectiveWindow - autoCompactBuffer(effectiveWindow)
	return tokens > threshold
}

// runCompact runs the compactor and applies the result to engine state.
// It atomically updates messages and ContextTokens, eliminates the redundant
// messageDelta calculation, and rewrites BeforeTokens/AfterTokens to use real
// API token values for display. Returns the rewritten result for event emission.
// The caller handles pre-conditions, event emissions, hooks, and post-compact bookkeeping.
//
// Goroutine safety: There is a theoretical TOCTOU window between the RLock
// (reading realTokens) and the later Lock (updating messages + ContextTokens).
// This is safe in practice because both reactive and proactive compact are
// serialized within the query loop — only one compact can run at a time.
func (e *Engine) runCompact(ctx context.Context, logLabel string) (*CompactResult, error) {
	e.mu.RLock()
	realTokens := e.ContextTokens
	sm := e.sessionMemory
	comp := e.compactor
	e.mu.RUnlock()

	// Try SM-compact first if session memory is available.
	// TS source: sessionMemoryCompact.ts — trySessionMemoryCompaction runs before LLM compact.
	if sm != nil {
		if ac, ok := comp.(*AutoCompactor); ok {
			if result, _ := ac.TrySMCompact(e.Messages(), sm); result != nil {
				// SM-compact succeeded
				messageDelta := result.BeforeTokens - result.AfterTokens
				e.mu.Lock()
				e.messages = result.Messages
				e.ContextTokens = e.ContextTokens - messageDelta
				if e.ContextTokens < 0 {
					e.logger.Error(logLabel+": contextTokens went negative (sm-compact)",
						"before", e.ContextTokens+messageDelta, "delta", messageDelta)
				}
				e.mu.Unlock()
				result.BeforeTokens = realTokens
				result.AfterTokens = realTokens - messageDelta
				return result, nil
			}
		}
	}

	// Fall back to LLM summarization compact
	result, err := comp.Compact(ctx, e.Messages())
	if err != nil {
		return nil, err
	}

	// Atomically update messages and ContextTokens to prevent
	// concurrent readers from seeing new messages with stale token counts.
	messageDelta := result.BeforeTokens - result.AfterTokens
	e.mu.Lock()
	e.messages = result.Messages
	e.ContextTokens = e.ContextTokens - messageDelta
	if e.ContextTokens < 0 {
		e.logger.Error(logLabel+": contextTokens went negative",
			"before", e.ContextTokens+messageDelta, "delta", messageDelta)
	}
	e.mu.Unlock()

	// Replace heuristic BeforeTokens/AfterTokens with real API values.
	result.BeforeTokens = realTokens
	result.AfterTokens = realTokens - messageDelta
	if result.AfterTokens < 0 {
		e.logger.Error(logLabel+": AfterTokens went negative",
			"realTokens", realTokens, "messageDelta", messageDelta)
	}
	return result, nil
}

// marshalMessages converts internal messages to API format.
// Strips response-only fields (Timestamp, Model, StopReason, Usage) that
// the Anthropic Messages API does not accept in request messages.
// Source: TS normalizeMessagesForAPI (no attachments, tool references, or virtual messages).
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

// applyBudget applies the per-message tool result budget to API messages.
// Converts types.Message → toolresult.BudgetMessage, runs the budget algorithm,
// then copies modified tool_result content back to the types.Message slice.
func (e *Engine) applyBudget(msgs []types.Message) []types.Message {
	if e.contentReplacementState == nil {
		return msgs
	}

	// Convert to budget messages.
	budgetMsgs := make([]toolresult.BudgetMessage, len(msgs))
	for i, msg := range msgs {
		blocks := make([]toolresult.BudgetBlock, len(msg.Content))
		for j, b := range msg.Content {
			blocks[j] = toolresult.BudgetBlock{
				Type:      string(b.Type),
				ID:        b.ID,
				Name:      b.Name,
				ToolUseID: b.ToolUseID,
				Content:   b.Content,
			}
		}
		budgetMsgs[i] = toolresult.BudgetMessage{
			ID:      msg.ID,
			Role:    string(msg.Role),
			Content: blocks,
		}
	}

	// Apply budget.
	skipNames := map[string]bool{"Read": true} // Read has Infinity limit — never budget
	result, records := toolresult.EnforceToolResultBudget(budgetMsgs, e.contentReplacementState, e.sessionID, skipNames)

	// Persist records to transcript for session resume stability.
	if len(records) > 0 && e.recordWriter != nil {
		e.recordWriter(records)
	}

	// Copy modified content back.
	changed := false
	for i := range msgs {
		if result[i].Role != "user" {
			continue
		}
		for j := range result[i].Content {
			b := &result[i].Content[j]
			if b.Type == "tool_result" && !bytes.Equal(b.Content, msgs[i].Content[j].Content) {
				msgs[i].Content[j].Content = b.Content
				changed = true
			}
		}
	}

	if changed {
		e.logger.Debug("applyBudget: modified tool result content")
	}

	return msgs
}
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

// refreshTools rebuilds the tool map and order from the provider if set,
// then merges MCP tools from the registry.
// Called at the start of each callLLM so late-registered tools are visible.
func (e *Engine) refreshTools() {
	if e.toolsProvider == nil {
		return
	}
	e.tools = e.toolsProvider()

	// Merge MCP tools from registry into the tool map.
	for _, t := range e.MCPTools() {
		e.tools[t.Name()] = t
	}

	// Register MCP resource tools only when at least one server supports resources.
	// Source: client.ts:2171-2198 — conditional tool injection.
	// gbot unifies into one path — refreshTools rebuilds from scratch each call.
	if e.mcpRegistry != nil && e.mcpRegistry.HasResourceSupport() {
		// Name-collision guard: don't register if an MCP server already provides these tools.
		// Source: client.ts:2183-2190 — toolMatchesName check
		if _, exists := e.tools["ListMcpResources"]; !exists {
			e.tools["ListMcpResources"] = mcpresource.NewListMcpResources(e.mcpRegistry)
		}
		if _, exists := e.tools["ReadMcpResource"]; !exists {
			e.tools["ReadMcpResource"] = mcpresource.NewReadMcpResource(e.mcpRegistry)
		}
	}

	// Register ToolSearch when deferred tool count meets activation threshold.
	// Source: utils/toolSearch.ts — shouldEnableToolSearch
	deferredCount := 0
	for _, t := range e.tools {
		if tool.IsDeferred(t) {
			deferredCount++
		}
	}
	if deferredCount > 0 {
		if _, exists := e.tools[ToolSearchToolName]; !exists {
			e.tools[ToolSearchToolName] = toolsearch.New()
		}
	}

	e.toolOrder = make([]string, 0, len(e.tools))
	for name := range e.tools {
		e.toolOrder = append(e.toolOrder, name)
	}
	slices.Sort(e.toolOrder)
}

// ---------------------------------------------------------------------------
// MCP integration — tool merging + shutdown
// ---------------------------------------------------------------------------

// MCPTools returns MCP tools as tool.Tool adapters from the registry.
func (e *Engine) MCPTools() []tool.Tool {
	if e.mcpRegistry == nil {
		return nil
	}
	discovered := e.mcpRegistry.GetTools()
	tools := make([]tool.Tool, 0, len(discovered))
	for _, dt := range discovered {
		tools = append(tools, NewMCPTool(dt, e.mcpRegistry))
	}
	return tools
}

// AllTools returns the combined tool map including MCP tools.
func (e *Engine) AllTools() map[string]tool.Tool {
	result := make(map[string]tool.Tool, len(e.tools))
	maps.Copy(result, e.tools)
	for _, t := range e.MCPTools() {
		result[t.Name()] = t
	}
	return result
}

// pendingMCPServerNames returns the names of MCP servers that are still connecting.
// Source: ToolSearchTool.ts:335-339 — getPendingServerNames()
func (e *Engine) pendingMCPServerNames() []string {
	if e.mcpRegistry == nil {
		return nil
	}
	return e.mcpRegistry.PendingServerNames()
}

// Close shuts down the engine and its MCP registry.
func (e *Engine) Close() {
	if e.onCloseFn != nil {
		e.onCloseFn(e.sessionID)
	}

	if e.hooks != nil {
		_ = e.hooks.SessionEnd(context.Background(), &hooks.HookInput{
			HookEventName: string(hooks.HookSessionEnd),
			SessionID:     e.sessionID,
		})
	}
	if e.mcpRegistry != nil {
		_ = e.mcpRegistry.Close()
	}
}

func (e *Engine) SetOnClose(fn func(sessionID string)) {
	e.onCloseFn = fn
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
	e.ContextTokens = 0
	e.toolSearch = newToolSearchState()
	e.mu.Unlock()
}

// appendMessage adds a message to the history under Lock.
func (e *Engine) appendMessage(msg types.Message) {
	e.mu.Lock()
	e.messages = append(e.messages, msg)
	e.mu.Unlock()
}

// appendInlineInterruptMessage appends the interrupt message to the last
// assistant message's content, searching backwards. This handles the case
// where tool results were appended after the assistant message.
// Also generates synthetic tool_result blocks for any orphaned tool_use
// blocks (tool_use without matching tool_result) to prevent API errors.
//
// Source: TS createUserInterruptionMessage + yieldMissingToolResultBlocks
// (query.ts:1015-1029) — generates synthetic tool_result blocks for all
// tool_use blocks in assistant messages when the abort signal fires.
func (e *Engine) appendInlineInterruptMessage() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.messages) == 0 {
		return
	}
	last := &e.messages[len(e.messages)-1]
	last.Content = append(last.Content, types.NewTextBlock(types.InterruptMessage))
	e.logger.Info("engine:append_inline_interrupt",
		"msg_index", len(e.messages)-1,
		"role", last.Role,
		"total_messages", len(e.messages),
		"content_blocks", len(last.Content))

	// TS align: yieldMissingToolResultBlocks (query.ts:123-149).
	// For each assistant message, find tool_use blocks that lack a matching
	// tool_result and generate synthetic error tool_results for them.
	e.appendSyntheticToolResultsLocked()
}

// appendSyntheticToolResultsLocked generates synthetic tool_result blocks for
// any tool_use blocks in assistant messages that don't have a matching
// tool_result in subsequent user messages. Must be called with e.mu held.
func (e *Engine) appendSyntheticToolResultsLocked() {
	// Collect all tool_use IDs that already have matching tool_results.
	hasResult := make(map[string]bool)
	for _, msg := range e.messages {
		if msg.Role != types.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolResult {
				hasResult[block.ToolUseID] = true
			}
		}
	}

	// Find orphaned tool_use IDs from all assistant messages.
	var orphans []string
	for _, msg := range e.messages {
		if msg.Role != types.RoleAssistant {
			continue
		}
		for _, block := range msg.Content {
			if block.Type == types.ContentTypeToolUse && !hasResult[block.ID] {
				orphans = append(orphans, block.ID)
			}
		}
	}

	if len(orphans) == 0 {
		return
	}

	// Build synthetic tool_result blocks for all orphaned tool_use IDs.
	blocks := make([]types.ContentBlock, 0, len(orphans))
	for _, id := range orphans {
		blocks = append(blocks, CreateSyntheticErrorBlock(id, AbortReasonUserInterrupted))
	}

	e.messages = append(e.messages, types.Message{
		Role:    types.RoleUser,
		Content: blocks,
	})
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
	RestoreToolSearchState(msgs, e.toolSearch)
	e.mu.Unlock()
}

// SetMessages replaces the engine's message history under Lock.
func (e *Engine) SetMessages(msgs []types.Message) {
	e.setMessages(msgs)
}

// SetFileHistory sets the file history tracker for rewind/restore.
// Must be called after Engine construction, before any tool execution.
// Sub-engines inherit the same Tracker automatically.
func (e *Engine) SetFileHistory(fh *filehistory.Tracker) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fileHistory = fh
}

// RewindResult contains information about what was rewound.
type RewindResult struct {
	MessageCount  int      // number of messages after rewind
	RestoredFiles []string // files restored to pre-edit state
}

// RewindTo truncates conversation to messages[:idx] and restores file history.
// Returns RewindResult with info about what was restored, or error if idx is invalid.
// Thread-safe: acquires Engine lock internally.
func (e *Engine) RewindTo(idx int) (*RewindResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if idx < 0 || idx > len(e.messages) {
		return nil, fmt.Errorf("rewind index %d out of range [0, %d]", idx, len(e.messages))
	}

	result := &RewindResult{}

	// 1. Truncate messages + rebuild toolSearch
	e.messages = e.messages[:idx]
	RestoreToolSearchState(e.messages, e.toolSearch)
	result.MessageCount = len(e.messages)

	// 2. Restore file history (if enabled)
	if e.fileHistory != nil {
		restored, err := e.fileHistory.RestoreToIndex(idx)
		if err != nil {
			// Log but don't fail — message rewind is more important than file restore
			e.logger.Error("engine:rewind:file_restore_failed", "err", err)
		} else {
			result.RestoredFiles = restored
		}
	}

	return result, nil
}

// SetSessionID sets the session ID for this engine.
func (e *Engine) SetSessionID(id string) {
	e.mu.Lock()
	e.sessionID = id
	e.mu.Unlock()
}

// SessionID returns the current session ID.
func (e *Engine) SessionID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.sessionID
}

// SetModel sets the model name for subsequent API calls.
func (e *Engine) SetModel(model string) {
	e.mu.Lock()
	e.model = model
	e.mu.Unlock()
}

// SetProvider replaces the LLM provider for subsequent API calls.
func (e *Engine) SetProvider(provider llm.Provider) {
	e.mu.Lock()
	e.provider = provider
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

// RegisterPostTurnHook adds a hook to be called after each successful agentic turn.
// TS source: registerPostSamplingHook in postSamplingHooks.ts.
func (e *Engine) RegisterPostTurnHook(hook PostTurnHook) {
	e.mu.Lock()
	e.postTurnHooks = append(e.postTurnHooks, hook)
	e.mu.Unlock()
}

// firePostTurnHooks calls all registered post-turn hooks.
// Only fires when auto-compact is enabled (ContextWindow > 0), matching TS behavior
// where session memory depends on auto-compact. Hooks run sequentially; errors are
// logged but do not abort the loop (fire-and-forget).
// TS source: executePostSamplingHooks in postSamplingHooks.ts.
func (e *Engine) firePostTurnHooks(ctx context.Context) {
	if e.isSubagent || e.autoCompactConfig.ContextWindow <= 0 {
		return
	}
	e.mu.RLock()
	hooks := make([]PostTurnHook, len(e.postTurnHooks))
	copy(hooks, e.postTurnHooks)
	msgs := e.messages
	src := e.querySource()
	e.mu.RUnlock()
	for _, hook := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.logger.Warn("post-turn hook panicked", "error", r)
				}
			}()
			hook(ctx, msgs, e.ContextTokens, src)
		}()
	}
}

// SetRecordWriter configures the callback for persisting ContentReplacementRecords.
// TS: writeToTranscript callback (toolResultStorage.ts:924-936).
func (e *Engine) SetRecordWriter(fn func([]toolresult.ContentReplacementRecord)) {
	e.mu.Lock()
	e.recordWriter = fn
	e.mu.Unlock()
}

// UpdateAutoCompactConfig updates only the auto-compact configuration
// without replacing the compactor. Used during model switch to update
// context window and max tokens.
func (e *Engine) UpdateAutoCompactConfig(cfg AutoCompactConfig) {
	e.mu.Lock()
	e.autoCompactConfig = cfg
	e.mu.Unlock()
}

// SetSessionMemory configures the session memory manager for this engine.
// Also registers the session memory extraction as a post-turn hook.
// TS source: sessionMemory.ts:357 — initSessionMemory.
func (e *Engine) SetSessionMemory(sm *session.SessionMemory) {
	e.mu.Lock()
	e.sessionMemory = sm
	e.mu.Unlock()

	if sm != nil {
		e.RegisterPostTurnHook(func(ctx context.Context, messages []types.Message, currentTokens int, querySource string) {
			// Only extract on main thread — TS: querySource check
			if !isMainThreadSource(querySource) {
				return
			}
			if !sm.ShouldExtract(currentTokens, messages) {
				return
			}
			// Count tool calls in last assistant turn
			toolCalls := 0
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Role == types.RoleAssistant {
					for _, block := range messages[i].Content {
						if block.Type == types.ContentTypeToolUse {
							toolCalls++
						}
					}
					break
				}
			}
			sm.RecordToolCalls(toolCalls)

			// Run extraction asynchronously — TS: extractSessionMemory runs in background via runForkedAgent
			go func() {
				if err := sm.Extract(ctx, messages, currentTokens); err != nil {
					e.logger.Warn("session memory extraction failed", "error", err)
				}
			}()
		})
	}
}

// ContextWindow returns the configured context window size in tokens.
func (e *Engine) ContextWindow() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.autoCompactConfig.ContextWindow
}

// SetMaxTokens updates the max output tokens for the current model.
// Called during model switch to reflect the new model's capabilities.
func (e *Engine) SetMaxTokens(n int) {
	e.mu.Lock()
	e.maxTokens = n
	e.mu.Unlock()
}

// ---------------------------------------------------------------------------
// TaggedDispatcher — wraps parent dispatcher to inject AgentMeta into sub-agent events
// ---------------------------------------------------------------------------

// taggedDispatcher wraps an types.EventDispatcher and injects AgentMeta into every event.
// Used by sub-engines so their tool events reach the parent TUI with agent context.
type taggedDispatcher struct {
	parent types.EventDispatcher
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
	Tools           map[string]tool.Tool // filtered tool set
	MaxTurns        int                  // 0 = no limit
	Model           string               // "" = inherit from parent
	ParentToolUseID string               // parent Agent tool call ID for event tagging
	AgentType       string               // "General", "Explore", "Plan"
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
	slices.Sort(toolOrder)

	// If parent has a dispatcher, wrap it to tag sub-agent events.
	var dispatcher types.EventDispatcher
	if e.dispatcher != nil && opts.ParentToolUseID != "" {
		depth := e.agentMetaDepth + 1
		dispatcher = &taggedDispatcher{
			parent: e.dispatcher,
			meta: &types.AgentMeta{
				ParentToolUseID: opts.ParentToolUseID,
				AgentType:       opts.AgentType,
				Depth:           depth,
			},
		}
	}

	return &Engine{
		provider:                e.provider,
		tools:                   opts.Tools,
		toolOrder:               toolOrder,
		model:                   model,
		maxTokens:               e.maxTokens,
		logger:                  e.logger,
		messages:                []types.Message{},
		tokenBudget:             0, // sub-agents bypass budget checks via isSubagent
		turnCount:               0,
		dispatcher:              dispatcher,
		notifications:           &notificationQueue{},
		isSubagent:              true,
		agentType:               opts.AgentType,
		maxTurns:                subMaxTurns(opts.MaxTurns),
		compactor:               e.compactor,
		autoCompactConfig:       e.autoCompactConfig,
		mcpRegistry:             e.mcpRegistry,
		hooks:                   e.hooks,
		permissionChecker:       e.permissionChecker,
		contentReplacementState: toolresult.CloneContentReplacementState(e.contentReplacementState),
			toolSearch:             newToolSearchState(), // fresh state; parent tools inherited via opts.Tools
			sessionID:            e.sessionID + "-sub-" + fmt.Sprintf("%d", subEngineSeq.Add(1)),
			onCloseFn:            e.onCloseFn,
		fileHistory:          e.fileHistory, // share same Tracker — sub-agent edits tracked too
			workingDir:            e.workingDir,
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

// querySource returns the query source identifier for prompt caching and microcompact.
// Source: utils/promptCategory.ts:16-28 — getQuerySourceForAgent()
func (e *Engine) querySource() string {
	if !e.isSubagent {
		return QuerySourceReplMainThread
	}
	// Forked agents with recursion guards — must return their dedicated
	// QuerySource constant so shouldAutoCompact can prevent deadlocks.
	if e.agentType == "compact" {
		return QuerySourceCompact
	}
	if e.agentType == "session_memory" {
		return QuerySourceSessionMemory
	}
	if e.agentType == "auto_dream" {
		return QuerySourceAutoDream
	}
	if isBuiltInAgent(e.agentType) {
		return "agent:builtin:" + e.agentType
	}
	return QuerySourceAgentCustom
}

// QuerySync executes the agentic loop synchronously (no goroutine, no channels).
// Used by sub-agents created via AgentTool. EventCh is nil — events are silently discarded.
// Source: TS sync sub-agents execute runAgent() directly in the caller's context.
func (e *Engine) QuerySync(ctx context.Context, userMessage string, systemPrompt json.RawMessage) QueryResult {
	return e.queryLoop(ctx, userMessage, systemPrompt)
}

// QueryWithExistingMessages executes the agentic turn loop starting from
// pre-constructed messages (no user message injection). Used by fork agents
// that build their own conversation history.
func (e *Engine) QueryWithExistingMessages(ctx context.Context, messages []types.Message, systemPrompt json.RawMessage) QueryResult {
	e.setMessages(messages)
	return e.runTurns(ctx, systemPrompt)
}

// Model returns the engine's model name.
func (e *Engine) Model() string { return e.model }

// SystemPrompt returns the stored system prompt bytes.
func (e *Engine) SystemPrompt() json.RawMessage { return e.systemPrompt }

// SetSystemPrompt stores the system prompt for later access by fork agents.
func (e *Engine) SetSystemPrompt(sp json.RawMessage) { e.systemPrompt = sp }

// subMaxTurns returns the max turns for a sub-engine.
// 0 or negative means no limit (same as TS built-in agents).
func subMaxTurns(n int) int {
	if n <= 0 {
		return 0
	}
	return n
}

// SetDispatcher is exported for use by external test packages.
// It allows tests to observe events dispatched by the engine.
func (e *Engine) SetDispatcher(d types.EventDispatcher) {
	e.dispatcher = d
}

// Dispatcher returns the event dispatcher for virtual tool events (e.g. dream).
func (e *Engine) Dispatcher() types.EventDispatcher {
	return e.dispatcher
}
