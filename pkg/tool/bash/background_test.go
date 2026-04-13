package bash

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNewBackgroundTaskRegistry(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	if r == nil {
		t.Fatal("NewBackgroundTaskRegistry() returned nil")
	}
	if len(r.List()) != 0 {
		t.Error("new registry should have no tasks")
	}
}

func TestBackgroundTaskRegistry_Spawn(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("echo hello", 1234, NewStreamingOutput(nil))
	if task == nil {
		t.Fatal("Spawn() returned nil")
	}
	if task.ID != "bg-1" {
		t.Errorf("ID = %q, want %q", task.ID, "bg-1")
	}
	if task.Command != "echo hello" {
		t.Errorf("Command = %q, want %q", task.Command, "echo hello")
	}
	if task.PID != 1234 {
		t.Errorf("PID = %d, want 1234", task.PID)
	}
	if task.Status != TaskRunning {
		t.Errorf("Status = %q, want %q", task.Status, TaskRunning)
	}
	if task.Output == nil {
		t.Error("Output should not be nil")
	}
	if task.done == nil {
		t.Error("done channel should not be nil")
	}
}

func TestBackgroundTaskRegistry_Spawn_IncrementalID(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task1 := r.Spawn("cmd1", 1, nil)
	task2 := r.Spawn("cmd2", 2, nil)

	if task1.ID == task2.ID {
		t.Errorf("IDs should be unique: %q == %q", task1.ID, task2.ID)
	}
}

func TestBackgroundTaskRegistry_List(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	r.Spawn("cmd1", 1, nil)
	r.Spawn("cmd2", 2, nil)

	tasks := r.List()
	if len(tasks) != 2 {
		t.Errorf("List() = %d tasks, want 2", len(tasks))
	}
}

func TestBackgroundTaskRegistry_Kill(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	// Use PID 0 to avoid killing actual processes in test
	task := r.Spawn("sleep 100", 0, NewStreamingOutput(nil))

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if task.Status != TaskKilled {
		t.Errorf("Status = %q, want %q", task.Status, TaskKilled)
	}

	// Verify done channel is closed
	select {
	case <-task.done:
		// Expected
	default:
		t.Error("done channel should be closed after kill")
	}
}

func TestBackgroundTaskRegistry_Kill_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	err := r.Kill("nonexistent")
	if err == nil {
		t.Error("Kill() on nonexistent task should return error")
	}
}

func TestBackgroundTaskRegistry_Kill_AlreadyCompleted(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("echo done", 0, nil)
	task.Complete(0)

	err := r.Kill(task.ID)
	if err == nil {
		t.Error("Kill() on completed task should return error")
	}
}

func TestBackgroundTaskRegistry_Wait(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("echo hello", 0, nil)

	// Complete in background
	go func() {
		time.Sleep(50 * time.Millisecond)
		task.Complete(0)
	}()

	code, err := r.Wait(task.ID)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestBackgroundTaskRegistry_Wait_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	_, err := r.Wait("nonexistent")
	if err == nil {
		t.Error("Wait() on nonexistent task should return error")
	}
}

func TestBackgroundTaskRegistry_Wait_NonZeroExit(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("exit 1", 0, nil)
	task.Complete(1)

	code, err := r.Wait(task.ID)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}
	if task.Status != TaskFailed {
		t.Errorf("Status = %q, want %q", task.Status, TaskFailed)
	}
}

func TestBackgroundTaskRegistry_Get(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("cmd", 1, nil)

	got, ok := r.Get(task.ID)
	if !ok {
		t.Error("Get() should find spawned task")
	}
	if got.ID != task.ID {
		t.Errorf("Get() = %q, want %q", got.ID, task.ID)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get() should not find nonexistent task")
	}
}

func TestBackgroundTaskRegistry_Remove(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("cmd", 1, nil)
	task.Complete(0)

	r.Remove(task.ID)

	_, ok := r.Get(task.ID)
	if ok {
		t.Error("Get() should not find removed task")
	}
}

