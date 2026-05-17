package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistory_Add(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("hello")
	h.Add("world")

	if len(h.items) != 2 {
		t.Errorf("Len() = %d, want 2", len(h.items))
	}
}

func TestHistory_AddEmpty(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("")
	if len(h.items) != 0 {
		t.Errorf("empty Add should not create entry, got Len() = %d", len(h.items))
	}
}

func TestHistory_AddDuplicate(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("hello")
	h.Add("hello")
	if len(h.items) != 1 {
		t.Errorf("duplicate Add should not create entry, got Len() = %d", len(h.items))
	}
}

func TestHistory_Up(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Add("second")
	h.Add("third")

	// Up from end should go to "third" (last item)
	res := h.Up("current")
	if res.Text != "third" {
		t.Errorf("Up() = %q, want %q", res.Text, "third")
	}
	if res.Cursor != CursorHome {
		t.Errorf("Up() cursor = %v, want CursorHome", res.Cursor)
	}

	// Up again should go to "second"
	res = h.Up(res.Text)
	if res.Text != "second" {
		t.Errorf("Up() = %q, want %q", res.Text, "second")
	}
	if res.Cursor != CursorHome {
		t.Errorf("Up() cursor = %v, want CursorHome", res.Cursor)
	}
}

func TestHistory_Down(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Add("second")

	res := h.Up("current")
	if res.Text != "second" {
		t.Fatalf("setup Up() failed, got %q", res.Text)
	}
	// Down from newest entry restores draft
	res = h.Down()
	if res.Text != "current" {
		t.Errorf("Down() = %q, want draft %q", res.Text, "current")
	}
	if res.Cursor != CursorEnd {
		t.Errorf("Down() cursor = %v, want CursorEnd", res.Cursor)
	}
}

func TestHistory_ResetNav(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Up("current")
	h.ResetNav()
	// After reset, Up should start fresh from end
	res := h.Up("current")
	if res.Text != "first" {
		t.Errorf("after ResetNav, Up() = %q, want %q", res.Text, "first")
	}
}

func TestHistory_MaxSize(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.maxSize = 3

	h.Add("a")
	h.Add("b")
	h.Add("c")
	h.Add("d") // should evict "a"

	if len(h.items) != 3 {
		t.Fatalf("Len() = %d, want 3", len(h.items))
	}
	if h.items[0] != "b" {
		t.Errorf("items[0] = %q, want %q", h.items[0], "b")
	}
}

func TestHistory_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	// Create and add entries
	h1 := NewHistory(path)
	h1.Add("first command")
	h1.Add("second command")

	if len(h1.items) != 2 {
		t.Fatalf("len(h1.items) = %d, want 2", len(h1.items))
	}

	// Load from file into new History
	h2 := NewHistory(path)
	if len(h2.items) != 2 {
		t.Fatalf("len(h2.items) = %d, want 2", len(h2.items))
	}
	if h2.items[0] != "first command" {
		t.Errorf("h2.items[0] = %q, want %q", h2.items[0], "first command")
	}
	if h2.items[1] != "second command" {
		t.Errorf("h2.items[1] = %q, want %q", h2.items[1], "second command")
	}
}

func TestHistory_PersistenceAppend(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	h1 := NewHistory(path)
	h1.Add("entry1")

	h2 := NewHistory(path)
	h2.Add("entry2")

	h3 := NewHistory(path)
	if len(h3.items) != 2 {
		t.Fatalf("len(h3.items) = %d, want 2", len(h3.items))
	}
	if h3.items[0] != "entry1" {
		t.Errorf("items[0] = %q, want %q", h3.items[0], "entry1")
	}
	if h3.items[1] != "entry2" {
		t.Errorf("items[1] = %q, want %q", h3.items[1], "entry2")
	}
}

func TestHistory_PersistenceEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	// Create empty file
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewHistory(path)
	if len(h.items) != 0 {
		t.Errorf("empty file: Len() = %d, want 0", len(h.items))
	}
}

