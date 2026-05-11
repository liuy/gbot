package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
)

// ListOutput is the output schema for the TaskList tool.
// Source: tools/TaskListTool/TaskListTool.ts — outputSchema
type ListOutput struct {
	Tasks []ListOutputTask `json:"tasks"`
}

// ListOutputTask is the task summary returned by TaskList.
type ListOutputTask struct {
	ID        string   `json:"id"`
	Subject   string   `json:"subject"`
	Status    string   `json:"status"`
	Owner     string   `json:"owner,omitempty"`
	BlockedBy []string `json:"blockedBy"`
}

// NewTaskList creates the TaskList tool.
// Source: tools/TaskListTool/TaskListTool.ts
func NewTaskList(list *List) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "TaskList",
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(json.RawMessage) (string, error) {
			return "List all tasks", nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return executeTaskList(list)
		},
		IsReadOnly_:        func(json.RawMessage) bool { return true },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		ShouldDefer_:       false,
		SearchHint_:        "list all tasks",
		MaxResultSizeChars: 100000,
		Prompt_:            taskListPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*ListOutput)
			if !ok {
				return fmt.Sprintf("%v", data)
			}
			// Source: TaskListTool.ts:91-114 — mapToolResultToToolResultBlockParam
			if len(out.Tasks) == 0 {
				return "No tasks found"
			}
			var lines []string
			for _, task := range out.Tasks {
				owner := ""
				if task.Owner != "" {
					owner = fmt.Sprintf(" (%s)", task.Owner)
				}
				blocked := ""
				if len(task.BlockedBy) > 0 {
					blocked = fmt.Sprintf(" [blocked by %s]", formatIDList(task.BlockedBy))
				}
				lines = append(lines, fmt.Sprintf("#%s [%s] %s%s%s",
					task.ID, task.Status, task.Subject, owner, blocked))
			}
			return strings.Join(lines, "\n")
		},
	})
}

// executeTaskList runs the TaskList tool logic.
// Source: TaskListTool.ts:65-90 — call()
func executeTaskList(list *List) (*tool.ToolResult, error) {
	allTasks, err := list.ListTasks()
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	// Source: TaskListTool.ts:68-69 — filter out _internal tasks
	var filtered []*Task
	for _, t := range allTasks {
		if t.Metadata == nil || t.Metadata["_internal"] == nil {
			filtered = append(filtered, t)
		}
	}

	// Source: TaskListTool.ts:73-75 — build set of completed IDs for blocker filtering
	completedIDs := make(map[string]bool)
	for _, t := range filtered {
		if t.Status == StatusCompleted {
			completedIDs[t.ID] = true
		}
	}

	// Source: TaskListTool.ts:77-83 — map to output, filter blockedBy
	outputTasks := make([]ListOutputTask, 0, len(filtered))
	for _, t := range filtered {
		// Filter blockedBy to only include uncompleted blockers
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

	return &tool.ToolResult{Data: &ListOutput{Tasks: outputTasks}}, nil
}
