package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Tool tracking types — source: StreamingToolExecutor.ts:19-32
// ---------------------------------------------------------------------------

// ToolStatus tracks the execution state of a tool call.
// Source: StreamingToolExecutor.ts:19 — 'queued' | 'executing' | 'completed' | 'yielded'
type ToolStatus int

const (
	StatusQueued    ToolStatus = iota // Tool is waiting to execute
	StatusExecuting                   // Tool is currently running
	StatusCompleted                   // Tool has finished
	StatusYielded                     // Results have been emitted
)

// TrackedTool tracks a single tool call through the execution lifecycle.
// Source: StreamingToolExecutor.ts:22-32 — TrackedTool type.
type TrackedTool struct {
	ID                string
	Name              string
	Input             json.RawMessage
	Status            ToolStatus
	IsConcurrencySafe bool
	Duration          time.Duration
	Result            *tool.ToolResult
	Err               error

	// done is closed when the tool completes (status → completed).
	// Callers wait on this to be notified of completion.
	done chan struct{}

	// resultBlocks holds the content blocks produced by this tool.
	resultBlocks []types.ContentBlock

	// newMessages holds messages injected by the tool (e.g., SkillTool content).
	// These are prepended before the tool_result message in the conversation.
	newMessages []types.Message
}

// ---------------------------------------------------------------------------
// StreamingToolExecutor — concurrent tool execution
// Source: StreamingToolExecutor.ts
//
// Concurrent-safe tools (Read, Glob, Grep, read-only Bash) run in parallel.
// Non-concurrent tools (Edit, Write, write Bash) require exclusive access.
// Results are returned in insertion order.
// ---------------------------------------------------------------------------

// StreamingToolExecutor executes tools with concurrency control.
// Source: StreamingToolExecutor.ts:40-51
type StreamingToolExecutor struct {
	mu        sync.Mutex
	tools     []*TrackedTool
	toolMap   map[string]tool.Tool
	emitEvent func(types.QueryEvent)
	tctx      *types.ToolUseContext
	messages  []types.Message // conversation history for tool context (set after assistant msg append)

	// Three-tier abort (TS: abortController.ts, spec:3750-3810)
	// rootCtx → siblingCtx via context.WithCancelCause.
	// siblingCancel kills all sibling tools but does NOT end the query.
	rootCtx       context.Context
	siblingCtx    context.Context
	siblingCancel context.CancelCauseFunc

	hasErrored  bool
	errToolDesc string
	discarded   bool

	// hooks is the lifecycle hooks system for PreToolUse/PostToolUse.
	hooks     *hooks.Hooks
	sessionID string // session ID for hook input construction

	// permChecker is the permission rules checker. Nil = default allow.
	// Set by engine before tool execution. Inherited by sub-engines.
	permChecker permission.PermissionChecker

	// sessionAllowed caches "Allow always" decisions for the current session.
	// Key format: "ToolName" or "ToolName:contentPattern".
	// 修正 3: session-scoped allow cache
	sessionAllowed map[string]bool

	// askMu serializes concurrent ask dialogs. Only one ask at a time.
	// 修正 4: concurrent ask serialization
	askMu sync.Mutex

	// isSubEngine is true for sub-engine executors. Sub-engines auto-deny asks
	// since they run in the background and can't show interactive dialogs.
	// 修正 6: sub-engine ask strategy
	isSubEngine bool

}

// NewStreamingToolExecutor creates a new concurrent tool executor.
// Source: StreamingToolExecutor.ts:53-62 — constructor.
func NewStreamingToolExecutor(
	toolMap map[string]tool.Tool,
	tctx *types.ToolUseContext,
	emitEvent func(types.QueryEvent),
	rootCtx context.Context,
) *StreamingToolExecutor {
	siblingCtx, siblingCancel := context.WithCancelCause(rootCtx)
	return &StreamingToolExecutor{
		tools:         make([]*TrackedTool, 0),
		toolMap:       toolMap,
		emitEvent:     emitEvent,
		tctx:          tctx,
		rootCtx:       rootCtx,
		siblingCtx:    siblingCtx,
		siblingCancel: siblingCancel,
	}
}

