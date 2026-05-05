package bash

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// mustWrite asserts that s.Write(data) succeeds and returns bytes written.
func mustWrite(t *testing.T, s *StreamingOutput, data []byte) int {
	t.Helper()
	n, err := s.Write(data)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	return n
}

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

	n, err := s.Write([]byte("line1\nline2\nline3\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 18 {
		t.Errorf("Write() = %d, want 18", n)
	}

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
	for range 25 {
		n, err := s.Write([]byte("line\n"))
		if err != nil {
			t.Fatalf("Write() error: %v", err)
		}
		if n != 5 {
			t.Errorf("Write() = %d, want 5", n)
		}
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
	n, err := s.Write([]byte("a\nb\nc\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write() = %d, want 6", n)
	}

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
	n, err := s.Write([]byte("hello\nworld\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 12 {
		t.Errorf("Write() = %d, want 12", n)
	}

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

	n, err := s.Write([]byte{})
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 0 {
		t.Errorf("Write() = %d, want 0", n)
	}
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
	n, err := s.Write([]byte("hel"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 3 {
		t.Errorf("Write() = %d, want 3", n)
	}
	n, err = s.Write([]byte("lo\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 3 {
		t.Errorf("Write() = %d, want 3", n)
	}

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
	n, err := s.Write([]byte("abc"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 3 {
		t.Errorf("Write() = %d, want 3", n)
	}
	n, err = s.Write([]byte("\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 1 {
		t.Errorf("Write() = %d, want 1", n)
	}

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

	n, err := s.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 6 {
		t.Errorf("Write() = %d, want 6", n)
	}
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

	for range 10 {
		wg.Go(func() {
			_, _ = s.Write([]byte("line\n"))
		})
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
	if _, err := s.Write([]byte("a\nb\nc")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

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
	if _, err := s.Write([]byte("\n")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

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
	if _, err := s.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := s.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

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
	if _, err := s.Write([]byte("hello\n")); err != nil { // complete line
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := s.Write([]byte("wor")); err != nil { // partial line (last fragment, no newline)
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := s.Write([]byte("ld")); err != nil { // partial append to last fragment
		t.Fatalf("Write() error: %v", err)
	}

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
	if _, err := s.Write([]byte("abc")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := s.Write([]byte("def")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if _, err := s.Write([]byte("ghi")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	if s.TotalBytes() != 9 {
		t.Errorf("TotalBytes() = %d, want 9", s.TotalBytes())
	}
}

func TestStreamingOutput_LinesClone(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	if _, err := s.Write([]byte("original\n")); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	lines := s.Lines()
	lines[0] = "modified"

	// Original should be unchanged
	original := s.Lines()
	if original[0] != "original" {
		t.Errorf("Lines() should return a clone, got %q", original[0])
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
	mustWrite(t, s, []byte("a\n"))

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
	mustWrite(t, s, []byte("\n\n\n"))

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
	for i := range 20 {
		mustWrite(t, s, []byte(strings.Repeat("x", i+1)+"\n"))
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

func TestStreamingOutput_LastLines(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write 30 lines — LastLines should return only last 20
	for i := range 30 {
		n, err := fmt.Fprintf(s, "line%d\n", i)
		if err != nil {
			t.Fatalf("Fprintf() error: %v", err)
		}
		if n == 0 {
			t.Errorf("Fprintf() wrote 0 bytes for line %d", i)
		}
	}

	got := s.LastLines()
	// Should contain "line10" through "line29" (last 20 lines)
	if !strings.Contains(got, "line10") {
		t.Error("LastLines() should contain line10")
	}
	if strings.Contains(got, "line9") {
		t.Error("LastLines() should NOT contain line9 (outside rolling window)")
	}
	if !strings.Contains(got, "line29") {
		t.Error("LastLines() should contain line29")
	}
}

func TestStreamingOutput_LastLines_Empty(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	got := s.LastLines()
	if got != "" {
		t.Errorf("LastLines() on empty = %q, want empty", got)
	}
}


func TestStreamingOutput_ReplaceLastLine(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	mustWrite(t, s, []byte("line1\nline2\n"))

	updates = updates[:0] // reset tracker
	s.ReplaceLastLine("line2-updated")

	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}

	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("Lines() = %d, want 2", len(lines))
	}
	if lines[0] != "line1" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "line1")
	}
	if lines[1] != "line2-updated" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "line2-updated")
	}
	if updates[0].TotalBytes <= 12 {
		t.Errorf("TotalBytes = %d, should have grown after ReplaceLastLine", updates[0].TotalBytes)
	}
}

func TestStreamingOutput_ReplaceLastLineEmpty(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	// Replace on empty buffer — should add as new line
	s.ReplaceLastLine("first-line")

	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1", len(lines))
	}
	if lines[0] != "first-line" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "first-line")
	}
}

func TestStreamingOutput_ReplaceLastLineNilCallback(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	mustWrite(t, s, []byte("hello\n"))
	s.ReplaceLastLine("replaced") // should not panic

	lines := s.Lines()
	if lines[0] != "replaced" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "replaced")
	}
}

func TestStreamingOutput_ReplaceLastLineProgress(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})

	// Simulate progress bar: Write line, then replace multiple times
	mustWrite(t, s, []byte("Downloading 10%\n"))
	updates = updates[:0]

	s.ReplaceLastLine("Downloading 50%")
	s.ReplaceLastLine("Downloading 90%")
	s.ReplaceLastLine("Done!")

	if len(updates) != 3 {
		t.Fatalf("updates = %d, want 3", len(updates))
	}

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %d, want 1", len(lines))
	}
	if lines[0] != "Done!" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "Done!")
	}
}
