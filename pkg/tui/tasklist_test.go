package tui

import (
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/tool/tasks"
)

func newTaskTestApp() *App {
	return newTestApp(&tuiMockProvider{})
}

func TestRenderTaskList_Empty(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary { return nil })

	result := app.renderTaskList()
	if result != "" {
		t.Errorf("no tasks should return empty string, got: %q", result)
	}
}

func TestRenderTaskList_Pending(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Fix auth", Status: "pending"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "[ ]") { // ◻
		t.Errorf("pending task should use ◻, got: %q", result)
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
	if !strings.Contains(result, "[▶]") { // ◼
		t.Errorf("in_progress task should use ◼, got: %q", result)
	}
	if !strings.Contains(result, "Writing tests") {
		t.Errorf("should show subject, got: %q", result)
	}
}

func TestRenderTaskList_Completed(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "3", Subject: "Done", Status: "completed"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "[✓]") { // ✔
		t.Errorf("completed task should use ✔, got: %q", result)
	}
	if !strings.Contains(result, "Done") {
		t.Errorf("should show subject, got: %q", result)
	}
}

func TestRenderTaskList_WithOwner(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{{ID: "1", Subject: "Task", Status: "pending", Owner: "agent-1"}}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "(@agent-1)") {
		t.Errorf("should show @owner, got: %q", result)
	}
}

func TestRenderTaskList_Blocked(t *testing.T) {
	app := newTaskTestApp()
	app.SetTaskListFn(func() []TaskSummary {
		return []TaskSummary{
			{ID: "2", Subject: "Blocked task", Status: "pending", BlockedBy: []string{"Fix auth"}},
		}
	})

	result := app.renderTaskList()
	if !strings.Contains(result, "blocked by Fix auth") {
		t.Errorf("should show blocker subject, got: %q", result)
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
	if !strings.Contains(view, "[ ]") || !strings.Contains(view, "Fix auth") {
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
	if strings.Contains(view, "[ ]") {
		t.Errorf("View should not contain task panel when no tasks, got:\n%s", view)
	}
}

func TestApp_TaskListAutoReset_E2E(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		taskList := tasks.NewList(dir)

		// Create and complete 2 tasks.
		_, _ = taskList.CreateTask("task 1", "", "", nil)
		_, _ = taskList.CreateTask("task 2", "", "", nil)
		completed := tasks.StatusCompleted
		_, _, _ = taskList.UpdateTask("1", tasks.TaskUpdates{Status: &completed})
		_, _, _ = taskList.UpdateTask("2", tasks.TaskUpdates{Status: &completed})

		app := newTaskTestApp()
		app.width = 80
		app.height = 24
		app.SetTaskListFn(func() []TaskSummary {
			if taskList.ShouldResetCompleted(5 * time.Second) {
				_ = taskList.ResetCompleted()
				return nil
			}
			allTasks, _ := taskList.ListTasks()
			var result []TaskSummary
			for _, t := range allTasks {
				result = append(result, TaskSummary{
					ID:      t.ID,
					Subject: t.Subject,
					Status:  string(t.Status),
				})
			}
			return result
		})
		app.SetAutoResetFn(func() bool {
			if taskList.ShouldResetCompleted(5 * time.Second) {
				_ = taskList.ResetCompleted()
				return true
			}
			return false
		})

		// Render 1: tasks visible, cache populated, dirty cleared.
		app.taskListDirty = true
		view1 := app.View()
		if !strings.Contains(view1, "task 1") {
			t.Fatal("first render should show tasks")
		}
		// After first render, taskListDirty is false (cache is active).
		if app.taskListDirty {
			t.Fatal("cache should be clean after first render")
		}

		time.Sleep(5 * time.Second)

		view2 := app.View()
		if strings.Contains(view2, "task 1") {
			t.Error("tasks should clear after 5s delay")
		}
	})
}

func TestApp_TaskListAutoReset_SessionResume(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()

		// Previous session: create and complete tasks.
		l1 := tasks.NewList(dir)
		_, _ = l1.CreateTask("task 1", "", "", nil)
		_, _ = l1.CreateTask("task 2", "", "", nil)
		completed := tasks.StatusCompleted
		_, _, _ = l1.UpdateTask("1", tasks.TaskUpdates{Status: &completed})
		_, _, _ = l1.UpdateTask("2", tasks.TaskUpdates{Status: &completed})

		// New session: fresh List, allDoneSince lost.
		l2 := tasks.NewList(dir)
		app := newTaskTestApp()
		app.width = 80
		app.height = 24
		app.SetAutoResetFn(func() bool {
			if l2.ShouldResetCompleted(5 * time.Second) {
				_ = l2.ResetCompleted()
				return true
			}
			return false
		})
		app.SetTaskListFn(func() []TaskSummary {
			allTasks, _ := l2.ListTasks()
			var result []TaskSummary
			for _, t := range allTasks {
				result = append(result, TaskSummary{
					ID:      t.ID,
					Subject: t.Subject,
					Status:  string(t.Status),
				})
			}
			return result
		})

		// First render after restart: tasks should be cleared immediately.
		app.taskListDirty = true
		view1 := app.View()
		if strings.Contains(view1, "task 1") {
			t.Error("session resume should clear completed tasks immediately, not wait 5s")
		}
	})
}
