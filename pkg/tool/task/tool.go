package task

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/liuy/gbot/pkg/hooks"
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

// TasksInput is the input schema for the Task tool. The action enum is the
// primary key: exactly one operation per call. Any parameter the action does
// not accept — including the legacy creates/updates/deletes/list/get keys —
// is rejected at dispatch, because the old flat arrays let mixed payloads
// run as silent partial no-ops.
type TasksInput struct {
	Action       string         `json:"action"`
	TaskID       string         `json:"taskId,omitempty"`       // update, get
	TaskIDs      []string       `json:"taskIds,omitempty"`      // delete (bulk)
	Subject      *string        `json:"subject,omitempty"`      // create (required), update
	Description  *string        `json:"description,omitempty"`  // create (required), update
	ActiveForm   *string        `json:"activeForm,omitempty"`   // create, update
	Status       *string        `json:"status,omitempty"`       // update
	AddBlocks    []string       `json:"addBlocks,omitempty"`    // update
	AddBlockedBy []string       `json:"addBlockedBy,omitempty"` // update
	Owner        *string        `json:"owner,omitempty"`        // update
	Metadata     map[string]any `json:"metadata,omitempty"`     // create, update

	// legacyParams records old-schema keys present in the input. No action
	// accepts them; dispatch rejects them so a half-migrated caller fails
	// loudly instead of silently no-op-ing.
	legacyParams []string
}

