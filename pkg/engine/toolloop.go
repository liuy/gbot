package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

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
	ID       string
	Name     string
	Input    json.RawMessage
	Status   ToolStatus
	Duration time.Duration
	Result   *tool.ToolResult
	Err      error
}

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
				Type: types.EventToolResult,
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
				Type: types.EventToolResult,
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
				Type: types.EventToolResult,
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
			Type: types.EventToolResult,
			ToolResult: &types.ToolResultEvent{
				ToolUseID: block.ID,
				Output:    outputJSON,
				Timing:    elapsed,
			},
		})
	}

	return results
}
