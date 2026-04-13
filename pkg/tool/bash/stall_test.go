package bash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
		{"Do something", false},        // no question mark
		{"This is fine", false},        // no pattern word
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
}

func TestWatchForStall_NoPrompt(t *testing.T) {
	t.Parallel()

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
}

func TestWatchForStall_CancelStops(t *testing.T) {
	t.Parallel()

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
}

func TestWatchForStall_OutputGrowth(t *testing.T) {
	t.Parallel()

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
		for i := 0; i < 15; i++ {
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
	_ = os.WriteFile(path, []byte(content), 0o644)

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
	if len(promptPatterns) != 10 {
		t.Errorf("len(promptPatterns) = %d, want 10", len(promptPatterns))
	}
}

// ---------------------------------------------------------------------------
// stallWatcher.check — unit test without real timers
// ---------------------------------------------------------------------------

func TestStallWatcher_Check_OutputGrows(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	_ = os.WriteFile(path, []byte("hello"), 0o644)

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

	w := &stallWatcher{
		outputPath: "/nonexistent/file",
		lastGrowth: time.Now(),
		onStall:    func(summary, tail string) {},
	}

	// File doesn't exist — should reset growth time and continue
	stop := w.check()
	if stop {
		t.Error("should not stop when file doesn't exist")
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
	_ = os.WriteFile(path, []byte(content), 0o644)

	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)), // already know the file size
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
	_ = os.WriteFile(path, []byte(content), 0o644)

	stalled := false
	w := &stallWatcher{
		outputPath: path,
		lastSize:   int64(len(content)), // already know the file size
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
	_ = os.WriteFile(path, []byte(content), 0o644)

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
	_ = os.WriteFile(path, []byte("Continue? (y/n)"), 0o644)

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

func TestLooksLikePrompt_DONE(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"DONE", true},
		{"done", false},
		{"  DONE  ", true},
		{"UNDONE", false},
	}
	for _, tt := range tests {
		if got := looksLikePrompt(tt.input); got != tt.want {
			t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikePrompt_More(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  bool
	}{
		{"-- More --", true},
		{"-- More -- 75% --", true},
		{"more output", false},
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
	f, _ := os.Create(path)
	_ = f.Close()
	// Make file unreadable
	_ = os.Chmod(path, 0o000)
	defer func() { _ = os.Chmod(path, 0o644) }()

	got := readTail(path, 1024)
	if got != "" {
		t.Errorf("readTail() on unreadable = %q, want empty", got)
	}
}
