package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liuy/gbot/pkg/filehistory"
	"github.com/liuy/gbot/pkg/hooks"
	"github.com/liuy/gbot/pkg/memory/long"
	"github.com/liuy/gbot/pkg/permission"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/toolresult"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Permission deny messages — source: src/utils/messages.ts:210-232
// ---------------------------------------------------------------------------

// userRejectMessage is returned when the user actively rejects a tool via the
// permission dialog. Instructs the LLM to STOP and wait.
// Source: messages.ts:212 — REJECT_MESSAGE
const userRejectMessage = "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed."

// subAgentRejectMessage is returned when a sub-engine's tool is denied.
// Source: messages.ts:216 — SUBAGENT_REJECT_MESSAGE
const subAgentRejectMessage = "Permission for this tool use was denied. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). Try a different approach or report the limitation to complete your task."

// denialWorkaroundGuidance is appended to rule-based deny messages.
// Source: messages.ts:226 — DENIAL_WORKAROUND_GUIDANCE
const denialWorkaroundGuidance = "IMPORTANT: You *may* attempt to accomplish this action using other tools that might naturally be used to accomplish this goal, e.g. using head instead of cat. But you *should not* attempt to work around this denial in malicious ways, e.g. do not use your ability to run tests to execute non-test actions. You should only try to work around this restriction in reasonable ways that do not attempt to bypass the intent behind this denial. If you believe this capability is essential to complete the user's request, STOP and explain to the user what you were trying to do and why you need this permission. Let the user decide how to proceed."

// ruleDenyMessage returns the error message for a rule-based tool deny.
// Source: messages.ts:234 — AUTO_REJECT_MESSAGE
func ruleDenyMessage(toolName string) string {
	return fmt.Sprintf("Permission to use %s has been denied. %s", toolName, denialWorkaroundGuidance)
}

// userOrSubRejectMessage returns the appropriate rejection message based on
// whether the executor is a sub-engine.
func (e *StreamingToolExecutor) userOrSubRejectMessage() string {
	if e.isSubEngine {
		return subAgentRejectMessage
	}
	return userRejectMessage
}

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
	tctx      *tool.ToolUseContext
	messages  []types.Message // conversation history for tool context (set after assistant msg append)
	memoryDir string          // per-engine memory override; empty = use workingDir-derived path

	// Three-tier abort (TS: abortController.ts, spec:3750-3810)
	// rootCtx → siblingCtx via context.WithCancelCause.
	// siblingCancel kills all sibling tools but does NOT end the query.
	rootCtx       context.Context
	siblingCtx    context.Context
	siblingCancel context.CancelCauseFunc

	hasErrored       bool
	errToolDesc      string
	discarded        bool
	assistantContent []types.ContentBlock // current assistant message's content blocks (mid-stream)

	// hooks is the lifecycle hooks system for PreToolUse/PostToolUse.
	hooks     *hooks.Hooks
	sessionID string // session ID for hook input construction

	// permChecker is the permission rules checker. Nil = default allow.
	// Set by engine before tool execution. Inherited by sub-engines.
	permChecker permission.PermissionChecker

	// fileHistory tracks file backups for rewind/restore.
	// Set by engine via SetFileHistory. Shared across sub-engines.
	fileHistory *filehistory.Tracker

	// currentTurnMsgID is the UUID of the user message that started the current query.
	// Copied from Engine.currentTurnMsgID so TrackEdit uses the correct messageID.
	currentTurnMsgID string

	// sessionAllowed caches "Allow always" decisions for the current session.
	sessionAllowed map[string]bool

	// askMu serializes concurrent ask dialogs. Only one ask at a time.
	askMu sync.Mutex

	// isSubEngine is true for sub-engine executors. Sub-engines auto-deny asks
	// since they run in the background and can't show interactive dialogs.
	isSubEngine bool
}

// NewStreamingToolExecutor creates a new concurrent tool executor.
// Source: StreamingToolExecutor.ts:53-62 — constructor.
func NewStreamingToolExecutor(
	toolMap map[string]tool.Tool,
	tctx *tool.ToolUseContext,
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
func (e *StreamingToolExecutor) SetMessages(messages []types.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = messages
}

func (e *StreamingToolExecutor) SetMemoryDir(dir string) { e.memoryDir = dir }

// SetAssistantContent sets the current assistant message's content blocks.
func (e *StreamingToolExecutor) SetAssistantContent(blocks []types.ContentBlock) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.assistantContent = blocks
}

