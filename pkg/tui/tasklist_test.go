package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTaskTestApp() *App {
	return newTestApp(&tuiMockProvider{})
}

func TestApp_CtrlT_Toggle(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Test task", Status: "pending"}}
	})

	if app.taskListVisible {
		t.Error("task list should start hidden")
	}

	// Toggle on
	msg := tea.KeyMsg{Type: tea.KeyCtrlT}
	model, _ := app.Update(msg)
	a := model.(*App)
	if !a.taskListVisible {
		t.Error("Ctrl+T should show task list")
	}

	// Toggle off
	model, _ = a.Update(msg)
	a = model.(*App)
	if a.taskListVisible {
		t.Error("second Ctrl+T should hide task list")
	}
}

func TestApp_CtrlT_NoOpWithoutFn(t *testing.T) {
	app := newTaskTestApp()
	// No SetTaskListFn called

	msg := tea.KeyMsg{Type: tea.KeyCtrlT}
	model, _ := app.Update(msg)
	a := model.(*App)
	if a.taskListVisible {
		t.Error("Ctrl+T should be no-op without taskListFn")
	}
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
	app.taskListVisible = true
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

	// Show task list
	app.taskListVisible = true
	app.taskListDirty = true

	view := app.View()
	if !strings.Contains(view, "☐") || !strings.Contains(view, "Fix auth") {
		t.Errorf("View should contain task list when visible, got:\n%s", view)
	}
}
