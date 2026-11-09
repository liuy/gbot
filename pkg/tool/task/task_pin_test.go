package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pin tests for the task.go mutation sweep; each assertion is exact so a
// single changed byte flips the test. Mutant line:mutator pairs are in the
// campaign ledger.

func pinWriteTaskFile(t *testing.T, dir, name string, task Task) {
	t.Helper()
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPinTask_WrappedErrors(t *testing.T) {
	// No t.Parallel: t.Setenv.
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}

	// TasksDir wraps the home-dir error (%w, so Unwrap is non-nil).
	t.Setenv("HOME", "")
	_, err := TasksDir("s")
	if err == nil || !strings.HasPrefix(err.Error(), "get home dir:") || errors.Unwrap(err) == nil {
		t.Errorf("TasksDir err = %v, want wrapped home error", err)
	}
}

func TestPinTask_InitWrapsMkdirError(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	blocker := filepath.Join(parent, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewList(filepath.Join(blocker, "sub"))
	err := l.Init()
	if err == nil || !strings.HasPrefix(err.Error(), "create tasks dir:") || errors.Unwrap(err) == nil {
		t.Errorf("Init err = %v, want wrapped mkdir error", err)
	}
}

func TestPinTask_CreateTaskWriteWraps(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	l := NewList(dir)

	// Unreadable directory fails the highest-ID scan with a wrapped error.
	ro := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
	if err := os.Chmod(ro, 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := NewList(ro).CreateTask("s", "d", "", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "find highest task ID:") || errors.Unwrap(err) == nil {
		t.Errorf("unreadable dir CreateTask err = %v, want wrapped scan error", err)
	}

	// Readable but unwritable directory fails at the task-file write; the
	// inner temp-write error stays wrapped.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	_, err = l.CreateTask("s", "d", "", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "write task file:") {
		t.Fatalf("unwritable dir CreateTask err = %v, want write-task-file prefix", err)
	}
	if errors.Unwrap(errors.Unwrap(err)) == nil {
		t.Errorf("write error chain lacks nested %%w: %v", err)
	}
	_ = os.Chmod(dir, 0o755)

	// A directory occupying the target path makes the rename fail.
	if err := os.Mkdir(filepath.Join(dir, "1.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = l.CreateTask("s", "d", "", nil)
	if err == nil || !strings.Contains(err.Error(), "rename temp to target") {
		t.Errorf("dir-blocked rename err = %v, want rename failure", err)
	}
	if _, ok := errors.AsType[*os.LinkError](errors.Unwrap(errors.Unwrap(err))); !ok {
		t.Errorf("rename error chain lacks *os.LinkError: %v", err)
	}
}

func TestPinTask_GetTaskWraps(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	l := NewList(dir)
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, id+".json")
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	_, err = l.GetTask(id)
	if err == nil || !strings.HasPrefix(err.Error(), "read task "+id+":") || errors.Unwrap(err) == nil {
		t.Errorf("unreadable GetTask err = %v, want wrapped read error", err)
	}
	_ = os.Chmod(p, 0o644)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = l.GetTask(id)
	if err == nil || !strings.HasPrefix(err.Error(), "parse task "+id+":") || errors.Unwrap(err) == nil {
		t.Errorf("corrupt GetTask err = %v, want wrapped parse error", err)
	}
}

func TestPinTask_UpdateWrapsAndGuards(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	l := NewList(dir)
	id, err := l.CreateTask("s", "d", "af", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Same-value updates must not report changed fields.
	same, fields, err := l.UpdateTask(id, TaskUpdates{
		Subject:     new("s"),
		Description: new("d"),
		ActiveForm:  new("af"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 0 {
		t.Errorf("same-value update fields = %v, want none", fields)
	}
	if same.Subject != "s" || same.Description != "d" || same.ActiveForm != "af" {
		t.Errorf("same-value update task = %+v", *same)
	}

	// Adding a block to a nonexistent task must not report addBlocks, and a
	// nonexistent blocker must not report addBlockedBy.
	_, fields, err = l.UpdateTask(id, TaskUpdates{AddBlocks: []string{"999"}, AddBlockedBy: []string{"998"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		if f == "addBlocks" || f == "addBlockedBy" {
			t.Errorf("block field %q reported for nonexistent counterpart: %v", f, fields)
		}
	}

	// A valid AddBlockedBy counterpart must report addBlockedBy.
	if _, err := l.CreateTask("second", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	_, fields, err = l.UpdateTask(id, TaskUpdates{AddBlockedBy: []string{"2"}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fields {
		if f == "addBlockedBy" {
			found = true
		}
	}
	if !found {
		t.Errorf("valid AddBlockedBy not reported: %v", fields)
	}

	// Write failure wraps.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	_, _, err = l.UpdateTask(id, TaskUpdates{Subject: new("changed")})
	if err == nil || !strings.HasPrefix(err.Error(), "write task "+id+":") || errors.Unwrap(errors.Unwrap(err)) == nil {
		t.Errorf("unwritable UpdateTask err = %v, want wrapped write error", err)
	}
	_ = os.Chmod(dir, 0o755)
}

//go:fix inline

func TestPinTask_CleanupSemantics(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("chmod-based permission tests are void as root")
	}
	dir := t.TempDir()
	l := NewList(dir)
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	completed := StatusCompleted
	if _, _, err := l.UpdateTask(id, TaskUpdates{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	// Drop the mark so cleanup's own high-water rewrite is the only one left.
	if err := os.Remove(filepath.Join(dir, ".highwatermark")); err != nil {
		t.Fatal(err)
	}
	// A directory named N.json must survive cleanup.
	if err := os.Mkdir(filepath.Join(dir, "5.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.CleanupCompleted(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); !os.IsNotExist(err) {
		t.Error("completed task file not deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "5.json")); err != nil {
		t.Errorf("directory named 5.json removed by cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".highwatermark")); err != nil {
		t.Errorf("high water mark removed by cleanup: %v", err)
	}
	// The pre-delete high water mark write keeps IDs monotonic.
	next, err := l.CreateTask("s2", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != "2" {
		t.Errorf("post-cleanup task ID = %q, want 2", next)
	}

	// Removed directory cleans up as a no-op, not an error.
	if err := NewList(filepath.Join(dir, "gone")).CleanupCompleted(); err != nil {
		t.Errorf("cleanup on missing dir err = %v, want nil", err)
	}

}

func TestPinTask_DeleteHighWaterMark(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(dir)
	// A task file above the water mark (e.g. restored from backup) must
	// raise the mark on delete so its ID is never reused. A junk entry
	// sorts before it and must not stop the scan.
	if err := os.WriteFile(filepath.Join(dir, "-junk.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinWriteTaskFile(t, dir, "9.json", Task{ID: "9", Subject: "s", Status: StatusPending})
	if deleted, err := l.DeleteTask("9"); err != nil || !deleted {
		t.Fatalf("delete 9: %v %v", deleted, err)
	}
	next, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next != "10" {
		t.Errorf("next ID after delete = %q, want 10", next)
	}

	// The scan must pass junk entries that sort before real task files.
	dirJ := t.TempDir()
	lJ := NewList(dirJ)
	// The water mark file sorts before every task file and is filtered by
	// the extension check; it must not stop the scan.
	if err := lJ.writeHighWaterMark(3); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirJ, "-junk.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinWriteTaskFile(t, dirJ, "5.json", Task{ID: "5", Subject: "s", Status: StatusPending})
	idJ, err := lJ.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if idJ != "6" {
		t.Errorf("next ID after junk+5.json = %q, want 6", idJ)
	}

	// The file scan keeps the maximum (not the last) ID.
	dir2 := t.TempDir()
	l2 := NewList(dir2)
	pinWriteTaskFile(t, dir2, "5.json", Task{ID: "5", Subject: "s", Status: StatusPending})
	pinWriteTaskFile(t, dir2, "3.json", Task{ID: "3", Subject: "s", Status: StatusPending})
	id, err := l2.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "6" {
		t.Errorf("next ID with 5+3 on disk = %q, want 6", id)
	}

	// Directories named N.json and extension-less numeric files must not
	// count as task IDs.
	dir4 := t.TempDir()
	l4 := NewList(dir4)
	if err := os.Mkdir(filepath.Join(dir4, "9.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir4, "7"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id4, err := l4.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id4 != "1" {
		t.Errorf("next ID with dir 9.json and file 7 = %q, want 1", id4)
	}

	// maxInt (files vs mark) picks the larger: mark 7 beats file 3.
	dir3 := t.TempDir()
	l3 := NewList(dir3)
	pinWriteTaskFile(t, dir3, "3.json", Task{ID: "3", Subject: "s", Status: StatusPending})
	if err := l3.writeHighWaterMark(7); err != nil {
		t.Fatal(err)
	}
	id, err = l3.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "8" {
		t.Errorf("next ID with mark 7 file 3 = %q, want 8", id)
	}

	// Unwritable dir wraps the delete error.
	if os.Getuid() != 0 {
		ro := t.TempDir()
		roList := NewList(ro)
		roID, err := roList.CreateTask("s", "d", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })
		if err := os.Chmod(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		if _, err := roList.DeleteTask(roID); err == nil || !strings.HasPrefix(err.Error(), "delete task "+roID+":") || errors.Unwrap(err) == nil {
			t.Errorf("unwritable DeleteTask err = %v, want wrapped delete error", err)
		}
		_ = os.Chmod(ro, 0o755)
	}
}

func TestPinTask_FirstTaskIDOne(t *testing.T) {
	t.Parallel()
	l := NewList(t.TempDir())
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "1" {
		t.Errorf("first task ID = %q, want 1", id)
	}
}

func TestPinTask_CascadeBothDirections(t *testing.T) {
	t.Parallel()
	// blockedBy side: 2.BlockedBy=[1], delete 1.
	dir := t.TempDir()
	l := NewList(dir)
	if _, err := l.CreateTask("a", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CreateTask("b", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if !l.BlockTask("1", "2") {
		t.Fatal("BlockTask(1,2) failed")
	}
	if _, err := l.DeleteTask("1"); err != nil {
		t.Fatal(err)
	}
	t2, err := l.GetTask("2")
	if err != nil || t2 == nil {
		t.Fatalf("task 2 after cascade: %v %v", t2, err)
	}
	if len(t2.BlockedBy) != 0 {
		t.Errorf("task 2 BlockedBy after cascade = %v, want empty", t2.BlockedBy)
	}

	// blocks side: 2.Blocks=[1], delete 1.
	dir2 := t.TempDir()
	l2 := NewList(dir2)
	if _, err := l2.CreateTask("a", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l2.CreateTask("b", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if !l2.BlockTask("2", "1") {
		t.Fatal("BlockTask(2,1) failed")
	}
	if _, err := l2.DeleteTask("1"); err != nil {
		t.Fatal(err)
	}
	t2b, err := l2.GetTask("2")
	if err != nil || t2b == nil {
		t.Fatalf("task 2 after cascade: %v %v", t2b, err)
	}
	if len(t2b.Blocks) != 0 {
		t.Errorf("task 2 Blocks after cascade = %v, want empty", t2b.Blocks)
	}
}

func TestPinTask_BlockPartialAndIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(dir)
	if _, err := l.CreateTask("a", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CreateTask("b", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	// Pre-seed 2.BlockedBy=[1]: only the Blocks side is new on the next
	// call, so that side's changed flag alone must trigger the write.
	t2, err := l.GetTask("2")
	if err != nil {
		t.Fatal(err)
	}
	t2.BlockedBy = []string{"1"}
	if err := os.WriteFile(filepath.Join(dir, "2.json"), mustPinMarshal(t, *t2), 0o644); err != nil {
		t.Fatal(err)
	}
	if !l.BlockTask("1", "2") {
		t.Fatal("BlockTask(1,2) failed")
	}
	t1, err := l.GetTask("1")
	if err != nil {
		t.Fatal(err)
	}
	if len(t1.Blocks) != 1 || t1.Blocks[0] != "2" {
		t.Errorf("task 1 Blocks = %v, want [2]", t1.Blocks)
	}

	// Fully idempotent call must not rewrite the files. atomicWrite replaces
	// files via rename (new inode), so os.SameFile detects any rewrite
	// without timing assumptions.
	t1p := filepath.Join(dir, "1.json")
	t2p := filepath.Join(dir, "2.json")
	i1, err := os.Stat(t1p)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := os.Stat(t2p)
	if err != nil {
		t.Fatal(err)
	}
	if !l.BlockTask("1", "2") {
		t.Fatal("idempotent BlockTask failed")
	}
	j1, err := os.Stat(t1p)
	if err != nil {
		t.Fatal(err)
	}
	j2, err := os.Stat(t2p)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(i1, j1) || !os.SameFile(i2, j2) {
		t.Error("idempotent BlockTask rewrote task files")
	}
}

func TestPinTask_ListScansPastNonTaskEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(dir)
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The high water mark sorts before task files and must not stop the scan.
	tasks, err := l.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("ListTasks = %v, want the created task", tasks)
	}

	// A corrupt file must not stop later valid files from loading.
	dir2 := t.TempDir()
	l2 := NewList(dir2)
	if err := os.WriteFile(filepath.Join(dir2, "1.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "-junk.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	pinWriteTaskFile(t, dir2, "2.json", Task{ID: "2", Subject: "s", Status: StatusPending, Blocks: []string{}, BlockedBy: []string{}})
	tasks, err = l2.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "2" {
		t.Fatalf("ListTasks with corrupt file = %v, want only task 2", tasks)
	}
}

func TestPinTask_SanitizeComponentRanges(t *testing.T) {
	t.Parallel()
	tt := []struct{ in, want string }{
		{"a", "a"}, {"z", "z"}, {"A", "A"}, {"Z", "Z"},
		{"0", "0"}, {"9", "9"}, {"_", "_"}, {"-", "-"},
		{"aA0_-", "aA0_-"},
		{".", "-"}, {"é", "-"}, {"a/b", "a-b"}, {"a b", "a-b"},
	}
	for _, c := range tt {
		if got := sanitizePathComponent(c.in); got != c.want {
			t.Errorf("sanitizePathComponent(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func mustPinMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPinTask_SetDirLockReleased(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(t.TempDir())
	if err := l.SetDir(dir); err != nil {
		t.Fatal(err)
	}
	// SetDir must release its lock; a leaked lock wedges every later call.
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if id != "1" {
		t.Errorf("post-SetDir task ID = %q, want 1", id)
	}
}

func TestPinTask_UpdateLockReleased(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(dir)
	id, err := l.CreateTask("s", "d", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.UpdateTask(id, TaskUpdates{Subject: new("x")}); err != nil {
		t.Fatal(err)
	}
	// UpdateTask must release its lock; BlockTask would wedge otherwise.
	if l.BlockTask(id, "999") {
		t.Error("BlockTask against nonexistent task reported true")
	}
}

func TestPinTask_ShouldCleanupLockReleased(t *testing.T) {
	t.Parallel()
	l := NewList(t.TempDir())
	if l.ShouldCleanupCompleted(time.Minute) {
		t.Error("empty list reports cleanup due")
	}
	// A second call must not wedge on a leaked lock.
	if l.ShouldCleanupCompleted(time.Minute) {
		t.Error("second cleanup check reports due")
	}
}

func TestPinTask_LocksMutualExclusion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewList(t.TempDir())
	if err := l.SetDir(dir); err != nil {
		t.Fatal(err)
	}
	// SetDir, UpdateTask, and ShouldCleanupCompleted must actually hold the
	// mutex across their bodies; concurrent access to shared fields is only
	// safe if the lock is held (run under -race).
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.ShouldCleanupCompleted(time.Minute)
	}()
	if _, err := l.CreateTask("s", "d", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.UpdateTask("1", TaskUpdates{Subject: new("x")}); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestPinTask_PromptWired(t *testing.T) {
	t.Parallel()
	prompt := tasksToolPrompt()
	if !strings.Contains(prompt, "structured task list") {
		t.Errorf("tasksToolPrompt starts with %.60q, want the task-list prompt", prompt)
	}
}
