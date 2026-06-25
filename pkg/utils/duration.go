package utils

import (
	"fmt"
	"time"
)

// FormatDuration returns a compact human-readable duration string.
// <1s: "0.3s", 1-59s: "3s", 60s-59m: "1m 23s", ≥60m: "1h 23m 45s".
func FormatDuration(d time.Duration) string {
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
