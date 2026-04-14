package bash

import (
	"context"
	"os/exec"
	"strings"
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
	task.Complete(0, false)

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
		task.Complete(0, false)
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
	task.Complete(1, false)

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
	task.Complete(0, false)

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

	task.Complete(0, false)

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

	task.Complete(1, false)

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
	task.Complete(0, false)

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
			task.Complete(0, false)
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

	task.Complete(0, false)

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

// ---------------------------------------------------------------------------
// FormatXML / escapeXML
// ---------------------------------------------------------------------------

func TestTaskNotification_FormatXML_Completion(t *testing.T) {
	t.Parallel()
	n := TaskNotification{
		TaskID:     "bg-1",
		ToolUseID:  "tu-123",
		Status:     "completed",
		Summary:    `Background command "test" completed`,
		OutputFile: "/tmp/output.txt",
	}
	xml := n.FormatXML()
	if !contains(xml, "<task-notification>") {
		t.Error("missing <task-notification>")
	}
	if !contains(xml, "<task-id>bg-1</task-id>") {
		t.Error("missing task-id")
	}
	if !contains(xml, "<tool-use-id>tu-123</tool-use-id>") {
		t.Error("missing tool-use-id")
	}
	if !contains(xml, "<output-file>/tmp/output.txt</output-file>") {
		t.Error("missing output-file")
	}
	if !contains(xml, "<status>completed</status>") {
		t.Error("missing status")
	}
	if !contains(xml, `<summary>Background command &quot;test&quot; completed</summary>`) {
		t.Error("missing summary")
	}
	if contains(xml, "Last output:") {
		t.Error("completion should not have Last output")
	}
}

func TestTaskNotification_FormatXML_Stall(t *testing.T) {
	t.Parallel()
	n := TaskNotification{
		TaskID:    "bg-2",
		Status:    "",
		Summary:   `Background command "test" appears to be waiting for interactive input`,
		IsStall:   true,
		Tail:      "Continue? (y/n)",
	}
	xml := n.FormatXML()
	// Stall notifications have no <status> tag
	if contains(xml, "<status>") {
		t.Error("stall should not have <status> tag")
	}
	if !contains(xml, "Last output:") {
		t.Error("stall should have Last output")
	}
	if !contains(xml, "Continue? (y/n)") {
		t.Error("stall should include tail content")
	}
	if !contains(xml, "The command is likely blocked") {
		t.Error("stall should have instructions")
	}
}

func TestTaskNotification_FormatXML_NoToolUseID(t *testing.T) {
	t.Parallel()
	n := TaskNotification{
		TaskID:  "bg-3",
		Status:  "failed",
		Summary: `Background command "x" failed`,
	}
	xml := n.FormatXML()
	if contains(xml, "<tool-use-id>") {
		t.Error("should not have tool-use-id when empty")
	}
	if contains(xml, "<output-file>") {
		t.Error("should not have output-file when empty")
	}
}

func TestTaskNotification_FormatXML_StallNoTail(t *testing.T) {
	t.Parallel()
	n := TaskNotification{
		TaskID:  "bg-4",
		IsStall: true,
		Summary: "stalled",
	}
	xml := n.FormatXML()
	if contains(xml, "Last output:") {
		t.Error("stall with empty tail should not have Last output")
	}
}

func TestEscapeXML(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"a&b", "a&amp;b"},
		{"a<b", "a&lt;b"},
		{"a>b", "a&gt;b"},
		{`a"b`, "a&quot;b"},
		{"a'b", "a&apos;b"},
		{`<script>"x&y"</script>`, "&lt;script&gt;&quot;x&amp;y&quot;&lt;/script&gt;"},
	}
	for _, tt := range tests {
		if got := escapeXML(tt.input); got != tt.want {
			t.Errorf("escapeXML(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// RegisterForeground
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_RegisterForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	output := NewStreamingOutput(nil)
	task := r.RegisterForeground("echo hello", "desc", output)

	if task.ID != "bg-1" {
		t.Errorf("ID = %q, want bg-1", task.ID)
	}
	if task.Command != "echo hello" {
		t.Errorf("Command = %q, want echo hello", task.Command)
	}
	if task.Description != "desc" {
		t.Errorf("Description = %q, want desc", task.Description)
	}
	if task.IsBackgrounded {
		t.Error("foreground task should have IsBackgrounded=false")
	}
	if task.Kind != "bash" {
		t.Errorf("Kind = %q, want bash", task.Kind)
	}
	if task.Output != output {
		t.Error("Output should be the passed output")
	}
}

// ---------------------------------------------------------------------------
// Background
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_Background(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.RegisterForeground("cmd", "", nil)

	if !r.Background(task.ID) {
		t.Error("Background should succeed on foreground task")
	}
	if !task.IsBackgrounded {
		t.Error("task should be backgrounded")
	}
}

func TestBackgroundTaskRegistry_Background_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	if r.Background("nonexistent") {
		t.Error("Background on nonexistent should return false")
	}
}

func TestBackgroundTaskRegistry_Background_AlreadyBackgrounded(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.Spawn("cmd", 0, nil) // Spawn sets IsBackgrounded=true
	if r.Background(task.ID) {
		t.Error("Background on already-backgrounded task should return false")
	}
}

func TestBackgroundTaskRegistry_Background_CompletedTask(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.RegisterForeground("cmd", "", nil)
	task.Complete(0, false)
	if r.Background(task.ID) {
		t.Error("Background on completed task should return false")
	}
}

// ---------------------------------------------------------------------------
// BackgroundAll
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_BackgroundAll(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	t1 := r.RegisterForeground("cmd1", "", nil)
	t2 := r.RegisterForeground("cmd2", "", nil)
	_ = r.Spawn("cmd3", 0, nil) // already backgrounded, should be skipped

	transitioned := r.BackgroundAll()
	if len(transitioned) != 2 {
		t.Fatalf("BackgroundAll() = %d, want 2", len(transitioned))
	}
	if !t1.IsBackgrounded || !t2.IsBackgrounded {
		t.Error("foreground tasks should be backgrounded")
	}
}

func TestBackgroundTaskRegistry_BackgroundAll_NoForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.Spawn("cmd", 0, nil)
	transitioned := r.BackgroundAll()
	if len(transitioned) != 0 {
		t.Errorf("BackgroundAll() = %d, want 0", len(transitioned))
	}
}

