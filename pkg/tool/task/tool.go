package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
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
	Creates []CreateItem `json:"creates,omitempty"`
	Updates []UpdateItem `json:"updates,omitempty"`
	Deletes []string     `json:"deletes,omitempty"`
	List    *bool        `json:"list,omitempty"`
	Get     *string      `json:"get,omitempty"`
}

// UnmarshalJSON tolerates task IDs sent as numbers in deletes and get — LLMs
// occasionally emit numeric IDs even though the schema declares "type":"string".
// UpdateItem handles its own taskId via a separate UnmarshalJSON.
func (t *TasksInput) UnmarshalJSON(data []byte) error {
	type raw struct {
		Creates []CreateItem    `json:"creates,omitempty"`
		Updates []UpdateItem    `json:"updates,omitempty"`
		Deletes []json.Number   `json:"deletes,omitempty"`
		List    *bool           `json:"list,omitempty"`
		Get     json.RawMessage `json:"get,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	t.Creates = r.Creates
	t.Updates = r.Updates
	t.List = r.List

	// deletes: accept numbers or strings
	for _, d := range r.Deletes {
		t.Deletes = append(t.Deletes, d.String())
	}

	// get: accept quoted string or number
	if len(r.Get) > 0 {
		trimmed := bytes.TrimSpace(r.Get)
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			t.Get = &s
		} else {
			var n json.Number
			if err := json.Unmarshal(trimmed, &n); err == nil {
				str := n.String()
				t.Get = &str
			}
		}
	}
	return nil
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

// UnmarshalJSON tolerates taskId sent as a number — LLMs occasionally emit
// numeric IDs even though the schema declares "type":"string". Without this,
// the whole batch fails with "cannot unmarshal number into string".
func (u *UpdateItem) UnmarshalJSON(data []byte) error {
	// Use json.RawMessage for taskId so we can accept string, number, or
	// empty string without json.Number's strict parsing.
	type rawUpdate struct {
		Subject      *string         `json:"subject,omitempty"`
		Description  *string         `json:"description,omitempty"`
		ActiveForm   *string         `json:"activeForm,omitempty"`
		Status       *string         `json:"status,omitempty"`
		AddBlocks    []string        `json:"addBlocks,omitempty"`
		AddBlockedBy []string        `json:"addBlockedBy,omitempty"`
		Owner        *string         `json:"owner,omitempty"`
		Metadata     map[string]any  `json:"metadata,omitempty"`
		TaskID       json.RawMessage `json:"taskId"`
	}
	var raw rawUpdate
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.Subject = raw.Subject
	u.Description = raw.Description
	u.ActiveForm = raw.ActiveForm
	u.Status = raw.Status
	u.AddBlocks = raw.AddBlocks
	u.AddBlockedBy = raw.AddBlockedBy
	u.Owner = raw.Owner
	u.Metadata = raw.Metadata
	// Accept quoted string ("5"), unquoted number (5), or empty string ("").
	trimmed := bytes.TrimSpace(raw.TaskID)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte(`""`)) {
		// Empty/absent: leave TaskID as zero value.
		return nil
	}
	// Try string first (most common), fall back to number.
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		u.TaskID = s
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err == nil {
		u.TaskID = n.String()
		return nil
	}
	return fmt.Errorf("taskId: cannot unmarshal %s into string or number", string(trimmed))
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
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o TasksOutput
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			// Wire text that happens to be a JSON object decodes into an
			// all-zero TasksOutput (unknown fields ignored), which replay
			// would render as "No changes" instead of falling back to the
			// wire text. Uniform rule across wire-plaintext tools.
			if len(o.Created) == 0 && len(o.Updated) == 0 && len(o.Deleted) == 0 &&
				o.List == nil && o.Get == nil {
				return nil, fmt.Errorf("task: decoded output lacks identifying fields (not a legacy JSON result)")
			}
			return &o, nil
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*TasksOutput)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			return []types.ContentBlock{types.NewTextBlock(wireText(out))}
		},
	})
}

// wireText renders the LLM-facing plain-text form: one segment per TS task
// tool, segments joined with a blank line, entries within a segment one per
// line. Sources: TaskCreateTool.ts:135, TaskUpdateTool.ts:364-405,
// TaskGetTool.ts:99-127, TaskListTool.ts:91-115. gbot-only shapes (per-entry
// create errors, the deletes segment) have no TS counterpart; their wording
// is this codebase's own.
func wireText(out *TasksOutput) string {
	var segments []string

	if len(out.Created) > 0 {
		lines := make([]string, 0, len(out.Created))
		for _, c := range out.Created {
			if c.Error != "" {
				lines = append(lines, fmt.Sprintf("Failed to create task \"%s\": %s", c.Subject, c.Error))
				continue
			}
			lines = append(lines, fmt.Sprintf("Task #%s created successfully: %s", c.ID, c.Subject))
		}
		segments = append(segments, strings.Join(lines, "\n"))
	}

	if len(out.Updated) > 0 {
		lines := make([]string, 0, len(out.Updated))
		for _, u := range out.Updated {
			if !u.Success {
				// Failure rides the normal result so a missing task does not
				// cancel sibling tools (TaskUpdateTool.ts:373-382).
				if u.Error != "" {
					lines = append(lines, u.Error)
				} else {
					lines = append(lines, fmt.Sprintf("Task #%s not found", u.TaskID))
				}
				continue
			}
			// Empty updatedFields keeps the trailing space — the TS template
			// literal renders "Updated task #5 " verbatim (:384).
			lines = append(lines, fmt.Sprintf("Updated task #%s %s", u.TaskID, strings.Join(u.UpdatedFields, ", ")))
		}
		segments = append(segments, strings.Join(lines, "\n"))
	}

	if len(out.Deleted) > 0 {
		lines := make([]string, 0, len(out.Deleted))
		for _, d := range out.Deleted {
			if d.Success {
				lines = append(lines, fmt.Sprintf("Deleted task #%s", d.ID))
			} else {
				lines = append(lines, fmt.Sprintf("Failed to delete task #%s: %s", d.ID, d.Error))
			}
		}
		segments = append(segments, strings.Join(lines, "\n"))
	}

	if out.Get != nil {
		if out.Get.Task == nil {
			segments = append(segments, "Task not found")
		} else {
			g := out.Get.Task
			lines := []string{
				fmt.Sprintf("Task #%s: %s", g.ID, g.Subject),
				fmt.Sprintf("Status: %s", g.Status),
				fmt.Sprintf("Description: %s", g.Description),
			}
			if len(g.BlockedBy) > 0 {
				lines = append(lines, "Blocked by: "+prefixedIDs(g.BlockedBy))
			}
			if len(g.Blocks) > 0 {
				lines = append(lines, "Blocks: "+prefixedIDs(g.Blocks))
			}
			segments = append(segments, strings.Join(lines, "\n"))
		}
	}

	if out.List != nil {
		if len(out.List.Tasks) == 0 {
			segments = append(segments, "No tasks found")
		} else {
			lines := make([]string, 0, len(out.List.Tasks))
			for _, tl := range out.List.Tasks {
				owner := ""
				if tl.Owner != "" {
					owner = fmt.Sprintf(" (%s)", tl.Owner)
				}
				blocked := ""
				if len(tl.BlockedBy) > 0 {
					blocked = fmt.Sprintf(" [blocked by %s]", prefixedIDs(tl.BlockedBy))
				}
				lines = append(lines, fmt.Sprintf("#%s [%s] %s%s%s", tl.ID, tl.Status, tl.Subject, owner, blocked))
			}
			segments = append(segments, strings.Join(lines, "\n"))
		}
	}

	return strings.Join(segments, "\n\n")
}

// prefixedIDs renders ["1","2"] as "#1, #2" — the TS `#${id}` join(', ').
func prefixedIDs(ids []string) string {
	prefixed := make([]string, len(ids))
	for i, id := range ids {
		prefixed[i] = "#" + id
	}
	return strings.Join(prefixed, ", ")
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
	switch v := data.(type) {
	case *TasksOutput:
		return renderTasksOutput(v)
	default:
		return fmt.Sprintf("%v", data)
	}
}

func renderTasksOutput(out *TasksOutput) string {
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
