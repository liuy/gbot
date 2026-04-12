package tui

import (
	"strings"
	"testing"
	"time"
)

func TestFormatDuration_Milliseconds(t *testing.T) {
	t.Parallel()
	v := formatDuration(500 * time.Millisecond)
	if v != "0.5s" {
		t.Errorf("formatDuration(500ms) = %q, want %q", v, "0.5s")
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	t.Parallel()
	v := formatDuration(2500 * time.Millisecond)
	if v != "2.5s" {
		t.Errorf("formatDuration(2.5s) = %q, want %q", v, "2.5s")
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	t.Parallel()
	v := formatDuration(0)
	if v != "0.0s" {
		t.Errorf("formatDuration(0) = %q, want %q", v, "0.0s")
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
	// formatElapsed always shows seconds (0.1s minimum)
	start := time.Now().Add(-100 * time.Millisecond)
	v := formatElapsed(start)
	if !strings.HasSuffix(v, "s") {
		t.Errorf("formatElapsed(100ms ago) = %q, want s suffix", v)
	}
}

// ---------------------------------------------------------------------------
// formatTokenCount
// ---------------------------------------------------------------------------

func TestFormatTokenCount_Under1000(t *testing.T) {
	t.Parallel()
	v := formatTokenCount(42)
	if v != "42" {
		t.Errorf("formatTokenCount(42) = %q, want %q", v, "42")
	}
}

func TestFormatTokenCount_Over1000(t *testing.T) {
	t.Parallel()
	v := formatTokenCount(1500)
	if v != "1.5k" {
		t.Errorf("formatTokenCount(1500) = %q, want %q", v, "1.5k")
	}
}

func TestFormatTokenCount_Exactly1000(t *testing.T) {
	t.Parallel()
	v := formatTokenCount(1000)
	if v != "1.0k" {
		t.Errorf("formatTokenCount(1000) = %q, want %q", v, "1.0k")
	}
}
