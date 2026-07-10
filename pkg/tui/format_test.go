package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
	"github.com/liuy/gbot/pkg/utils"
)

// ---------------------------------------------------------------------------
// formatDuration
// ---------------------------------------------------------------------------

func TestFormatDuration_Milliseconds(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(300 * time.Millisecond)
	if v != "0.3s" {
		t.Errorf("utils.FormatDuration(300ms) = %q, want %q", v, "0.3s")
	}
}

func TestFormatDuration_SubSecond(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(50 * time.Millisecond)
	if v != "0.1s" {
		t.Errorf("utils.FormatDuration(50ms) = %q, want %q", v, "0.1s")
	}
}

func TestFormatDuration_OneSecond(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(1 * time.Second)
	if v != "1s" {
		t.Errorf("utils.FormatDuration(1s) = %q, want %q", v, "1s")
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(42 * time.Second)
	if v != "42s" {
		t.Errorf("utils.FormatDuration(42s) = %q, want %q", v, "42s")
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(90 * time.Second)
	if v != "1m 30s" {
		t.Errorf("utils.FormatDuration(90s) = %q, want %q", v, "1m 30s")
	}
}

func TestFormatDuration_MinutesNoSeconds(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(60 * time.Second)
	if v != "1m 0s" {
		t.Errorf("utils.FormatDuration(60s) = %q, want %q", v, "1m 0s")
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(3723 * time.Second) // 1h 2m 3s
	if v != "1h 2m 3s" {
		t.Errorf("utils.FormatDuration(3723s) = %q, want %q", v, "1h 2m 3s")
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	t.Parallel()
	v := utils.FormatDuration(0)
	if v != "0.0s" {
		t.Errorf("utils.FormatDuration(0) = %q, want %q", v, "0.0s")
	}
}

// ---------------------------------------------------------------------------
// formatElapsed
// ---------------------------------------------------------------------------

func TestFormatElapsed(t *testing.T) {
	start := time.Now().Add(-2 * time.Second) // REAL-TIME: formatElapsed duration
	v := utils.FormatDuration(time.Since(start))
	if !strings.HasPrefix(v, "2") || !strings.HasSuffix(v, "s") {
		t.Errorf("utils.FormatDuration(time.Since(2s ago)) = %q, want prefix '2' and suffix 's'", v)
	}
}

func TestFormatElapsed_Milliseconds(t *testing.T) {
	start := time.Now().Add(-100 * time.Millisecond) // REAL-TIME: formatElapsed duration
	v := utils.FormatDuration(time.Since(start))
	if !strings.HasSuffix(v, "s") {
		t.Errorf("utils.FormatDuration(time.Since(100ms ago)) = %q, want s suffix", v)
	}
	if !strings.Contains(v, "0.") {
		t.Errorf("utils.FormatDuration(time.Since(100ms ago)) = %q, want sub-second format (0.Xs)", v)
	}
}

// ---------------------------------------------------------------------------
// formatTokenCount
// ---------------------------------------------------------------------------

func TestFormatTokenCount_Under1000(t *testing.T) {
	t.Parallel()
	v := types.FormatTokenCount(42)
	if v != "42" {
		t.Errorf("FormatTokenCount(42) = %q, want %q", v, "42")
	}
}

func TestFormatTokenCount_Over1000(t *testing.T) {
	t.Parallel()
	// 1500 / 1024 ≈ 1.465 → "1.5k"
	v := types.FormatTokenCount(1500)
	if v != "1.5k" {
		t.Errorf("FormatTokenCount(1500) = %q, want %q", v, "1.5k")
	}
}

func TestFormatTokenCount_Exactly1024(t *testing.T) {
	t.Parallel()
	// 1024 / 1024 = 1.0k
	v := types.FormatTokenCount(1024)
	if v != "1.0k" {
		t.Errorf("FormatTokenCount(1024) = %q, want %q", v, "1.0k")
	}
}

// ---------------------------------------------------------------------------
// formatRetryError
// ---------------------------------------------------------------------------

func TestFormatRetryError_StreamInterrupted(t *testing.T) {
	t.Parallel()
	got := formatRetryError(string(types.RetryErrorStreamInterrupted))
	if got != "Connection interrupted" {
		t.Errorf("formatRetryError(stream_interrupted) = %q, want %q", got, "Connection interrupted")
	}
}

func TestFormatRetryError_StreamEnded(t *testing.T) {
	t.Parallel()
	got := formatRetryError(string(types.RetryErrorStreamEnded))
	if got != "Connection lost" {
		t.Errorf("formatRetryError(stream_ended) = %q, want %q", got, "Connection lost")
	}
}

func TestFormatRetryError_UnknownType(t *testing.T) {
	t.Parallel()
	got := formatRetryError("unknown_error")
	if got != "Request timed out. Check your internet connection" {
		t.Errorf("formatRetryError(unknown) = %q, want timeout message", got)
	}
}

func TestFormatRetryError_EmptyType(t *testing.T) {
	t.Parallel()
	got := formatRetryError("")
	if got != "Request timed out. Check your internet connection" {
		t.Errorf("formatRetryError(empty) = %q, want timeout message", got)
	}
}

func TestAnimateTokenValue(t *testing.T) {
	t.Parallel()
	// Below 1000: step = 1
	got := animateTokenValue(0, 5)
	if got != 1 {
		t.Errorf("animateTokenValue(0,5) = %d, want 1", got)
	}
	// Above 1000: step = 100
	got = animateTokenValue(1000, 1500)
	if got != 1100 {
		t.Errorf("animateTokenValue(1000,1500) = %d, want 1100", got)
	}
	// Clamps to target
	got = animateTokenValue(1400, 1500)
	if got != 1500 {
		t.Errorf("animateTokenValue(1400,1500) = %d, want 1500", got)
	}
}
