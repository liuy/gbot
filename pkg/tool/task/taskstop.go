package task

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// StopInput is the input schema for the TaskStop tool.
// Source: TaskStopTool.ts — inputSchema
type StopInput struct {
	TaskID string `json:"task_id,omitempty"`
}

// StopOutput is the output schema for the TaskStop tool.
// Source: TaskStopTool.ts — outputSchema
type StopOutput struct {
	Message  string `json:"message"`
	TaskID   string `json:"task_id"`
	TaskType string `json:"task_type"`
	Command  string `json:"command,omitempty"`
}

// NewTaskStop creates the TaskStop tool.
// Source: tools/TaskStopTool/TaskStopTool.ts
func NewTaskStop(reg Registry) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"task_id": {
				"type": "string",
				"description": "The ID of the background task to stop"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "TaskStop",
		Aliases_: []string{"KillShell"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in StopInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Stop a running background task", nil
			}
			return fmt.Sprintf("Stop task %s", in.TaskID), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskStop(reg, input)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: `- Stops a running background task by its ID
- Takes a task_id parameter identifying the task to stop
- Returns a success or failure status
- Use this tool when you need to terminate a long-running task`,
		RenderResult_: func(data any) string {
			out, ok := data.(*StopOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			return out.Message
		},
	})
}

// executeTaskStop runs the TaskStop tool logic.
// Source: TaskStopTool.ts — call() + validateInput()
func executeTaskStop(reg Registry, input json.RawMessage) (*tool.ToolResult, error) {
	var in StopInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.TaskID == "" {
		return nil, fmt.Errorf("missing required parameter: task_id")
	}

	// Validate task exists and is running
	info, found := reg.Get(in.TaskID)
	if !found {
		return nil, fmt.Errorf("no task found with ID: %s", in.TaskID)
	}

	if isTerminal(info.Status) {
		return nil, fmt.Errorf("task %s is not running (status: %s)", in.TaskID, info.Status)
	}

	// Kill the task
	if err := reg.Kill(in.TaskID); err != nil {
		return nil, fmt.Errorf("failed to stop task %s: %w", in.TaskID, err)
	}

	cmd := info.Command
	if cmd == "" {
		cmd = info.Description
	}

	return &tool.ToolResult{
		Data: &StopOutput{
			Message:  fmt.Sprintf("Successfully stopped task: %s (%s)", in.TaskID, cmd),
			TaskID:   in.TaskID,
			TaskType: info.Type,
			Command:  cmd,
		},
	}, nil
}
