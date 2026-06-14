package bash

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Streaming output — progress events during command execution
// Source: TaskOutput.ts — memory cap + rolling window + disk spill
// ---------------------------------------------------------------------------

// streamingLastLines is the rolling window size for recent output lines.
// Source: TaskOutput.ts — CircularBuffer(1000), getRecent(5) for progress
const streamingLastLines = 20

// spillThreshold is the memory threshold before spilling output to disk.
// Source: TaskOutput.ts — DEFAULT_MAX_MEMORY = 8 * 1024 * 1024
const spillThreshold = 8 * 1024 * 1024 // 8MB

// StreamingUpdate is sent on the progress channel during command execution.
// Source: BashTool.tsx:826 — runShellCommand() yields progress events.
type StreamingUpdate struct {
	Lines        []string // last ~20 lines (rolling window for TUI)
	TotalLines   int      // always tracks complete line count
	TotalBytes   int64    // always tracks total bytes written
	IsIncomplete bool     // true while command is running, false on completion
}

// StreamingOutput accumulates command output and reports progress.
// Thread-safe for concurrent Write and Read operations.
//
// Memory-first design (aligned with TS TaskOutput):
//   - Small output (< 8MB): accumulated in lines []string, no file I/O
//   - Large output (>= 8MB): automatically spills to temp file, lines freed
//   - lastLines rolling window always maintained for TUI progress
//
// Source: TaskOutput.ts — memory cap + disk spill, ShellCommand.ts — size watchdog
type StreamingOutput struct {
	mu          sync.Mutex
	lines       []string     // full output, memory mode only (nil after spill)
	rawBuf      bytes.Buffer // raw bytes copy for disk spill (cleared after spill)
	totalBytes  int64
	totalLines  int
	lastLines   []string // rolling window of last 20 lines
	partialLine bool
	onProgress  func(StreamingUpdate)

	// disk spill state
	spilled  bool
	file     *os.File
	filePath string
}

// NewStreamingOutput creates a new StreamingOutput with the given progress callback.
func NewStreamingOutput(onProgress func(StreamingUpdate)) *StreamingOutput {
	return &StreamingOutput{
		onProgress: onProgress,
	}
}

// Write appends output data, splits on newlines, and calls onProgress.
// In memory mode: accumulates in lines + rawBuf.
// After spill: writes raw bytes to file, only updates lastLines.
// Returns the number of bytes written (always len(p)) and any error.
// Thread-safe.
//
// Source: TaskOutput.ts:176-200 (#writeBuffered) — memory cap logic
// Source: outputLimits.ts — BASH_MAX_OUTPUT_DEFAULT = 30_000
func (s *StreamingOutput) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalBytes += int64(len(p))

	// Count complete lines (newlines) in this chunk
	// Source: TaskOutput.ts:216-236 — lineCount from lastIndexOf('\n')
	s.totalLines += bytes.Count(p, []byte{'\n'})

	// newline split — always runs to maintain lastLines + partialLine
	parts := bytes.Split(p, []byte{'\n'})
	for i, part := range parts {
		text := string(part)
		isLast := i == len(parts)-1

		// Trailing empty fragment from '\n' at end of input — no new line
		if text == "" && isLast {
			s.partialLine = false
			continue
		}

		if s.partialLine {
			// Append to existing partial line
			if !s.spilled && len(s.lines) > 0 {
				s.lines[len(s.lines)-1] += text
			}
			if len(s.lastLines) > 0 {
				s.lastLines[len(s.lastLines)-1] += text
			}
		} else {
			// New line
			if !s.spilled {
				s.lines = append(s.lines, text)
			}
			s.lastLines = append(s.lastLines, text)
		}

		s.partialLine = isLast

		// Trim rolling window
		if len(s.lastLines) > streamingLastLines {
			s.lastLines = s.lastLines[len(s.lastLines)-streamingLastLines:]
		}
	}

	// Write to target: file (spilled) or rawBuf (memory, pre-spill)
	if s.spilled {
		_, _ = s.file.Write(p)
	} else {
		s.rawBuf.Write(p) // accumulate raw bytes for potential spill
	}

	// Check if spill needed
	s.maybeSpill()

	// Always send progress (TUI needs continuous updates)
	if s.onProgress != nil {
		s.onProgress(StreamingUpdate{
			Lines:        slices.Clone(s.lastLines),
			TotalLines:   s.totalLines,
			TotalBytes:   s.totalBytes,
			IsIncomplete: true,
		})
	}
	return len(p), nil
}

