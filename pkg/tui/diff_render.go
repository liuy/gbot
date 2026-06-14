package tui

import (
	"strings"
	"unicode/utf8"
)

// ANSI escape sequence constants for diff rendering.
const (
	diffAddBg = "\x1b[48;5;22m" // dark green background
	diffDelBg = "\x1b[48;5;52m" // dark red background
	diffReset = "\x1b[0m"
)

// visibleWidth returns the visible width of s, skipping ANSI escape sequences.
// CJK characters count as 2 columns; other characters count as 1.
func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			// Skip ANSI escape sequence: ESC [ ... <final byte>
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) {
					if s[i] >= 0x40 && s[i] <= 0x7e {
						i++
						break
					}
					i++
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r >= 0x1100 &&
			(r <= 0x115F || // Hangul Jamo
				r == 0x2329 || r == 0x232A ||
				(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) || // CJK
				(r >= 0xAC00 && r <= 0xD7A3) || // Hangul Syllables
				(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
				(r >= 0xFE10 && r <= 0xFE19) || // Vertical forms
				(r >= 0xFE30 && r <= 0xFE6F) || // CJK Compatibility Forms
				(r >= 0xFF01 && r <= 0xFF60) || // Fullwidth Forms
				(r >= 0xFFE0 && r <= 0xFFE6) ||
				(r >= 0x20000 && r <= 0x2FFFD) ||
				(r >= 0x30000 && r <= 0x3FFFD)) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// isDiffMarker checks if a line starts with a diff gutter pattern.
// Matches the output of tool.RenderDiff: " NNN +content" (added),
// " NNN -content" (removed), " NNN  content" (context).
// Skips leading ANSI escape sequences.
func isDiffMarker(line string) (marker byte, ok bool) {
	// Skip leading ANSI codes
	i := 0
	for i < len(line) && line[i] == '\x1b' {
		i++
		if i < len(line) && line[i] == '[' {
			i++
			for i < len(line) {
				if line[i] >= 0x40 && line[i] <= 0x7e {
					i++
					break
				}
				i++
			}
		}
	}
	// Must start with leading spaces then digits then space then marker.
	// RenderDiff uses %*d padding, so maxDigits > 1 produces multiple spaces.
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return 0, false
	}
	rest := line[i:]
	digitCount := 0
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		rest = rest[1:]
		digitCount++
	}
	// Must have digits followed by space then marker
	if digitCount == 0 || len(rest) < 2 || rest[0] != ' ' {
		return 0, false
	}
	ch := rest[1]
	if ch != '+' && ch != '-' && ch != ' ' {
		return 0, false
	}
	return ch, true
}

// applyDiffBackground adds full-width background color to diff lines.
// Added lines (+) get green bg, removed lines (-) get red bg.
// Context lines and non-diff lines are left unchanged.
//
// The function runs *after* wordWrap has already split long diff lines into
// width-sized visual rows. The first visual row carries the diff marker
// (" NNN +content"); subsequent visual rows have no marker and must inherit
// the bg of the preceding diff line. lastBg therefore persists across
// continuation lines and is only switched when the next marker appears.
func applyDiffBackground(output string, width int) string {
	lines := strings.Split(output, "\n")
	var rebuilt []string
	changed := false
	lastBg := ""
	for _, line := range lines {
		marker, ok := isDiffMarker(line)
		if ok {
			switch marker {
			case '+':
				lastBg = diffAddBg
			case '-':
				lastBg = diffDelBg
			default:
				lastBg = ""
			}
			if lastBg == "" {
				rebuilt = append(rebuilt, line)
				continue
			}
			changed = true
			vw := visibleWidth(line)
			pad := max(width-vw, 0)
			rebuilt = append(rebuilt, lastBg+line+diffReset+lastBg+strings.Repeat(" ", pad)+diffReset)
		} else if lastBg != "" {
			// Wrapped continuation of a diff line — keep inheriting lastBg
			// until the next marker arrives.
			changed = true
			vw := visibleWidth(line)
			pad := max(width-vw, 0)
			rebuilt = append(rebuilt, lastBg+line+strings.Repeat(" ", pad)+diffReset)
		} else {
			rebuilt = append(rebuilt, line)
		}
	}
	if !changed {
		return output
	}
	return strings.Join(rebuilt, "\n")
}

