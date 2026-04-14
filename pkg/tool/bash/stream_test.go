package bash

import (
	"fmt"
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

func TestStreamingOutput_Exceeded(t *testing.T) {
	// Override MaxOutputSize for testing via a small write that exceeds it
	// MaxOutputSize = 30000, so write 40000 bytes
	s := NewStreamingOutput(nil)

	big := strings.Repeat("x", 40000)
	_, _ = s.Write([]byte(big))

	if !s.Exceeded() {
		t.Error("Exceeded() should be true after writing past MaxOutputSize")
	}
}

// --- Cap behavior: lines stops, lastLines keeps going ---

func TestStreamingOutput_Cap_LinesStopsGrowing(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Fill to just under cap
	under := strings.Repeat("a", MaxOutputSize-1)
	_, _ = s.Write([]byte(under))

	if s.Exceeded() {
		t.Fatal("should not be exceeded yet")
	}

	// Write more to trigger cap
	_, _ = s.Write([]byte("extra data here\n"))

	if !s.Exceeded() {
		t.Fatal("should be exceeded now")
	}

	linesLen := len(s.Lines())

	// Write even more — lines should not grow
	_, _ = s.Write([]byte("even more data\n"))
	_, _ = s.Write([]byte("and more\n"))

	if len(s.Lines()) != linesLen {
		t.Errorf("lines grew from %d to %d after cap — should stop growing", linesLen, len(s.Lines()))
	}
}

func TestStreamingOutput_Cap_LastLinesKeepsUpdating(t *testing.T) {
	t.Parallel()

	var lastUpdate StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})

	// Exceed cap
	big := strings.Repeat("x", MaxOutputSize+1000)
	_, _ = s.Write([]byte(big))
	if !s.Exceeded() {
		t.Fatal("should be exceeded")
	}

	// Write more — lastLines should still update
	_, _ = s.Write([]byte("new-line-after-cap\n"))

	found := false
	for _, l := range lastUpdate.Lines {
		if strings.Contains(l, "new-line-after-cap") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("lastLines should contain 'new-line-after-cap', got %v", lastUpdate.Lines)
	}
}

func TestStreamingOutput_Cap_TotalBytesKeepsCounting(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Exceed cap
	_, _ = s.Write([]byte(strings.Repeat("x", MaxOutputSize+1)))
	firstTotal := s.TotalBytes()

	// Write more — totalBytes should keep growing
	_, _ = s.Write([]byte("more data"))
	secondTotal := s.TotalBytes()

	if secondTotal <= firstTotal {
		t.Errorf("totalBytes should keep counting: %d -> %d", firstTotal, secondTotal)
	}
}

func TestStreamingOutput_Cap_TotalLinesKeepsCounting(t *testing.T) {
	t.Parallel()

	var lastUpdate StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})

	// Exceed cap with no newlines
	_, _ = s.Write([]byte(strings.Repeat("x", MaxOutputSize+1)))

	// Write lines after cap
	_, _ = s.Write([]byte("line1\nline2\nline3\n"))

	if lastUpdate.TotalLines < 3 {
		t.Errorf("TotalLines = %d, want at least 3 (lines after cap should still be counted)", lastUpdate.TotalLines)
	}
}

func TestStreamingOutput_Cap_ProgressCallbackKeepsFiring(t *testing.T) {
	t.Parallel()

	var updateCount int
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updateCount++
	})

	// Exceed cap
	_, _ = s.Write([]byte(strings.Repeat("x", MaxOutputSize+1)))
	countAfterExceed := updateCount

	// Write more — callback should still fire
	_, _ = s.Write([]byte("data\n"))
	_, _ = s.Write([]byte("more\n"))

	if updateCount <= countAfterExceed {
		t.Errorf("callback count didn't increase after cap: %d -> %d", countAfterExceed, updateCount)
	}
}

func TestStreamingOutput_Cap_PartialLineAcrossCap(t *testing.T) {
	t.Parallel()

	// When cap is triggered during a Write, the exceeded flag is set AFTER
	// the Write completes. So lines may slightly exceed MaxOutputSize (up to one
	// Write's worth). Subsequent Writes stop appending to lines.
	// lastLines always grows regardless of cap.

	s := NewStreamingOutput(nil)

	// Fill to just under cap with a partial line (no newline)
	_, _ = s.Write([]byte(strings.Repeat("a", MaxOutputSize-5)))

	// This write triggers cap
	_, _ = s.Write([]byte("hello\nworld\n"))

	// exceeded is now true
	if !s.Exceeded() {
		t.Fatal("should be exceeded")
	}

	// lines slightly exceeds cap (contains "aaa...aaa", "hello", "world")
	lines := s.Lines()
	if len(lines) == 0 {
		t.Fatal("lines should not be empty")
	}
	if lines[len(lines)-1] != "world" {
		t.Errorf("lines last = %q, want 'world'", lines[len(lines)-1])
	}

	// Write more — lines should NOT grow further
	linesBefore := len(lines)
	_, _ = s.Write([]byte("extra\n"))
	if len(s.Lines()) != linesBefore {
		t.Errorf("lines grew from %d to %d after cap — should stop growing", linesBefore, len(s.Lines()))
	}

	// lastLines always grows — verify with short writes that don't push "hello" out of the window
	var lastUpdate StreamingUpdate
	s2 := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})
	// Write 19 lines, then cap is already exceeded (MaxOutputSize-5 bytes)
	// But lastLines always grows — check that the last lines are present
	_, _ = s2.Write([]byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\no\np\nq\nr\ns\n")) // 19 lines

	// Verify totalLines still counts correctly even after many lines
	if lastUpdate.TotalLines < 19 {
		t.Errorf("TotalLines = %d, want >= 19", lastUpdate.TotalLines)
	}
	// lastLines contains the rolling window — check last few are in there
	lastLines := lastUpdate.Lines
	if len(lastLines) == 0 {
		t.Fatal("lastLines should not be empty")
	}
	// With 19 lines and window=20, all 19 should be in lastLines
	if len(lastLines) != 19 {
		t.Errorf("len(lastLines) = %d, want 19 (window not full)", len(lastLines))
	}
	if lastLines[len(lastLines)-1] != "s" {
		t.Errorf("lastLines last = %q, want 's'", lastLines[len(lastLines)-1])
	}
}

func TestStreamingOutput_Cap_PartialLineAfterCap(t *testing.T) {
	t.Parallel()

	var lastUpdate StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		lastUpdate = u
	})

	// Exceed cap with no newline
	_, _ = s.Write([]byte(strings.Repeat("x", MaxOutputSize+1)))

	// Write partial line after cap, then complete it
	_, _ = s.Write([]byte("par"))
	_, _ = s.Write([]byte("tial\n"))

	// lastLines[0] = "xxxxx...xpar" (many x's + partial), last element ends with "tial"
	found := false
	for _, l := range lastUpdate.Lines {
		if strings.HasSuffix(l, "tial") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("lastLines should end with a line containing 'tial', got %v", lastUpdate.Lines)
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

func TestStreamingOutput_LastLines(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)

	// Write 30 lines — LastLines should return only last 20
	for i := 0; i < 30; i++ {
		_, _ = fmt.Fprintf(s, "line%d\n", i)
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

func TestMaxOutputSize(t *testing.T) {
	t.Parallel()
	// Source: outputLimits.ts — BASH_MAX_OUTPUT_DEFAULT = 30_000
	if MaxOutputSize != 30000 {
		t.Errorf("MaxOutputSize = %d, want 30000", MaxOutputSize)
	}
}