// maybeSpill checks if output exceeds spillThreshold and writes to disk if so.
// Uses rawBuf for bulk write to avoid partialLine corruption from line-by-line reconstruction.
func (s *StreamingOutput) maybeSpill() {
	if s.spilled || s.totalBytes < spillThreshold {
		return
	}
	f, err := os.CreateTemp("", "gbot-output-")
	if err != nil {
		return // graceful degradation: stay in memory mode
	}
	// rawBuf contains all raw bytes written so far (correct newlines, no partialLine issues)
	if _, err := f.Write(s.rawBuf.Bytes()); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return
	}
	s.file = f
	s.filePath = f.Name()
	s.spilled = true
	s.lines = nil
	s.rawBuf = bytes.Buffer{} // release underlying byte slice (Reset only clears length)
}

// LastLines returns the rolling window of recent lines joined with newlines.
// Returns at most streamingLastLines (20) lines — much cheaper than Lines()
// for cases that only need the tail (e.g., stall detection).
func (s *StreamingOutput) LastLines() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lastLines, "\n")
}

// Lines returns all accumulated lines.
// Returns nil if output has spilled to disk (overflow not supported).
func (s *StreamingOutput) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spilled {
		return nil
	}
	return slices.Clone(s.lines)
}

// TotalBytes returns the total bytes written.
func (s *StreamingOutput) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}

// String returns accumulated output as a single string.
// After spill: returns first MaxOutputSize bytes via bounded file read.
func (s *StreamingOutput) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spilled {
		return s.readContentLocked(MaxOutputSize)
	}
	var buf bytes.Buffer
	for i, line := range s.lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	return buf.String()
}

// ReadContent returns the output content, capped at maxBytes.
// For spilled output, uses io.LimitReader to avoid loading entire file.
func (s *StreamingOutput) ReadContent(maxBytes int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readContentLocked(maxBytes)
}

// readContentLocked returns output content. Must be called with s.mu held.
func (s *StreamingOutput) readContentLocked(maxBytes int64) string {
	if !s.spilled {
		var buf bytes.Buffer
		for _, line := range s.lines {
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line)
		}
		return buf.String()
	}
	// Spilled: use LimitReader for bounded read (no OOM)
	f, err := os.Open(s.filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Get file size before reading (Stat doesn't depend on file offset)
	info, _ := f.Stat()
	fileSize := int64(0)
	if info != nil {
		fileSize = info.Size()
	}

	lr := io.LimitReader(f, maxBytes)
	data, err := io.ReadAll(lr)
	if err != nil {
		return ""
	}
	content := string(data)
	if fileSize > maxBytes {
		return strings.TrimRight(content, "\n") +
			"\n... [" + fmt.Sprintf("%d bytes truncated", fileSize-maxBytes) + "]"
	}
	return strings.TrimRight(content, "\n")
}

// FinalUpdate sends a final progress update with IsIncomplete=false.
func (s *StreamingOutput) FinalUpdate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onProgress != nil {
		s.onProgress(StreamingUpdate{
			Lines:        slices.Clone(s.lastLines),
			TotalLines:   s.totalLines,
			TotalBytes:   s.totalBytes,
			IsIncomplete: false,
		})
	}
}

// ReplaceLastLine replaces the last line in lastLines (and lines if memory mode).
// Used by Screen's Replace events to update progress bars in-place.
// After spill: only updates lastLines (file is append-only).
// Thread-safe.
func (s *StreamingOutput) ReplaceLastLine(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.spilled {
		// After spill: only update lastLines (file is append-only)
		if len(s.lastLines) > 0 {
			s.lastLines[len(s.lastLines)-1] = line
		} else {
			s.lastLines = append(s.lastLines, line)
		}
	} else {
		// Memory mode: update both lines and lastLines (original behavior)
		if len(s.lines) > 0 {
			s.lines[len(s.lines)-1] = line
		} else {
			s.lines = append(s.lines, line)
		}
		if len(s.lastLines) > 0 {
			s.lastLines[len(s.lastLines)-1] = line
		} else {
			s.lastLines = append(s.lastLines, line)
		}
	}
	s.totalBytes += int64(len(line))

	if s.onProgress != nil {
		s.onProgress(StreamingUpdate{
			Lines:        slices.Clone(s.lastLines),
			TotalLines:   s.totalLines,
			TotalBytes:   s.totalBytes,
			IsIncomplete: true,
		})
	}
}

// Spilled returns whether output has been spilled to disk.
func (s *StreamingOutput) Spilled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spilled
}

// IsPartialLine returns whether the output ends with an incomplete line
// (no trailing newline). Used by PTYSession.Drain for prompt detection.
func (s *StreamingOutput) IsPartialLine() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.partialLine
}

// Cleanup closes and removes the temp file if output was spilled to disk.
// Must be called exactly once: either from sync path return or background completion.
func (s *StreamingOutput) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
		_ = os.Remove(s.filePath)
		s.file = nil
		s.filePath = ""
	}
}
