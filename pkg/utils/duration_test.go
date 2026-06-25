package utils

import (
	"testing"
	"time"
)

func TestFormatDuration_Milliseconds(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(300 * time.Millisecond); got != "0.3s" {
		t.Errorf("FormatDuration(300ms) = %q, want %q", got, "0.3s")
	}
}

func TestFormatDuration_SubSecond(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(50 * time.Millisecond); got != "0.1s" {
		t.Errorf("FormatDuration(50ms) = %q, want %q", got, "0.1s")
	}
}

func TestFormatDuration_OneSecond(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(1 * time.Second); got != "1s" {
		t.Errorf("FormatDuration(1s) = %q, want %q", got, "1s")
	}
}

func TestFormatDuration_Seconds(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(42 * time.Second); got != "42s" {
		t.Errorf("FormatDuration(42s) = %q, want %q", got, "42s")
	}
}

func TestFormatDuration_Minutes(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(90 * time.Second); got != "1m 30s" {
		t.Errorf("FormatDuration(90s) = %q, want %q", got, "1m 30s")
	}
}

func TestFormatDuration_Hours(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(3723 * time.Second); got != "1h 2m 3s" {
		t.Errorf("FormatDuration(3723s) = %q, want %q", got, "1h 2m 3s")
	}
}

func TestFormatDuration_Zero(t *testing.T) {
	t.Parallel()
	if got := FormatDuration(0); got != "0.0s" {
		t.Errorf("FormatDuration(0) = %q, want %q", got, "0.0s")
	}
}