// SetMessages sets the conversation history on the executor.
// Called from engine.go after the assistant message is appended (so messages
// include the triggering assistant turn) but before ExecuteAll runs.
// This ensures tools like Agent can access the full parent conversation
// for fork-agent message construction.
func (e *StreamingToolExecutor) SetMessages(messages []types.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = messages
}

// SetHooks injects the hooks system into the executor.
// Called from engine.go after construction.
func (e *StreamingToolExecutor) SetHooks(h *hooks.Hooks, sessionID string) {
	e.hooks = h
	e.sessionID = sessionID
}

// SetPermissionChecker injects the permission checker into the executor.
// Called from engine.go when creating the streaming executor.
func (e *StreamingToolExecutor) SetPermissionChecker(pc permission.PermissionChecker) {
	e.permChecker = pc
}

// SetSubEngine marks this executor as running inside a sub-engine.
// 修正 6: sub-engine ask strategy
func (e *StreamingToolExecutor) SetSubEngine(v bool) {
	e.isSubEngine = v
}

// askUser asks the user for permission via TUI dialog.
// Blocks until the user responds, a context is cancelled, or the executor is discarded.
// 修正 2: uses doEmit. 修正 3: sessionAllowed cache. 修正 4: askMu serialization.
// 修正 6: sub-engine auto-deny. 修正 7: three-way select.
func (e *StreamingToolExecutor) askUser(tt *TrackedTool, decision permission.Decision, matchedContent string) types.PermissionUserDecision {
	// 修正 6: sub-engine directly deny
	if e.isSubEngine {
		return types.UserDecisionDeny
	}

	// 修正 3: check session-scoped cache
	cacheKey := tt.Name
	if matchedContent != "" {
		cacheKey = tt.Name + ":" + matchedContent
	}
	if e.sessionAllowed != nil && e.sessionAllowed[cacheKey] {
		return types.UserDecisionAllow
	}

	// 修正 4: serialize concurrent asks
	e.askMu.Lock()
	defer e.askMu.Unlock()

	// Double-check: cache may have been updated while waiting for lock
	if e.sessionAllowed != nil && e.sessionAllowed[cacheKey] {
		return types.UserDecisionAllow
	}

	decisionCh := make(chan types.PermissionUserDecision, 1)

	// Build RuleDetail string from matched rule
	ruleDetail := ""
	if decision.Rule != nil {
		ruleDetail = decision.Rule.Value.ToolName
		if decision.Rule.Value.RuleContent != nil {
			ruleDetail += "(" + *decision.Rule.Value.RuleContent + ")"
		}
		ruleDetail += " from " + decision.Rule.Source + " settings"
	}

	e.doEmit(types.QueryEvent{ // 修正 2: use doEmit
		Type: types.EventPermissionAsk,
		PermissionAsk: &types.PermissionAskEvent{
			ToolName:   tt.Name,
			Input:      tt.Input,
			Message:    decision.Message,
			RuleDetail: ruleDetail,
			ResponseCh: decisionCh,
		},
	})

	// 修正 7: three-way select
	select {
	case d, ok := <-decisionCh:
		if !ok {
			return types.UserDecisionDeny
		}
		if d == types.UserDecisionAllowAlways {
			// 修正 3: write to session cache
			if e.sessionAllowed == nil {
				e.sessionAllowed = make(map[string]bool)
			}
			e.sessionAllowed[cacheKey] = true
		}
		return d
	case <-e.rootCtx.Done():
		return types.UserDecisionDeny
	case <-e.siblingCtx.Done():
		return types.UserDecisionDeny
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// AddTool adds a tool block to the execution queue and starts it if eligible.
// Source: StreamingToolExecutor.ts:76-124 — addTool().
func (e *StreamingToolExecutor) AddTool(block types.ContentBlock) {
	t, ok := e.toolMap[block.Name]
	if !ok {
		// Source: StreamingToolExecutor.ts:78-100 — unknown tool → error result.
		errMsg := fmt.Sprintf("No such tool available: %s", block.Name)
		errBlock := CreateToolErrorBlock(block.ID, errMsg)
		e.doEmit(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     block.ID,
				Output:        errBlock.Content,
				DisplayOutput: errMsg,
				IsError:       true,
			},
		})
		tt := &TrackedTool{
			ID:                block.ID,
			Name:              block.Name,
			Input:             block.Input,
			Status:            StatusCompleted,
			IsConcurrencySafe: true,
			done:              make(chan struct{}),
			resultBlocks:      []types.ContentBlock{errBlock},
		}
		close(tt.done)
		e.mu.Lock()
		e.tools = append(e.tools, tt)
		e.mu.Unlock()
		return
	}

	// Source: StreamingToolExecutor.ts:104-113 — determine isConcurrencySafe.
	// If IsConcurrencySafe panics, treat as non-safe (matches TS catch behavior).
	isSafe := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				isSafe = false
			}
		}()
		isSafe = t.IsConcurrencySafe(block.Input)
	}()

	tt := &TrackedTool{
		ID:                block.ID,
		Name:              block.Name,
		Input:             block.Input,
		Status:            StatusQueued,
		IsConcurrencySafe: isSafe,
		done:              make(chan struct{}),
	}
	e.mu.Lock()
	e.tools = append(e.tools, tt)
	e.mu.Unlock()

	// Source: StreamingToolExecutor.ts:123 — processQueue after add.
	e.processQueue()
}

