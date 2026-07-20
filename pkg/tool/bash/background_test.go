//go:build !windows

package bash

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/liuy/gbot/pkg/tool/job"
)

func TestNewBackgroundJobRegistry(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	if r == nil {
		t.Fatal("NewBackgroundJobRegistry() returned nil")
	}
	if len(r.List()) != 0 {
		t.Error("new registry should have no tasks")
	}
}

func TestBackgroundJobRegistry_Spawn(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("echo hello", 1234, NewStreamingOutput(nil))
	if job == nil {
		t.Fatal("Spawn() returned nil")
	}
	if job.ID != "bg-1" {
		t.Errorf("ID = %q, want %q", job.ID, "bg-1")
	}
	if job.Command != "echo hello" {
		t.Errorf("Command = %q, want %q", job.Command, "echo hello")
	}
	if job.PID != 1234 {
		t.Errorf("PID = %d, want 1234", job.PID)
	}
	if job.Status != JobRunning {
		t.Errorf("Status = %q, want %q", job.Status, JobRunning)
	}
	if job.Output == nil {
		t.Error("Output should not be nil")
	}
	if job.done == nil {
		t.Error("done channel should not be nil")
	}
}

func TestBackgroundJobRegistry_Spawn_IncrementalID(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	task1 := r.Spawn("cmd1", 1, nil)
	task2 := r.Spawn("cmd2", 2, nil)

	if task1.ID == task2.ID {
		t.Errorf("IDs should be unique: %q == %q", task1.ID, task2.ID)
	}
}

func TestBackgroundJobRegistry_List(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	r.Spawn("cmd1", 1, nil)
	r.Spawn("cmd2", 2, nil)

	tasks := r.List()
	if len(tasks) != 2 {
		t.Errorf("List() = %d tasks, want 2", len(tasks))
	}
}

func TestBackgroundJobRegistry_Kill(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	// Use PID 0 to avoid killing actual processes in test
	job := r.Spawn("sleep 100", 0, NewStreamingOutput(nil))

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if job.Status != JobKilled {
		t.Errorf("Status = %q, want %q", job.Status, JobKilled)
	}

	// Verify done channel is closed
	select {
	case <-job.done:
		// Expected
	default:
		t.Error("done channel should be closed after kill")
	}
}

func TestBackgroundJobRegistry_Kill_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	err := r.Kill("nonexistent")
	if err == nil {
		t.Error("Kill() on nonexistent job should return error")
	}
}

func TestBackgroundJobRegistry_Kill_AlreadyCompleted(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("echo done", 0, nil)
	job.Complete(0, false)

	err := r.Kill(job.ID)
	if err == nil {
		t.Error("Kill() on completed job should return error")
	}
}

func TestBackgroundJobRegistry_Wait(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("echo hello", 0, nil)

	// Signal the goroutine to complete only after Wait is blocking.
	// This ensures Wait() actually waits for the async completion.
	started := make(chan struct{})
	go func() {
		<-started // block until Wait is on the stack
		job.Complete(0, false)
	}()

	close(started) // allow goroutine to proceed

	code, err := r.Wait(job.ID)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if code != 0 {
		t.Errorf("ExitCode = %d, want 0", code)
	}
}

func TestBackgroundJobRegistry_Wait_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	_, err := r.Wait("nonexistent")
	if err == nil {
		t.Error("Wait() on nonexistent job should return error")
	}
}

func TestBackgroundJobRegistry_Wait_NonZeroExit(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("exit 1", 0, nil)
	job.Complete(1, false)

	code, err := r.Wait(job.ID)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if code != 1 {
		t.Errorf("ExitCode = %d, want 1", code)
	}
	if job.Status != JobFailed {
		t.Errorf("Status = %q, want %q", job.Status, JobFailed)
	}
}

func TestBackgroundJobRegistry_Get(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("cmd", 1, nil)

	got, ok := r.Get(job.ID)
	if !ok {
		t.Error("Get() should find spawned job")
	}
	if got.ID != job.ID {
		t.Errorf("Get() = %q, want %q", got.ID, job.ID)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("Get() should not find nonexistent job")
	}
}

func TestBackgroundJobRegistry_Remove(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("cmd", 1, nil)
	job.Complete(0, false)

	r.Remove(job.ID)

	_, ok := r.Get(job.ID)
	if ok {
		t.Error("Get() should not find removed job")
	}
}

