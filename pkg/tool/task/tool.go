package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
)

// StatusChange records a status transition.
type StatusChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// GetOutputTask is the task subset returned by get operations.
type GetOutputTask struct {
	ID          string     `json:"id"`
	Subject     string     `json:"subject"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	Blocks      []string   `json:"blocks"`
	BlockedBy   []string   `json:"blockedBy"`
}

// ListOutputTask is the task summary returned by list operations.
type ListOutputTask struct {
	ID        string   `json:"id"`
	Subject   string   `json:"subject"`
	Status    string   `json:"status"`
	Owner     string   `json:"owner,omitempty"`
	BlockedBy []string `json:"blockedBy"`
}

// TasksInput is the unified input schema for the Tasks tool.
// All fields are optional — the LLM includes only what it needs.
type TasksInput struct {
	Creates []CreateItem  `json:"creates,omitempty"`
	Updates []UpdateItem  `json:"updates,omitempty"`
	Deletes []string      `json:"deletes,omitempty"`
	List    *bool         `json:"list,omitempty"`
	Get     *string       `json:"get,omitempty"`
}

type CreateItem struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type UpdateItem struct {
	TaskID       string         `json:"taskId"`
	Subject      *string        `json:"subject,omitempty"`
	Description  *string        `json:"description,omitempty"`
	ActiveForm   *string        `json:"activeForm,omitempty"`
	Status       *string        `json:"status,omitempty"`
	AddBlocks    []string       `json:"addBlocks,omitempty"`
	AddBlockedBy []string       `json:"addBlockedBy,omitempty"`
	Owner        *string        `json:"owner,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// TasksOutput is the unified output schema for the Tasks tool.
// Only sections that were requested are populated.
type TasksOutput struct {
	Created []CreateResult `json:"created,omitempty"`
	Updated []UpdateResult `json:"updated,omitempty"`
	Deleted []DeleteResult `json:"deleted,omitempty"`
	List    *ListResult    `json:"list,omitempty"`
	Get     *GetResult     `json:"get,omitempty"`
}

type CreateResult struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Error   string `json:"error,omitempty"`
}

type UpdateResult struct {
	Success       bool          `json:"success"`
	TaskID        string        `json:"taskId"`
	UpdatedFields []string      `json:"updatedFields,omitempty"`
	Error         string        `json:"error,omitempty"`
	StatusChange  *StatusChange `json:"statusChange,omitempty"`
}

type DeleteResult struct {
	ID      string `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type ListResult struct {
	Tasks []ListOutputTask `json:"tasks"`
}

type GetResult struct {
	Task *GetOutputTask `json:"task"`
}

var tasksToolSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "creates": {
      "type": "array",
      "items": {
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
      },
      "description": "Tasks to create"
    },
    "updates": {
      "type": "array",
      "items": {
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
            "description": "Present continuous form shown in spinner when in_progress"
          },
          "status": {
            "type": "string",
            "description": "New status. Use 'deleted' to permanently delete the task."
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
            "description": "Metadata keys to merge. Set a key to null to delete it."
          }
        }
      },
      "description": "Task updates to apply"
    },
    "deletes": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Task IDs to permanently delete"
    },
    "list": {
      "type": "boolean",
      "description": "Set to true to list all tasks"
    },
    "get": {
      "type": "string",
      "description": "Task ID to retrieve"
    }
  }
}`)

// New creates the unified Tasks tool that merges TaskCreate, TaskUpdate,
// TaskGet, and TaskList into a single batch-capable tool.
func New(list *List) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Task",
		InputSchema_: func() json.RawMessage { return tasksToolSchema },
		Description_: func(input json.RawMessage) (string, error) {
			return tasksDescription(list, input)
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return tasksCall(list, input)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in TasksInput
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			return len(in.Creates) == 0 && len(in.Updates) == 0 && len(in.Deletes) == 0
		},
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "manage task list — create, update, delete, list, or get tasks",
		MaxResultSizeChars: 100000,
		Prompt_:            tasksToolPrompt(),
		RenderResult_:      tasksRenderResult,
	})
}