// Discard marks all pending and in-progress tools as discarded.
// Source: StreamingToolExecutor.ts:69-71 — discard().
func (e *StreamingToolExecutor) Discard() {
	e.mu.Lock()
	e.discarded = true
	e.mu.Unlock()
	e.siblingCancel(context.Canceled)
}

// doEmit emits an event only if the executor has not been discarded.
// After Discard(), the underlying channel may be closed, so we must
// skip emissions to avoid a "send on closed channel" panic.
func (e *StreamingToolExecutor) doEmit(evt types.QueryEvent) {
	e.mu.Lock()
	fn := e.emitEvent
	d := e.discarded
	e.mu.Unlock()
	if !d && fn != nil {
		fn(evt)
	}
}

// ExecuteAllResult holds the results from executing all tool blocks.
type ExecuteAllResult struct {
	ToolResultBlocks []types.ContentBlock
	NewMessages      []types.Message // all newMessages from all tools, in order
}

// ExecuteAll adds all tool blocks, runs them with concurrency, and returns
// results in insertion order. This is the main public API for the executor.
// Source: StreamingToolExecutor.ts — addTool + getRemainingResults combined.
func (e *StreamingToolExecutor) ExecuteAll(blocks []types.ContentBlock) *ExecuteAllResult {
	// Phase 1: Add all tool blocks (starts goroutines via processQueue).
	for _, block := range blocks {
		if block.Type != types.ContentTypeToolUse {
			continue
		}
		e.AddTool(block)
	}

	if len(e.tools) == 0 {
		return &ExecuteAllResult{}
	}

	// Phase 2: Wait for all tools to complete.
	// Source: StreamingToolExecutor.ts:453-490 — getRemainingResults().
	// Copy done channels under lock, then wait outside to avoid deadlock.
	e.mu.Lock()
	doneChans := make([]chan struct{}, len(e.tools))
	for i, tt := range e.tools {
		doneChans[i] = tt.done
	}
	e.mu.Unlock()

	for _, ch := range doneChans {
		select {
		case <-ch:
			// Tool completed normally.
		case <-e.rootCtx.Done():
			// Query cancelled. Tool goroutines detect siblingCtx cancellation
			// and complete shortly. Wait for this tool's goroutine to finish.
			<-ch
		}
	}

	// Phase 3: Collect results in insertion order.
	e.mu.Lock()
	defer e.mu.Unlock()

	var results []types.ContentBlock
	var allNewMessages []types.Message
	for _, tt := range e.tools {
		if len(tt.resultBlocks) > 0 {
			results = append(results, tt.resultBlocks...)
		}
		if len(tt.newMessages) > 0 {
			allNewMessages = append(allNewMessages, tt.newMessages...)
		}
	}
	return &ExecuteAllResult{
		ToolResultBlocks: results,
		NewMessages:      allNewMessages,
	}
}

// ---------------------------------------------------------------------------
// Concurrency control — source: StreamingToolExecutor.ts:129-151
// ---------------------------------------------------------------------------