// StartedToolIDs returns tool IDs that have been started (executing or completed).
// Used to identify orphaned tool_uses during mid-stream abort.
func (e *StreamingToolExecutor) StartedToolIDs() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make(map[string]bool)
	for _, tt := range e.tools {
		if tt.Status == StatusExecuting || tt.Status == StatusCompleted {
			ids[tt.ID] = true
		}
	}
	return ids
}

// SetHooks injects the hooks system into the executor.
func (e *StreamingToolExecutor) SetHooks(h *hooks.Hooks, sessionID string) {
	e.hooks = h
	e.sessionID = sessionID
}

// SetPermissionChecker injects the permission checker into the executor.
func (e *StreamingToolExecutor) SetPermissionChecker(pc permission.PermissionChecker) {
	e.permChecker = pc
}

// SetFileHistory injects the file history tracker into the executor.
func (e *StreamingToolExecutor) SetFileHistory(fh *filehistory.Tracker) {
	e.fileHistory = fh
}

// SetSubEngine marks this executor as running inside a sub-engine.
func (e *StreamingToolExecutor) SetSubEngine(v bool) {
	e.isSubEngine = v
}

// askUser asks the user for permission via TUI dialog.
// Blocks until the user responds, a context is cancelled, or the executor is discarded.
func (e *StreamingToolExecutor) askUser(tt *TrackedTool, decision permission.Decision, matchedContent string) types.UserDecision {
	cacheKey := tt.Name
	if matchedContent != "" {
		cacheKey = tt.Name + ":" + matchedContent
	}
	if e.sessionAllowed != nil && e.sessionAllowed[cacheKey] {
		return types.DecisionAllow
	}

	e.askMu.Lock()
	defer e.askMu.Unlock()

	// Double-check: cache may have been updated while waiting for lock
	if e.sessionAllowed != nil && e.sessionAllowed[cacheKey] {
		return types.DecisionAllow
	}

	decisionCh := make(chan types.AskResponse, 1)

	ruleDetail := ""
	if decision.Rule != nil {
		ruleDetail = decision.Rule.Value.ToolName
		if decision.Rule.Value.RuleContent != nil {
			ruleDetail += "(" + *decision.Rule.Value.RuleContent + ")"
		}
		ruleDetail += " from " + decision.Rule.Source + " settings"
	}

	e.doEmit(types.QueryEvent{
		Type: types.EventAsk,
		Ask: &types.AskEvent{
			Kind:       types.AskPermission,
			ToolName:   tt.Name,
			Input:      tt.Input,
			Message:    decision.Message,
			RuleDetail: ruleDetail,
			ResponseCh: decisionCh,
		},
	})

	select {
	case resp, ok := <-decisionCh:
		if !ok {
			return types.DecisionDeny
		}
		if resp.Decision == types.DecisionAllowAlways {
			if e.sessionAllowed == nil {
				e.sessionAllowed = make(map[string]bool)
			}
			e.sessionAllowed[cacheKey] = true
		}
		return resp.Decision
	case <-e.rootCtx.Done():
		return types.DecisionDeny
	case <-e.siblingCtx.Done():
		return types.DecisionDeny
	}
}

// toolNotFoundHints maps tool names that LLM training data references but
// don't exist in gbot (or were merged) to actionable Grep/Bash equivalents.
// Surfaces in AddTool's "No such tool" error so the model can self-correct
// on the next turn instead of looping the same broken call.
var toolNotFoundHints = map[string]string{
	"Glob": "Tool 'Glob' does not exist — it was merged into Grep. " +
		"Pass the glob via the `glob` JSON field: Grep {\"glob\": \"*.go\"} " +
		"(use \"**/*.go\" for recursive).",
}