func tasksDescription(list *List, input json.RawMessage) (string, error) {
	var in TasksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "Manage tasks", nil
	}

	hasCreates := len(in.Creates) > 0
	hasUpdates := len(in.Updates) > 0
	hasDeletes := len(in.Deletes) > 0
	hasGet := in.Get != nil
	hasList := in.List != nil && *in.List

	parts := make([]string, 0, 3)

	if hasCreates {
		if len(in.Creates) == 1 {
			parts = append(parts, in.Creates[0].Subject)
		} else {
			parts = append(parts, fmt.Sprintf("Create %d tasks", len(in.Creates)))
		}
	}
	if hasUpdates {
		if len(in.Updates) == 1 {
			if t, _ := list.GetTask(in.Updates[0].TaskID); t != nil {
				parts = append(parts, t.Subject)
			} else {
				parts = append(parts, "#"+in.Updates[0].TaskID)
			}
		} else {
			parts = append(parts, fmt.Sprintf("Update %d tasks", len(in.Updates)))
		}
	}
	if hasDeletes {
		parts = append(parts, fmt.Sprintf("Delete %d tasks", len(in.Deletes)))
	}
	if hasGet {
		if t, _ := list.GetTask(*in.Get); t != nil {
			parts = append(parts, t.Subject)
		} else {
			parts = append(parts, "#"+*in.Get)
		}
	}
	if hasList {
		parts = append(parts, "List all tasks")
	}

	if len(parts) == 0 {
		return "Manage tasks", nil
	}
	return strings.Join(parts, ", "), nil
}

// Execution order: creates -> updates -> deletes -> get -> list.
// A failure in one item does NOT stop subsequent items.
func tasksCall(list *List, input json.RawMessage) (*tool.ToolResult, error) {
	var in TasksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	var out TasksOutput

	out.Created = tasksHandleCreates(list, in.Creates)
	out.Updated = tasksHandleUpdates(list, in.Updates)
	out.Deleted = tasksHandleDeletes(list, in.Deletes)

	if in.Get != nil {
		out.Get = tasksHandleGet(list, *in.Get)
	}
	if in.List != nil && *in.List {
		out.List = tasksHandleList(list)
	}

	return &tool.ToolResult{Data: &out}, nil
}

func tasksHandleCreates(list *List, items []CreateItem) []CreateResult {
	if len(items) == 0 {
		return nil
	}
	results := make([]CreateResult, 0, len(items))
	for _, item := range items {
		if item.Subject == "" {
			results = append(results, CreateResult{Error: "subject is required"})
			continue
		}
		if item.Description == "" {
			results = append(results, CreateResult{Subject: item.Subject, Error: "description is required"})
			continue
		}
		id, err := list.CreateTask(item.Subject, item.Description, item.ActiveForm, item.Metadata)
		if err != nil {
			results = append(results, CreateResult{Subject: item.Subject, Error: err.Error()})
			continue
		}
		results = append(results, CreateResult{ID: id, Subject: item.Subject})
	}
	return results
}

func tasksHandleUpdates(list *List, items []UpdateItem) []UpdateResult {
	if len(items) == 0 {
		return nil
	}
	results := make([]UpdateResult, 0, len(items))
	for _, item := range items {
		if item.TaskID == "" {
			results = append(results, UpdateResult{Error: "taskId is required"})
			continue
		}

		existingTask, err := list.GetTask(item.TaskID)
		if err != nil {
			results = append(results, UpdateResult{TaskID: item.TaskID, Error: err.Error()})
			continue
		}
		if existingTask == nil {
			results = append(results, UpdateResult{
				Success: false,
				TaskID:  item.TaskID,
				Error:   "Task not found",
			})
			continue
		}

		if item.Status != nil && *item.Status == "deleted" {
			deleted, delErr := list.DeleteTask(item.TaskID)
			if delErr != nil {
				results = append(results, UpdateResult{TaskID: item.TaskID, Error: delErr.Error()})
				continue
			}
			if deleted {
				results = append(results, UpdateResult{
					Success:       true,
					TaskID:        item.TaskID,
					UpdatedFields: []string{"deleted"},
					StatusChange:  &StatusChange{From: string(existingTask.Status), To: "deleted"},
				})
			} else {
				results = append(results, UpdateResult{
					Success: false,
					TaskID:  item.TaskID,
					Error:   "Failed to delete task",
				})
			}
			continue
		}

		u := TaskUpdates{
			AddBlocks:    item.AddBlocks,
			AddBlockedBy: item.AddBlockedBy,
		}
		if item.Subject != nil {
			u.Subject = item.Subject
		}
		if item.Description != nil {
			u.Description = item.Description
		}
		if item.ActiveForm != nil {
			u.ActiveForm = item.ActiveForm
		}
		if item.Status != nil {
			ts := TaskStatus(*item.Status)
			u.Status = &ts
		}
		if item.Owner != nil {
			u.Owner = item.Owner
		}
		if item.Metadata != nil {
			u.Metadata = item.Metadata
		}

		_, updatedFields, updateErr := list.UpdateTask(item.TaskID, u)
		if updateErr != nil {
			results = append(results, UpdateResult{TaskID: item.TaskID, Error: updateErr.Error()})
			continue
		}

		var statusChange *StatusChange
		if u.Status != nil && string(*u.Status) != string(existingTask.Status) {
			statusChange = &StatusChange{
				From: string(existingTask.Status),
				To:   string(*u.Status),
			}
		}

		results = append(results, UpdateResult{
			Success:       true,
			TaskID:        item.TaskID,
			UpdatedFields: updatedFields,
			StatusChange:  statusChange,
		})
	}
	return results
}

