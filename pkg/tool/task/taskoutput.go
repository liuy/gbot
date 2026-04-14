package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// OutputInput is the input schema for the TaskOutput tool.
// Source: TaskOutputTool.tsx — inputSchema
type OutputInput struct {
	TaskID string `json:"task_id"`
	Block  *bool  `json:"block,omitempty"`   // default true
	Timeout int   `json:"timeout,omitempty"` // default 30000ms
}

// OutputOutput is the output schema for the TaskOutput tool.
// Source: TaskOutputTool.tsx — TaskOutputToolOutput
type OutputOutput struct {
	RetrievalStatus string    `json:"retrieval_status"` // success, timeout, not_ready
	Task            *TaskInfo `json:"task"`
}

// NewTaskOutput creates the TaskOutput tool.
// Source: tools/TaskOutputTool/TaskOutputTool.tsx
func NewTaskOutput(reg Registry) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["task_id"],
		"properties": {
			"task_id": {
				"type": "string",
				"description": "The task ID to get output from"
			},
			"block": {
				"type": "boolean",
				"default": true,
				"description": "Whether to wait for completion"
			},
			"timeout": {
				"type": "number",
				"default": 30000,
				"description": "Max wait time in ms"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "TaskOutput",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in OutputInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Get task output", nil
			}
			return fmt.Sprintf("TaskOutput(%s)", in.TaskID), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskOutput(ctx, reg, input)
		},
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: taskOutputPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*OutputOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			if out.Task == nil {
				return fmt.Sprintf("Status: %s", out.RetrievalStatus)
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Status: %s | Task: %s (%s)", out.RetrievalStatus, out.Task.ID, out.Task.Status)
			if out.Task.Output != "" {
				sb.WriteByte('\n')
				sb.WriteString(out.Task.Output)
			}
			return sb.String()
		},
	})
}

// executeTaskOutput runs the TaskOutput tool logic.
// Source: TaskOutputTool.tsx — call() + waitForTaskCompletion
func executeTaskOutput(ctx context.Context, reg Registry, input json.RawMessage) (*tool.ToolResult, error) {
	var in OutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}

	block := true
	if in.Block != nil {
		block = *in.Block
	}

	timeout := 30000
	if in.Timeout > 0 {
		timeout = in.Timeout
	}

	// Initial lookup
	info, found := reg.Get(in.TaskID)
	if !found {
		return nil, fmt.Errorf("no task found with ID: %s", in.TaskID)
	}

	// If already terminal, return immediately
	if isTerminal(info.Status) {
		return &tool.ToolResult{
			Data: &OutputOutput{
				RetrievalStatus: "success",
				Task:            info,
			},
		}, nil
	}

	// If block=false, return current state
	if !block {
		return &tool.ToolResult{
			Data: &OutputOutput{
				RetrievalStatus: "not_ready",
				Task:            info,
			},
		}, nil
	}

	// block=true: poll until terminal or timeout
	// Source: TaskOutputTool.tsx:122-140 — waitForTaskCompletion polling loop
	deadline := time.After(time.Duration(timeout) * time.Millisecond)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			// Timeout — return current state
			info, _ = reg.Get(in.TaskID)
			return &tool.ToolResult{
				Data: &OutputOutput{
					RetrievalStatus: "timeout",
					Task:            info,
				},
			}, nil
		case <-ticker.C:
			info, _ = reg.Get(in.TaskID)
			if info != nil && isTerminal(info.Status) {
				return &tool.ToolResult{
					Data: &OutputOutput{
						RetrievalStatus: "success",
						Task:            info,
					},
				}, nil
			}
		}
	}
}

// isTerminal returns true for terminal task statuses.
func isTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "killed"
}
