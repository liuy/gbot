package bash

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// mustWriteStream asserts that output.Write(data) succeeds.
func mustWriteStream(t *testing.T, output *StreamingOutput, data []byte) {
	t.Helper()
	if _, err := output.Write(data); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// looksLikePrompt
// ---------------------------------------------------------------------------

func TestLooksLikePrompt_YN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Continue? (y/n)", true},
		{"Continue? (Y/N)", true},
		{"Continue? (y/N)", true},
		{"Do you want to proceed? (y/n) ", true},
		{"random output", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_BracketYN(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Continue? [y/n]", true},
		{"Continue? [Y/N]", true},
		{"Proceed [y/N] ", true},
		{"no prompt here", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_YesNo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Continue? (yes/no)", true},
		{"(Yes/No) confirm?", true},
		{"no match", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_DirectedQuestions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Do you want to continue?", true},
		{"Would you like to proceed?", true},
		{"Shall I continue?", true},
		{"Are you sure about this?", true},
		{"Ready to deploy?", true},
		{"Do something", false}, // no question mark
		{"This is fine", false}, // no pattern word
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_PressAnyKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Press any key to continue", true},
		{"Press Enter to proceed", true},
		{"press any key", true},
		{"press something else", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_Continue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Continue?", true},
		{"continue?", true},
		{"CONTINUE?", true},
		{"Continue", false}, // no question mark
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_Overwrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Overwrite?", true},
		{"overwrite?", true},
		{"OVERWRITE?", true},
		{"Overwrite file.txt", false}, // no question mark
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_MultilineTail(t *testing.T) {
	t.Parallel()
	// Only the last line should be checked
	input := "line 1\nline 2\nline 3\nContinue? (y/n)"
	if !looksLikePrompt(input) {
		t.Error("should detect prompt in last line of multiline output")
	}

	input2 := "Continue? (y/n)\nline 1\nline 2"
	if looksLikePrompt(input2) {
		t.Error("should NOT detect prompt when it's not the last line")
	}
}

func TestLooksLikePrompt_NonPrompt(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"Building project...",
		"Tests passed: 42/42",
		"Compiling main.go",
		"done",
		"127.0.0.1 - - [12/Apr/2026] GET /",
		"",
		"   ",
	}
	for _, input := range inputs {
		if looksLikePrompt(input) {
			t.Errorf("looksLikePrompt(%q) = true, want false", input)
		}
	}
}

// ---------------------------------------------------------------------------
// lastNonEmptyLine
// ---------------------------------------------------------------------------

func TestLastNonEmptyLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"line1\nline2", "line2"},
		{"line1\nline2\n", "line2"},
		{"line1\nline2\n  ", "line2"},
		{"", ""},
		{"\n\n", ""},
	}
	for _, tt := range tests {
		if got := lastNonEmptyLine(tt.input); got != tt.want {
			t.Errorf("lastNonEmptyLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// watchForStall
// ---------------------------------------------------------------------------

func TestWatchForStall_DetectsPrompt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {

		// Create a temp output file with prompt content
		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.txt")

		// Write initial content
		if err := os.WriteFile(outputPath, []byte("Building...\nCompiling...\nContinue? (y/n)"), 0o644); err != nil {
			t.Fatal(err)
		}

		detected := make(chan string, 1)
		cancel := watchForStall(outputPath, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		select {
		case summary := <-detected:
			cancel()
			if summary != "appears to be waiting for interactive input" {
				t.Errorf("summary = %q, want stall message", summary)
			}
		case <-time.After(60 * time.Second):
			cancel()
			t.Fatal("watchForStall did not detect prompt within timeout")
		}
	})
}

func TestWatchForStall_NoPrompt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {

		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.txt")

		// Write non-prompt content
		if err := os.WriteFile(outputPath, []byte("Building...\nCompiling...\nTests passed"), 0o644); err != nil {
			t.Fatal(err)
		}

		detected := make(chan string, 1)
		cancel := watchForStall(outputPath, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		// Should not detect stall for non-prompt output
		select {
		case <-detected:
			cancel()
			t.Error("should not detect stall for non-prompt output")
		case <-time.After(55 * time.Second):
			// Expected — no detection within one stall threshold cycle
			cancel()
		}
	})
}

func TestWatchForStall_CancelStops(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {

		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.txt")
		if err := os.WriteFile(outputPath, []byte("Continue? (y/n)"), 0o644); err != nil {
			t.Fatal(err)
		}

		called := false
		cancel := watchForStall(outputPath, func(summary, tail string) {
			called = true
		})

		// Cancel immediately
		cancel()

		// Wait a bit to ensure the goroutine has exited
		time.Sleep(stallCheckInterval + 100*time.Millisecond)

		if called {
			t.Error("onStall should not be called after cancel")
		}
	})
}

func TestWatchForStall_OutputGrowth(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {

		dir := t.TempDir()
		outputPath := filepath.Join(dir, "output.txt")
		if err := os.WriteFile(outputPath, []byte("Continue? (y/n)"), 0o644); err != nil {
			t.Fatal(err)
		}

		detected := make(chan string, 1)

		// Continuously grow the file to prevent stall detection
		cancel := watchForStall(outputPath, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		// Keep growing the file
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 15 {
				time.Sleep(3 * time.Second)
				f, err := os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					return
				}
				_, _ = f.WriteString("more output\n")
				_ = f.Close()
			}
		}()

		select {
		case <-detected:
			cancel()
			t.Error("should not detect stall while output is growing")
		case <-done:
			cancel()
			// Expected — growth prevented stall detection
		}
	})
}

// ---------------------------------------------------------------------------
// readTail
// ---------------------------------------------------------------------------

func TestReadTail(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "hello world"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readTail(path, 1024)
	if got != content {
		t.Errorf("readTail() = %q, want %q", got, content)
	}
}

func TestReadTail_Truncate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := "0123456789"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readTail(path, 5)
	if got != "56789" {
		t.Errorf("readTail(5) = %q, want %q", got, "56789")
	}
}

