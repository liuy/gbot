package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
)

// GetInput is the input schema for the TaskGet tool.
// Source: tools/TaskGetTool/TaskGetTool.ts — inputSchema
type GetInput struct {
	TaskID string `json:"taskId"`
}

// GetOutput is the output schema for the TaskGet tool.
// Source: tools/TaskGetTool/TaskGetTool.ts — outputSchema
type GetOutput struct {
	Task *GetOutputTask `json:"task"` // nil = not found
}

// GetOutputTask is the task subset returned by TaskGet.
type GetOutputTask struct {
	ID          string     `json:"id"`
	Subject     string     `json:"subject"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Blocks      []string   `json:"blocks"`
	BlockedBy   []string   `json:"blockedBy"`
}

// NewTaskGet creates the TaskGet tool.
// Source: tools/TaskGetTool/TaskGetTool.ts
func NewTaskGet(list *List) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["taskId"],
		"properties": {
			"taskId": {
				"type": "string",
				"description": "The ID of the task to retrieve"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "TaskGet",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in GetInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Retrieve a task by ID", nil
			}
			if t, _ := list.GetTask(in.TaskID); t != nil {
				return t.Subject, nil
			}
			return "#" + in.TaskID, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskGet(list, input)
		},
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "retrieve a task by ID",
		MaxResultSizeChars: 100000,
		Prompt_:            taskGetPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*GetOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			// Source: TaskGetTool.ts:99-127 — mapToolResultToToolResultBlockParam
			if out.Task == nil {
				return "Task not found"
			}
			t := out.Task
			var lines []string
			lines = append(lines, fmt.Sprintf("Task #%s: %s", t.ID, t.Subject))
			lines = append(lines, fmt.Sprintf("Status: %s", t.Status))
			lines = append(lines, fmt.Sprintf("Description: %s", t.Description))
			if len(t.BlockedBy) > 0 {
				lines = append(lines, fmt.Sprintf("Blocked by: %s", formatIDList(t.BlockedBy)))
			}
			if len(t.Blocks) > 0 {
				lines = append(lines, fmt.Sprintf("Blocks: %s", formatIDList(t.Blocks)))
			}
			return strings.Join(lines, "\n")
		},
	})
}

// executeTaskGet runs the TaskGet tool logic.
// Source: TaskGetTool.ts:73-98 — call()
func executeTaskGet(list *List, input json.RawMessage) (*tool.ToolResult, error) {
	var in GetInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.TaskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}

	task, err := list.GetTask(in.TaskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	if task == nil {
		// Source: TaskGetTool.ts:78-84 — return { task: null }
		return &tool.ToolResult{Data: &GetOutput{Task: nil}}, nil
	}

	// Source: TaskGetTool.ts:86-97 — return full task details
	return &tool.ToolResult{Data: &GetOutput{Task: &GetOutputTask{
		ID:          task.ID,
		Subject:     task.Subject,
		Description: task.Description,
		Status:      task.Status,
		Blocks:      task.Blocks,
		BlockedBy:   task.BlockedBy,
	}}}, nil
}

// formatIDList formats task IDs as "#1, #2, #3".
// Source: TaskGetTool.ts:115-119 — task.blockedBy.map(id => `#${id}`).join(', ')
func formatIDList(ids []string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "#" + id
	}
	return strings.Join(parts, ", ")
}
