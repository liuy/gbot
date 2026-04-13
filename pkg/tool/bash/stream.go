package bash

import (
	"bytes"
	"slices"
	"sync"
)

// ---------------------------------------------------------------------------
// Streaming output — progress events during command execution
// Source: TaskOutput.ts — memory cap + rolling window
// ---------------------------------------------------------------------------

// streamingLastLines is the rolling window size for recent output lines.
// Source: TaskOutput.ts — CircularBuffer(1000), getRecent(5) for progress
const streamingLastLines = 20

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
// Two separate tracking mechanisms:
//   - lines: full output buffer, capped at MaxOutputSize (for LLM consumption)
//   - lastLines: rolling window of last 20 lines, never capped (for TUI progress)
//
// Source: TaskOutput.ts — memory cap + rolling window, ShellCommand.ts — size watchdog
type StreamingOutput struct {
	mu          sync.Mutex
	lines       []string // full output, capped at MaxOutputSize bytes
	totalBytes  int64    // always tracks total bytes
	totalLines  int      // always tracks complete line count (newlines seen)
	lastLines   []string // rolling window of last 20 lines, never capped
	exceeded    bool     // true if lines buffer reached MaxOutputSize
	partialLine bool     // tracks mid-line state across Write calls
	onProgress  func(StreamingUpdate)
}

// NewStreamingOutput creates a new StreamingOutput with the given progress callback.
func NewStreamingOutput(onProgress func(StreamingUpdate)) *StreamingOutput {
	return &StreamingOutput{
		onProgress: onProgress,
	}
}

// Write appends output data, splits on newlines, and calls onProgress.
// After lines buffer exceeds MaxOutputSize, lines stops growing but lastLines
// continues updating so TUI progress never stalls.
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

	// Split on newlines, accumulate lines
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
			if !s.exceeded && len(s.lines) > 0 {
				s.lines[len(s.lines)-1] += text
			}
			if len(s.lastLines) > 0 {
				s.lastLines[len(s.lastLines)-1] += text
			}
		} else {
			// New line
			if !s.exceeded {
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

	// Check cap for lines buffer after processing
	// Source: outputLimits.ts — BASH_MAX_OUTPUT_DEFAULT = 30_000
	if !s.exceeded && s.totalBytes > int64(MaxOutputSize) {
		s.exceeded = true
	}

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

// Exceeded returns true if the output exceeded MaxOutputSize.
func (s *StreamingOutput) Exceeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exceeded
}

// Lines returns all accumulated lines (up to MaxOutputSize).
func (s *StreamingOutput) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

// TotalBytes returns the total bytes written.
func (s *StreamingOutput) TotalBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalBytes
}

// String returns all accumulated output as a single string.
func (s *StreamingOutput) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var buf bytes.Buffer
	for i, line := range s.lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	return buf.String()
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