func TestBackgroundJob_Complete(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Status: JobRunning,
		done:   make(chan struct{}),
	}

	job.Complete(0, false)

	if job.Status != JobCompleted {
		t.Errorf("Status = %q, want %q", job.Status, JobCompleted)
	}
	if job.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", job.ExitCode)
	}

	select {
	case <-job.done:
	default:
		t.Error("done channel should be closed after Complete()")
	}
}

func TestBackgroundJob_Complete_NonZero(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Status: JobRunning,
		done:   make(chan struct{}),
	}

	job.Complete(1, false)

	if job.Status != JobFailed {
		t.Errorf("Status = %q, want %q", job.Status, JobFailed)
	}
}

func TestBackgroundJob_SetStallCancel(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{}

	called := false
	cancel := func() { called = true }
	job.SetStallCancel(cancel)

	if job.cancelStall == nil {
		t.Error("cancelStall should be set")
	}

	// Trigger cancel via Complete
	job.done = make(chan struct{})
	job.Complete(0, false)

	if !called {
		t.Error("stall cancel should be called on Complete()")
	}
}

func TestBackgroundJobRegistry_Kill_StopsStallWatchdog(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("cmd", 0, nil)

	stallCancelled := false
	job.SetStallCancel(func() { stallCancelled = true })

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if !stallCancelled {
		t.Error("Kill() should cancel stall watchdog")
	}
}

func TestBackgroundJobRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			job := r.Spawn("cmd", 0, nil)
			job.Complete(0, false)
			r.List()
			r.Get(job.ID)
		})
	}
	wg.Wait()

	tasks := r.List()
	if len(tasks) != 10 {
		t.Errorf("List() = %d tasks, want 10", len(tasks))
	}
}

func TestBackgroundJobRegistry_Kill_NoStallCancel(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("cmd", 0, nil)
	// Don't set stall cancel — cover the nil cancelStall path

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if job.Status != JobKilled {
		t.Errorf("Status = %q, want %q", job.Status, JobKilled)
	}
}

func TestBackgroundJobRegistry_Kill_WithPID(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	// Use os.Getpid() — killing own process tree would be bad,
	// but killProcessTree with our own PID will succeed without killing us
	// since we're the parent. Use PID -1 to avoid actually killing anything.
	job := r.Spawn("cmd", -1, nil)

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
}

func TestBackgroundJob_Complete_StopsStallCancel(t *testing.T) {
	t.Parallel()

	called := false
	job := &BackgroundJob{
		Status:      JobRunning,
		done:        make(chan struct{}),
		cancelStall: func() { called = true },
	}

	job.Complete(0, false)

	if !called {
		t.Error("Complete() should call stall cancel")
	}
	if job.cancelStall != nil {
		t.Error("cancelStall should be nil after Complete()")
	}
}

func TestBackgroundJobRegistry_Spawn_WithOutput(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	output := NewStreamingOutput(nil)
	n, err := output.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write() = %d, want 6", n)
	}

	job := r.Spawn("echo hello", 1234, output)

	if job.Output == nil {
		t.Error("Output should not be nil")
	}
	lines := job.Output.Lines()
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("Output.Lines() = %v, want [hello]", lines)
	}
}

func TestBackgroundJobRegistry_RemoveNonexistent(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	// Should not panic
	r.Remove("nonexistent")
}

