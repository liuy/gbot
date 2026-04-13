package bash

import (
	"strings"
	"sync"
	"testing"
)

func TestStreamingOutput_WriteBasic(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	n, err := s.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write() = %d, want 6", n)
	}

	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if updates[0].TotalLines != 1 {
		t.Errorf("TotalLines = %d, want 1", updates[0].TotalLines)
	}
	if updates[0].TotalBytes != 6 {
		t.Errorf("TotalBytes = %d, want 6", updates[0].TotalBytes)
	}
	if !updates[0].IsIncomplete {
		t.Error("IsIncomplete = false, want true")
	}
	if len(updates[0].Lines) != 1 || updates[0].Lines[0] != "hello" {
		t.Errorf("Lines = %v, want [hello]", updates[0].Lines)
	}
}

func TestStreamingOutput_WriteMultipleLines(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	_, _ = s.Write([]byte("line1\nline2\nline3\n"))

	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if updates[0].TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", updates[0].TotalLines)
	}
}

func TestStreamingOutput_RollingWindow(t *testing.T) {
	t.Parallel()

	var lastUpdate StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})

	// Write 25 lines — should keep only last 20
	for i := 0; i < 25; i++ {
		_, _ = s.Write([]byte("line\n"))
	}

	if len(lastUpdate.Lines) != streamingLastLines {
		t.Errorf("len(Lines) = %d, want %d", len(lastUpdate.Lines), streamingLastLines)
	}
	// First line in window should be "line5" (25-20=5, 0-indexed)
	if lastUpdate.Lines[0] != "line" {
		t.Errorf("first line = %q, want %q", lastUpdate.Lines[0], "line")
	}
	if lastUpdate.TotalLines != 25 {
		t.Errorf("TotalLines = %d, want 25", lastUpdate.TotalLines)
	}
}

func TestStreamingOutput_SizeExceeded(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write a chunk that won't exceed the limit
	_, _ = s.Write([]byte("small"))
	if s.Exceeded() {
		t.Error("Exceeded() = true, want false for small output")
	}

	// Can't easily test 5GB in a unit test, so test the logic by checking
	// that totalBytes tracks correctly
	if s.TotalBytes() != 5 {
		t.Errorf("TotalBytes() = %d, want 5", s.TotalBytes())
	}
}

func TestStreamingOutput_NilCallback(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	// Should not panic
	n, err := s.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write() = %d, want 6", n)
	}
}

func TestStreamingOutput_Lines(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("a\nb\nc\n"))

	lines := s.Lines()
	if len(lines) != 3 {
		t.Fatalf("Lines() = %d lines, want 3", len(lines))
	}
	if lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Errorf("Lines() = %v, want [a b c]", lines)
	}
}

func TestStreamingOutput_String(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("hello\nworld\n"))

	got := s.String()
	want := "hello\nworld"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStreamingOutput_EmptyWrite(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	_, _ = s.Write([]byte{})
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1 (empty write still reports)", len(updates))
	}
	if updates[0].TotalLines != 0 {
		t.Errorf("TotalLines = %d, want 0", updates[0].TotalLines)
	}
	if updates[0].TotalBytes != 0 {
		t.Errorf("TotalBytes = %d, want 0", updates[0].TotalBytes)
	}
}

func TestStreamingOutput_PartialLineFragment(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write partial line (no newline)
	_, _ = s.Write([]byte("hel"))
	_, _ = s.Write([]byte("lo\n"))

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1", len(lines))
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "hello")
	}
}

func TestStreamingOutput_PartialLineAtStart(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write partial line then newline
	_, _ = s.Write([]byte("abc"))
	_, _ = s.Write([]byte("\n"))

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1", len(lines))
	}
	if lines[0] != "abc" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "abc")
	}
}

func TestStreamingOutput_FinalUpdate(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	_, _ = s.Write([]byte("hello\n"))
	s.FinalUpdate()

	// Should have 2 updates: one from Write, one from FinalUpdate
	if len(updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(updates))
	}
	if updates[1].IsIncomplete {
		t.Error("final update IsIncomplete = true, want false")
	}
}

func TestStreamingOutput_ConcurrentWrites(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Write([]byte("line\n"))
		}()
	}
	wg.Wait()

	lines := s.Lines()
	if len(lines) != 10 {
		t.Errorf("Lines() = %d, want 10", len(lines))
	}
	if s.TotalBytes() != 50 { // 10 * 5 bytes ("line\n")
		t.Errorf("TotalBytes() = %d, want 50", s.TotalBytes())
	}
}

