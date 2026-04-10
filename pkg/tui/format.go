package tui

import (
	"fmt"
	"time"
)

// formatElapsed returns a formatted elapsed time string like "1.2s" or "45ms".
func formatElapsed(start time.Time) string {
	elapsed := time.Since(start)
	if elapsed < time.Second {
		return fmt.Sprintf("%.0fms", float64(elapsed.Milliseconds()))
	}
	return fmt.Sprintf("%.1fs", elapsed.Seconds())
}

// formatDuration returns a formatted duration string.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.0fms", float64(d.Milliseconds()))
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
