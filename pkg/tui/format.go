package tui

import (
	"fmt"
	"time"
)

// formatElapsed returns a human-readable elapsed time string.
// <1s: "0.3s", 1-59s: "3s", 60s-59m: "1m 23s", ≥60m: "1h 23m".
func formatElapsed(start time.Time) string {
	return formatDuration(time.Since(start))
}

// formatDuration returns a human-readable duration string.
func formatDuration(d time.Duration) string {
	s := int(d.Seconds())
	switch {
	case s < 1:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		m := s / 60
		sec := s % 60
		return fmt.Sprintf("%dm %ds", m, sec)
	default:
		h := s / 3600
		m := (s % 3600) / 60
		sec := s % 60
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
}

// formatTokenCount formats a token count: <1000 as-is, >=1000 as "1.2k".
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// animateTokenValue increments displayed toward target by one step.
// <1000: +1 per tick. >=1000: +100 per tick (matches 0.1k display precision).
func animateTokenValue(displayed, target int) int {
	if displayed >= target {
		return target
	}
	step := 1
	if displayed >= 1000 {
		step = 100
	}
	displayed += step
	if displayed > target {
		displayed = target
	}
	return displayed
}
