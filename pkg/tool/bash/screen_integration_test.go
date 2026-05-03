package bash

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
)

// ---------------------------------------------------------------------------
// Chain tests: Screen → StreamingOutput full pipeline
// ---------------------------------------------------------------------------
// These tests wire Screen events to StreamingOutput exactly like the production
// call sites in bash.go (lines 304-311, 358-365, 829-836), then feed raw bytes
// through drainPTY to verify the complete Screen→StreamingOutput chain.
//
// No real PTY needed — uses dataThenEOFReader to simulate PTY output.

// newChainScreen creates a Screen wired to a StreamingOutput (production pattern).
func newChainScreen(s *StreamingOutput) *tool.Screen {
	return tool.NewScreen(func(ev tool.ScreenEvent) {
		switch ev.Kind {
		case tool.ScreenAppend:
			_, _ = s.Write([]byte(ev.Content + "\n"))
		case tool.ScreenReplace:
			s.ReplaceLastLine(ev.Content)
		}
	})
}

// newCollectorScreen creates a Screen wired to a []string collector (non-streaming pattern).
func newCollectorScreen(lines *[]string) *tool.Screen {
	return tool.NewScreen(func(ev tool.ScreenEvent) {
		switch ev.Kind {
		case tool.ScreenAppend:
			*lines = append(*lines, ev.Content)
		case tool.ScreenReplace:
			n := len(*lines)
			if n != 0 {
				(*lines)[n-1] = ev.Content
			}
		}
	})
}

func TestChainTest_ProgressBar(t *testing.T) {
	t.Parallel()

	var updates []StreamingUpdate
	s := NewStreamingOutput(func(u StreamingUpdate) {
		updates = append(updates, u)
	})
	screen := newChainScreen(s)

	// Simulate PTY output: progress bar updates then final message
	drainPTY(&dataThenEOFReader{data: []byte("Downloading 10%\rDownloading 50%\rDownloading 100%\nDone!\n")}, screen)

	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("Lines() = %v, want 2 lines", lines)
	}
	if lines[0] != "Downloading 100%" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "Downloading 100%")
	}
	if lines[1] != "Done!" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "Done!")
	}

	// Progress callbacks should fire: Append(10%), Replace(50%), Replace(100%), Append(Done!)
	// Each triggers either Write (fires callback) or ReplaceLastLine (fires callback)
	if len(updates) < 4 {
		t.Errorf("updates = %d, want at least 4", len(updates))
	}

	// Final update should have both lines visible
	lastUpdate := updates[len(updates)-1]
	found := slices.Contains(lastUpdate.Lines, "Done!")
	if !found {
		t.Errorf("last update should contain 'Done!', got %v", lastUpdate.Lines)
	}
}

func TestChainTest_AnsiColorPreserved(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	screen := newChainScreen(s)

	drainPTY(&dataThenEOFReader{data: []byte("\x1b[31mred\x1b[0m\n\x1b[32mgreen\x1b[0m\n")}, screen)

	lines := s.Lines()
	if len(lines) != 2 {
		t.Fatalf("Lines() = %v, want 2 lines", lines)
	}
	if lines[0] != "\x1b[31mred\x1b[0m" {
		t.Errorf("lines[0] = %q, want red with ANSI", lines[0])
	}
	if lines[1] != "\x1b[32mgreen\x1b[0m" {
		t.Errorf("lines[1] = %q, want green with ANSI", lines[1])
	}
}

func TestChainTest_MixedLinesAndCR(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	screen := newChainScreen(s)

	drainPTY(&dataThenEOFReader{data: []byte("line1\nprogress 10%\rprogress 100%\nline2\n")}, screen)

	lines := s.Lines()
	if len(lines) != 3 {
		t.Fatalf("Lines() = %v, want 3 lines", lines)
	}
	if lines[0] != "line1" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "line1")
	}
	if lines[1] != "progress 100%" {
		t.Errorf("lines[1] = %q, want %q", lines[1], "progress 100%")
	}
	if lines[2] != "line2" {
		t.Errorf("lines[2] = %q, want %q", lines[2], "line2")
	}
}

func TestChainTest_OutputLinesCollector(t *testing.T) {
	t.Parallel()

	// Simulates the non-streaming path in executePTY (bash.go:558-567)
	var outputLines []string
	screen := newCollectorScreen(&outputLines)

	drainPTY(&dataThenEOFReader{data: []byte("line1\nprog 10%\rprog 90%\rprog 100%\nline2\n")}, screen)

	output := strings.Join(outputLines, "\n")
	if len(outputLines) != 3 {
		t.Fatalf("outputLines = %v, want 3 lines", outputLines)
	}
	if outputLines[0] != "line1" {
		t.Errorf("outputLines[0] = %q, want %q", outputLines[0], "line1")
	}
	if outputLines[1] != "prog 100%" {
		t.Errorf("outputLines[1] = %q, want %q (replaced from 10pct to 90pct to 100pct)", outputLines[1], "prog 100%")
	}
	if outputLines[2] != "line2" {
		t.Errorf("outputLines[2] = %q, want %q", outputLines[2], "line2")
	}
	if !strings.Contains(output, "line1") || !strings.Contains(output, "prog 100%") || !strings.Contains(output, "line2") {
		t.Errorf("joined output = %q, missing expected content", output)
	}
}

func TestChainTest_Recovery_InterruptedEscape(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	screen := newChainScreen(s)

	// Simulate escape sequence split across two reads from PTY
	drainPTY(&chunkedReader{chunks: [][]byte{
		[]byte("\x1b[31"),
		[]byte("mhello\n"),
	}}, screen)

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %v, want 1 line", lines)
	}
	if lines[0] != "\x1b[31mhello" {
		t.Errorf("lines[0] = %q, want red-colored hello", lines[0])
	}
}

func TestChainTest_LargeProgress(t *testing.T) {
	t.Parallel()

	s := NewStreamingOutput(nil)
	screen := newChainScreen(s)

	// Build 100 progress updates + final line
	var input []byte
	for range 100 {
		input = append(input, []byte("progress...")...)
		input = append(input, '\r')
	}
	input = append(input, []byte("done!\n")...)

	drainPTY(&dataThenEOFReader{data: input}, screen)

	lines := s.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() = %v, want 1 line (all progress replaced)", lines)
	}
	// "progress..." was Append, then 99 Replaces, then "done!" is Replace
	// (because lineEmitted=true from last \r)
	if lines[0] != "done!" {
		t.Errorf("lines[0] = %q, want %q", lines[0], "done!")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// chunkedReader returns its chunks one at a time, simulating partial reads.
type chunkedReader struct {
	chunks [][]byte
	idx    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}