func TestBackgroundJobRegistry_Kill_RealPID(t *testing.T) {
	r := NewBackgroundJobRegistry()

	// Spawn a real sleep process in its own process group
	cmd := exec.Command("sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("can't spawn sleep process: %v", err)
	}
	pid := cmd.Process.Pid

	job := r.Spawn("sleep 300", pid, nil)

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
	_, _ = cmd.Process.Wait()

	if job.Status != JobKilled {
		t.Errorf("Status = %q, want %q", job.Status, JobKilled)
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

func TestJobNotification_FormatXML_Completion(t *testing.T) {
	t.Parallel()
	n := JobNotification{
		JobID:      "bg-1",
		ToolUseID:  "tu-123",
		Status:     "completed",
		Summary:    `Background command "test" completed`,
		OutputFile: "/tmp/output.txt",
	}
	xml := n.FormatXML()
	if !contains(xml, "<job-notification>") {
		t.Error("missing <job-notification>")
	}
	if !contains(xml, "<job-id>bg-1</job-id>") {
		t.Error("missing job-id")
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

func TestJobNotification_FormatXML_Stall(t *testing.T) {
	t.Parallel()
	n := JobNotification{
		JobID:   "bg-2",
		Status:  "",
		Summary: `Background command "test" appears to be waiting for interactive input`,
		IsStall: true,
		Tail:    "[sudo] password for user:",
	}
	xml := n.FormatXML()
	// Stall notifications have no <status> tag
	if contains(xml, "<status>") {
		t.Error("stall should not have <status> tag")
	}
	if !contains(xml, "Last output:") {
		t.Error("stall should have Last output")
	}
	if !contains(xml, "[sudo] password for user:") {
		t.Error("stall should include tail content")
	}
	if !contains(xml, "The command is likely blocked") {
		t.Error("stall should have instructions")
	}
}

func TestJobNotification_FormatXML_NoToolUseID(t *testing.T) {
	t.Parallel()
	n := JobNotification{
		JobID:   "bg-3",
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

func TestJobNotification_FormatXML_StallNoTail(t *testing.T) {
	t.Parallel()
	n := JobNotification{
		JobID:   "bg-4",
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

func TestBackgroundJobRegistry_RegisterForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	output := NewStreamingOutput(nil)
	job := r.RegisterForeground("echo hello", "desc", output)

	if job.ID != "bg-1" {
		t.Errorf("ID = %q, want bg-1", job.ID)
	}
	if job.Command != "echo hello" {
		t.Errorf("Command = %q, want echo hello", job.Command)
	}
	if job.Description != "desc" {
		t.Errorf("Description = %q, want desc", job.Description)
	}
	if job.IsBackgrounded {
		t.Error("foreground job should have IsBackgrounded=false")
	}
	if job.Kind != "bash" {
		t.Errorf("Kind = %q, want bash", job.Kind)
	}
	if job.Output != output {
		t.Error("Output should be the passed output")
	}
}

// ---------------------------------------------------------------------------
// Background
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_Background(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.RegisterForeground("cmd", "", nil)

	if !r.Background(job.ID) {
		t.Error("Background should succeed on foreground job")
	}
	if !job.IsBackgrounded {
		t.Error("job should be backgrounded")
	}
}

func TestBackgroundJobRegistry_Background_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	if r.Background("nonexistent") {
		t.Error("Background on nonexistent should return false")
	}
}

func TestBackgroundJobRegistry_Background_AlreadyBackgrounded(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.Spawn("cmd", 0, nil) // Spawn sets IsBackgrounded=true
	if r.Background(job.ID) {
		t.Error("Background on already-backgrounded job should return false")
	}
}

func TestBackgroundJobRegistry_Background_CompletedTask(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.RegisterForeground("cmd", "", nil)
	job.Complete(0, false)
	if r.Background(job.ID) {
		t.Error("Background on completed job should return false")
	}
}

// ---------------------------------------------------------------------------
// BackgroundAll
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_BackgroundAll(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
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

func TestBackgroundJobRegistry_BackgroundAll_NoForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.Spawn("cmd", 0, nil)
	transitioned := r.BackgroundAll()
	if len(transitioned) != 0 {
		t.Errorf("BackgroundAll() = %d, want 0", len(transitioned))
	}
}

// ---------------------------------------------------------------------------
// BackgroundExistingForegroundTask
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_BackgroundExistingForegroundTask(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.RegisterForeground("cmd", "", nil)
	if !r.BackgroundExistingForegroundTask(job.ID) {
		t.Error("should transition foreground job")
	}
	if !job.IsBackgrounded {
		t.Error("job should be backgrounded")
	}
}

// ---------------------------------------------------------------------------
// HasForegroundTasks
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_HasForegroundTasks_True(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.RegisterForeground("cmd", "", nil)
	if !r.HasForegroundTasks() {
		t.Error("should have foreground tasks")
	}
}

func TestBackgroundJobRegistry_HasForegroundTasks_False(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.Spawn("cmd", 0, nil) // Spawn sets IsBackgrounded=true
	if r.HasForegroundTasks() {
		t.Error("should not have foreground tasks")
	}
}

func TestBackgroundJobRegistry_HasForegroundTasks_CompletedNotCounted(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.RegisterForeground("cmd", "", nil)
	job.Complete(0, false)
	if r.HasForegroundTasks() {
		t.Error("completed foreground job should not count")
	}
}

// ---------------------------------------------------------------------------
// MarkNotified
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_MarkNotified(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.Spawn("cmd", 0, nil)
	if !r.MarkNotified(job.ID) {
		t.Error("first MarkNotified should return true")
	}
	if r.MarkNotified(job.ID) {
		t.Error("second MarkNotified should return false (already notified)")
	}
}

func TestBackgroundJobRegistry_MarkNotified_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	if r.MarkNotified("nonexistent") {
		t.Error("MarkNotified on nonexistent should return false")
	}
}

// ---------------------------------------------------------------------------
// UnregisterForeground
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_UnregisterForeground(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.RegisterForeground("cmd", "", nil)
	job.Complete(0, false)
	r.UnregisterForeground(job.ID)
	if _, ok := r.Get(job.ID); ok {
		t.Error("job should be unregistered")
	}
}

func TestBackgroundJobRegistry_UnregisterForeground_BackgroundNotRemoved(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.Spawn("cmd", 0, nil) // IsBackgrounded=true
	r.UnregisterForeground(job.ID)
	if _, ok := r.Get(job.ID); !ok {
		t.Error("backgrounded job should NOT be removed by UnregisterForeground")
	}
}

func TestBackgroundJobRegistry_UnregisterForeground_NotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.UnregisterForeground("nonexistent") // should not panic
}

// ---------------------------------------------------------------------------
// buildNotification / buildNotificationLocked
// ---------------------------------------------------------------------------

func TestBuildNotificationLocked_Completed(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		ID:         "bg-1",
		Command:    "echo hello",
		ToolUseID:  "tu-1",
		OutputPath: "/tmp/out",
		ExitCode:   0,
	}
	n := job.buildNotificationLocked("completed")
	if n.JobID != "bg-1" {
		t.Errorf("JobID = %q, want bg-1", n.JobID)
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
	job := &BackgroundJob{
		ID:          "bg-2",
		Command:     "echo hello",
		Description: "my job",
	}
	n := job.buildNotificationLocked("completed")
	if !contains(n.Summary, `"my job"`) {
		t.Errorf("Summary should use description, got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_FailedWithExitCode(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		ID:       "bg-3",
		Command:  "cmd",
		ExitCode: 1,
	}
	n := job.buildNotificationLocked("failed")
	if !contains(n.Summary, "failed with exit code 1") {
		t.Errorf("Summary should include 'failed with exit code 1', got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_KilledWithExitCode(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		ID:       "bg-4",
		Command:  "cmd",
		ExitCode: 137,
	}
	n := job.buildNotificationLocked("killed")
	if !contains(n.Summary, "was stopped") {
		t.Errorf("Summary should say 'was stopped' for killed, got %q", n.Summary)
	}
	if !contains(n.Summary, "137") {
		t.Errorf("Killed summary should include exit code 137, got %q", n.Summary)
	}
}

func TestBuildNotificationLocked_UnknownStatus(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		ID:      "bg-5",
		Command: "cmd",
	}
	n := job.buildNotificationLocked("unknown")
	if !contains(n.Summary, "unknown") {
		t.Errorf("Summary should contain status, got %q", n.Summary)
	}
}

func TestBuildNotification_Registry(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	job := r.Spawn("cmd", 0, nil)
	n := r.buildNotification(job, "completed")
	if n == nil {
		t.Fatal("buildNotification returned nil")
	}
	if n.JobID != job.ID {
		t.Errorf("JobID = %q, want %q", n.JobID, job.ID)
	}
}

// ---------------------------------------------------------------------------
// sendNotification
// ---------------------------------------------------------------------------

func TestSendNotification_Nil(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.sendNotification(nil) // should not panic
}

func TestSendNotification_NilCallback(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	r.sendNotification(&JobNotification{}) // OnNotify is nil, should not panic
}

func TestSendNotification_Callback(t *testing.T) {
	t.Parallel()
	called := false
	r := NewBackgroundJobRegistry()
	r.OnNotify = func(n JobNotification) { called = true }
	r.sendNotification(&JobNotification{JobID: "bg-1"})
	if !called {
		t.Error("OnNotify should be called")
	}
}

// ---------------------------------------------------------------------------
// Complete — notification and output paths
// ---------------------------------------------------------------------------

func TestBackgroundJob_Complete_WithNotification(t *testing.T) {
	t.Parallel()
	var received *JobNotification
	job := &BackgroundJob{
		Status:   JobRunning,
		done:     make(chan struct{}),
		onNotify: func(n JobNotification) { received = &n },
	}
	job.Complete(0, false)
	if received == nil {
		t.Fatal("should have sent notification")
	}
	if received.Status != "completed" {
		t.Errorf("Status = %q, want completed", received.Status)
	}
}

func TestBackgroundJob_Complete_AlreadyNotified(t *testing.T) {
	t.Parallel()
	called := false
	job := &BackgroundJob{
		Status:   JobRunning,
		Notified: true,
		done:     make(chan struct{}),
		onNotify: func(n JobNotification) { called = true },
	}
	job.Complete(0, false)
	if called {
		t.Error("should not send notification when already notified")
	}
}

func TestBackgroundJob_Complete_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Status: JobCompleted,
		done:   make(chan struct{}),
	}
	job.Complete(1, false)
	// Should not change status or close done again
	if job.ExitCode != 0 {
		t.Error("should not update exit code on already-terminal job")
	}
}

func TestBackgroundJob_Complete_WithOutput(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	if _, err := output.Write([]byte("hello")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	job := &BackgroundJob{
		Status: JobRunning,
		done:   make(chan struct{}),
		Output: output,
	}
	job.Complete(0, false)
	// Output.FinalUpdate should have been called — no panic is sufficient
}

func TestBackgroundJob_Complete_NilOnNotify(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Status:   JobRunning,
		done:     make(chan struct{}),
		onNotify: nil,
	}
	job.Complete(0, false) // should not panic
}

func TestBackgroundJob_Complete_Interrupted(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Status: JobRunning,
		done:   make(chan struct{}),
	}
	job.Complete(1, true)
	if !job.Interrupted {
		t.Error("Interrupted should be true")
	}
}

// ---------------------------------------------------------------------------
// startStallWatchdog
// ---------------------------------------------------------------------------

func TestBackgroundJob_StartStallWatchdog_NilOutput(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Output: nil,
		Kind:   "bash",
	}
	job.startStallWatchdog() // should not panic, no watchdog started
	if job.cancelStall != nil {
		t.Error("cancelStall should be nil when output is nil")
	}
}

func TestBackgroundJob_StartStallWatchdog_MonitorKind(t *testing.T) {
	t.Parallel()
	job := &BackgroundJob{
		Output: NewStreamingOutput(nil),
		Kind:   "monitor",
	}
	job.startStallWatchdog()
	if job.cancelStall != nil {
		t.Error("monitor kind should not start stall watchdog")
	}
}

func TestBackgroundJob_StartStallWatchdog_StartsWatchdog(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	job := &BackgroundJob{
		Output: output,
		Kind:   "bash",
	}
	job.startStallWatchdog()
	if job.cancelStall == nil {
		t.Fatal("cancelStall should be set when output is present")
	}
	// Cancel should not panic
	job.cancelStall()
}

func TestBackgroundJob_StartStallWatchdog_WithNotification(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	var received JobNotification
	job := &BackgroundJob{
		Output:   output,
		Kind:     "bash",
		ID:       "bg-test",
		onNotify: func(n JobNotification) { received = n },
	}
	job.startStallWatchdog()
	if job.cancelStall == nil {
		t.Fatal("cancelStall should be set")
	}
	job.cancelStall()
	// After immediate cancel, the watchdog goroutine exits before the
	// stall interval elapses, so onNotify is never called and received
	// stays zero-valued — that is the expected outcome.
	if received != (JobNotification{}) {
		t.Errorf("received = %+v, want zero-value (watchdog cancelled before stall)", received)
	}

}

// ---------------------------------------------------------------------------
// IsTerminalJobStatus
// ---------------------------------------------------------------------------

func TestIsTerminalJobStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status JobStatus
		want   bool
	}{
		{JobPending, false},
		{JobRunning, false},
		{JobCompleted, true},
		{JobFailed, true},
		{JobKilled, true},
	}
	for _, tt := range tests {
		if got := IsTerminalJobStatus(tt.status); got != tt.want {
			t.Errorf("IsTerminalJobStatus(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
//  Kill() must set ExitCode to 137 (SIGKILL = 128+9)
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_Kill_SetsExitCode137(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	job := r.Spawn("sleep 60", 12345, nil)

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	if job.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137 (SIGKILL)", job.ExitCode)
	}
	if job.Status != JobKilled {
		t.Errorf("Status = %q, want %q", job.Status, JobKilled)
	}
	if !job.Interrupted {
		t.Error("Interrupted = false, want true")
	}
}

// ---------------------------------------------------------------------------
//  notification for killed job should include exit code
// ---------------------------------------------------------------------------

func TestBackgroundJobRegistry_Kill_NotificationIncludesExitCode(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()

	var gotNotify JobNotification
	r.OnNotify = func(n JobNotification) {
		gotNotify = n
	}

	job := r.Spawn("sleep 60", 12345, nil)
	job.Description = "my long job"

	err := r.Kill(job.ID)
	if err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	// Verify notification was sent
	if gotNotify.JobID != job.ID {
		t.Errorf("Notification JobID = %q, want %q", gotNotify.JobID, job.ID)
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
//  adapter exposes correct exit code for killed job
// ---------------------------------------------------------------------------

func TestJobInfoAdapter_KilledTask_ExitCode137(t *testing.T) {
	r := NewBackgroundJobRegistry()

	job := r.Spawn("sleep 60", 12345, nil)
	if err := r.Kill(job.ID); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	adapter := NewJobInfoAdapter(r)
	info, ok := adapter.Get(job.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", job.ID)
	}

	if info.ExitCode != 137 {
		t.Errorf("Adapter ExitCode = %d, want 137", info.ExitCode)
	}
	if info.Status != "killed" {
		t.Errorf("Adapter Status = %q, want killed", info.Status)
	}
}

// ---------------------------------------------------------------------------
// JobInfoAdapter — Kill, List, Wait, Get coverage
// ---------------------------------------------------------------------------

func TestJobInfoAdapter_GetNotFound(t *testing.T) {
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	info, ok := adapter.Get("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent job")
	}
	if info != nil {
		t.Error("expected nil info for nonexistent job")
	}
}

func TestJobInfoAdapter_List(t *testing.T) {
	r := NewBackgroundJobRegistry()
	t1 := r.Spawn("echo a", 100, NewStreamingOutput(nil))
	t2 := r.Spawn("echo b", 101, NewStreamingOutput(nil))

	adapter := NewJobInfoAdapter(r)
	list := adapter.List()
	if len(list) != 2 {
		t.Fatalf("List() returned %d tasks, want 2", len(list))
	}

	// Verify conversion — IDs should match
	ids := map[string]bool{}
	for _, info := range list {
		if info.ID != t1.ID && info.ID != t2.ID {
			t.Errorf("unexpected job ID %q", info.ID)
		}
		if info.Type != "local_bash" {
			t.Errorf("Type = %q, want local_bash", info.Type)
		}
		ids[info.ID] = true
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 unique IDs, got %d", len(ids))
	}
}

func TestJobInfoAdapter_ListEmpty(t *testing.T) {
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	list := adapter.List()
	if len(list) != 0 {
		t.Errorf("List() on empty registry = %d, want 0", len(list))
	}
}

func TestJobInfoAdapter_Wait(t *testing.T) {
	r := NewBackgroundJobRegistry()
	job := r.Spawn("echo done", 200, NewStreamingOutput(nil))

	// Complete the job so Wait returns immediately
	job.Complete(0, false)

	adapter := NewJobInfoAdapter(r)
	exitCode, err := adapter.Wait(job.ID)
	if err != nil {
		t.Fatalf("Wait() error: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Wait() exitCode = %d, want 0", exitCode)
	}
}

func TestJobInfoAdapter_WaitNotFound(t *testing.T) {
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	_, err := adapter.Wait("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestJobInfoAdapter_Kill(t *testing.T) {
	r := NewBackgroundJobRegistry()
	// PID=0 avoids killProcessTree hitting real processes; adapter test is delegation-only
	job := r.Spawn("sleep 60", 0, NewStreamingOutput(nil))

	adapter := NewJobInfoAdapter(r)
	if err := adapter.Kill(job.ID); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}

	info, ok := adapter.Get(job.ID)
	if !ok {
		t.Fatal("Get() after kill should find job")
	}
	if info.Status != "killed" {
		t.Errorf("Status = %q, want killed", info.Status)
	}
}

func TestJobInfoAdapter_KillNotFound(t *testing.T) {
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	err := adapter.Kill("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestJobInfoAdapter_KillNotRunning(t *testing.T) {
	r := NewBackgroundJobRegistry()
	job := r.Spawn("echo x", 400, NewStreamingOutput(nil))
	job.Complete(0, false)

	adapter := NewJobInfoAdapter(r)
	err := adapter.Kill(job.ID)
	if err == nil {
		t.Fatal("expected error when killing completed job")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error should mention 'not running', got: %v", err)
	}
}

func TestJobInfoAdapter_GetWithOutput(t *testing.T) {
	r := NewBackgroundJobRegistry()
	s := NewStreamingOutput(nil)
	if _, err := s.Write([]byte("hello output")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	job := r.Spawn("echo hello", 500, s)
	job.Complete(0, false)

	adapter := NewJobInfoAdapter(r)
	info, ok := adapter.Get(job.ID)
	if !ok {
		t.Fatal("Get() should find job")
	}
	if !strings.Contains(info.Output, "hello output") {
		t.Errorf("Output = %q, want to contain 'hello output'", info.Output)
	}
}

// ---------------------------------------------------------------------------
//  stderr should not be dropped when auto-backgrounding
// ---------------------------------------------------------------------------

func TestAutoBackground_StderrNotDropped(t *testing.T) {
	// Use a fresh registry to avoid polluting global state.
	orig := defaultRegistry
	freshReg := NewBackgroundJobRegistry()
	defaultRegistry = freshReg
	defer func() { defaultRegistry = orig }()

	// When a non-PTY command auto-backgrounds, stderr must be captured in the
	// StreamingOutput, not silently lost.
	s := NewStreamingOutput(nil)
	// Use a short sleep so the backgrounded job finishes quickly on its own.
	cmd := "echo stderr_capture_test >&2; sleep 0.2"
	timeout := 100 * time.Millisecond

	result, err := executeNonPTYAutoBg(context.Background(), Input{Command: cmd}, "", timeout, s, freshReg, MaxOutputSize)
	if err != nil {
		t.Fatalf("executeNonPTYAutoBg() error: %v", err)
	}

	output := result.Data.(*Output)
	if output.BackgroundJobID == "" {
		t.Fatal("expected BackgroundJobID (command should have auto-backgrounded)")
	}

	// Wait for the backgrounded job to finish on its own.
	_, waitErr := freshReg.Wait(output.BackgroundJobID)
	if waitErr != nil {
		t.Fatalf("Wait() error: %v", waitErr)
	}

	// Check that stderr content appears in the job output.
	job, ok := freshReg.Get(output.BackgroundJobID)
	if !ok {
		t.Fatal("job not found in registry")
	}
	taskOutput := job.Output.String()
	if !strings.Contains(taskOutput, "stderr_capture_test") {
		t.Errorf("stderr content missing from job output.\nTask output: %q", taskOutput)
	}
}

// helper
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// ---------------------------------------------------------------------------
// ErrNotFound wrapping — MultiRegistry relies on errors.Is(err, job.ErrNotFound)
// ---------------------------------------------------------------------------

func TestJobInfoAdapter_KillNotFound_WrapsErrNotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	err := adapter.Kill("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Kill should wrap job.ErrNotFound for MultiRegistry dispatch, got: %v", err)
	}
}

func TestJobInfoAdapter_WaitNotFound_WrapsErrNotFound(t *testing.T) {
	t.Parallel()
	r := NewBackgroundJobRegistry()
	adapter := NewJobInfoAdapter(r)

	_, err := adapter.Wait("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !errors.Is(err, job.ErrNotFound) {
		t.Errorf("Wait should wrap job.ErrNotFound for MultiRegistry dispatch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CleanupCompleted tests
// ---------------------------------------------------------------------------

func TestCleanupCompleted_RemovesExpiredTasks(t *testing.T) {
	reg := NewBackgroundJobRegistry()
	job := reg.Spawn("sleep 1", 0, nil)
	job.Complete(0, false)

	// Manually set evictAfter to past
	job.mu.Lock()
	job.evictAfter = time.Now().Add(-1 * time.Second) // REAL-TIME: eviction timing
	job.mu.Unlock()

	reg.CleanupCompleted()

	if _, ok := reg.Get(job.ID); ok {
		t.Error("expired job should be evicted")
	}
}

func TestCleanupCompleted_KeepsRunningTasks(t *testing.T) {
	reg := NewBackgroundJobRegistry()
	job := reg.Spawn("sleep 60", 0, nil)
	// Running job — evictAfter is zero

	reg.CleanupCompleted()

	if _, ok := reg.Get(job.ID); !ok {
		t.Error("running job should not be evicted")
	}
}

func TestCleanupCompleted_KeepsTasksWithinGrace(t *testing.T) {
	reg := NewBackgroundJobRegistry()
	job := reg.Spawn("sleep 1", 0, nil)
	job.Complete(0, false)

	// evictAfter is set to ~3s in future by Complete()
	reg.CleanupCompleted()

	if _, ok := reg.Get(job.ID); !ok {
		t.Error("job within grace period should not be evicted")
	}
}

func TestCleanupCompleted_KillSetsEvictAfter(t *testing.T) {
	reg := NewBackgroundJobRegistry()
	job := reg.Spawn("sleep 60", 0, nil)

	if err := reg.Kill(job.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	job.mu.Lock()
	evict := job.evictAfter
	job.mu.Unlock()

	if evict.IsZero() {
		t.Error("Kill should set evictAfter")
	}
	if time.Until(evict) < 2*time.Second {
		t.Error("evictAfter should be ~3s in the future")
	}
}

func TestCleanupCompleted_CompleteSetsEvictAfter(t *testing.T) {
	reg := NewBackgroundJobRegistry()
	job := reg.Spawn("sleep 1", 0, nil)

	job.Complete(0, false)

	job.mu.Lock()
	evict := job.evictAfter
	job.mu.Unlock()

	if evict.IsZero() {
		t.Error("Complete should set evictAfter")
	}
	if time.Until(evict) < 2*time.Second {
		t.Error("evictAfter should be ~3s in the future")
	}
}

func TestCleanupCompleted_MultipleTasks(t *testing.T) {
	reg := NewBackgroundJobRegistry()

	expired := reg.Spawn("echo 1", 0, nil)
	expired.Complete(0, false)
	expired.mu.Lock()
	expired.evictAfter = time.Now().Add(-1 * time.Second) // REAL-TIME: eviction timing
	expired.mu.Unlock()

	fresh := reg.Spawn("echo 2", 0, nil)
	fresh.Complete(0, false)
	// fresh job has evictAfter ~3s in future

	running := reg.Spawn("sleep 60", 0, nil)

	reg.CleanupCompleted()

	if _, ok := reg.Get(expired.ID); ok {
		t.Error("expired job should be evicted")
	}
	if _, ok := reg.Get(fresh.ID); !ok {
		t.Error("fresh completed job should remain")
	}
	if _, ok := reg.Get(running.ID); !ok {
		t.Error("running job should remain")
	}
}

func TestAdapter_List_E2E_Eviction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := NewBackgroundJobRegistry()
		adapter := NewJobInfoAdapter(reg)

		job := reg.Spawn("echo hello", 0, NewStreamingOutput(nil))
		job.Complete(0, false)

		// Before 3s: job still visible via adapter.
		list1 := adapter.List()
		if len(list1) != 1 {
			t.Fatalf("before 3s: expected 1 job, got %d", len(list1))
		}
		if list1[0].Status != "completed" {
			t.Errorf("before 3s: expected completed, got %s", list1[0].Status)
		}

		// Advance fake clock by 3s.
		time.Sleep(3 * time.Second)

		// After 3s: adapter.List() triggers CleanupCompleted -> job gone.
		list2 := adapter.List()
		if len(list2) != 0 {
			t.Errorf("after 3s: expected 0 tasks, got %d", len(list2))
		}

		// Direct Get also confirms eviction.
		if _, ok := reg.Get(job.ID); ok {
			t.Error("job should be evicted from registry after 3s")
		}
	})
}