// canExecuteTool checks if a tool can start based on current concurrency state.
// Source: StreamingToolExecutor.ts:129-135 — canExecuteTool().
//
// Invariant:
//
//	executing.length == 0 → any tool can start
//	executing all safe + new tool safe → parallel OK
//	new tool unsafe → only when nothing else running
//
// Must be called with e.mu held.
func (e *StreamingToolExecutor) canExecuteTool(isSafe bool) bool {
	var executing []*TrackedTool
	for _, t := range e.tools {
		if t.Status == StatusExecuting {
			executing = append(executing, t)
		}
	}
	if len(executing) == 0 {
		return true
	}
	if !isSafe {
		return false
	}
	for _, t := range executing {
		if !t.IsConcurrencySafe {
			return false
		}
	}
	return true
}

// processQueue iterates queued tools and starts those that can execute.
// Source: StreamingToolExecutor.ts:140-151 — processQueue().
func (e *StreamingToolExecutor) processQueue() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, tt := range e.tools {
		if tt.Status != StatusQueued {
			continue
		}
		if e.canExecuteTool(tt.IsConcurrencySafe) {
			tt.Status = StatusExecuting
			go e.executeTool(tt)
		} else if !tt.IsConcurrencySafe {
			// Can't execute this non-safe tool, and since we need to maintain
			// order for non-concurrent tools, stop here.
			// Source: StreamingToolExecutor.ts:148 — break on blocked non-safe.
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Tool execution — source: StreamingToolExecutor.ts:265-405
// ---------------------------------------------------------------------------

// getAbortReason determines why a tool should be cancelled.
// Source: StreamingToolExecutor.ts:210-231 — getAbortReason().
// Must be called with e.mu held.
// Tools with InterruptBlock are NOT cancelled on user interrupt.
func (e *StreamingToolExecutor) getAbortReason(t tool.Tool) string {
	if e.discarded {
		return "streaming_fallback"
	}
	if e.hasErrored {
		return "sibling_error"
	}
	select {
	case <-e.rootCtx.Done():
		if t.InterruptBehavior() == tool.InterruptBlock {
			return ""
		}
		return "user_interrupted"
	default:
		return ""
	}
}

// getToolDescription returns a short description of the tool call for error messages.
// Source: StreamingToolExecutor.ts:243-252 — getToolDescription().
func getToolDescription(tt *TrackedTool) string {
	var input struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(tt.Input, &input); err != nil {
		return tt.Name
	}
	summary := input.Command
	if summary == "" {
		summary = input.FilePath
	}
	if summary == "" {
		summary = input.Pattern
	}
	if len(summary) > 40 {
		summary = summary[:40] + "…"
	}
	if summary != "" {
		return fmt.Sprintf("%s(%s)", tt.Name, summary)
	}
	return tt.Name
}

// executeTool runs a single tool to completion. Called as a goroutine.
// Source: StreamingToolExecutor.ts:265-405 — executeTool().
func (e *StreamingToolExecutor) executeTool(tt *TrackedTool) {
	defer func() {
		e.mu.Lock()
		if tt.Status != StatusCompleted {
			tt.Status = StatusCompleted
		}
		e.mu.Unlock()
		close(tt.done)
		// Source: StreamingToolExecutor.ts:402-404 — processQueue after completion.
		e.processQueue()
	}()

	// Look up tool definition first (needed for interrupt behavior check).
	t, ok := e.toolMap[tt.Name]

	// Check if already aborted before running.
	// Source: StreamingToolExecutor.ts:276-292 — check abort before execution.
	e.mu.Lock()
	reason := e.getAbortReason(t)
	e.mu.Unlock()
	if reason != "" {
		errBlock := CreateSyntheticErrorBlock(tt.ID, reason)
		errMsg := extractErrMsg(errBlock.Content)
		e.doEmit(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     tt.ID,
				Output:        errBlock.Content,
				DisplayOutput: errMsg,
				IsError:       true,
			},
		})
		tt.resultBlocks = []types.ContentBlock{errBlock}
		return
	}
	if !ok {
		// Should not happen (checked in AddTool), but handle defensively.
		errMsg := fmt.Sprintf("No such tool available: %s", tt.Name)
		errBlock := CreateToolErrorBlock(tt.ID, errMsg)
		e.doEmit(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     tt.ID,
				Output:        errBlock.Content,
				DisplayOutput: errMsg,
				IsError:       true,
			},
		})
		tt.resultBlocks = []types.ContentBlock{errBlock}
		return
	}

	// Use siblingCtx so Bash errors can cancel siblings.
	start := time.Now()

	// Build per-tool ToolUseContext with the correct ToolUseID.
	// The executor-level tctx may be nil (created inline during callLLM),
	// so we always create a fresh copy with tt.ID set.
	toolCtx := e.buildToolCtx(tt.ID)

		// ── Permission check (before hooks) ──
		// Source: toolExecution.ts — hasPermissionsToUseTool runs before hooks.
		// Three-phase: bare-tool deny → bare-tool ask → content-level matching → allow.
		if e.permChecker != nil {
			decision := e.permChecker.Check(tt.Name, tt.Input)

			// Phase 1: bare-tool deny
			if decision.Action == permission.ActionDeny {
				errMsg := fmt.Sprintf("permission denied: %s", decision.Message)
				e.doEmit(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     tt.ID,
						Output:        []byte(errMsg),
						DisplayOutput: errMsg,
						IsError:       true,
					},
				})
				tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte(errMsg), true)}
				return
			}

				// Phase 2: bare-tool ask
				if decision.Action == permission.ActionAsk {
					userDecision := e.askUser(tt, decision, "")
					if userDecision != types.UserDecisionAllow && userDecision != types.UserDecisionAllowAlways {
						errMsg := fmt.Sprintf("permission denied: %s", decision.Message)
						e.doEmit(types.QueryEvent{
							Type: types.EventToolEnd,
							ToolResult: &types.ToolResultEvent{
								ToolUseID:     tt.ID,
								Output:        []byte(errMsg),
								DisplayOutput: errMsg,
								IsError:       true,
							},
						})
						tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte(errMsg), true)}
						return
					}
				}

			// Phase 3: content-level matching
			if len(decision.ContentRules) > 0 {
				action, matchedPattern := e.checkContentPermissions(tt.Name, tt.Input, decision.ContentRules)
				if action == permission.ActionDeny {
					errMsg := fmt.Sprintf("permission denied: %s content rule matched", tt.Name)
					e.doEmit(types.QueryEvent{
						Type: types.EventToolEnd,
						ToolResult: &types.ToolResultEvent{
							ToolUseID:     tt.ID,
							Output:        []byte(errMsg),
							DisplayOutput: errMsg,
							IsError:       true,
						},
					})
					tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte(errMsg), true)}
					return
				}
					if action == permission.ActionAsk {
						// 修正 9: use pattern from checkContentPermissions (avoids double check)
						matchedContent := matchedPattern

						userDecision := e.askUser(tt, permission.Decision{
							Action:  permission.ActionAsk,
							Message: fmt.Sprintf("tool %s requires permission by content rule", tt.Name),
						}, matchedContent)
						if userDecision != types.UserDecisionAllow && userDecision != types.UserDecisionAllowAlways {
							errMsg := fmt.Sprintf("permission denied: %s content rule matched", tt.Name)
							e.doEmit(types.QueryEvent{
								Type: types.EventToolEnd,
								ToolResult: &types.ToolResultEvent{
									ToolUseID:     tt.ID,
									Output:        []byte(errMsg),
									DisplayOutput: errMsg,
									IsError:       true,
								},
							})
							tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte(errMsg), true)}
							return
						}
					}
			}
		}

		// PreToolUse hook — blocking result prevents tool execution.
		// Source: toolHooks.ts:435 — runPreToolUseHooks.
		if e.hooks != nil {
			hookInput := &hooks.HookInput{
				HookEventName: string(hooks.HookPreToolUse),
				SessionID:     e.sessionID,
				ToolName:      tt.Name,
				ToolInput:     tt.Input,
				ToolUseID:     tt.ID,
			}
			decision, _ := e.hooks.PreToolUse(e.siblingCtx, hookInput)
			if decision == hooks.HookDecisionBlock {
				errMsg := "blocked by hook"
				e.doEmit(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     tt.ID,
						Output:        []byte(errMsg),
						DisplayOutput: errMsg,
						IsError:       true,
					},
				})
				tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte(errMsg), true)}
				return
			}
		}

	// Try streaming execution first (ToolWithStreaming interface).
	// Source: StreamingToolExecutor.ts:320-382 — runToolUse generator.
	if streamer, ok := t.(tool.ToolWithStreaming); ok {
		var lastDisplayOutput string
		result, err := streamer.ExecuteStream(e.siblingCtx, tt.Input, toolCtx, func(u tool.ProgressUpdate) {
			if len(u.Lines) > 0 {
				display := strings.Join(u.Lines, "\n")
				lastDisplayOutput = display
				e.doEmit(types.QueryEvent{
					Type: types.EventToolOutputDelta,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     tt.ID,
						DisplayOutput: display,
						Timing:        time.Since(start),
					},
				})
			}
		})
		elapsed := time.Since(start)
		tt.Duration = elapsed

		if err != nil {
			e.emitToolError(tt, err, elapsed)
			return
		}

		outputJSON := marshalToolOutput(t, result.Data, true)
		outputJSON = truncateToolOutput(outputJSON, t.MaxResultSize())
		displayOutput := t.RenderResult(result.Data)
		if displayOutput == "" && lastDisplayOutput != "" {
			displayOutput = lastDisplayOutput
		}
		e.doEmit(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     tt.ID,
				Output:        outputJSON,
				DisplayOutput: displayOutput,
				Timing:        elapsed,
			},
		})
		tt.Result = result
		tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, outputJSON, false)}
		if len(result.NewMessages) > 0 {
			tt.newMessages = result.NewMessages
		}
		e.applyContextModifier(tt, result)
		e.firePostToolUseHook(tt, false)
		return
	}

	// Fallback: non-streaming Call().
	result, err := t.Call(e.siblingCtx, tt.Input, toolCtx)
	elapsed := time.Since(start)
	tt.Duration = elapsed

	if err != nil {
		e.emitToolError(tt, err, elapsed)
		return
	}

	if result == nil {
		// Tool returned nil result without error — treat as empty.
		tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte("null"), false)}
		return
	}

	outputJSON := marshalToolOutput(t, result.Data, true)
	outputJSON = truncateToolOutput(outputJSON, t.MaxResultSize())
	displayOutput := t.RenderResult(result.Data)
	e.doEmit(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     tt.ID,
			Output:        outputJSON,
			DisplayOutput: displayOutput,
			Timing:        elapsed,
		},
	})
	tt.Result = result
	tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, outputJSON, false)}
	if len(result.NewMessages) > 0 {
		tt.newMessages = result.NewMessages
	}
	e.applyContextModifier(tt, result)
	e.firePostToolUseHook(tt, false)
}