func TestBackgroundTask_Complete(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Status: TaskRunning,
		done:   make(chan struct{}),
	}

	task.Complete(0)

	if task.Status != TaskCompleted {
		t.Errorf("Status = %q, want %q", task.Status, TaskCompleted)
	}
	if task.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", task.ExitCode)
	}

	select {
	case <-task.done:
	default:
		t.Error("done channel should be closed after Complete()")
	}
}

func TestBackgroundTask_Complete_NonZero(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Status: TaskRunning,
		done:   make(chan struct{}),
	}

	task.Complete(1)

	if task.Status != TaskFailed {
		t.Errorf("Status = %q, want %q", task.Status, TaskFailed)
	}
}

func TestBackgroundTask_SetStallCancel(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{}

	called := false
	cancel := func() { called = true }
	task.SetStallCancel(cancel)

	if task.cancelStall == nil {
		t.Error("cancelStall should be set")
	}

	// Trigger cancel via Complete
	task.done = make(chan struct{})
	task.Complete(0)

	if !called {
		t.Error("stall cancel should be called on Complete()")
	}
}

func TestBackgroundTaskRegistry_Kill_StopsStallWatchdog(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("cmd", 0, nil)

	stallCancelled := false
	task.SetStallCancel(func() { stallCancelled = true })

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if !stallCancelled {
		t.Error("Kill() should cancel stall watchdog")
	}
}

func TestBackgroundTaskRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := r.Spawn("cmd", 0, nil)
			task.Complete(0)
			r.List()
			r.Get(task.ID)
		}()
	}
	wg.Wait()

	tasks := r.List()
	if len(tasks) != 10 {
		t.Errorf("List() = %d tasks, want 10", len(tasks))
	}
}

func TestBackgroundTaskRegistry_Kill_NoStallCancel(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("cmd", 0, nil)
	// Don't set stall cancel — cover the nil cancelStall path

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if task.Status != TaskKilled {
		t.Errorf("Status = %q, want %q", task.Status, TaskKilled)
	}
}

func TestBackgroundTaskRegistry_Kill_WithPID(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	// Use os.Getpid() — killing own process tree would be bad,
	// but killProcessTree with our own PID will succeed without killing us
	// since we're the parent. Use PID -1 to avoid actually killing anything.
	task := r.Spawn("cmd", -1, nil)

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
}

func TestBackgroundTask_Complete_StopsStallCancel(t *testing.T) {
	t.Parallel()

	called := false
	task := &BackgroundTask{
		Status:     TaskRunning,
		done:       make(chan struct{}),
		cancelStall: func() { called = true },
	}

	task.Complete(0)

	if !called {
		t.Error("Complete() should call stall cancel")
	}
	if task.cancelStall != nil {
		t.Error("cancelStall should be nil after Complete()")
	}
}

func TestBackgroundTaskRegistry_Spawn_WithOutput(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	output := NewStreamingOutput(nil)
	_, _ = output.Write([]byte("hello\n"))

	task := r.Spawn("echo hello", 1234, output)

	if task.Output == nil {
		t.Error("Output should not be nil")
	}
	lines := task.Output.Lines()
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("Output.Lines() = %v, want [hello]", lines)
	}
}

func TestBackgroundTaskRegistry_RemoveNonexistent(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	// Should not panic
	r.Remove("nonexistent")
}

func TestBackgroundTaskRegistry_Kill_RealPID(t *testing.T) {
	r := NewBackgroundTaskRegistry()

	// Spawn a real sleep process in its own process group
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("can't spawn sleep process: %v", err)
	}
	pid := cmd.Process.Pid

	task := r.Spawn("sleep 300", pid, nil)

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if task.Status != TaskKilled {
		t.Errorf("Status = %q, want %q", task.Status, TaskKilled)
	}
}

func TestDefaultRegistry(t *testing.T) {
	r := DefaultRegistry()
	if r == nil {
		t.Fatal("DefaultRegistry() returned nil")
	}
	// Should return the same instance on repeated calls
	r2 := DefaultRegistry()
	if r != r2 {
		t.Error("DefaultRegistry() should return the same instance")
	}
}