func toolNotFoundHint(name string) string {
	return toolNotFoundHints[name]
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------
// Source: StreamingToolExecutor.ts:76-124 — addTool().
func (e *StreamingToolExecutor) AddTool(block types.ContentBlock) {
	t, ok := e.toolMap[block.Name]
	if !ok {
		errMsg := fmt.Sprintf("No such tool available: %s", block.Name)
		// Tool name maps to a known merged/renamed tool (e.g. Glob → Grep).
		// Hint takes priority over the generic message so the model can
		// self-correct on the next turn.
		if hint := toolNotFoundHint(block.Name); hint != "" {
			errMsg = hint
		} else if e.tctx != nil && e.tctx.Options.Tools != nil {
			// When ToolSearch is active and the tool exists in the full map but
			// is deferred, hint the model to use ToolSearch to discover its schema.
			// Source: toolExecution.ts — buildSchemaNotSentHint
			if fullTool, exists := e.tctx.Options.Tools[block.Name]; exists && tool.IsDeferred(fullTool) {
				if _, hasTS := e.toolMap[ToolSearchToolName]; hasTS {
					errMsg = fmt.Sprintf(
						"Tool %s is deferred and its schema has not been loaded. "+
							"Call ToolSearch first with query \"select:%s\" to discover its parameters, then retry.",
						block.Name, block.Name,
					)
				}
			}
		}
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
		case <-e.rootCtx.Done():
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
		return AbortReasonStreamingFallback
	}
	if e.hasErrored {
		return AbortReasonSiblingError
	}
	select {
	case <-e.rootCtx.Done():
		if t.InterruptBehavior() == tool.InterruptBlock {
			return ""
		}
		return AbortReasonUserInterrupted
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
	// Cleanup defer registered FIRST so it runs LAST (LIFO order).
	// This ensures recovery (second defer) runs first and sets resultBlocks
	// BEFORE this defer closes tt.done.
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
	// Panic recovery: registered SECOND so it runs FIRST (LIFO order).
	// Sets resultBlocks before the cleanup defer closes tt.done.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("engine: panic in executeTool", "tool", tt.Name, "error", r, "stack", string(debug.Stack()))
			errMsg := fmt.Sprintf("internal error in tool %s: %v", tt.Name, r)
			errBytes, _ := json.Marshal(errMsg)
			e.doEmit(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     tt.ID,
					Output:        errBytes,
					DisplayOutput: errMsg,
					IsError:       true,
				},
			})
			tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
		}
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
		if hint := toolNotFoundHint(tt.Name); hint != "" {
			errMsg = hint
		}
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

	toolCtx := e.buildToolCtx(tt.ID)

	// ── Permission check (before hooks) ──
	// Source: toolExecution.ts — hasPermissionsToUseTool runs before hooks.
	// Three-phase: bare-tool deny → bare-tool ask → content-level matching → allow.
	// The checker is a system component — if it panics (e.g. nil interface trap),
	// silently disable it and let the tool execute, rather than failing the tool.

	// Memory path bypass: auto-allow writes to the memory directory.
	// TS: isAutoMemPath (filesystem.ts:1572-1581) — write carve-out for auto-memory.
	if isMemoryPathWrite(tt.Name, tt.Input, e.memoryDir) {
		// Skip permission check — memory dir writes are always allowed.
	} else if e.permChecker != nil {
		var decision permission.Decision
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("engine: permChecker.Check panicked, disabling", "tool", tt.Name, "error", r)
					e.permChecker = nil
					decision = permission.Decision{Action: permission.ActionAllow}
				}
			}()
			decision = e.permChecker.Check(tt.Name, tt.Input)
		}()

		// Phase 1: bare-tool deny (rule-based, no user interaction)
		if decision.Action == permission.ActionDeny {
			errMsg := ruleDenyMessage(tt.Name)
			errBytes, _ := json.Marshal(errMsg)
			e.doEmit(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     tt.ID,
					Output:        errBytes,
					DisplayOutput: errMsg,
					IsError:       true,
				},
			})
			tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
			return
		}

		// Phase 2: bare-tool ask
		if decision.Action == permission.ActionAsk {
			// Fire PermissionRequest hook — plugins can auto-allow/deny.
			// Source: interactiveHandler.ts:412 — executePermissionRequestHooks
			if e.hooks != nil {
				hookInput := &hooks.HookInput{
					HookEventName: "PermissionRequest",
					ToolName:      tt.Name,
				}
				for _, r := range e.hooks.PermissionRequest(context.Background(), hookInput) {
					if r.Output != nil && r.Output.Decision == "allow" {
						continue // hook approved, skip askUser — fall through to Phase 3
					}
					if r.Output != nil && r.Output.Decision == "block" {
						errMsg := ruleDenyMessage(tt.Name)
						errBytes, _ := json.Marshal(errMsg)
						e.doEmit(types.QueryEvent{
							Type: types.EventToolEnd,
							ToolResult: &types.ToolResultEvent{
								ToolUseID:     tt.ID,
								Output:        errBytes,
								DisplayOutput: errMsg,
								IsError:       true,
							},
						})
						tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
						return
					}
				}
			}
			userDecision := e.askUser(tt, decision, "")
			if userDecision != types.DecisionAllow && userDecision != types.DecisionAllowAlways {
				errMsg := e.userOrSubRejectMessage()
				errBytes, _ := json.Marshal(errMsg)
				e.doEmit(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     tt.ID,
						Output:        errBytes,
						DisplayOutput: errMsg,
						IsError:       true,
					},
				})
				tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
				return
			}
		}

		// Phase 3: content-level matching
		if len(decision.ContentRules) > 0 {
			action, matchedPattern := e.checkContentPermissions(tt.Name, tt.Input, decision.ContentRules)
			if action == permission.ActionDeny {
				errMsg := ruleDenyMessage(tt.Name)
				errBytes, _ := json.Marshal(errMsg)
				e.doEmit(types.QueryEvent{
					Type: types.EventToolEnd,
					ToolResult: &types.ToolResultEvent{
						ToolUseID:     tt.ID,
						Output:        errBytes,
						DisplayOutput: errMsg,
						IsError:       true,
					},
				})
				tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
				return
			}
			if action == permission.ActionAsk {
				matchedContent := matchedPattern

				userDecision := e.askUser(tt, permission.Decision{
					Action:  permission.ActionAsk,
					Message: fmt.Sprintf("tool %s requires permission by content rule", tt.Name),
				}, matchedContent)
				if userDecision != types.DecisionAllow && userDecision != types.DecisionAllowAlways {
					errMsg := e.userOrSubRejectMessage()
					errBytes, _ := json.Marshal(errMsg)
					e.doEmit(types.QueryEvent{
						Type: types.EventToolEnd,
						ToolResult: &types.ToolResultEvent{
							ToolUseID:     tt.ID,
							Output:        errBytes,
							DisplayOutput: errMsg,
							IsError:       true,
						},
					})
					tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
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
			errMsg := fmt.Sprintf("Execution stopped by PreToolUse hook for tool %s", tt.Name)
			errBytes, _ := json.Marshal(errMsg)
			e.doEmit(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     tt.ID,
					Output:        errBytes,
					DisplayOutput: errMsg,
					IsError:       true,
				},
			})
			tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, errBytes, true)}
			return
		}
	}

	// Re-check: Discard may have been called between getAbortReason and here.
	// Without this, a goroutine launched by processQueue can pass the abort check
	// before Discard() sets the flag, then race to call t.Call().
	// Only check discarded — other cancellation reasons are properly handled
	// by getAbortReason (e.g., InterruptBlock tools must not be cancelled here).
	e.mu.Lock()
	if e.discarded {
		e.mu.Unlock()
		errBlock := CreateSyntheticErrorBlock(tt.ID, AbortReasonStreamingFallback)
		e.doEmit(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID:     tt.ID,
				Output:        errBlock.Content,
				DisplayOutput: extractErrMsg(errBlock.Content),
				IsError:       true,
			},
		})
		tt.resultBlocks = []types.ContentBlock{errBlock}
		return
	}
	e.mu.Unlock()

	// Unified execution: always call t.Call() with OnProgress set in ToolUseContext.
	// Tools that support streaming (e.g., Bash) use tctx.OnProgress to emit
	// EventToolOutputDelta events during execution. Non-streaming tools ignore it.
	// Source: StreamingToolExecutor.ts:320-382 — runToolUse generator.
	var lastDisplayOutput atomic.Pointer[string]
	toolCtx.OnProgress = func(u tool.ProgressUpdate) {
		if len(u.Lines) > 0 {
			display := strings.Join(u.Lines, "\n")
			lastDisplayOutput.Store(&display)
			e.doEmit(types.QueryEvent{
				Type: types.EventToolOutputDelta,
				ToolResult: &types.ToolResultEvent{
					ToolUseID:     tt.ID,
					DisplayOutput: display,
					Timing:        time.Since(start),
				},
			})
		}
	}

	// File history tracking: TrackEdit BEFORE edit, Bash snapshot BEFORE execution.
	var bashSnap map[string]*filehistory.FileSnapshot

	if e.fileHistory != nil && e.currentTurnMsgID != "" {
		switch tt.Name {
		case "Edit", "Write":
			filePath := extractFilePathFromInput(tt.Input)
			if filePath != "" {
				if err := e.fileHistory.TrackEdit(filePath); err != nil {
					slog.Warn("filehistory:track_edit_failed", "file", filePath, "err", err)
				}
			}
		case "Bash":
			if toolCtx.WorkingDir != "" {
				if snap, snapErr := filehistory.TakeSnapshot(toolCtx.WorkingDir); snapErr == nil {
					bashSnap = snap
				}
			}
		}
	} else if tt.Name == "Edit" || tt.Name == "Write" || tt.Name == "Bash" {
		slog.Warn("engine:file_history_skip", "tool", tt.Name, "hasFileHistory", e.fileHistory != nil, "currentTurnMsgID", e.currentTurnMsgID)
	}

	result, err := t.Call(e.siblingCtx, tt.Input, toolCtx)
	elapsed := time.Since(start)
	tt.Duration = elapsed

	if err != nil {
		e.emitToolError(t, tt, err, elapsed)
		return
	}

	if result == nil {
		// Tool returned nil result without error — treat as empty.
		tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, []byte("null"), false)}
		return
	}

	outputJSON := marshalToolOutput(t, result.Data, true)
	pr := toolresult.MaybePersistLargeToolResult(outputJSON, t.Name(), t.MaxResultSize(), tt.ID, e.sessionID)
	outputJSON = pr.Output
	outputJSON = prependDuration(outputJSON, elapsed)
	displayOutput := t.RenderResult(result.Data)
	if displayOutput == "" {
		if p := lastDisplayOutput.Load(); p != nil && *p != "" {
			displayOutput = *p
		}
	}
	srk := tool.SearchReadKind{}
	if ts, ok := t.(tool.ToolWithSearchOrRead); ok {
		srk = ts.IsSearchOrRead(tt.Input)
	}
	e.doEmit(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     tt.ID,
			Output:        outputJSON,
			DisplayOutput: displayOutput,
			Timing:        elapsed,
			IsBackground:  isBackgroundResult(result.Data),
			IsSearch:      srk.IsSearch,
			IsRead:        srk.IsRead,
			IsList:        srk.IsList,
			IsLsp:         srk.IsLsp,
		},
	})
	tt.Result = result
	// Record file backup for rewind/restore (Edit/Write tools only)
	// File history: track Bash file changes AFTER execution.
	// Edit/Write tools already called TrackEdit BEFORE execution above.
	if e.fileHistory != nil && bashSnap != nil {
		changes, err := filehistory.DetectChanges(toolCtx.WorkingDir, bashSnap)
		if err != nil {
			slog.Warn("filehistory:bash:detect_changes_failed", "err", err)
		} else {
			for _, ch := range changes {
				if err := e.fileHistory.TrackEditFromContent(ch.Path, ch.BeforeContent); err != nil {
					slog.Warn("filehistory:bash:track_edit_failed", "file", ch.Path, "err", err)
				}
			}
		}
	}
	tt.resultBlocks = []types.ContentBlock{types.NewToolResultBlock(tt.ID, outputJSON, false)}
	if len(result.NewMessages) > 0 {
		tt.newMessages = result.NewMessages
	}
	e.applyContextModifier(tt, result)
	e.firePostToolUseHook(tt, false)
}