// truncateToolOutput truncates tool output if it exceeds maxChars.
// Source: TS applyToolResultBudget.
func truncateToolOutput(output []byte, maxChars int) []byte {
	if maxChars <= 0 || len(output) <= maxChars {
		return output
	}
	truncated := output[:maxChars]
	return append(truncated, []byte("\n\n[Output truncated]")...)
}


// marshalToolOutput serializes a tool result for sending to the LLM.
// If the tool implements ToolWithWireFormat, its custom format is used.
// Otherwise, doubleWrap=true wraps the result as a JSON string (streaming/concurrent),
// and doubleWrap=false passes raw JSON (sequential).
func marshalToolOutput(t tool.Tool, data any, doubleWrap bool) []byte {
	if wf, ok := t.(tool.ToolWithWireFormat); ok {
		b, _ := json.Marshal(wf.FormatWireResult(data))
		return b
	}
	if doubleWrap {
		raw, _ := json.Marshal(data)
		wrapped, _ := json.Marshal(string(raw))
		return wrapped
	}
	b, _ := json.Marshal(data)
	return b
}

// emitToolError emits error events and result blocks for a failed tool.
// Source: StreamingToolExecutor.ts:354-364 — Bash errors cancel siblings.
func (e *StreamingToolExecutor) emitToolError(tt *TrackedTool, err error, elapsed time.Duration) {
	e.firePostToolUseHook(tt, true)
	errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
	e.doEmit(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     tt.ID,
			Output:        errJSON,
			DisplayOutput: err.Error(),
			IsError:       true,
			Timing:        elapsed,
		},
	})
	tt.Err = err
	tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errJSON, true)}

	// Source: StreamingToolExecutor.ts:359 — BASH_TOOL_NAME check.
	// Only Bash errors cancel siblings. Other tool failures are independent.
	if tt.Name == "Bash" {
		e.mu.Lock()
		e.hasErrored = true
		e.errToolDesc = getToolDescription(tt)
		e.mu.Unlock()
		e.siblingCancel(fmt.Errorf("sibling_error"))
	}
}

