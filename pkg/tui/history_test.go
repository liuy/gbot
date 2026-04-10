package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistory_Add(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("hello")
	h.Add("world")

	if h.Len() != 2 {
		t.Errorf("Len() = %d, want 2", h.Len())
	}
}

func TestHistory_AddEmpty(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("")
	if h.Len() != 0 {
		t.Errorf("empty Add should not create entry, got Len() = %d", h.Len())
	}
}

func TestHistory_AddDuplicate(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("hello")
	h.Add("hello")
	if h.Len() != 1 {
		t.Errorf("duplicate Add should not create entry, got Len() = %d", h.Len())
	}
}

func TestHistory_Up(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Add("second")
	h.Add("third")

	// Up from end should go to "third" (last item)
	cmd, ok := h.Up("current")
	if !ok {
		t.Fatal("Up() returned false")
	}
	if cmd != "third" {
		t.Errorf("Up() = %q, want %q", cmd, "third")
	}

	// Up again should go to "second"
	cmd, ok = h.Up(cmd)
	if !ok {
		t.Fatal("Up() returned false")
	}
	if cmd != "second" {
		t.Errorf("Up() = %q, want %q", cmd, "second")
	}
}

func TestHistory_Down(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Add("second")

	h.Up("current")
	cmd, ok := h.Down()
	if !ok {
		t.Fatal("Down() returned false")
	}
	if cmd != "second" {
		t.Errorf("Down() = %q, want %q", cmd, "second")
	}
}

func TestHistory_ResetNav(t *testing.T) {
	t.Parallel()

	h := NewHistory("")
	h.Add("first")
	h.Up("current")
	h.ResetNav()
	// After reset, Up should start fresh from end
	cmd, _ := h.Up("current")
	if cmd != "first" {
		t.Errorf("after ResetNav, Up() = %q, want %q", cmd, "first")
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

	if h.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", h.Len())
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

	if h1.Len() != 2 {
		t.Fatalf("h1.Len() = %d, want 2", h1.Len())
	}

	// Load from file into new History
	h2 := NewHistory(path)
	if h2.Len() != 2 {
		t.Fatalf("h2.Len() = %d, want 2", h2.Len())
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
	if h3.Len() != 2 {
		t.Fatalf("h3.Len() = %d, want 2", h3.Len())
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
	if h.Len() != 0 {
		t.Errorf("empty file: Len() = %d, want 0", h.Len())
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
	if h.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", h.Len())
	}
	if h.items[0] != "good" {
		t.Errorf("items[0] = %q, want %q", h.items[0], "good")
	}
}

func TestHistory_NoFilePath(t *testing.T) {
	h := NewHistory("")
	h.Add("test")
	if h.Len() != 1 {
		t.Errorf("Len() = %d, want 1", h.Len())
	}
	// No file created — no crash
}

func TestHistory_NilFilePath(t *testing.T) {
	h := NewHistory("")
	h.Add("test")
	// Should work fine without persistence
	if h.Len() != 1 {
		t.Errorf("Len() = %d, want 1", h.Len())
	}
}