func TestHistory_PersistenceMalformedLine(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")

	// Write file with one good line and one bad line
	if err := os.WriteFile(path, []byte("bad json\n{\"display\":\"good\",\"timestamp\":123}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewHistory(path)
	if len(h.items) != 1 {
		t.Fatalf("Len() = %d, want 1", len(h.items))
	}
	if h.items[0] != "good" {
		t.Errorf("items[0] = %q, want %q", h.items[0], "good")
	}
}

func TestHistory_NoFilePath(t *testing.T) {
	h := NewHistory("")
	h.Add("test")
	if len(h.items) != 1 {
		t.Errorf("Len() = %d, want 1", len(h.items))
	}
	// No file created — no crash
}

func TestHistory_NilFilePath(t *testing.T) {
	h := NewHistory("")
	h.Add("test")
	// Should work fine without persistence
	if len(h.items) != 1 {
		t.Errorf("Len() = %d, want 1", len(h.items))
	}
}

// ---------------------------------------------------------------------------
// Additional coverage
// ---------------------------------------------------------------------------

func TestHistory_RelativePathRejected(t *testing.T) {
	h := NewHistory("relative/path.jsonl")
	h.Add("test")
	// Relative path should disable persistence — no crash
	if len(h.items) != 1 {
		t.Errorf("Len() = %d, want 1", len(h.items))
	}
	if h.filePath != "" {
		t.Errorf("filePath should be empty for relative path, got %q", h.filePath)
	}
}

func TestHistory_Up_Empty(t *testing.T) {
	h := NewHistory("")
	res := h.Up("current")
	if res.Text != "current" {
		t.Errorf("Up on empty should return current, got %q", res.Text)
	}
	if res.Cursor != CursorNone {
		t.Errorf("Up on empty cursor = %v, want CursorNone", res.Cursor)
	}
}

func TestHistory_Up_ClampedAtOldest(t *testing.T) {
	h := NewHistory("")
	h.Add("only")
	res := h.Up("x")
	if res.Text != "only" {
		t.Fatalf("first Up = %q, want %q", res.Text, "only")
	}
	// Second Up: already at oldest, no-op (rollback per TS line 166-171)
	res = h.Up("x")
	if res.Cursor != CursorNone {
		t.Errorf("clamped Up should be no-op, cursor = %v", res.Cursor)
	}
}

func TestHistory_Down_Empty(t *testing.T) {
	h := NewHistory("")
	res := h.Down()
	if res.Text != "" {
		t.Errorf("Down on empty should return empty, got %q", res.Text)
	}
	if res.Cursor != CursorNone {
		t.Errorf("Down on empty cursor = %v, want CursorNone", res.Cursor)
	}
}

func TestHistory_Down_ClampedAtEnd(t *testing.T) {
	h := NewHistory("")
	h.Add("a")
	h.Add("b")
	h.Up("x")
	// Down from newest: restore draft "x"
	res := h.Down()
	if res.Text != "x" {
		t.Errorf("Down from newest = %q, want draft %q", res.Text, "x")
	}
	if res.Cursor != CursorEnd {
		t.Errorf("Down from newest cursor = %v, want CursorEnd", res.Cursor)
	}
	// Down again: already in draft (historyIndex=0), no-op
	res = h.Down()
	if res.Cursor != CursorNone {
		t.Errorf("Down in draft should be no-op, cursor = %v", res.Cursor)
	}
}

func TestHistory_Up_CurrentMatchesLast(t *testing.T) {
	h := NewHistory("")
	h.Add("first")
	h.Add("second")
	// TS does not skip matching entries; displays newest regardless
	res := h.Up("second")
	if res.Text != "second" {
		t.Errorf("Up with current=last = %q, want %q (TS shows newest)", res.Text, "second")
	}
}

func TestHistory_Save_MkdirFail(t *testing.T) {
	// Use a path where parent dir creation will fail (permission denied)
	h := NewHistory("/proc/nonexistent/history.jsonl")
	h.Add("test") // should not panic
	// File won't be saved, but no crash
}

func TestHistory_Load_EmptyDisplay(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")
	// Write entry with empty display
	if err := os.WriteFile(path, []byte("{\"display\":\"\",\"timestamp\":123}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	if len(h.items) != 0 {
		t.Errorf("empty display should be skipped, Len() = %d", len(h.items))
	}
}

func TestHistory_Load_CapAtMaxSize(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.jsonl")
	// Write 110 entries
	var lines []string
	for i := range 110 {
		entry := fmt.Sprintf("{\"display\":\"entry%d\",\"timestamp\":%d}", i, i)
		lines = append(lines, entry)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	if len(h.items) != 100 {
		t.Errorf("should cap at 100, got Len() = %d", len(h.items))
	}
}

func TestHistory_Save_WriteError(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "history.jsonl")
	// Create file but make parent directory read-only
	subdir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	// MkdirAll will succeed on subsequent saves since dir exists
	// To trigger write error, make the file itself read-only
	if err := os.WriteFile(path, []byte(""), 0o444); err != nil {
		t.Fatal(err)
	}
	h.Add("test") // Should not panic even if write fails
}

func TestHistory_Save_FileOpenError(t *testing.T) {
	// Path to a directory instead of file — OpenFile will fail
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "dir_as_file")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	h := NewHistory(path)
	h.Add("test") // Should not panic
}

func TestHistory_RemoveLast(t *testing.T) {
	h := NewHistory("")
	h.Add("first")
	h.Add("second")
	h.Add("third")

	if len(h.items) != 3 {
		t.Fatalf("Len() = %d, want 3 before RemoveLast", len(h.items))
	}

	h.RemoveLast()

	if len(h.items) != 2 {
		t.Fatalf("Len() = %d after RemoveLast, want 2", len(h.items))
	}
	if h.items[0] != "first" {
		t.Errorf("items[0] = %q, want %q", h.items[0], "first")
	}
	if h.items[1] != "second" {
		t.Errorf("items[1] = %q, want %q", h.items[1], "second")
	}
	// RemoveLast resets navigation state
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0 after RemoveLast", h.historyIndex)
	}
	if h.savedDraft != "" {
		t.Errorf("savedDraft = %q, want empty after RemoveLast", h.savedDraft)
	}
}

func TestHistory_RemoveLast_Empty(t *testing.T) {
	h := NewHistory("")
	h.RemoveLast() // should not panic
	if len(h.items) != 0 {
		t.Errorf("Len() = %d after RemoveLast on empty, want 0", len(h.items))
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0", h.historyIndex)
	}
}

func TestHistory_RemoveLast_SingleItem(t *testing.T) {
	h := NewHistory("")
	h.Add("only")
	h.RemoveLast()
	if len(h.items) != 0 {
		t.Errorf("Len() = %d, want 0", len(h.items))
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0", h.historyIndex)
	}
}

// ---------------------------------------------------------------------------
// Draft save/restore — aligned with TS useArrowKeyHistory.tsx
// historyIndex 0 = draft, 1+ = navigating (1=newest, N=oldest)
// savedDraft = user's input saved on first Up (only if non-empty)
// ---------------------------------------------------------------------------

func TestHistory_Draft_SaveOnFirstUp(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	// First Up with draft "my draft" should save it
	res := h.Up("my draft")
	if res.Text != "old2" {
		t.Errorf("Up() = %q, want %q", res.Text, "old2")
	}
	if h.savedDraft != "my draft" {
		t.Errorf("savedDraft = %q, want %q", h.savedDraft, "my draft")
	}
}

func TestHistory_Draft_RestoreOnDown(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	h.Up("my draft") // saves draft, shows "old2"
	h.Up("my draft") // shows "old1"

	// Down back to "old2"
	res := h.Down()
	if res.Text != "old2" {
		t.Errorf("Down = %q, want %q", res.Text, "old2")
	}
	if res.Cursor != CursorEnd {
		t.Errorf("Down cursor = %v, want CursorEnd", res.Cursor)
	}

	// Down past newest: back to draft (historyIndex=0)
	res = h.Down()
	if res.Text != "my draft" {
		t.Errorf("Down past newest = %q, want draft %q", res.Text, "my draft")
	}
	if res.Cursor != CursorEnd {
		t.Errorf("Down to draft cursor = %v, want CursorEnd", res.Cursor)
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0 (at draft)", h.historyIndex)
	}
}

func TestHistory_Draft_ClearWhenNoDraft(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	// Up with empty input — savedDraft stays empty
	h.Up("")
	// Down from newest: back to draft (empty, since no input when Up was pressed)
	res := h.Down()
	if res.Text != "" {
		t.Errorf("Down from newest with empty draft = %q, want empty", res.Text)
	}
	if res.Cursor != CursorEnd {
		t.Errorf("Down to empty draft cursor = %v, want CursorEnd", res.Cursor)
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0 (at draft)", h.historyIndex)
	}
}

func TestHistory_Draft_FullCycle(t *testing.T) {
	h := NewHistory("")
	h.Add("a")
	h.Add("b")
	h.Add("c")

	// Up from draft "typing..." → c (newest)
	res := h.Up("typing...")
	if res.Text != "c" {
		t.Fatalf("Up1 = %q, want c", res.Text)
	}
	// Up → b
	res = h.Up("typing...")
	if res.Text != "b" {
		t.Fatalf("Up2 = %q, want b", res.Text)
	}
	// Down → c
	res = h.Down()
	if res.Text != "c" {
		t.Fatalf("Down1 = %q, want c", res.Text)
	}
	// Down past newest → draft "typing..."
	res = h.Down()
	if res.Text != "typing..." {
		t.Errorf("Down2 = %q, want draft %q", res.Text, "typing...")
	}
	// Now in draft (historyIndex=0). Down → no-op
	res = h.Down()
	if res.Cursor != CursorNone {
		t.Errorf("Down in draft should be no-op, cursor = %v", res.Cursor)
	}
	// Up from draft → re-enter history at newest "c"
	res = h.Up("typing...")
	if res.Text != "c" {
		t.Errorf("Up from draft = %q, want c", res.Text)
	}
	if res.Cursor != CursorHome {
		t.Errorf("Up from draft cursor = %v, want CursorHome", res.Cursor)
	}
}

func TestHistory_Draft_UpFromDraftEntersHistory(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	// Enter history, then down to draft
	h.Up("我的草稿") // → old2
	h.Down()        // → draft "我的草稿"

	// Up from draft → re-enter history at newest
	res := h.Up("我的草稿")
	if res.Text != "old2" {
		t.Errorf("Up from draft = %q, want old2", res.Text)
	}
	if h.historyIndex == 0 {
		t.Error("historyIndex should be > 0 after Up")
	}
}

func TestHistory_Draft_UpFromDraftThenDown(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	// idle → history → draft
	h.Up("草稿") // → old2
	h.Down()     // → draft "草稿"

	// Up from draft → back to old2
	res := h.Up("草稿")
	if res.Text != "old2" {
		t.Fatalf("Up from draft = %q, want old2", res.Text)
	}
	// Down again → back to draft
	res = h.Down()
	if res.Text != "草稿" {
		t.Errorf("Down = %q, want draft %q", res.Text, "草稿")
	}
}

func TestHistory_Draft_ResetByAdd(t *testing.T) {
	h := NewHistory("")
	h.Add("old")
	h.Up("draft")
	// Add resets navigation state and draft
	h.Add("new")
	if h.savedDraft != "" {
		t.Errorf("savedDraft should be cleared after Add, got %q", h.savedDraft)
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex should be 0 after Add, got %d", h.historyIndex)
	}
}

func TestHistory_Draft_ResetByResetNav(t *testing.T) {
	h := NewHistory("")
	h.Add("old")
	h.Up("draft")
	h.ResetNav()
	if h.savedDraft != "" {
		t.Errorf("savedDraft should be cleared after ResetNav, got %q", h.savedDraft)
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex should be 0 after ResetNav, got %d", h.historyIndex)
	}
}

func TestHistory_Down_NoOpOutsideNav(t *testing.T) {
	h := NewHistory("")
	h.Add("old1")
	h.Add("old2")

	// Down without prior Up — should be no-op (historyIndex=0)
	res := h.Down()
	if res.Cursor != CursorNone {
		t.Errorf("Down without nav cursor = %v, want CursorNone", res.Cursor)
	}
}

func TestHistory_Draft_SubmittedEmptyDraftCleared(t *testing.T) {
	h := NewHistory("")
	h.Add("测试消息")

	// Input is empty (just submitted)
	// Up → shows "测试消息" from history
	res := h.Up("")
	if res.Text != "测试消息" {
		t.Fatalf("after Up, text = %q, want %q", res.Text, "测试消息")
	}
	// Down → back to draft (empty, since input was empty when Up was pressed)
	res = h.Down()
	if res.Text != "" {
		t.Errorf("after Down, text = %q, want empty (draft was empty)", res.Text)
	}
	if h.historyIndex != 0 {
		t.Errorf("historyIndex = %d, want 0 (at draft)", h.historyIndex)
	}
	// Down again → no-op (historyIndex=0)
	res = h.Down()
	if res.Cursor != CursorNone {
		t.Errorf("Down in empty draft should be no-op, cursor = %v", res.Cursor)
	}
	// Up from draft → re-enter history
	res = h.Up("")
	if res.Text != "测试消息" {
		t.Errorf("Up from empty draft = %q, want 测试消息", res.Text)
	}
}
