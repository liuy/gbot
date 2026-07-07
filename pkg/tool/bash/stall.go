package bash

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Stall detection — watchdog for interactive prompts in background jobs
// Source: LocalShellTask.tsx:24-104
// ---------------------------------------------------------------------------

// Stall detection constants.
// Source: LocalShellTask.tsx:24-26
const (
	stallCheckInterval = 5 * time.Second
	stallThreshold     = 45 * time.Second
	stallTailBytes     = 1024
)

// drainStallThreshold is the production stall gate duration.
const drainStallThreshold = 3 * time.Second

// drainStallThresholdVar allows tests to shorten the stall gate.
// Guarded by drainStallMu to avoid data races under -race with parallel tests.
var (
	drainStallMu           sync.Mutex
	drainStallThresholdVar = drainStallThreshold
)

// SetDrainStallThreshold overrides the drain-level stall gate.
// Exported for cross-package tests (e.g. engine integration tests).
func SetDrainStallThreshold(d time.Duration) {
	drainStallMu.Lock()
	drainStallThresholdVar = d
	drainStallMu.Unlock()
}

// getDrainStallThreshold returns the current stall gate.
func getDrainStallThreshold() time.Duration {
	drainStallMu.Lock()
	defer drainStallMu.Unlock()
	return drainStallThresholdVar
}

// passwordPattern matches common sudo password prompts.
// Only used after drainStallThreshold confirms the output actually stopped.
var passwordPattern = regexp.MustCompile(`(?i)\bPassword\b.*:\s*$`)

// looksLikePrompt checks whether the tail of the output looks like an
// interactive prompt the model can act on.
//
// In Drain: gated by a 3-second stall timer — never runs during streaming
// output, only when the process is genuinely blocked waiting for input.
// In stall watcher: gated by stallThreshold (45s) for background jobs.
func looksLikePrompt(tail string) bool {
	lastLine := lastNonEmptyLine(tail)
	return passwordPattern.MatchString(lastLine)
}

// isPasswordPrompt checks whether the tail looks like a password prompt
// (masked input). Used by PTYSession.Drain to determine if the InputDialog
// should mask user input.
func isPasswordPrompt(tail string) bool {
	return looksLikePrompt(tail)
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
	job        *BackgroundJob
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
func watchForStallStream(job *BackgroundJob, onStall func(summary, tail string)) func() {
	w := &streamStallWatcher{
		job:        job,
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
	if w.job.Output != nil {
		size = w.job.Output.TotalBytes()
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
	if w.job.Output != nil {
		tail = w.job.Output.LastLines()
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
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}

	size := info.Size()
	if size == 0 {
		return ""
	}

	offset := max(size-int64(maxBytes), 0)
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