// isBackgroundResult returns true if the tool result data is a SubQueryResult
// from a fork agent that was launched asynchronously.
func isBackgroundResult(data any) bool {
	sqr, ok := data.(*types.SubQueryResult)
	return ok && sqr.AsyncLaunched
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
func (e *StreamingToolExecutor) emitToolError(t tool.Tool, tt *TrackedTool, err error, elapsed time.Duration) {
	e.firePostToolUseHook(tt, true)
	// Error content must be a JSON string (not object) for Anthropic API compatibility.
	// MiniMax/Anthropic API ignores objects in tool_result.content → LLM sees "null".
	fullErr := err.Error()
	errJSON, _ := json.Marshal(fullErr)
	errJSON = prependDuration(errJSON, elapsed)
	// Let the tool decide how to display its own errors via RenderResult.
	// Tools like Edit override this to show short summaries instead of
	// dumping the full search string. The full error is still sent to
	// the LLM via Output (errJSON).
	displayErr := t.RenderResult(fullErr)
	e.doEmit(types.QueryEvent{
		Type: types.EventToolEnd,
		ToolResult: &types.ToolResultEvent{
			ToolUseID:     tt.ID,
			Output:        errJSON,
			DisplayOutput: displayErr,
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
func (e *StreamingToolExecutor) buildToolCtx(toolUseID string) *tool.ToolUseContext {
	e.mu.Lock()
	msgs := e.messages
	ac := e.assistantContent
	e.mu.Unlock()

	if e.tctx == nil {
		return &tool.ToolUseContext{ToolUseID: toolUseID, Messages: msgs, AssistantContent: ac}
	}
	cp := *e.tctx
	cp.ToolUseID = toolUseID
	if len(msgs) > 0 {
		cp.Messages = msgs
	}
	if len(ac) > 0 {
		cp.AssistantContent = ac
	}
	// Wire OnAskInput: creates channel, emits AskEvent{Kind: AskInput}, returns channel.
	if cp.OnAskInput == nil {
		emitFn := e.emitEvent
		cp.OnAskInput = func(prompt string, masked bool, deadline time.Time) chan types.AskResponse {
			ch := make(chan types.AskResponse, 1)
			emitFn(types.QueryEvent{
				Type: types.EventAsk,
				Ask: &types.AskEvent{
					Kind:       types.AskInput,
					Prompt:     prompt,
					Masked:     masked,
					Deadline:   deadline,
					ResponseCh: ch,
				},
			})
			return ch
		}
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
// Checker does bare-tool matching; content matching is tool-specific.
// Dispatches to registered content checkers instead of hardcoded switch.
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
	tctx *tool.ToolUseContext,
	emitEvent func(types.QueryEvent),
) *ExecuteAllResult {
	executor := NewStreamingToolExecutor(tools, tctx, emitEvent, ctx)
	return executor.ExecuteAll(blocks)
}

// isMemoryPathWrite checks if a tool call is a Write to the auto-memory directory.
// When true, the permission check is bypassed — memory writes are always allowed.
// TS: isAutoMemPath bypass (filesystem.ts:1572-1581)
func isMemoryPathWrite(toolName string, input json.RawMessage, memoryDirOverride string) bool {
	if toolName != "Write" || !long.IsAutoMemoryEnabled() {
		return false
	}

	var writeInput struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &writeInput); err != nil || writeInput.FilePath == "" {
		return false
	}

	absPath, err := filepath.Abs(writeInput.FilePath)
	if err != nil {
		return false
	}

	if memoryDirOverride != "" {
		normalized := filepath.Clean(absPath)
		overrideDir := strings.TrimSuffix(memoryDirOverride, string(filepath.Separator)) + string(filepath.Separator)
		if strings.HasPrefix(normalized+string(filepath.Separator), overrideDir) {
			return true
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return false
	}

	return long.IsMemoryPath(cwd, absPath)
}

// extractFilePathFromInput parses the file_path field from a tool's JSON input.
// Used by TrackEdit to get the file path BEFORE tool execution.
func extractFilePathFromInput(input json.RawMessage) string {
	var parsed struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &parsed); err != nil {
		return ""
	}
	return parsed.FilePath
}

// prependDuration adds a [spent Xs] prefix to tool result content for LLM context.
// outputJSON is a JSON-encoded string like "some output" — we decode, prepend, re-encode.
func prependDuration(outputJSON json.RawMessage, d time.Duration) json.RawMessage {
	var s string
	if json.Unmarshal(outputJSON, &s) != nil {
		return outputJSON
	}
	prefix := fmt.Sprintf("[Tool spent %.1fs]", d.Seconds())
	prefixed, err := json.Marshal(prefix + s)
	if err != nil {
		return outputJSON
	}
	return prefixed
}