// ---------------------------------------------------------------------------
// BackgroundExistingForegroundTask
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_BackgroundExistingForegroundTask(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.RegisterForeground("cmd", "", nil)
	if !r.BackgroundExistingForegroundTask(task.ID) {
		t.Error("should transition foreground task")
	}
	if !task.IsBackgrounded {
		t.Error("task should be backgrounded")
	}
}

// ---------------------------------------------------------------------------
// HasForegroundTasks
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_HasForegroundTasks_True(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.RegisterForeground("cmd", "", nil)
	if !r.HasForegroundTasks() {
		t.Error("should have foreground tasks")
	}
}

func TestBackgroundTaskRegistry_HasForegroundTasks_False(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.Spawn("cmd", 0, nil) // Spawn sets IsBackgrounded=true
	if r.HasForegroundTasks() {
		t.Error("should not have foreground tasks")
	}
}

func TestBackgroundTaskRegistry_HasForegroundTasks_CompletedNotCounted(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.RegisterForeground("cmd", "", nil)
	task.Complete(0, false)
	if r.HasForegroundTasks() {
		t.Error("completed foreground task should not count")
	}
}

// ---------------------------------------------------------------------------
// MarkNotified
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_MarkNotified(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.Spawn("cmd", 0, nil)
	if !r.MarkNotified(task.ID) {
		t.Error("first MarkNotified should return true")
	}
	if r.MarkNotified(task.ID) {
		t.Error("second MarkNotified should return false (already notified)")
	}
}