func TestReadTail_Nonexistent(t *testing.T) {
	t.Parallel()
	got := readTail("/nonexistent/file.txt", 1024)
	if got != "" {
		t.Errorf("readTail() on nonexistent = %q, want empty", got)
	}
}

func TestReadTail_Empty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	got := readTail(path, 1024)
	if got != "" {
		t.Errorf("readTail() on empty = %q, want empty", got)
	}
}

func TestReadTail_SmallFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	// File smaller than maxBytes — offset should be 0
	content := "hi"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := readTail(path, 1024)
	if got != content {
		t.Errorf("readTail() = %q, want %q", got, content)
	}
}

func TestStallWatcher_Check_CancelledAfterReadTail(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "Continue? (y/n)"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)),
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    nil,
	}
	w.cancelled.Store(true) // cancelled before readTail returns

	// cancelled=true causes early return in check() before onStall
	stop := w.check()
	if !stop {
		t.Error("should stop when cancelled")
	}
}

func TestReadTail_StatError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nodir", "file.txt")
	// Directory doesn't exist, so Open will fail
	got := readTail(path, 1024)
	if got != "" {
		// May return empty or partial — just shouldn't panic
		t.Logf("readTail on missing dir returned %q", got)
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestStallConstants(t *testing.T) {
	t.Parallel()
	if stallCheckInterval != 5*time.Second {
		t.Errorf("stallCheckInterval = %v, want 5s", stallCheckInterval)
	}
	if stallThreshold != 45*time.Second {
		t.Errorf("stallThreshold = %v, want 45s", stallThreshold)
	}
	if stallTailBytes != 1024 {
		t.Errorf("stallTailBytes = %d, want 1024", stallTailBytes)
	}
}

func TestPromptPatternsCount(t *testing.T) {
	t.Parallel()
	if len(promptPatterns) != 8 {
		t.Errorf("len(promptPatterns) = %d, want 8", len(promptPatterns))
	}
}

// ---------------------------------------------------------------------------
// stallWatcher.check — unit test without real timers
// ---------------------------------------------------------------------------

func TestStallWatcher_Check_OutputGrows(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &stallWatcher{
		outputPath: path,
		lastGrowth: time.Now(),
		onStall:    func(summary, tail string) {},
	}

	// First check should detect the file size
	stop := w.check()
	if stop {
		t.Error("should not stop on first check")
	}
	if w.lastSize != 5 {
		t.Errorf("lastSize = %d, want 5", w.lastSize)
	}
}

func TestStallWatcher_Check_NoFile(t *testing.T) {
	t.Parallel()

	// TS: stat failure → empty catch () => {} — does NOT reset lastGrowth.
	// If file never appears, stall should eventually trigger after threshold.
	past := time.Now().Add(-60 * time.Second)
	w := &stallWatcher{
		outputPath: "/nonexistent/file",
		lastGrowth: past,
		onStall:    func(summary, tail string) {},
	}

	stop := w.check()
	if stop {
		t.Error("should not stop when file doesn't exist")
	}
	// Key assertion: lastGrowth should NOT be reset on stat failure (TS alignment)
	if w.lastGrowth != past {
		t.Errorf("lastGrowth was reset on stat failure, want it preserved at %v", past)
	}
}

func TestStallWatcher_Check_Cancelled(t *testing.T) {
	t.Parallel()

	w := &stallWatcher{
		outputPath: "/dev/null",
		onStall:    func(summary, tail string) {},
	}
	w.cancelled.Store(true)

	stop := w.check()
	if !stop {
		t.Error("should stop when cancelled")
	}
}

func TestStallWatcher_Check_StalledNoPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "building project...\ncompiling..."
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)),               // already know the file size
		lastGrowth: time.Now().Add(-60 * time.Second), // past threshold
		onStall:    func(summary, tail string) { t.Error("should not call onStall for non-prompt") },
	}

	stop := w.check()
	if stop {
		t.Error("should not stop for non-prompt stall")
	}
	// Should have reset lastGrowth
	if time.Since(w.lastGrowth) > time.Second {
		t.Error("should have reset lastGrowth")
	}
}