// buildToolCtx creates a per-tool ToolUseContext with the correct ToolUseID.
// The executor-level tctx is shared and may be nil (created inline during callLLM).
// Each tool needs its own context with its specific ID for identity-aware operations
// (e.g., Agent tool needs ToolUseID to tag sub-agent events via ParentToolUseID).

// firePostToolUseHook fires PostToolUse or PostToolUseFailure hook.
func (e *StreamingToolExecutor) firePostToolUseHook(tt *TrackedTool, isError bool) {
	if e.hooks == nil {
		return
	}
	hookInput := &hooks.HookInput{
		SessionID: e.sessionID,
		ToolName:  tt.Name,
		ToolInput: tt.Input,
		ToolUseID: tt.ID,
	}
	if isError {
		hookInput.HookEventName = string(hooks.HookPostToolUseFailure)
		e.hooks.PostToolUseFailure(e.siblingCtx, hookInput)
	} else {
		hookInput.HookEventName = string(hooks.HookPostToolUse)
		e.hooks.PostToolUse(e.siblingCtx, hookInput)
	}
}
func (e *StreamingToolExecutor) buildToolCtx(toolUseID string) *types.ToolUseContext {
	e.mu.Lock()
	msgs := e.messages
	e.mu.Unlock()

	if e.tctx == nil {
		return &types.ToolUseContext{ToolUseID: toolUseID, Messages: msgs}
	}
	cp := *e.tctx
	cp.ToolUseID = toolUseID
	if len(msgs) > 0 {
		cp.Messages = msgs
	}
	return &cp
}