// UnmarshalJSON tolerates task IDs sent as numbers — LLMs occasionally emit
// numeric IDs even though the schema declares "type":"string".
func (t *TasksInput) UnmarshalJSON(data []byte) error {
	type raw struct {
		Action       string          `json:"action"`
		TaskID       json.RawMessage `json:"taskId"`
		TaskIDs      []json.Number   `json:"taskIds"`
		Subject      *string         `json:"subject"`
		Description  *string         `json:"description"`
		ActiveForm   *string         `json:"activeForm"`
		Status       *string         `json:"status"`
		AddBlocks    []string        `json:"addBlocks"`
		AddBlockedBy []string        `json:"addBlockedBy"`
		Owner        *string         `json:"owner"`
		Metadata     map[string]any  `json:"metadata"`
		Creates      json.RawMessage `json:"creates"`
		Updates      json.RawMessage `json:"updates"`
		Deletes      json.RawMessage `json:"deletes"`
		List         *bool           `json:"list"`
		Get          json.RawMessage `json:"get"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	t.Action = r.Action
	t.TaskIDs = make([]string, len(r.TaskIDs))
	for i, id := range r.TaskIDs {
		t.TaskIDs[i] = id.String()
	}
	t.Subject = r.Subject
	t.Description = r.Description
	t.ActiveForm = r.ActiveForm
	t.Status = r.Status
	t.AddBlocks = r.AddBlocks
	t.AddBlockedBy = r.AddBlockedBy
	t.Owner = r.Owner
	t.Metadata = r.Metadata

	// taskId: accept quoted string ("5"), unquoted number (5), or empty
	// string (""); anything else fails the whole call instead of silently
	// dropping the ID the caller meant to target.
	trimmed := bytes.TrimSpace(r.TaskID)
	if len(trimmed) > 0 {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			t.TaskID = s
		} else {
			var n json.Number
			if err := json.Unmarshal(trimmed, &n); err == nil {
				t.TaskID = n.String()
			} else {
				return fmt.Errorf("taskId: cannot unmarshal %s into string or number", string(trimmed))
			}
		}
	}

	legacyPresent := map[string]bool{
		"creates": rawPresent(r.Creates),
		"updates": rawPresent(r.Updates),
		"deletes": rawPresent(r.Deletes),
		"list":    r.List != nil,
		"get":     rawPresent(r.Get),
	}
	for _, name := range [...]string{"creates", "updates", "deletes", "list", "get"} {
		if legacyPresent[name] {
			t.legacyParams = append(t.legacyParams, name)
		}
	}
	return nil
}

// rawPresent reports whether a legacy raw value carries a non-null JSON value.
func rawPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
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
    "action": {
      "type": "string",
      "enum": ["create", "update", "delete", "get", "list"],
      "description": "Operation to perform (exactly one per call)"
    },
    "taskId": {
      "type": "string",
      "description": "The ID of the task to update or get (required for update and get)"
    },
    "taskIds": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Task IDs to permanently delete (required for delete; accepts multiple)"
    },
    "subject": {
      "type": "string",
      "description": "A brief actionable title in imperative form (required for create, optional for update)"
    },
    "description": {
      "type": "string",
      "description": "What needs to be done (required for create, optional for update)"
    },
    "activeForm": {
      "type": "string",
      "description": "Present continuous form shown in spinner when in_progress (e.g., \"Running tests\")"
    },
    "status": {
      "type": "string",
      "enum": ["pending", "in_progress", "completed"],
      "description": "New status — e.g. \"in_progress\" before starting work, \"completed\" only when fully done (update only)"
    },
    "addBlocks": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Task IDs that this task blocks (update only)"
    },
    "addBlockedBy": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Task IDs that block this task (update only)"
    },
    "owner": {
      "type": "string",
      "description": "New owner for the task (update only; empty string clears)"
    },
    "metadata": {
      "type": "object",
      "description": "Metadata keys to merge. Set a key to null to delete it (create and update)"
    }
  },
  "required": ["action"]
}`)

// updateStatuses mirrors the enum the input schema declares for status.
// "deleted" is not a status — deletion is the delete action.
var updateStatuses = []string{string(StatusPending), string(StatusInProgress), string(StatusCompleted)}

// New creates the unified Tasks tool that merges TaskCreate, TaskUpdate,
// TaskGet, and TaskList behind an action enum: exactly one operation per
// call, with delete the only bulk action (its taskIds accept multiple IDs).
//
// hk gates the create and completion call sites; nil disables the gate. It is
// captured at construction rather than stored on the List: a List is shared
// between the main engine and its sub-engines (CreateTools runs for a
// sub-engine while the main engine is mid-task), and the gate belongs to the
// tool's call sites, not to task storage — the TUI and the WUI task panel
// mutate the same List without any hook gate.
func New(list *List, hk *hooks.Hooks) tool.Tool {
	return tool.BuildTool(tool.ToolDef{
		Name_:        "Task",
		InputSchema_: func() json.RawMessage { return tasksToolSchema },
		Description_: func(input json.RawMessage) (string, error) {
			return tasksDescription(list, input)
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return tasksCall(ctx, list, hk, input, tctx)
		},
		IsReadOnly_: func(input json.RawMessage) bool {
			var in TasksInput
			if err := json.Unmarshal(input, &in); err != nil {
				return false
			}
			// Unknown actions default to not read-only: they will fail at
			// dispatch anyway, and treating them as mutating is the safe side.
			return in.Action == "get" || in.Action == "list"
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

	switch in.Action {
	case "create":
		if in.Subject != nil && *in.Subject != "" {
			return *in.Subject, nil
		}
		return "Create task", nil
	case "update", "get":
		if t, _ := list.GetTask(in.TaskID); t != nil {
			return t.Subject, nil
		}
		return "#" + in.TaskID, nil
	case "delete":
		return fmt.Sprintf("Delete %d tasks", len(in.TaskIDs)), nil
	case "list":
		return "List all tasks", nil
	default:
		return "Manage tasks", nil
	}
}

// actionParams lists, per action, the only parameters the action accepts.
// list accepts none, hence the nil slice.
var actionParams = map[string][]string{
	"create": {"subject", "description", "activeForm", "metadata"},
	"update": {"taskId", "subject", "description", "activeForm", "status", "addBlocks", "addBlockedBy", "owner", "metadata"},
	"delete": {"taskIds"},
	"get":    {"taskId"},
	"list":   nil,
}

// disallowedParams returns the parameters present in the input that the
// action does not accept, in fixed schema order with legacy keys appended in
// creates/updates/deletes/list/get order. Presence means non-empty string,
// non-nil pointer/map, or non-empty slice, so an explicitly-empty value
// (taskId:"", taskIds:[]) flows through to the required-param checks instead.
func disallowedParams(in *TasksInput, action string) []string {
	allowed := make(map[string]bool, len(actionParams[action]))
	for _, p := range actionParams[action] {
		allowed[p] = true
	}
	present := []struct {
		name string
		ok   bool
	}{
		{"taskId", in.TaskID != ""},
		{"taskIds", len(in.TaskIDs) > 0},
		{"subject", in.Subject != nil},
		{"description", in.Description != nil},
		{"activeForm", in.ActiveForm != nil},
		{"status", in.Status != nil},
		{"addBlocks", len(in.AddBlocks) > 0},
		{"addBlockedBy", len(in.AddBlockedBy) > 0},
		{"owner", in.Owner != nil},
		{"metadata", in.Metadata != nil},
	}
	var bad []string
	for _, p := range present {
		if p.ok && !allowed[p.name] {
			bad = append(bad, p.name)
		}
	}
	return append(bad, in.legacyParams...)
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// tasksCall validates the action enum and the per-action parameter set, then
// dispatches to exactly one handler. Missing required IDs for get/delete fail
// at tool level (they had no old equivalent); create/update keep their
// in-result error entries by flowing through the handlers unchanged.
func tasksCall(ctx context.Context, list *List, hk *hooks.Hooks, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	var in TasksInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	_, known := actionParams[in.Action]
	if !known {
		if in.Action == "" {
			// Job's missing-action error appends a "run list" hint; the Task
			// enum is self-explanatory, so the enum list alone is the fix.
			return nil, fmt.Errorf(`action is required — valid actions are "create", "update", "delete", "get", "list"`)
		}
		return nil, fmt.Errorf(`unknown action %q — valid actions are "create", "update", "delete", "get", "list"`, in.Action)
	}

	if bad := disallowedParams(&in, in.Action); len(bad) > 0 {
		if accepted := actionParams[in.Action]; len(accepted) > 0 {
			return nil, fmt.Errorf("action %q accepts only: %s — got %s", in.Action, strings.Join(accepted, ", "), strings.Join(bad, ", "))
		}
		return nil, fmt.Errorf("action %q accepts no other params — got %s", in.Action, strings.Join(bad, ", "))
	}

	var out TasksOutput

	switch in.Action {
	case "create":
		out.Created = tasksHandleCreates(ctx, list, hk, []CreateItem{{
			Subject:     derefString(in.Subject),
			Description: derefString(in.Description),
			ActiveForm:  derefString(in.ActiveForm),
			Metadata:    in.Metadata,
		}}, tctx)
	case "update":
		out.Updated = tasksHandleUpdates(ctx, list, hk, []UpdateItem{{
			TaskID:       in.TaskID,
			Subject:      in.Subject,
			Description:  in.Description,
			ActiveForm:   in.ActiveForm,
			Status:       in.Status,
			AddBlocks:    in.AddBlocks,
			AddBlockedBy: in.AddBlockedBy,
			Owner:        in.Owner,
			Metadata:     in.Metadata,
		}}, tctx)
	case "delete":
		if len(in.TaskIDs) == 0 {
			return nil, fmt.Errorf(`action "delete" requires taskIds — run {"action":"list"} to see tasks`)
		}
		out.Deleted = tasksHandleDeletes(list, in.TaskIDs)
	case "get":
		if in.TaskID == "" {
			return nil, fmt.Errorf(`action "get" requires taskId — run {"action":"list"} to see tasks`)
		}
		out.Get = tasksHandleGet(list, in.TaskID)
	case "list":
		out.List = tasksHandleList(list)
	}

	return &tool.ToolResult{Data: &out}, nil
}

func tasksHandleCreates(ctx context.Context, list *List, hk *hooks.Hooks, items []CreateItem, tctx *tool.ToolUseContext) []CreateResult {
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

		// The hook runs after the file is written so it receives a real
		// task_id, and a blocking hook undoes the creation
		// (TaskCreateTool.ts:81-113). TS throws, which fails the whole tool
		// call; gbot's batch tool reports it on the affected entry so sibling
		// operations in the same call still run.
		if feedback := taskCreatedFeedback(ctx, hk, tctx, id, item.Subject, item.Description); feedback != "" {
			if _, delErr := list.DeleteTask(id); delErr != nil {
				results = append(results, CreateResult{Subject: item.Subject, Error: delErr.Error()})
				continue
			}
			results = append(results, CreateResult{Subject: item.Subject, Error: feedback})
			continue
		}

		results = append(results, CreateResult{ID: id, Subject: item.Subject})
	}
	return results
}

func tasksHandleUpdates(ctx context.Context, list *List, hk *hooks.Hooks, items []UpdateItem, tctx *tool.ToolUseContext) []UpdateResult {
	if len(items) == 0 {
		return nil
	}
	results := make([]UpdateResult, 0, len(items))
	for _, item := range items {
		if item.TaskID == "" {
			results = append(results, UpdateResult{Error: "taskId is required"})
			continue
		}

		// TS rejects an unrecognised status at the input boundary (Zod parses
		// the enum before the tool body runs). gbot has no schema-validation
		// layer, so the check belongs here: a near-miss like "Complete" would
		// otherwise slip past the TaskCompleted gate below, which compares
		// against the literal "completed", and be written to the task file.
		if item.Status != nil && !slices.Contains(updateStatuses, *item.Status) {
			results = append(results, UpdateResult{
				TaskID: item.TaskID,
				Error:  fmt.Sprintf("invalid status %q: must be one of %s", *item.Status, strings.Join(updateStatuses, ", ")),
			})
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

		// TaskCompleted fires only on a real transition into completed
		// (TaskUpdateTool.ts:230-232), and a blocking hook aborts the whole
		// update before updateTask is called (:255-264) — field changes in the
		// same call are dropped too, not just the status.
		if item.Status != nil && *item.Status == string(StatusCompleted) && TaskStatus(*item.Status) != existingTask.Status {
			if feedback := taskCompletedFeedback(ctx, hk, tctx, existingTask); feedback != "" {
				results = append(results, UpdateResult{
					Success:       false,
					TaskID:        item.TaskID,
					UpdatedFields: []string{},
					Error:         feedback,
				})
				continue
			}
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

// callContextFields carries the session id and working dir into the hook
// input. The Tasks tool holds no engine reference (the List is shared between
// the main engine and its sub-engines), so the per-call ToolUseContext is the
// only carrier of which session and which directory the event belongs to. A
// nil tctx means the caller built no context at all, and both stay empty.
func callContextFields(tctx *tool.ToolUseContext) (sessionID, cwd string) {
	if tctx == nil {
		return "", ""
	}
	cwd = tctx.WorkingDir
	if cwd == "" {
		cwd = tctx.OriginalWorkingDir
	}
	return tctx.Options.SessionID, cwd
}

// taskCreatedFeedback returns the joined TaskCreated hook feedback TS throws
// with, or "" when no hook blocks. A blocking hook means the caller must undo
// the creation.
// Source: TaskCreateTool.ts:92-113.
func taskCreatedFeedback(ctx context.Context, hk *hooks.Hooks, tctx *tool.ToolUseContext, id, subject, description string) string {
	if hk == nil {
		return ""
	}
	sessionID, cwd := callContextFields(tctx)
	var blocking []string
	for _, r := range hk.TaskCreated(ctx, &hooks.HookInput{
		HookEventName:   string(hooks.HookTaskCreated),
		SessionID:       sessionID,
		Cwd:             cwd,
		TaskID:          id,
		TaskSubject:     subject,
		TaskDescription: description,
	}) {
		if r.Outcome == hooks.HookOutcomeBlocking {
			blocking = append(blocking, hooks.TaskCreatedHookMessage(r))
		}
	}
	return strings.Join(blocking, "\n")
}

// taskCompletedFeedback returns the joined TaskCompleted hook feedback, or ""
// when no hook blocks. A blocking hook means the caller must abort the update.
// The hook sees the pre-update subject/description, matching TS's use of
// existingTask (TaskUpdateTool.ts:235-245).
func taskCompletedFeedback(ctx context.Context, hk *hooks.Hooks, tctx *tool.ToolUseContext, t *Task) string {
	if hk == nil {
		return ""
	}
	sessionID, cwd := callContextFields(tctx)
	var blocking []string
	for _, r := range hk.TaskCompleted(ctx, &hooks.HookInput{
		HookEventName:   string(hooks.HookTaskCompleted),
		SessionID:       sessionID,
		Cwd:             cwd,
		TaskID:          t.ID,
		TaskSubject:     t.Subject,
		TaskDescription: t.Description,
	}) {
		if r.Outcome == hooks.HookOutcomeBlocking {
			blocking = append(blocking, hooks.TaskCompletedHookMessage(r))
		}
	}
	return strings.Join(blocking, "\n")
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