func TestBackgroundTaskRegistry_MarkNotified_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	if r.MarkNotified("nonexistent") {
		t.Error("MarkNotified on nonexistent should return false")
	}
}

// ---------------------------------------------------------------------------
// UnregisterForeground
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_UnregisterForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.RegisterForeground("cmd", "", nil)
	task.Complete(0, false)
	r.UnregisterForeground(task.ID)
	if _, ok := r.Get(task.ID); ok {
		t.Error("task should be unregistered")
	}
}

func TestBackgroundTaskRegistry_UnregisterForeground_BackgroundNotRemoved(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.Spawn("cmd", 0, nil) // IsBackgrounded=true
	r.UnregisterForeground(task.ID)
	if _, ok := r.Get(task.ID); !ok {
		t.Error("backgrounded task should NOT be removed by UnregisterForeground")
	}
}

func TestBackgroundTaskRegistry_UnregisterForeground_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.UnregisterForeground("nonexistent") // should not panic
}

// ---------------------------------------------------------------------------
// buildNotification / buildNotificationLocked
// ---------------------------------------------------------------------------

func TestBuildNotificationLocked_Completed(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		ID:          "bg-1",
		Command:     "echo hello",
		ToolUseID:   "tu-1",
		OutputPath:  "/tmp/out",
		ExitCode:    0,
	}
	n := task.buildNotificationLocked("completed")
	if n.TaskID != "bg-1" {
		t.Errorf("TaskID = %q, want bg-1", n.TaskID)
	}
	if n.Status != "completed" {
		t.Errorf("Status = %q, want completed", n.Status)
	}
	if n.OutputFile != "/tmp/out" {
		t.Errorf("OutputFile = %q, want /tmp/out", n.OutputFile)
	}
	wantSummary := `Background command "echo hello" completed (exit code 0)`
	if n.Summary != wantSummary {
		t.Errorf("Summary = %q, want %q", n.Summary, wantSummary)
	}
}

func TestBuildNotificationLocked_WithDescription(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		ID:          "bg-2",
		Command:     "echo hello",
		Description: "my task",
	}
	n := task.buildNotificationLocked("completed")
	if !contains(n.Summary, `"my task"`) {
		t.Errorf("Summary should use description, got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_FailedWithExitCode(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		ID:       "bg-3",
		Command:  "cmd",
		ExitCode: 1,
	}
	n := task.buildNotificationLocked("failed")
	if !contains(n.Summary, "failed with exit code 1") {
		t.Errorf("Summary should include 'failed with exit code 1', got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_KilledWithExitCode(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		ID:       "bg-4",
		Command:  "cmd",
		ExitCode: 137,
	}
	n := task.buildNotificationLocked("killed")
	if !contains(n.Summary, "was stopped") {
		t.Errorf("Summary should say 'was stopped' for killed, got %q", n.Summary)
	}
	if !contains(n.Summary, "137") {
		t.Errorf("Killed summary should include exit code 137, got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_UnknownStatus(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		ID:      "bg-5",
		Command: "cmd",
	}
	n := task.buildNotificationLocked("unknown")
	if !contains(n.Summary, "unknown") {
		t.Errorf("Summary should contain status, got %q", n.Summary)
	}
}

func TestBuildNotification_Registry(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	task := r.Spawn("cmd", 0, nil)
	n := r.buildNotification(task, "completed")
	if n == nil {
		t.Fatal("buildNotification returned nil")
	}
	if n.TaskID != task.ID {
		t.Errorf("TaskID = %q, want %q", n.TaskID, task.ID)
	}
}

// ---------------------------------------------------------------------------
// sendNotification
// ---------------------------------------------------------------------------

func TestSendNotification_Nil(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.sendNotification(nil) // should not panic
}

func TestSendNotification_NilCallback(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()
	r.sendNotification(&TaskNotification{}) // OnNotify is nil, should not panic
}

func TestSendNotification_Callback(t *testing.T) {
	t.Parallel()
	called := false
	r := NewBackgroundTaskRegistry()
	r.OnNotify = func(n TaskNotification) { called = true }
	r.sendNotification(&TaskNotification{TaskID: "bg-1"})
	if !called {
		t.Error("OnNotify should be called")
	}
}

// ---------------------------------------------------------------------------
// Complete — notification and output paths
// ---------------------------------------------------------------------------

func TestBackgroundTask_Complete_WithNotification(t *testing.T) {
	t.Parallel()
	var received *TaskNotification
	task := &BackgroundTask{
		Status:   TaskRunning,
		done:     make(chan struct{}),
		onNotify: func(n TaskNotification) { received = &n },
	}
	task.Complete(0, false)
	if received == nil {
		t.Fatal("should have sent notification")
	}
	if received.Status != "completed" {
		t.Errorf("Status = %q, want completed", received.Status)
	}
}

func TestBackgroundTask_Complete_AlreadyNotified(t *testing.T) {
	t.Parallel()
	called := false
	task := &BackgroundTask{
		Status:   TaskRunning,
		Notified: true,
		done:     make(chan struct{}),
		onNotify: func(n TaskNotification) { called = true },
	}
	task.Complete(0, false)
	if called {
		t.Error("should not send notification when already notified")
	}
}

func TestBackgroundTask_Complete_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Status:   TaskCompleted,
		done:     make(chan struct{}),
	}
	task.Complete(1, false)
	// Should not change status or close done again
	if task.ExitCode != 0 {
		t.Error("should not update exit code on already-terminal task")
	}
}

