package tui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDuration_Milliseconds(t *testing.T) {
	t.Parallel()

	v := formatDuration(500 * time.Millisecond)
	if !strings.HasSuffix(v, "ms") {
		t.Errorf("formatDuration(500ms) = %q, want ms suffix", v)
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	t.Parallel()

	v := formatDuration(2500 * time.Millisecond)
	if !strings.HasSuffix(v, "s") {
		t.Errorf("formatDuration(2.5s) = %q, want s suffix", v)
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	t.Parallel()

	v := formatDuration(0)
	if v != "0ms" {
		t.Errorf("formatDuration(0) = %q, want %q", v, "0ms")
	}
}

func TestFormatElapsed(t *testing.T) {
	// Not parallel — timing sensitive
	start := time.Now().Add(-2 * time.Second)
	v := formatElapsed(start)
	if !strings.HasSuffix(v, "s") {
		t.Errorf("formatElapsed(2s ago) = %q, want s suffix", v)
	}
}

func TestFormatElapsed_Milliseconds(t *testing.T) {
	// Not parallel — timing sensitive
	start := time.Now().Add(-100 * time.Millisecond)
	v := formatElapsed(start)
	if !strings.HasSuffix(v, "ms") {
		t.Errorf("formatElapsed(100ms ago) = %q, want ms suffix", v)
	}
}
