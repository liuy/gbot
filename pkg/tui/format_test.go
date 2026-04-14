package tui

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// formatDuration
// ---------------------------------------------------------------------------

func TestFormatDuration_Milliseconds(t *testing.T) {
	t.Parallel()
	v := formatDuration(300 * time.Millisecond)
	if v != "0.3s" {
		t.Errorf("formatDuration(300ms) = %q, want %q", v, "0.3s")
	}
}

func TestFormatDuration_SubSecond(t *testing.T) {
	t.Parallel()
	v := formatDuration(50 * time.Millisecond)
	if v != "0.1s" {
		t.Errorf("formatDuration(50ms) = %q, want %q", v, "0.1s")
	}
}

func TestFormatDuration_OneSecond(t *testing.T) {
	t.Parallel()
	v := formatDuration(1 * time.Second)
	if v != "1s" {
		t.Errorf("formatDuration(1s) = %q, want %q", v, "1s")
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	t.Parallel()
	v := formatDuration(42 * time.Second)
	if v != "42s" {
		t.Errorf("formatDuration(42s) = %q, want %q", v, "42s")
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	t.Parallel()
	v := formatDuration(90 * time.Second)
	if v != "1m 30s" {
		t.Errorf("formatDuration(90s) = %q, want %q", v, "1m 30s")
	}
}

func TestFormatDuration_MinutesNoSeconds(t *testing.T) {
	t.Parallel()
	v := formatDuration(60 * time.Second)
	if v != "1m 0s" {
		t.Errorf("formatDuration(60s) = %q, want %q", v, "1m 0s")
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	t.Parallel()
	v := formatDuration(3723 * time.Second) // 1h 2m 3s
	if v != "1h 2m 3s" {
		t.Errorf("formatDuration(3723s) = %q, want %q", v, "1h 2m 3s")
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	t.Parallel()
	v := formatDuration(0)
	if v != "0.0s" {
		t.Errorf("formatDuration(0) = %q, want %q", v, "0.0s")
	}
}

// ---------------------------------------------------------------------------
// formatElapsed
// ---------------------------------------------------------------------------

func TestFormatElapsed(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	v := formatElapsed(start)
	if !strings.HasSuffix(v, "s") {
		t.Errorf("formatElapsed(2s ago) = %q, want s suffix", v)
	}
}

func TestFormatElapsed_Milliseconds(t *testing.T) {
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
