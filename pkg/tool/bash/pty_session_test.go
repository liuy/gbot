package bash

import (
	"testing"
)

// trackPartialLines is a pure-bytes helper used by Drain for partial-line
// detection. These tests run identically on Linux and Windows.
// Direct coverage of trackPartialLines — previously only exercised via
// the integration test for Drain's prompt-detection path.

func TestTrackPartialLines_Empty(t *testing.T) {
	t.Parallel()
	partial, lines := trackPartialLines(nil, false, nil)
	if partial {
		t.Errorf("partial = true, want false for empty input")
	}
	if len(lines) != 0 {
		t.Errorf("lines = %v, want empty", lines)
	}
}

func TestTrackPartialLines_SingleLineNoNewline(t *testing.T) {
	t.Parallel()
	partial, lines := trackPartialLines([]byte("hello"), false, nil)
	if !partial {
		t.Error("partial = false, want true (no trailing newline)")
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want 1 entry", lines)
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "hello")
	}
}

func TestTrackPartialLines_TrailingNewline(t *testing.T) {
	t.Parallel()
	partial, lines := trackPartialLines([]byte("hello\n"), false, nil)
	if partial {
		t.Error("partial = true, want false (trailing newline terminates line)")
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want 1 entry", lines)
	}
	if lines[0] != "hello" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "hello")
	}
}

func TestTrackPartialLines_MultipleLines(t *testing.T) {
	t.Parallel()
	partial, lines := trackPartialLines([]byte("line1\nline2\nline3"), false, nil)
	if !partial {
		t.Error("partial = false, want true (last line has no newline)")
	}
	if len(lines) != 3 {
		t.Fatalf("lines = %v, want 3 entries", lines)
	}
	want := []string{"line1", "line2", "line3"}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], w)
		}
	}
}

func TestTrackPartialLines_AppendsToPreviousPartial(t *testing.T) {
	t.Parallel()
	// Continuation: previous chunk ended partial with "par", new chunk "tial\nend"
	partial, lines := trackPartialLines([]byte("tial\nend"), true, []string{"par"})
	if !partial {
		t.Error("partial = false, want true (trailing 'end' has no newline)")
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2 entries", lines)
	}
	if lines[0] != "partial" {
		t.Errorf("lines[0] = %q, want %q (appended to previous partial)", lines[0], "partial")
	}
	if lines[1] != "end" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "end")
	}
}

func TestTrackPartialLines_TrimsRollingWindow(t *testing.T) {
	t.Parallel()
	// streamingLastLines is the window cap. Inject streamingLastLines+2 lines
	// and verify only the last streamingLastLines are kept.
	input := make([]byte, 0, (streamingLastLines+2)*6)
	for i := 0; i < streamingLastLines+2; i++ {
		input = append(input, []byte("aaaaa\n")...)
	}
	partial, lines := trackPartialLines(input, false, nil)
	if partial {
		t.Error("partial = true, want false (input ends with newline)")
	}
	if len(lines) != streamingLastLines {
		t.Errorf("len(lines) = %d, want %d (rolling window)", len(lines), streamingLastLines)
	}
}
