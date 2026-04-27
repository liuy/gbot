package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// UpdateInput is the input schema for the TaskUpdate tool.
// Source: tools/TaskUpdateTool/TaskUpdateTool.ts — inputSchema
type UpdateInput struct {
	TaskID      string         `json:"taskId"`
	Subject     *string        `json:"subject,omitempty"`
	Description *string        `json:"description,omitempty"`
	ActiveForm  *string        `json:"activeForm,omitempty"`
	Status      *string        `json:"status,omitempty"` // "pending"|"in_progress"|"completed"|"deleted"
	AddBlocks   []string       `json:"addBlocks,omitempty"`
	AddBlockedBy []string      `json:"addBlockedBy,omitempty"`
	Owner       *string        `json:"owner,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// StatusChange records a status transition.
// Source: tools/TaskUpdateTool/TaskUpdateTool.ts — outputSchema.statusChange
type StatusChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// UpdateOutput is the output schema for the TaskUpdate tool.
// Source: tools/TaskUpdateTool/TaskUpdateTool.ts — outputSchema
type UpdateOutput struct {
	Success       bool          `json:"success"`
	TaskID        string        `json:"taskId"`
	UpdatedFields []string      `json:"updatedFields"`
	Error         string        `json:"error,omitempty"`
	StatusChange  *StatusChange `json:"statusChange,omitempty"`
}

// NewTaskUpdate creates the TaskUpdate tool.
// Source: tools/TaskUpdateTool/TaskUpdateTool.ts
func NewTaskUpdate(list *List) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["taskId"],
		"properties": {
			"taskId": {
				"type": "string",
				"description": "The ID of the task to update"
			},
			"subject": {
				"type": "string",
				"description": "New subject for the task"
			},
			"description": {
				"type": "string",
				"description": "New description for the task"
			},
			"activeForm": {
				"type": "string",
				"description": "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")"
			},
			"status": {
				"type": "string",
				"description": "New status for the task. Use 'deleted' to permanently delete the task."
			},
			"addBlocks": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Task IDs that this task blocks"
			},
			"addBlockedBy": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Task IDs that block this task"
			},
			"owner": {
				"type": "string",
				"description": "New owner for the task"
			},
			"metadata": {
				"type": "object",
				"description": "Metadata keys to merge into the task. Set a key to null to delete it."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "TaskUpdate",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in UpdateInput
			if err := json.Unmarshal(input, &in); err != nil {
				return "Update a task", nil
			}
			if t, _ := list.GetTask(in.TaskID); t != nil {
				return t.Subject, nil
			}
			return "#" + in.TaskID, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskUpdate(list, input)
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100000,
		Prompt_:            taskUpdatePrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*UpdateOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			// Source: TaskUpdateTool.ts:364-404 — mapToolResultToToolResultBlockParam
			if !out.Success {
				if out.Error != "" {
					return out.Error
				}
				return fmt.Sprintf("Task #%s not found", out.TaskID)
			}
			return fmt.Sprintf("Updated task #%s %s", out.TaskID, strings.Join(out.UpdatedFields, ", "))
		},
	})
}

// executeTaskUpdate runs the TaskUpdate tool logic.
// Source: TaskUpdateTool.ts:123-363 — call()
//
// TS features deferred (P1): hooks, auto-expand, agent swarm, mailbox, verification nudge.
func executeTaskUpdate(list *List, input json.RawMessage) (*tool.ToolResult, error) {
	var in UpdateInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.TaskID == "" {
		return nil, fmt.Errorf("taskId is required")
	}

	// Source: TaskUpdateTool.ts:146-156 — check task exists
	existingTask, err := list.GetTask(in.TaskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if existingTask == nil {
		return &tool.ToolResult{Data: &UpdateOutput{
			Success:       false,
			TaskID:        in.TaskID,
			UpdatedFields: []string{},
			Error:         "Task not found",
		}}, nil
	}

	// Source: TaskUpdateTool.ts:214-227 — handle status='deleted' (physical delete)
	if in.Status != nil && *in.Status == "deleted" {
		deleted, err := list.DeleteTask(in.TaskID)
		if err != nil {
			return nil, fmt.Errorf("delete task: %w", err)
		}
		if deleted {
			return &tool.ToolResult{Data: &UpdateOutput{
				Success:       true,
				TaskID:        in.TaskID,
				UpdatedFields: []string{"deleted"},
				StatusChange:  &StatusChange{From: string(existingTask.Status), To: "deleted"},
			}}, nil
		}
		return &tool.ToolResult{Data: &UpdateOutput{
			Success:       false,
			TaskID:        in.TaskID,
			UpdatedFields: []string{},
			Error:         "Failed to delete task",
		}}, nil
	}

	// Build TaskUpdates from input, comparing to existing values.
	// Source: TaskUpdateTool.ts:160-270
	u := TaskUpdates{
		AddBlocks:   in.AddBlocks,
		AddBlockedBy: in.AddBlockedBy,
	}

	if in.Subject != nil {
		u.Subject = in.Subject
	}
	if in.Description != nil {
		u.Description = in.Description
	}
	if in.ActiveForm != nil {
		u.ActiveForm = in.ActiveForm
	}
	if in.Status != nil {
		ts := TaskStatus(*in.Status)
		u.Status = &ts
	}
	if in.Owner != nil {
		u.Owner = in.Owner
	}
	if in.Metadata != nil {
		u.Metadata = in.Metadata
	}

	// Source: TaskUpdateTool.ts:272-274 — apply updates
	_, updatedFields, err := list.UpdateTask(in.TaskID, u)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	// Source: TaskUpdateTool.ts:351-362 — build statusChange
	var statusChange *StatusChange
	if u.Status != nil && string(*u.Status) != string(existingTask.Status) {
		statusChange = &StatusChange{
			From: string(existingTask.Status),
			To:   string(*u.Status),
		}
	}

	return &tool.ToolResult{Data: &UpdateOutput{
		Success:       true,
		TaskID:        in.TaskID,
		UpdatedFields: updatedFields,
		StatusChange:  statusChange,
	}}, nil
}
