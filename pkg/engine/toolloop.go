package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/liuy/gbot/pkg/tool"
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

	// Three-tier abort (TS: abortController.ts, spec:3750-3810)
	// rootCtx → siblingCtx via context.WithCancelCause.
	// siblingCancel kills all sibling tools but does NOT end the query.
	rootCtx       context.Context
	siblingCtx    context.Context
	siblingCancel context.CancelCauseFunc

	hasErrored  bool
	errToolDesc string
	discarded   bool
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
		e.emitEvent(types.QueryEvent{
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
}

// ExecuteAll adds all tool blocks, runs them with concurrency, and returns
// results in insertion order. This is the main public API for the executor.
// Source: StreamingToolExecutor.ts — addTool + getRemainingResults combined.
func (e *StreamingToolExecutor) ExecuteAll(blocks []types.ContentBlock) []types.ContentBlock {
	// Phase 1: Add all tool blocks (starts goroutines via processQueue).
	for _, block := range blocks {
		if block.Type != types.ContentTypeToolUse {
			continue
		}
		e.AddTool(block)
	}

	if len(e.tools) == 0 {
		return nil
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
	for _, tt := range e.tools {
		if len(tt.resultBlocks) > 0 {
			results = append(results, tt.resultBlocks...)
		}
	}
	return results
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
func (e *StreamingToolExecutor) getAbortReason() string {
	if e.discarded {
		return "streaming_fallback"
	}
	if e.hasErrored {
		return "sibling_error"
	}
	select {
	case <-e.rootCtx.Done():
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
	_ = json.Unmarshal(tt.Input, &input)
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

	// Check if already aborted before running.
	// Source: StreamingToolExecutor.ts:276-292 — check abort before execution.
	e.mu.Lock()
	reason := e.getAbortReason()
	e.mu.Unlock()
	if reason != "" {
		errBlock := CreateSyntheticErrorBlock(tt.ID, reason)
		errMsg := extractErrMsg(errBlock.Content)
		e.emitEvent(types.QueryEvent{
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

	// Look up tool definition.
	t, ok := e.toolMap[tt.Name]
	if !ok {
		// Should not happen (checked in AddTool), but handle defensively.
		errMsg := fmt.Sprintf("No such tool available: %s", tt.Name)
		errBlock := CreateToolErrorBlock(tt.ID, errMsg)
		e.emitEvent(types.QueryEvent{
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

	// Try streaming execution first (ToolWithStreaming interface).
	// Source: StreamingToolExecutor.ts:320-382 — runToolUse generator.
	if streamer, ok := t.(tool.ToolWithStreaming); ok {
		var lastDisplayOutput string
		result, err := streamer.ExecuteStream(e.siblingCtx, tt.Input, toolCtx, func(u tool.ProgressUpdate) {
			if len(u.Lines) > 0 {
				display := strings.Join(u.Lines, "\n")
				lastDisplayOutput = display
				e.emitEvent(types.QueryEvent{
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

		rawJSON, _ := json.Marshal(result.Data)
		outputJSON, _ := json.Marshal(string(rawJSON))
		displayOutput := t.RenderResult(result.Data)
		if displayOutput == "" && lastDisplayOutput != "" {
			displayOutput = lastDisplayOutput
		}
		e.emitEvent(types.QueryEvent{
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
		e.applyContextModifier(tt, result)
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

	rawJSON, _ := json.Marshal(result.Data)
	outputJSON, _ := json.Marshal(string(rawJSON))
	displayOutput := t.RenderResult(result.Data)
	e.emitEvent(types.QueryEvent{
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
	e.applyContextModifier(tt, result)
}

// emitToolError emits error events and result blocks for a failed tool.
// Source: StreamingToolExecutor.ts:354-364 — Bash errors cancel siblings.
func (e *StreamingToolExecutor) emitToolError(tt *TrackedTool, err error, elapsed time.Duration) {
	errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
	e.emitEvent(types.QueryEvent{
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
func (e *StreamingToolExecutor) buildToolCtx(toolUseID string) *types.ToolUseContext {
	if e.tctx == nil {
		return &types.ToolUseContext{ToolUseID: toolUseID}
	}
	cp := *e.tctx
	cp.ToolUseID = toolUseID
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
) []types.ContentBlock {
	executor := NewStreamingToolExecutor(tools, tctx, emitEvent, ctx)
	return executor.ExecuteAll(blocks)
}

// ---------------------------------------------------------------------------
// SequentialToolLoop — Phase 1 fallback (no concurrency)
// ---------------------------------------------------------------------------

// SequentialToolLoop executes tool calls one at a time.
// Source: StreamingToolExecutor.ts — simplified for Phase 1 (no concurrency).
//
// The full StreamingToolExecutor supports:
//   - Concurrent-safe tools executing in parallel
//   - Sibling error cascading (Bash errors kill sibling processes)
//   - Context modifier application (only for non-concurrent tools)
//   - Progress message yielding
//
// Phase 1 simplification: all tools execute sequentially, no concurrency.
func SequentialToolLoop(
	ctx context.Context,
	tools map[string]tool.Tool,
	blocks []types.ContentBlock,
	tctx *types.ToolUseContext,
	emitEvent func(types.QueryEvent),
) []types.ContentBlock {
	var results []types.ContentBlock

	for _, block := range blocks {
		if block.Type != types.ContentTypeToolUse {
			continue
		}

		// Check for cancellation between tool calls.
		// Source: StreamingToolExecutor.ts:276-292 — check abort before execution.
		select {
		case <-ctx.Done():
			results = append(results, CreateSyntheticErrorBlock(block.ID, "user_interrupted"))
			emitEvent(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID: block.ID,
					Output:    json.RawMessage(`{"error":"cancelled"}`),
					IsError:   true,
				},
			})
			continue
		default:
		}

		t, ok := tools[block.Name]
		if !ok {
			// Source: StreamingToolExecutor.ts:77-100 — unknown tool returns error result.
			errBlock := CreateToolErrorBlock(block.ID, fmt.Sprintf("No such tool available: %s", block.Name))
			results = append(results, errBlock)
			emitEvent(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID: block.ID,
					Output:    errBlock.Content,
					IsError:   true,
				},
			})
			continue
		}

		// Execute the tool.
		// Source: StreamingToolExecutor.ts:265-405 — executeTool().
		start := time.Now()
		result, err := t.Call(ctx, block.Input, tctx)
		elapsed := time.Since(start)

		if err != nil {
			// Source: StreamingToolExecutor.ts:351-363 — error result.
			errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
			results = append(results, types.NewToolResultBlock(block.ID, errJSON, true))
			emitEvent(types.QueryEvent{
				Type: types.EventToolEnd,
				ToolResult: &types.ToolResultEvent{
					ToolUseID: block.ID,
					Output:    errJSON,
					IsError:   true,
					Timing:    elapsed,
				},
			})
			continue
		}

		// Source: StreamingToolExecutor.ts:391-395 — apply context modifier
		// only for non-concurrent tools. Phase 1 always applies since all are sequential.
		if result != nil && result.ContextModifier != nil && tctx != nil {
			tctx = result.ContextModifier(tctx)
		}

		outputJSON, _ := json.Marshal(result.Data)
		results = append(results, types.NewToolResultBlock(block.ID, outputJSON, false))
		emitEvent(types.QueryEvent{
			Type: types.EventToolEnd,
			ToolResult: &types.ToolResultEvent{
				ToolUseID: block.ID,
				Output:    outputJSON,
				Timing:    elapsed,
			},
		})
	}

	return results
}