func TestStallWatcher_Check_StalledWithPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "Continue? (y/n)"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	stalled := false
	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)),               // already know the file size
		lastGrowth: time.Now().Add(-60 * time.Second), // past threshold
		onStall: func(summary, tail string) {
			stalled = true
		},
	}

	stop := w.check()
	if !stop {
		t.Error("should stop for stalled prompt")
	}
	if !stalled {
		t.Error("onStall should have been called")
	}
}

func TestStallWatcher_Check_StalledWithPrompt_NilCallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "Continue? (y/n)"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)),
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    nil,
	}

	// Should not panic with nil callback
	stop := w.check()
	if !stop {
		t.Error("should stop for stalled prompt even with nil callback")
	}
}

func TestStallWatcher_Check_UnderThreshold(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(path, []byte("Continue? (y/n)"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := &stallWatcher{
		outputPath: path,
		lastGrowth: time.Now(), // just now — under threshold
		onStall:    func(summary, tail string) { t.Error("should not stall under threshold") },
	}

	stop := w.check()
	if stop {
		t.Error("should not stop under threshold")
	}
}

func TestStallWatcher_Check_CancelledAfterPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	_ = os.WriteFile(path, []byte("Continue? (y/n)"), 0o644)

	w := &stallWatcher{
		outputPath: path,
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    nil,
	}
	w.cancelled.Store(true)

	// Cancelled before check — should stop without calling onStall
	stop := w.check()
	if !stop {
		t.Error("should stop when cancelled")
	}
}

func TestLooksLikePrompt_Password(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"Password:", true},
		{"password:", true},
		{"Enter Password:", true},
		{"passwords are secret", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestReadTail_ReadAtError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/unreadable.txt"
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	// Make file unreadable
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod() error: %v", err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()

	got := readTail(path, 1024)
	if got != "" {
		t.Errorf("readTail() on unreadable = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// streamStallWatcher.check — unit tests (no real timers needed)
// ---------------------------------------------------------------------------

func TestStreamStallWatcher_Check_OutputGrows(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("hello"))
	task := &BackgroundTask{Output: output}

	w := &streamStallWatcher{
		task:       task,
		lastGrowth: time.Now(),
		onStall:    func(summary, tail string) {},
	}

	stop := w.check()
	if stop {
		t.Error("should not stop on first check")
	}
	if w.lastSize != 5 {
		t.Errorf("lastSize = %d, want 5", w.lastSize)
	}
}

func TestStreamStallWatcher_Check_NilOutput(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-60 * time.Second)
	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: nil},
		lastGrowth: past,
		onStall:    func(summary, tail string) {},
	}

	// Nil output → size=0, no growth, past threshold, but tail="" which is not a prompt
	stop := w.check()
	if stop {
		t.Error("should not stop when tail is empty (not a prompt)")
	}
}

