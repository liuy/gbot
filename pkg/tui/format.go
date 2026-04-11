package tui

import (
	"fmt"
	"time"
)

// formatElapsed returns a formatted elapsed time string like "1.2s" or "45ms".
func formatElapsed(start time.Time) string {
	elapsed := time.Since(start)
	return fmt.Sprintf("%.1fs", elapsed.Seconds())
}

// formatDuration returns a formatted duration string with 0.1s precision.
func formatDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
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
