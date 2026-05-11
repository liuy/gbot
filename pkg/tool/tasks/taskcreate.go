package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
)

// CreateInput is the input schema for the TaskCreate tool.
// Source: tools/TaskCreateTool/TaskCreateTool.ts — inputSchema
type CreateInput struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// CreateOutput is the output schema for the TaskCreate tool.
// Source: tools/TaskCreateTool/TaskCreateTool.ts — outputSchema
type CreateOutput struct {
	Task struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
	} `json:"task"`
}

// NewTaskCreate creates the TaskCreate tool.
// Source: tools/TaskCreateTool/TaskCreateTool.ts
func NewTaskCreate(list *List) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["subject", "description"],
		"properties": {
			"subject": {
				"type": "string",
				"description": "A brief title for the task"
			},
			"description": {
				"type": "string",
				"description": "What needs to be done"
			},
			"activeForm": {
				"type": "string",
				"description": "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")"
			},
			"metadata": {
				"type": "object",
				"description": "Arbitrary metadata to attach to the task"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "TaskCreate",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in CreateInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Create a task in the task list", nil
			}
			return in.Subject, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskCreate(list, input)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "create a task in the task list",
		MaxResultSizeChars: 100000,
		Prompt_:            taskCreatePrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*CreateOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			// Source: TaskCreateTool.ts:131 — mapToolResultToToolResultBlockParam
			return fmt.Sprintf("Task #%s created successfully: %s", out.Task.ID, out.Task.Subject)
		},
	})
}

// executeTaskCreate runs the TaskCreate tool logic.
// Source: TaskCreateTool.ts:80-128 — call()
//
// TS hooks (executeTaskCreatedHooks) and auto-expand (setAppState) are P1 deferred.
func executeTaskCreate(list *List, input json.RawMessage) (*tool.ToolResult, error) {
	var in CreateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if in.Description == "" {
		return nil, fmt.Errorf("description is required")
	}

	// Source: TaskCreateTool.ts:81-90 — createTask
	id, err := list.CreateTask(in.Subject, in.Description, in.ActiveForm, in.Metadata)
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}

	var out CreateOutput
	out.Task.ID = id
	out.Task.Subject = in.Subject

	return &tool.ToolResult{Data: &out}, nil
}