func TestStreamingOutput_MultipleNewlinesInOneWrite(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("a\nb\nc"))

	lines := s.Lines()
	if len(lines) != 3 {
		t.Fatalf("Lines() = %v, want 3 lines", lines)
	}
	if lines[0] != "a" || lines[1] != "b" || lines[2] != "c" {
		t.Errorf("Lines() = %v, want [a b c]", lines)
	}
}

func TestStreamingOutput_TrailingNewlineOnly(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("\n"))

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1", len(lines))
	}
	if lines[0] != "" {
		t.Errorf("lines[0] = %q, want empty string", lines[0])
	}
}

func TestStreamingOutput_WriteThenNewline(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("first\n"))
	_, _ = s.Write([]byte("second\n"))

	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("Lines() = %d, want 2", len(lines))
	}

	got := s.String()
	want := "first\nsecond"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestStreamingOutput_PartialLineAppendToExisting(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("hello\n"))  // complete line
	_, _ = s.Write([]byte("wor"))      // partial line (last fragment, no newline)
	_, _ = s.Write([]byte("ld"))       // partial append to last fragment

	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("Lines() = %d, want 2: %v", len(lines), lines)
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "hello")
	}
	if lines[1] != "world" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "world")
	}
}

func TestStreamingOutput_EmptyString(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	got := s.String()
	if got != "" {
		t.Errorf("String() on empty = %q, want empty", got)
	}
}

func TestStreamingOutput_TotalBytesAccumulated(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("abc"))
	_, _ = s.Write([]byte("def"))
	_, _ = s.Write([]byte("ghi"))

	if s.TotalBytes() != 9 {
		t.Errorf("TotalBytes() = %d, want 9", s.TotalBytes())
	}
}

func TestStreamingOutput_LinesClone(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("original\n"))

	lines := s.Lines()
	lines[0] = "modified"

	// Original should be unchanged
	original := s.Lines()
	if original[0] != "original" {
		t.Errorf("Lines() should return a clone, got %q", original[0])
	}
}

func TestMaxOutputBytes(t *testing.T) {
	t.Parallel()

	if maxOutputBytes != 5*1024*1024*1024 {
		t.Errorf("maxOutputBytes = %d, want 5GB", maxOutputBytes)
	}
}

func TestStreamingOutput_Exceeded(t *testing.T) {
	// Override maxOutputBytes for testing
	orig := maxOutputBytes
	maxOutputBytes = 10 // very small limit
	defer func() { maxOutputBytes = orig }()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("hello world")) // 11 bytes > 10

	if !s.Exceeded() {
		t.Error("Exceeded() should be true after writing past limit")
	}
}

func TestStreamingLastLines(t *testing.T) {
	t.Parallel()

	if streamingLastLines != 20 {
		t.Errorf("streamingLastLines = %d, want 20", streamingLastLines)
	}
}

func TestStreamingOutput_FinalUpdateEmpty(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	// FinalUpdate without any writes
	s.FinalUpdate()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if updates[0].IsIncomplete {
		t.Error("final update should have IsIncomplete=false")
	}
	if updates[0].TotalLines != 0 {
		t.Errorf("TotalLines = %d, want 0", updates[0].TotalLines)
	}
}

func TestStreamingOutput_WriteEmptyLastFragment(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	// "a\n" → Split gives ["a", ""], last fragment "" is ignored
	_, _ = s.Write([]byte("a\n"))

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1: %v", len(lines), lines)
	}
	if lines[0] != "a" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "a")
	}
}

func TestStreamingOutput_WriteOnlyNewlines(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	_, _ = s.Write([]byte("\n\n\n"))

	lines := s.Lines()
	// Split("\n\n\n") = ["", "", "", ""], last "" is empty fragment
	// So we get 3 lines: ["", "", ""]
	if len(lines) != 3 {
		t.Fatalf("Lines() = %d, want 3: %v", len(lines), lines)
	}
}

func TestStreamingOutput_RollingWindowExact(t *testing.T) {
	t.Parallel()

	var lastUpdate StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})

	// Write exactly 20 lines — all should be kept
	for i := 0; i < 20; i++ {
		_, _ = s.Write([]byte(strings.Repeat("x", i+1) + "\n"))
	}

	if len(lastUpdate.Lines) != 20 {
		t.Errorf("len(Lines) = %d, want 20", len(lastUpdate.Lines))
	}
	// First line should be "x" (1 char), last should be 20 x's
	if len(lastUpdate.Lines[0]) != 1 {
		t.Errorf("first line len = %d, want 1", len(lastUpdate.Lines[0]))
	}
	if len(lastUpdate.Lines[19]) != 20 {
		t.Errorf("last line len = %d, want 20", len(lastUpdate.Lines[19]))
	}
}