func TestStreamStallWatcher_Check_Cancelled(t *testing.T) {
	t.Parallel()
	w := &streamStallWatcher{
		task:    &BackgroundTask{},
		onStall: func(summary, tail string) {},
	}
	w.cancelled.Store(true)

	stop := w.check()
	if !stop {
		t.Error("should stop when cancelled")
	}
}

func TestStreamStallWatcher_Check_UnderThreshold(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("Continue? (y/n)"))

	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: output},
		lastGrowth: time.Now(), // just now — under threshold
		onStall:    func(summary, tail string) { t.Error("should not stall") },
	}

	stop := w.check()
	if stop {
		t.Error("should not stop under threshold")
	}
}

func TestStreamStallWatcher_Check_StalledNoPrompt(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("building...\ncompiling..."))

	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: output},
		lastSize:   100, // force no growth
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    func(summary, tail string) { t.Error("should not stall for non-prompt") },
	}

	stop := w.check()
	if stop {
		t.Error("should not stop for non-prompt stall")
	}
	if time.Since(w.lastGrowth) > time.Second {
		t.Error("should have reset lastGrowth")
	}
}

func TestStreamStallWatcher_Check_StalledWithPrompt(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("Continue? (y/n)"))

	stalled := false
	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: output},
		lastSize:   100, // force no growth
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall: func(summary, tail string) {
			stalled = true
		},
	}

	stop := w.check()
	if !stop {
		t.Error("should stop for stalled prompt")
	}
	if !stalled {
		t.Error("onStall should have been called")
	}
}

func TestStreamStallWatcher_Check_StalledWithPrompt_NilCallback(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("Continue? (y/n)"))

	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: output},
		lastSize:   100,
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    nil,
	}

	stop := w.check()
	if !stop {
		t.Error("should stop even with nil callback")
	}
}

func TestStreamStallWatcher_Check_CancelledAfterReadTail(t *testing.T) {
	t.Parallel()
	output := NewStreamingOutput(nil)
	mustWriteStream(t, output, []byte("Continue? (y/n)"))

	w := &streamStallWatcher{
		task:       &BackgroundTask{Output: output},
		lastSize:   100,
		lastGrowth: time.Now().Add(-60 * time.Second),
		onStall:    nil,
	}
	w.cancelled.Store(true)

	stop := w.check()
	if !stop {
		t.Error("should stop when cancelled after readTail")
	}
}

// ---------------------------------------------------------------------------
// watchForStallStream — synctest integration tests
// ---------------------------------------------------------------------------

func TestWatchForStallStream_DetectsPrompt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Building...\nContinue? (y/n)"))

		task := &BackgroundTask{
			Output: output,
			Kind:   "bash",
		}

		detected := make(chan string, 1)
		cancel := watchForStallStream(task, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		select {
		case summary := <-detected:
			cancel()
			if summary != "appears to be waiting for interactive input" {
				t.Errorf("summary = %q, want stall message", summary)
			}
		case <-time.After(60 * time.Second):
			cancel()
			t.Fatal("watchForStallStream did not detect prompt within timeout")
		}
	})
}

func TestWatchForStallStream_CancelStops(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		task := &BackgroundTask{
			Output: output,
			Kind:   "bash",
		}

		calledCh := make(chan struct{}, 1)
		cancel := watchForStallStream(task, func(summary, tail string) {
			select {
			case calledCh <- struct{}{}:
			default:
			}
		})

		cancel()

		time.Sleep(stallCheckInterval + 100*time.Millisecond)

		select {
		case <-calledCh:
			t.Error("onStall should not be called after cancel")
		default:
		}
	})
}

