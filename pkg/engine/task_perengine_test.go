package engine

import (
	"log/slog"
	"os"
	"testing"

	"github.com/liuy/gbot/pkg/tool/task"
)

func TestEngine_TaskList_PerEngine(t *testing.T) {
	t.Parallel()

	tl1 := task.NewList("")
	eng1 := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl1,
	})
	t.Cleanup(func() { eng1.Close() })

	tl2 := task.NewList("")
	eng2 := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl2,
	})
	t.Cleanup(func() { eng2.Close() })

	store1 := newTestStore(t)
	eng1.SetStore(store1, t.TempDir())
	if err := eng1.NewSession(t.TempDir(), ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	store2 := newTestStore(t)
	eng2.SetStore(store2, t.TempDir())
	if err := eng2.NewSession(t.TempDir(), ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	expectedDir1, err := task.TasksDir(eng1.SessionID())
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng1.TaskList().Dir() != expectedDir1 {
		t.Errorf("eng1.TaskList().Dir() = %q, want %q", eng1.TaskList().Dir(), expectedDir1)
	}

	expectedDir2, err := task.TasksDir(eng2.SessionID())
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng2.TaskList().Dir() != expectedDir2 {
		t.Errorf("eng2.TaskList().Dir() = %q, want %q", eng2.TaskList().Dir(), expectedDir2)
	}

	if eng1.TaskList().Dir() == eng2.TaskList().Dir() {
		t.Errorf("eng1 and eng2 task dirs must differ, both = %q", eng1.TaskList().Dir())
	}
	if eng1.TaskList() == eng2.TaskList() {
		t.Error("eng1.TaskList() and eng2.TaskList() must be distinct pointers")
	}
}

func TestSwitchSession_UpdatesTaskDir(t *testing.T) {
	t.Parallel()

	tl := task.NewList("")
	eng := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	store := newTestStore(t)
	projectDir := t.TempDir()
	eng.SetStore(store, projectDir)

	// Create first session.
	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	firstSessionID := eng.SessionID()
	firstDir := eng.TaskList().Dir()

	expectedFirstDir, err := task.TasksDir(firstSessionID)
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if firstDir != expectedFirstDir {
		t.Errorf("after NewSession: dir = %q, want %q", firstDir, expectedFirstDir)
	}

	// Seed first session with a task.
	if _, err := tl.CreateTask("first task", "desc", "doing", nil); err != nil {
		t.Fatalf("CreateTask in first session: %v", err)
	}
	tasks1, err := tl.ListTasks()
	if err != nil {
		t.Fatalf("ListTasks in first session: %v", err)
	}
	if len(tasks1) != 1 {
		t.Fatalf("first session should have 1 task, got %d", len(tasks1))
	}

	// Create a second session in the store so SwitchSession can load it.
	second, err := store.CreateSessionWithEngine(projectDir, "test-model", "main")
	if err != nil {
		t.Fatalf("CreateSessionWithEngine: %v", err)
	}

	// Switch to second session.
	if _, err := eng.SwitchSession(second.SessionID); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}

	expectedSecondDir, err := task.TasksDir(second.SessionID)
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng.TaskList().Dir() != expectedSecondDir {
		t.Errorf("after SwitchSession: dir = %q, want %q", eng.TaskList().Dir(), expectedSecondDir)
	}
	if eng.TaskList().Dir() == firstDir {
		t.Errorf("SwitchSession should change dir, still %q", firstDir)
	}

	// New session must not inherit tasks from the first session.
	newTasks, err := eng.TaskList().ListTasks()
	if err != nil {
		t.Fatalf("ListTasks after switch: %v", err)
	}
	if len(newTasks) != 0 {
		t.Errorf("new session should have 0 tasks, got %d", len(newTasks))
	}

	// First session's task file must still exist in its original dir (mutual exclusion).
	entries, err := os.ReadDir(firstDir)
	if err != nil {
		t.Fatalf("ReadDir first session dir: %v", err)
	}
	jsonCount := 0
	for _, e := range entries {
		if e.Name() != "high-water-mark.json" && e.Name() != ".highwatermark" && e.Name() != ".DS_Store" {
			jsonCount++
		}
	}
	if jsonCount != 1 {
		t.Errorf("first session dir should still have 1 task file, found %d non-system files in %q", jsonCount, firstDir)
	}
}

func TestForkSession_UpdatesTaskDir(t *testing.T) {
	t.Parallel()

	tl := task.NewList("")
	eng := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	store := newTestStore(t)
	projectDir := t.TempDir()
	eng.SetStore(store, projectDir)

	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	originalDir := eng.TaskList().Dir()

	// Fork the current session.
	forked, err := eng.ForkSession("")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if len(forked) != 0 {
		t.Fatalf("forked session should have 0 messages, got %d", len(forked))
	}

	forkedID := eng.SessionID()
	if forkedID == "" {
		t.Fatal("ForkSession should set a session ID")
	}

	expectedForkDir, err := task.TasksDir(forkedID)
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng.TaskList().Dir() != expectedForkDir {
		t.Errorf("after ForkSession: dir = %q, want %q", eng.TaskList().Dir(), expectedForkDir)
	}
	if eng.TaskList().Dir() == originalDir {
		t.Errorf("ForkSession should change dir, still %q", originalDir)
	}

	// Fork copies messages but not task files — new session starts empty.
	tasks, err := eng.TaskList().ListTasks()
	if err != nil {
		t.Fatalf("ListTasks after fork: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("forked session should have 0 tasks (fork copies messages, not task files), got %d", len(tasks))
	}
}

func TestNewSession_SetsTaskDir(t *testing.T) {
	t.Parallel()
	tl := task.NewList("")
	eng := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })
	store := newTestStore(t)
	projectDir := t.TempDir()
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sessionID := eng.SessionID()
	if sessionID == "" {
		t.Fatal("NewSession should leave a non-empty session ID")
	}
	expectedDir, err := task.TasksDir(sessionID)
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng.TaskList().Dir() != expectedDir {
		t.Errorf("after NewSession: dir = %q, want %q", eng.TaskList().Dir(), expectedDir)
	}
}
func TestSetSessionID_SetsTaskDir(t *testing.T) {
	t.Parallel()

	tl := task.NewList("")
	eng := New(&Params{
		Logger:   slog.Default(),
		TaskList: tl,
	})
	t.Cleanup(func() { eng.Close() })

	eng.SetSessionID("known-id")

	expectedDir, err := task.TasksDir("known-id")
	if err != nil {
		t.Fatalf("TasksDir: %v", err)
	}
	if eng.TaskList().Dir() != expectedDir {
		t.Errorf("after SetSessionID: dir = %q, want %q", eng.TaskList().Dir(), expectedDir)
	}
}