func TestBackgroundTask_Complete_WithOutput(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	_, _ = output.Write([]byte("hello"))
	task := &BackgroundTask{
		Status: TaskRunning,
		done:   make(chan struct{}),
		Output: output,
	}
	task.Complete(0, false)
	// Output.FinalUpdate should have been called — no panic is sufficient
}

func TestBackgroundTask_Complete_NilOnNotify(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Status:   TaskRunning,
		done:     make(chan struct{}),
		onNotify: nil,
	}
	task.Complete(0, false) // should not panic
}

func TestBackgroundTask_Complete_Interrupted(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Status: TaskRunning,
		done:   make(chan struct{}),
	}
	task.Complete(1, true)
	if !task.Interrupted {
		t.Error("Interrupted should be true")
	}
}

// ---------------------------------------------------------------------------
// startStallWatchdog
// ---------------------------------------------------------------------------

func TestBackgroundTask_StartStallWatchdog_NilOutput(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Output: nil,
		Kind:   "bash",
	}
	task.startStallWatchdog() // should not panic, no watchdog started
	if task.cancelStall != nil {
		t.Error("cancelStall should be nil when output is nil")
	}
}

func TestBackgroundTask_StartStallWatchdog_MonitorKind(t *testing.T) {
	t.Parallel()
	task := &BackgroundTask{
		Output: NewStreamingOutput(nil),
		Kind:   "monitor",
	}
	task.startStallWatchdog()
	if task.cancelStall != nil {
		t.Error("monitor kind should not start stall watchdog")
	}
}

func TestBackgroundTask_StartStallWatchdog_StartsWatchdog(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	task := &BackgroundTask{
		Output: output,
		Kind:   "bash",
	}
	task.startStallWatchdog()
	if task.cancelStall == nil {
		t.Fatal("cancelStall should be set when output is present")
	}
	// Cancel should not panic
	task.cancelStall()
}

func TestBackgroundTask_StartStallWatchdog_WithNotification(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	var received TaskNotification
	task := &BackgroundTask{
		Output:   output,
		Kind:     "bash",
		ID:       "bg-test",
		onNotify: func(n TaskNotification) { received = n },
	}
	task.startStallWatchdog()
	if task.cancelStall == nil {
		t.Fatal("cancelStall should be set")
	}
	task.cancelStall()
	_ = received
}

// ---------------------------------------------------------------------------
// IsTerminalTaskStatus
// ---------------------------------------------------------------------------

func TestIsTerminalTaskStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{TaskPending, false},
		{TaskRunning, false},
		{TaskCompleted, true},
		{TaskFailed, true},
		{TaskKilled, true},
	}
	for _, tt := range tests {
		if got := IsTerminalTaskStatus(tt.status); got != tt.want {
			t.Errorf("IsTerminalTaskStatus(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Bug fix: Kill() must set ExitCode to 137 (SIGKILL = 128+9)
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_Kill_SetsExitCode137(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("sleep 60", 12345, nil)

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if task.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137 (SIGKILL)", task.ExitCode)
	}
	if task.Status != TaskKilled {
		t.Errorf("Status = %q, want %q", task.Status, TaskKilled)
	}
	if !task.Interrupted {
		t.Error("Interrupted = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Bug fix: notification for killed task should include exit code
// ---------------------------------------------------------------------------

func TestBackgroundTaskRegistry_Kill_NotificationIncludesExitCode(t *testing.T) {
	t.Parallel()
	r := NewBackgroundTaskRegistry()

	var gotNotify TaskNotification
	r.OnNotify = func(n TaskNotification) {
		gotNotify = n
	}

	task := r.Spawn("sleep 60", 12345, nil)
	task.Description = "my long task"

	err := r.Kill(task.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	// Verify notification was sent
	if gotNotify.TaskID != task.ID {
		t.Errorf("Notification TaskID = %q, want %q", gotNotify.TaskID, task.ID)
	}
	if gotNotify.Status != "killed" {
		t.Errorf("Notification Status = %q, want killed", gotNotify.Status)
	}
	// Notification summary should mention exit code 137
	if !contains(gotNotify.Summary, "137") {
		t.Errorf("Notification Summary = %q, want mention of exit code 137", gotNotify.Summary)
	}
}

// ---------------------------------------------------------------------------
// Bug fix: adapter exposes correct exit code for killed task
// ---------------------------------------------------------------------------

func TestTaskInfoAdapter_KilledTask_ExitCode137(t *testing.T) {
	r := NewBackgroundTaskRegistry()

	task := r.Spawn("sleep 60", 12345, nil)
	_ = r.Kill(task.ID)

	adapter := NewTaskInfoAdapter(r)
	info, ok := adapter.Get(task.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", task.ID)
	}

	if info.ExitCode != 137 {
		t.Errorf("Adapter ExitCode = %d, want 137", info.ExitCode)
	}
	if info.Status != "killed" {
		t.Errorf("Adapter Status = %q, want killed", info.Status)
	}
}

// ---------------------------------------------------------------------------
// TDD: stderr should not be dropped when auto-backgrounding
// ---------------------------------------------------------------------------

func TestAutoBackground_StderrNotDropped(t *testing.T) {
	// When a non-PTY command auto-backgrounds, stderr must be captured in the
	// task's StreamingOutput, not silently lost.
	s := NewStreamingOutput(nil)
	cmd := "echo stderr_capture_test >&2; sleep 10"
	timeout := 100 * time.Millisecond

	result, err := executeNonPTYStreamingAutoBg(context.Background(), Input{Command: cmd}, "", timeout, s, DefaultRegistry())
	if err != nil {
		t.Fatalf("executeNonPTYStreamingAutoBg() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.BackgroundTaskID == "" {
		t.Fatal("expected BackgroundTaskID (command should have auto-backgrounded)")
	}

	// Wait for the task to complete via the global registry
	reg := DefaultRegistry()
	_, _ = reg.Wait(output.BackgroundTaskID)

	// Check that stderr content appears in the task output
	task, ok := reg.Get(output.BackgroundTaskID)
	if !ok {
		t.Fatal("task not found in registry")
	}
	taskOutput := task.Output.String()
	if !strings.Contains(taskOutput, "stderr_capture_test") {
		t.Errorf("stderr content missing from task output.\nTask output: %q", taskOutput)
	}
}

// helper
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