func TestWatchForStallStream_NoPrompt(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Building...\nCompiling...\nTests passed"))

		task := &BackgroundTask{
			Output: output,
			Kind:   "bash",
		}

		detected := make(chan string, 1)
		cancel := watchForStallStream(task, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		select {
		case <-detected:
			cancel()
			t.Error("should not detect stall for non-prompt output")
		case <-time.After(55 * time.Second):
			cancel()
		}
	})
}

func TestWatchForStallStream_OutputGrowth(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		task := &BackgroundTask{
			Output: output,
			Kind:   "bash",
		}

		detected := make(chan string, 1)
		cancel := watchForStallStream(task, func(summary, tail string) {
			select {
			case detected <- summary:
			default:
			}
		})

		// Keep growing the output to prevent stall detection
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 15 {
				time.Sleep(3 * time.Second)
				mustWriteStream(t, output, []byte("more output\n"))
			}
		}()

		select {
		case <-detected:
			cancel()
			t.Error("should not detect stall while output is growing")
		case <-done:
			cancel()
		}
	})
}

// ---------------------------------------------------------------------------
// startStallWatchdog — integration via synctest
// ---------------------------------------------------------------------------

func TestStartStallWatchdog_FiresNotification(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		receivedCh := make(chan TaskNotification, 1)
		task := &BackgroundTask{
			Output:      output,
			Kind:        "bash",
			ID:          "bg-test",
			Command:     "test-cmd",
			Description: "test desc",
			onNotify: func(n TaskNotification) {
				select {
				case receivedCh <- n:
				default:
				}
			},
		}

		task.startStallWatchdog()
		if task.cancelStall == nil {
			t.Fatal("cancelStall should be set")
		}

		// Wait for stall to fire
		time.Sleep(stallThreshold + stallCheckInterval + time.Second)

		select {
		case received := <-receivedCh:
			if received.TaskID != "bg-test" {
				t.Errorf("TaskID = %q, want bg-test", received.TaskID)
			}
			if !received.IsStall {
				t.Error("notification should be stall")
			}
			if !contains(received.Summary, "test desc") {
				t.Errorf("Summary should contain description, got %q", received.Summary)
			}
		default:
			t.Fatal("no notification received")
		}

		task.cancelStall()
	})
}

func TestStartStallWatchdog_SkipsAlreadyNotified(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		calledCh := make(chan struct{}, 1)
		task := &BackgroundTask{
			Output:   output,
			Kind:     "bash",
			ID:       "bg-test",
			Notified: true, // already notified — stall callback should bail
			onNotify: func(n TaskNotification) {
				select {
				case calledCh <- struct{}{}:
				default:
				}
			},
		}

		task.startStallWatchdog()

		time.Sleep(stallThreshold + stallCheckInterval + time.Second)

		select {
		case <-calledCh:
			t.Error("should not send notification when already notified")
		default:
			// Expected
		}

		task.cancelStall()
	})
}

func TestStartStallWatchdog_UsesCommandWhenNoDescription(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		receivedCh := make(chan TaskNotification, 1)
		task := &BackgroundTask{
			Output:  output,
			Kind:    "bash",
			ID:      "bg-test",
			Command: "my-command",
			onNotify: func(n TaskNotification) {
				select {
				case receivedCh <- n:
				default:
				}
			},
		}

		task.startStallWatchdog()

		time.Sleep(stallThreshold + stallCheckInterval + time.Second)

		select {
		case received := <-receivedCh:
			if !contains(received.Summary, "my-command") {
				t.Errorf("Summary should use command when no description, got %q", received.Summary)
			}
		default:
			t.Fatal("no notification received")
		}

		task.cancelStall()
	})
}

func TestStartStallWatchdog_NilOnNotify(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		output := NewStreamingOutput(nil)
		mustWriteStream(t, output, []byte("Continue? (y/n)"))

		task := &BackgroundTask{
			Output:   output,
			Kind:     "bash",
			ID:       "bg-test",
			Command:  "cmd",
			onNotify: nil,
		}

		task.startStallWatchdog()

		// Should not panic
		time.Sleep(stallThreshold + stallCheckInterval + time.Second)

		task.cancelStall()
	})
}
