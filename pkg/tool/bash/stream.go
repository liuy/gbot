package bash

import (
	"bytes"
	"slices"
	"sync"
)

// ---------------------------------------------------------------------------
// Streaming output — progress events during command execution
// Source: ShellCommand.ts streaming output, TaskOutput.ts
// ---------------------------------------------------------------------------

// maxOutputBytes is the maximum output size before the size watchdog triggers.
// Source: ShellCommand.ts:239-261, diskOutput.ts:30 — MAX_TASK_OUTPUT_BYTES = 5GB
var maxOutputBytes int64 = 5 * 1024 * 1024 * 1024 // 5GB

// streamingLastLines is the rolling window size for recent output lines.
const streamingLastLines = 20

// StreamingUpdate is sent on the progress channel during command execution.
// Source: BashTool.tsx:826 — runShellCommand() yields progress events.
type StreamingUpdate struct {
	Lines        []string // last ~20 lines
	TotalLines   int
	TotalBytes   int64
	IsIncomplete bool // true while command is running, false on completion
}

// StreamingOutput accumulates command output and reports progress.
// Thread-safe for concurrent Write and Read operations.
//
// Source: ShellCommand.ts — streaming output accumulates lines and bytes,
// TaskOutput.ts — tracks total output size for size watchdog.
type StreamingOutput struct {
	mu          sync.Mutex
	lines       []string
	totalBytes  int64
	lastLines   []string // rolling window of last 20
	exceeded    bool     // true if totalBytes > maxOutputBytes
	partialLine bool     // true if last element in lines is an unterminated fragment
	onProgress  func(StreamingUpdate)
}

// NewStreamingOutput creates a new StreamingOutput with the given progress callback.
func NewStreamingOutput(onProgress func(StreamingUpdate)) *StreamingOutput {
	return &StreamingOutput{
		onProgress: onProgress,
	}
}

// Write appends output data, splits on newlines, and calls onProgress.
// Returns the number of bytes written (always len(p)) and any error.
// Thread-safe.
//
// Source: ShellCommand.ts:239-261 — size watchdog checks per write.
// Source: TaskOutput.ts — line accumulation with rolling window.
func (s *StreamingOutput) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalBytes += int64(len(p))

	// Size watchdog (Step 2.4)
	// Source: ShellCommand.ts:239-261 — stat output file, kill if > MAX_TASK_OUTPUT_BYTES
	if s.totalBytes > maxOutputBytes {
		s.exceeded = true
	}

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

		if s.partialLine && len(s.lines) > 0 {
			// Append to existing partial line
			s.lines[len(s.lines)-1] += text
			if len(s.lastLines) > 0 {
				s.lastLines[len(s.lastLines)-1] += text
			}
		} else {
			// New line
			s.lines = append(s.lines, text)
			s.lastLines = append(s.lastLines, text)
		}

		// If this is the last fragment (no trailing \n), mark as partial
		s.partialLine = isLast

		if len(s.lastLines) > streamingLastLines {
			s.lastLines = s.lastLines[len(s.lastLines)-streamingLastLines:]
		}
	}

	if s.onProgress != nil {
		s.onProgress(StreamingUpdate{
			Lines:        slices.Clone(s.lastLines),
			TotalLines:   len(s.lines),
			TotalBytes:   s.totalBytes,
			IsIncomplete: true,
		})
	}
	return len(p), nil
}

// Exceeded returns true if the output exceeded maxOutputBytes.
// Source: ShellCommand.ts:239-261 — size watchdog check.
func (s *StreamingOutput) Exceeded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exceeded
}

// Lines returns all accumulated lines.
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
			TotalLines:   len(s.lines),
			TotalBytes:   s.totalBytes,
			IsIncomplete: false,
		})
	}
}
