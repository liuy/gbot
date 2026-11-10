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

// TestFormatDuration_Boundaries pins the three band thresholds (1s / 60s / 3600s)
// from both sides, plus the divisors and moduli inside the m/h bands. The band
// edges are the only inputs where a ±1 on a threshold literal is observable, and
// the divisor/modulus pairs (119s vs 120s, 7199s vs 7260s) are the first values
// where s/60, s%3600 and s%60 disagree with their ±1 variants.
func TestFormatDuration_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		d    time.Duration
		want string
	}{
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m 0s"},
		{90 * time.Second, "1m 30s"},
		{119 * time.Second, "1m 59s"},
		{120 * time.Second, "2m 0s"},
		{179 * time.Second, "2m 59s"},
		{180 * time.Second, "3m 0s"},
		{3599 * time.Second, "59m 59s"},
		{3600 * time.Second, "1h 0m 0s"},
		{3661 * time.Second, "1h 1m 1s"},
		{7199 * time.Second, "1h 59m 59s"},
		{7200 * time.Second, "2h 0m 0s"},
		{7260 * time.Second, "2h 1m 0s"},
		{10799 * time.Second, "2h 59m 59s"},
	}
	for _, tc := range cases {
		if got := FormatDuration(tc.d); got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
