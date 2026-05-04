package job

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
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

// NewJobStop creates the TaskStop tool.
// Source: tools/TaskStopTool/TaskStopTool.ts
func NewJobStop(reg Registry) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"task_id": {
				"type": "string",
				"description": "The ID of the background job to stop"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "JobStop",
		Aliases_: []string{"KillShell"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in StopInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Stop a running background job", nil
			}
			return fmt.Sprintf("Stop job %s", in.TaskID), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeJobStop(reg, input)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:        true,
		SearchHint_:         "kill a running background task",
		MaxResultSizeChars:   100000,
		Prompt_: jobStopPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*StopOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			return out.Message
		},
	})
}

// executeJobStop runs the TaskStop tool logic.
// Source: TaskStopTool.ts — call() + validateInput()
func executeJobStop(reg Registry, input json.RawMessage) (*tool.ToolResult, error) {
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
		return nil, fmt.Errorf("no job found with ID: %s", in.TaskID)
	}

	if isTerminal(info.Status) {
		return nil, fmt.Errorf("job %s is not running (status: %s)", in.TaskID, info.Status)
	}

	// Kill the task
	if err := reg.Kill(in.TaskID); err != nil {
		return nil, fmt.Errorf("failed to stop job %s: %w", in.TaskID, err)
	}

	cmd := info.Command
	if cmd == "" {
		cmd = info.Description
	}

	return &tool.ToolResult{
		Data: &StopOutput{
			Message:  fmt.Sprintf("Successfully stopped job: %s (%s)", in.TaskID, cmd),
			TaskID:   in.TaskID,
			TaskType: info.Type,
			Command:  cmd,
		},
	}, nil
}
