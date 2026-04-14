package bash

import (
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Stall detection — watchdog for interactive prompts in background tasks
// Source: LocalShellTask.tsx:24-104
// ---------------------------------------------------------------------------

// Stall detection constants.
// Source: LocalShellTask.tsx:24-26
const (
	stallCheckInterval = 5 * time.Second
	stallThreshold     = 45 * time.Second
	stallTailBytes     = 1024
)

// promptPatterns are last-line patterns that suggest a command is blocked
// waiting for keyboard input. Used to gate the stall notification.
// Source: LocalShellTask.tsx:28-38
var promptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\(y\/n\)`),
	regexp.MustCompile(`(?i)\[y\/n\]`),
	regexp.MustCompile(`(?i)\(yes\/no\)`),
	regexp.MustCompile(`(?i)\b(?:Do you|Would you|Shall I|Are you sure|Ready to)\b.*\? *$`),
	regexp.MustCompile(`(?i)Press (any key|Enter)`),
	regexp.MustCompile(`(?i)Continue\?`),
	regexp.MustCompile(`(?i)Overwrite\?`),
	regexp.MustCompile(`(?i)Password:`),
}

// looksLikePrompt checks whether the tail of the output looks like an
// interactive prompt the model can act on. It examines the last non-empty
// line against the prompt patterns.
//
// Source: LocalShellTask.tsx:39-42
func looksLikePrompt(tail string) bool {
	lastLine := lastNonEmptyLine(tail)
	for _, p := range promptPatterns {
		if p.MatchString(lastLine) {
			return true
		}
	}
	return false
}

// lastNonEmptyLine returns the last line from a multiline string after
// trimming trailing whitespace, matching the TS: tail.trimEnd().split('\n').pop()
func lastNonEmptyLine(tail string) string {
	trimmed := strings.TrimRight(tail, " \t\r\n")
	lines := strings.Split(trimmed, "\n")
	// strings.Split always returns at least one element, so len(lines) >= 1
	return lines[len(lines)-1]
}

// stallWatcher tracks output growth to detect stalled commands.
// Mirrors the closure state in LocalShellTask.tsx:46-104.
type stallWatcher struct {
	outputPath string
	lastSize   int64
	lastGrowth time.Time
	cancelled  atomic.Bool
	onStall    func(summary string, tail string)
}

// watchForStall starts a goroutine that monitors the output file at outputPath
// for stall conditions. If output stops growing for stallThreshold and the tail
// looks like a prompt, onStall is called.
//
// Returns a CancelFunc that stops the watchdog.
// Source: LocalShellTask.tsx:46-104
func watchForStall(outputPath string, onStall func(summary, tail string)) func() {
	w := &stallWatcher{
		outputPath: outputPath,
		lastGrowth: time.Now(),
		onStall:    onStall,
	}

	done := make(chan struct{})
	cancel := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(stallCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				if w.check() {
					return // stall detected, stop watching
				}
			}
		}
	}()

	return func() {
		w.cancelled.Store(true)
		close(cancel)
		<-done
	}
}

// check performs one stall check cycle.
// Source: LocalShellTask.tsx:52-98
func (w *stallWatcher) check() (stop bool) {
	if w.cancelled.Load() {
		return true
	}

	info, err := os.Stat(w.outputPath)
	if err != nil {
		// File may not exist yet — do NOT reset lastGrowth (TS: empty catch).
		// If file never appears, stall will trigger after threshold.
		return false
	}

	if info.Size() > w.lastSize {
		w.lastSize = info.Size()
		w.lastGrowth = time.Now()
		return false
	}

	if time.Since(w.lastGrowth) < stallThreshold {
		return false
	}

	// Output stalled — check if tail looks like a prompt
	tail := readTail(w.outputPath, stallTailBytes)
	if w.cancelled.Load() {
		return true
	}

	if !looksLikePrompt(tail) {
		// Not a prompt — keep watching. Reset so the next check is
		// stallThreshold out instead of re-reading the tail on every tick.
		// Source: LocalShellTask.tsx:65-68
		w.lastGrowth = time.Now()
		return false
	}

	// Stall detected with prompt — invoke callback and stop
	w.cancelled.Store(true)
	if w.onStall != nil {
		w.onStall("appears to be waiting for interactive input", tail)
	}
	return true
}

// ---------------------------------------------------------------------------
// Streaming stall detection — monitors StreamingOutput for stall conditions
// Same algorithm as watchForStall but uses StreamingOutput instead of file.
// Source: LocalShellTask.tsx:46-104 — startStallWatchdog
// ---------------------------------------------------------------------------

// streamStallWatcher tracks StreamingOutput growth to detect stalled commands.
type streamStallWatcher struct {
	task       *BackgroundTask
	lastSize   int64
	lastGrowth time.Time
	cancelled  atomic.Bool
	onStall    func(summary, tail string)
}

// watchForStallStream monitors a StreamingOutput for stall conditions.
// If output stops growing for stallThreshold and the tail looks like a prompt,
// onStall is called. Returns a CancelFunc that stops the watchdog.
//
// Source: LocalShellTask.tsx:46-104 — startStallWatchdog (same algorithm, streaming data source)
func watchForStallStream(task *BackgroundTask, onStall func(summary, tail string)) func() {
	w := &streamStallWatcher{
		task:       task,
		lastGrowth: time.Now(),
		onStall:    onStall,
	}

	done := make(chan struct{})
	cancel := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(stallCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-cancel:
				return
			case <-ticker.C:
				if w.check() {
					return // stall detected, stop watching
				}
			}
		}
	}()

	return func() {
		w.cancelled.Store(true)
		close(cancel)
		<-done
	}
}

// check performs one stall check cycle using StreamingOutput.
// Source: LocalShellTask.tsx:52-98 — same algorithm as stallWatcher.check
func (w *streamStallWatcher) check() (stop bool) {
	if w.cancelled.Load() {
		return true
	}

	// Source: LocalShellTask.tsx:53 — stat(outputPath).then(s => s.size)
	var size int64
	if w.task.Output != nil {
		size = w.task.Output.TotalBytes()
	}

	if size > w.lastSize {
		w.lastSize = size
		w.lastGrowth = time.Now()
		return false
	}

	if time.Since(w.lastGrowth) < stallThreshold {
		return false
	}

	// Output stalled — check if tail looks like a prompt
	// Source: LocalShellTask.tsx:60-61 — tailFile(outputPath, STALL_TAIL_BYTES)
	var tail string
	if w.task.Output != nil {
		lines := w.task.Output.Lines()
		tail = strings.Join(lines, "\n")
	}

	if w.cancelled.Load() {
		return true
	}

	if !looksLikePrompt(tail) {
		// Not a prompt — keep watching. Reset so next check is stallThreshold out.
		// Source: LocalShellTask.tsx:65-68
		w.lastGrowth = time.Now()
		return false
	}

	// Stall detected with prompt — invoke callback and stop
	w.cancelled.Store(true)
	if w.onStall != nil {
		w.onStall("appears to be waiting for interactive input", tail)
	}
	return true
}

// readTail reads the last N bytes from a file.
// Mirrors tailFile from utils/fsOperations.ts — uses loop read to handle short reads.
func readTail(path string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return ""
	}

	size := info.Size()
	if size == 0 {
		return ""
	}

	offset := size - int64(maxBytes)
	if offset < 0 {
		offset = 0
	}
	bytesToRead := int(size - offset)

	buf := make([]byte, bytesToRead)
	// TS: while (totalRead < bytesToRead) { read(...); if (bytesRead === 0) break; }
	totalRead := 0
	for totalRead < bytesToRead {
		n, err := f.ReadAt(buf[totalRead:], offset+int64(totalRead))
		if n == 0 {
			break
		}
		totalRead += n
		if err != nil {
			break
		}
	}
	return string(buf[:totalRead])
}
