package tui

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// visibleWidth
// ---------------------------------------------------------------------------

func TestVisibleWidth_PlainASCII(t *testing.T) {
	t.Parallel()
	if got := visibleWidth("hello"); got != 5 {
		t.Errorf("visibleWidth(%q) = %d, want 5", "hello", got)
	}
}

func TestVisibleWidth_Empty(t *testing.T) {
	t.Parallel()
	if got := visibleWidth(""); got != 0 {
		t.Errorf("visibleWidth(\"\") = %d, want 0", got)
	}
}

func TestVisibleWidth_AnsiCodes(t *testing.T) {
	t.Parallel()
	input := "\x1b[38;5;10mhello\x1b[0m"
	if got := visibleWidth(input); got != 5 {
		t.Errorf("visibleWidth with ANSI = %d, want 5", got)
	}
}

func TestVisibleWidth_CJK(t *testing.T) {
	t.Parallel()
	input := "你好"
	if got := visibleWidth(input); got != 4 {
		t.Errorf("visibleWidth(%q) = %d, want 4", input, got)
	}
}

func TestVisibleWidth_MixedANSIAndCJK(t *testing.T) {
	t.Parallel()
	input := "\x1b[1m你好world\x1b[22m"
	if got := visibleWidth(input); got != 9 {
		t.Errorf("visibleWidth = %d, want 9 (4 CJK + 5 ASCII)", got)
	}
}

func TestVisibleWidth_Tab(t *testing.T) {
	t.Parallel()
	input := "a\tb"
	if got := visibleWidth(input); got != 3 {
		t.Errorf("visibleWidth(%q) = %d, want 3", input, got)
	}
}

// ---------------------------------------------------------------------------
// isDiffMarker
// ---------------------------------------------------------------------------

func TestIsDiffMarker_AddedLine(t *testing.T) {
	t.Parallel()
	marker, ok := isDiffMarker(" 123 +content")
	if !ok || marker != '+' {
		t.Errorf("isDiffMarker = %c, %v; want '+', true", marker, ok)
	}
}

func TestIsDiffMarker_RemovedLine(t *testing.T) {
	t.Parallel()
	marker, ok := isDiffMarker(" 456 -old")
	if !ok || marker != '-' {
		t.Errorf("isDiffMarker = %c, %v; want '-', true", marker, ok)
	}
}

func TestIsDiffMarker_ContextLine(t *testing.T) {
	t.Parallel()
	marker, ok := isDiffMarker(" 789  ctx")
	if !ok || marker != ' ' {
		t.Errorf("isDiffMarker = %c, %v; want ' ', true", marker, ok)
	}
}

func TestIsDiffMarker_WithLeadingANSI(t *testing.T) {
	t.Parallel()
	marker, ok := isDiffMarker("\x1b[38;5;10m 123 +colored\x1b[0m")
	if !ok || marker != '+' {
		t.Errorf("isDiffMarker with ANSI = %c, %v; want '+', true", marker, ok)
	}
}

func TestIsDiffMarker_NonDiffLine(t *testing.T) {
	t.Parallel()
	_, ok := isDiffMarker("just a regular line")
	if ok {
		t.Error("isDiffMarker should return false for non-diff line")
	}
}

func TestIsDiffMarker_PlusPrefix(t *testing.T) {
	t.Parallel()
	_, ok := isDiffMarker("+content here")
	if ok {
		t.Error("isDiffMarker should return false for line starting with +")
	}
}

func TestIsDiffMarker_NoDigit(t *testing.T) {
	t.Parallel()
	_, ok := isDiffMarker("  +content")
	if ok {
		t.Error("isDiffMarker should return false with no digits")
	}
}

// ---------------------------------------------------------------------------
// applyDiffBackground
// ---------------------------------------------------------------------------

func TestApplyDiffBackground_AddedLine(t *testing.T) {
	t.Parallel()
	result := applyDiffBackground(" 123 +content here", 40)
	if !strings.Contains(result, "\x1b[48;5;22m") {
		t.Errorf("expected green bg for added line, got: %q", result)
	}
	if got := visibleWidth(result); got != 40 {
		t.Errorf("visibleWidth after padding = %d, want 40", got)
	}
}

func TestApplyDiffBackground_RemovedLine(t *testing.T) {
	t.Parallel()
	result := applyDiffBackground(" 123 -old content", 40)
	if !strings.Contains(result, "\x1b[48;5;52m") {
		t.Errorf("expected red bg for removed line, got: %q", result)
	}
	if got := visibleWidth(result); got != 40 {
		t.Errorf("visibleWidth after padding = %d, want 40", got)
	}
}

func TestApplyDiffBackground_ContextLine(t *testing.T) {
	t.Parallel()
	result := applyDiffBackground(" 123  context line", 40)
	if strings.Contains(result, "\x1b[48;") {
		t.Errorf("context line should have no bg, got: %q", result)
	}
}

func TestApplyDiffBackground_NonDiffLine(t *testing.T) {
	t.Parallel()
	input := "just a regular line"
	result := applyDiffBackground(input, 40)
	if result != input {
		t.Errorf("non-diff line should be unchanged, got: %q", result)
	}
}

func TestApplyDiffBackground_Multiline(t *testing.T) {
	t.Parallel()
	input := " 1 -old\n 1 +new\n 2  ctx"
	result := applyDiffBackground(input, 30)
	lines := strings.Split(result, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "\x1b[48;5;52m") {
		t.Errorf("line 0 should have red bg")
	}
	if !strings.Contains(lines[1], "\x1b[48;5;22m") {
		t.Errorf("line 1 should have green bg")
	}
	if strings.Contains(lines[2], "\x1b[48;") {
		t.Errorf("line 2 should have no bg")
	}
}

func TestApplyDiffBackground_WithLeadingAnsi(t *testing.T) {
	t.Parallel()
	input := "\x1b[38;5;10m 123 +colored\x1b[0m"
	result := applyDiffBackground(input, 30)
	if !strings.Contains(result, diffAddBg) {
		t.Errorf("expected green bg, got: %q", result)
	}
	if got := visibleWidth(result); got != 30 {
		t.Errorf("visibleWidth = %d, want 30", got)
	}
}

func TestApplyDiffBackground_WrappedLine(t *testing.T) {
	t.Parallel()
	// A diff line followed by a wrapped continuation (no diff marker)
	input := " 1 +very long content that was wrapped\nwrapped continuation here"
	result := applyDiffBackground(input, 40)
	lines := strings.Split(result, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	// First line: green bg (added)
	if !strings.Contains(lines[0], "\x1b[48;5;22m") {
		t.Errorf("line 0 should have green bg")
	}
	// Wrapped continuation: should also have green bg
	if !strings.Contains(lines[1], "\x1b[48;5;22m") {
		t.Errorf("wrapped line should inherit green bg, got: %q", lines[1])
	}
	if got := visibleWidth(lines[1]); got != 40 {
		t.Errorf("wrapped line visibleWidth = %d, want 40", got)
	}
}
