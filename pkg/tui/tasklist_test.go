package tui

import (
	"strconv"
	"strings"
	"testing"
)

func newTaskTestApp() *App {
	return newTestApp(&tuiMockProvider{})
}

func TestRenderTaskList_Empty(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary { return nil })

	result := app.renderTaskList()
	if !strings.Contains(result, "No tasks") {
		t.Errorf("empty task list should show 'No tasks', got: %q", result)
	}
}

func TestRenderTaskList_Pending(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Fix auth", Status: "pending"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "☐") {
		t.Errorf("pending task should use ☐, got: %q", result)
	}
	if !strings.Contains(result, "1") {
		t.Errorf("should show task ID, got: %q", result)
	}
	if !strings.Contains(result, "Fix auth") {
		t.Errorf("should show subject, got: %q", result)
	}
}

func TestRenderTaskList_InProgress(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "2", Subject: "Writing tests", Status: "in_progress"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "▶") {
		t.Errorf("in_progress task should use ▶, got: %q", result)
	}
}

func TestRenderTaskList_Completed(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "3", Subject: "Done", Status: "completed"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "☑") {
		t.Errorf("completed task should use ☑, got: %q", result)
	}
}

func TestRenderTaskList_WithOwner(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Task", Status: "pending", Owner: "agent-1"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "(agent-1)") {
		t.Errorf("should show owner, got: %q", result)
	}
}

func TestRenderTaskList_Blocked(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{
			{ID: "2", Subject: "Blocked task", Status: "pending", BlockedBy: []string{"1"}},
		}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "blocked by #1") {
		t.Errorf("should show blocker, got: %q", result)
	}
}

func TestRenderTaskList_MaxItems(t *testing.T) {
	app := newTaskTestApp()
	tasks := make([]TaskSummary, 10)
	for i := range tasks {
		tasks[i] = TaskSummary{ID: strconv.Itoa(i + 1), Subject: "Task", Status: "pending"}
	}
	app.SetTaskListFn(func() []TaskSummary { return tasks })

	result := app.renderTaskList()
	lines := strings.Split(result, "\n")
	if len(lines) != maxTaskPanelItems {
		t.Errorf("expected %d lines (capped), got %d", maxTaskPanelItems, len(lines))
	}
}

func TestRenderTaskList_NilFn(t *testing.T) {
	app := newTaskTestApp()
	// No SetTaskListFn
	result := app.renderTaskList()
	if result != "" {
		t.Errorf("nil fn should return empty, got: %q", result)
	}
}

func TestApp_TaskListDirty_OnToolEnd(t *testing.T) {
	app := newTaskTestApp()
	app.taskListDirty = false

	// Simulate tool end
	model, _ := app.Update(toolEndMsg{ToolUseID: "t1", Output: "ok"})
	a := model.(*App)
	if !a.taskListDirty {
		t.Error("toolEndMsg should mark task list dirty")
	}
}

func TestApp_TaskListCache_View(t *testing.T) {
	app := newTaskTestApp()
	app.width = 80
	app.height = 24
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Fix auth", Status: "pending"}}
	})

	// Task list auto-shows when dirty + fn returns tasks
	app.taskListDirty = true

	view := app.View()
	if !strings.Contains(view, "☐") || !strings.Contains(view, "Fix auth") {
		t.Errorf("View should contain task list when tasks exist, got:\n%s", view)
	}
}

func TestApp_TaskListAutoHide_NoTasks(t *testing.T) {
	app := newTaskTestApp()
	app.width = 80
	app.height = 24
	app.SetTaskListFn(func() []TaskSummary { return nil })

	// No tasks → panel should not appear in view
	app.taskListDirty = true
	view := app.View()
	if strings.Contains(view, "☐") {
		t.Errorf("View should not contain task panel when no tasks, got:\n%s", view)
	}
}