// applyContextModifier applies the tool's context modifier if it's not concurrency-safe.
// Source: StreamingToolExecutor.ts:388-395 — context modifier only for non-concurrent tools.
func (e *StreamingToolExecutor) applyContextModifier(tt *TrackedTool, result *tool.ToolResult) {
	if result == nil || result.ContextModifier == nil || tt.IsConcurrencySafe {
		return
	}
	if e.tctx != nil {
		e.mu.Lock()
		e.tctx = result.ContextModifier(e.tctx)
		e.mu.Unlock()
	}
}

// checkContentPermissions performs content-level permission matching for a tool.
// Source: 修正 15 — Checker does bare-tool matching; content matching is tool-specific.
// P2-5: dispatches to registered content checkers instead of hardcoded switch.
func (e *StreamingToolExecutor) checkContentPermissions(toolName string, input json.RawMessage, contentRules []permission.Rule) (permission.RuleAction, string) {
	action := permission.CheckContent(toolName, input, contentRules)
	if action == permission.ActionAsk {
		return action, permission.ExtractContentPattern(toolName, input, contentRules)
	}
	return action, ""
}

// extractErrMsg extracts the human-readable error message from a tool result
// block's JSON content (format: {"error":"message"}).
func extractErrMsg(content json.RawMessage) string {
	var m map[string]string
	if json.Unmarshal(content, &m) == nil {
		if msg, ok := m["error"]; ok {
			return msg
		}
	}
	return string(content)
}

// ---------------------------------------------------------------------------
// ConcurrentToolLoop — public API
// ---------------------------------------------------------------------------

// ConcurrentToolLoop creates a StreamingToolExecutor, adds all blocks, runs
// them with concurrency, and collects results.
func ConcurrentToolLoop(
	ctx context.Context,
	tools map[string]tool.Tool,
	blocks []types.ContentBlock,
	tctx *types.ToolUseContext,
	emitEvent func(types.QueryEvent),
) *ExecuteAllResult {
	executor := NewStreamingToolExecutor(tools, tctx, emitEvent, ctx)
	return executor.ExecuteAll(blocks)
}

// ---------------------------------------------------------------------------
// SequentialToolLoop is a deprecated stub.
// DEPRECATED: use ConcurrentToolLoop instead. Kept as stub for test compilation.
// ---------------------------------------------------------------------------

// SequentialToolLoop is a deprecated stub. Tests that reference it should be
// rewritten for ConcurrentToolLoop.
func SequentialToolLoop(
	ctx context.Context,
	tools map[string]tool.Tool,
	blocks []types.ContentBlock,
	tctx *types.ToolUseContext,
	onEvent func(types.QueryEvent),
) []tool.ToolResult {
	panic("SequentialToolLoop is deprecated, use ConcurrentToolLoop")
}