func tasksHandleDeletes(list *List, ids []string) []DeleteResult {
	if len(ids) == 0 {
		return nil
	}
	results := make([]DeleteResult, 0, len(ids))
	for _, id := range ids {
		deleted, err := list.DeleteTask(id)
		if err != nil {
			results = append(results, DeleteResult{ID: id, Error: err.Error()})
			continue
		}
		results = append(results, DeleteResult{ID: id, Success: deleted})
	}
	return results
}

func tasksHandleGet(list *List, id string) *GetResult {
	task, err := list.GetTask(id)
	if err != nil || task == nil {
		return &GetResult{Task: nil}
	}
	return &GetResult{Task: &GetOutputTask{
		ID:          task.ID,
		Subject:     task.Subject,
		Description: task.Description,
		Status:      task.Status,
		Blocks:      task.Blocks,
		BlockedBy:   task.BlockedBy,
	}}
}

func tasksHandleList(list *List) *ListResult {
	allTasks, err := list.ListTasks()
	if err != nil {
		return &ListResult{Tasks: nil}
	}

	var filtered []*Task
	for _, t := range allTasks {
		if t.Metadata == nil || t.Metadata["_internal"] == nil {
			filtered = append(filtered, t)
		}
	}

	completedIDs := make(map[string]bool)
	for _, t := range filtered {
		if t.Status == StatusCompleted {
			completedIDs[t.ID] = true
		}
	}

	outputTasks := make([]ListOutputTask, 0, len(filtered))
	for _, t := range filtered {
		activeBlockedBy := make([]string, 0, len(t.BlockedBy))
		for _, id := range t.BlockedBy {
			if !completedIDs[id] {
				activeBlockedBy = append(activeBlockedBy, id)
			}
		}
		outputTasks = append(outputTasks, ListOutputTask{
			ID:        t.ID,
			Subject:   t.Subject,
			Status:    string(t.Status),
			Owner:     t.Owner,
			BlockedBy: activeBlockedBy,
		})
	}

	return &ListResult{Tasks: outputTasks}
}

func tasksRenderResult(data any) string {
	out, ok := data.(*TasksOutput)
	if !ok {
		return fmt.Sprintf("%v", data)
	}

	var sections []string

	if len(out.Created) > 0 {
		parts := make([]string, 0, len(out.Created))
		for _, c := range out.Created {
			if c.Error != "" {
				if c.Subject != "" {
					parts = append(parts, fmt.Sprintf("%s FAILED: %s", c.Subject, c.Error))
				} else {
					parts = append(parts, fmt.Sprintf("FAILED: %s", c.Error))
				}
			} else {
				parts = append(parts, fmt.Sprintf("#%s %s", c.ID, c.Subject))
			}
		}
		sections = append(sections, "Created: "+strings.Join(parts, ", "))
	}

	if len(out.Updated) > 0 {
		parts := make([]string, 0, len(out.Updated))
		for _, u := range out.Updated {
			if !u.Success {
				parts = append(parts, fmt.Sprintf("#%s FAILED: %s", u.TaskID, u.Error))
			} else if u.StatusChange != nil {
				parts = append(parts, fmt.Sprintf("#%s %s->%s", u.TaskID, u.StatusChange.From, u.StatusChange.To))
			} else if len(u.UpdatedFields) > 0 {
				parts = append(parts, fmt.Sprintf("#%s %s", u.TaskID, strings.Join(u.UpdatedFields, ",")))
			} else {
				parts = append(parts, "#"+u.TaskID)
			}
		}
		sections = append(sections, "Updated: "+strings.Join(parts, ", "))
	}

	if len(out.Deleted) > 0 {
		parts := make([]string, 0, len(out.Deleted))
		for _, d := range out.Deleted {
			if d.Success {
				parts = append(parts, "#"+d.ID)
			} else {
				parts = append(parts, fmt.Sprintf("#%s FAILED: %s", d.ID, d.Error))
			}
		}
		sections = append(sections, "Deleted: "+strings.Join(parts, ", "))
	}

	if out.List != nil {
		if len(out.List.Tasks) == 0 {
			sections = append(sections, "Listed: 0 tasks")
		} else {
			sections = append(sections, fmt.Sprintf("Listed: %d tasks", len(out.List.Tasks)))
		}
	}

	if out.Get != nil {
		if out.Get.Task == nil {
			sections = append(sections, "Got: not found")
		} else {
			sections = append(sections, fmt.Sprintf("Got: #%s %s", out.Get.Task.ID, out.Get.Task.Subject))
		}
	}

	if len(sections) == 0 {
		return "No changes"
	}
	return strings.Join(sections, "\n")
}
