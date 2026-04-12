package bash

import (
	"regexp"
)

// ansiEscapeRe matches ANSI escape sequences that may appear in PTY output.
// Extended beyond markdown.go's stripANSI to handle full PTY output including:
// - CSI sequences (SGR, CUP, ED, EL, etc.)
// - OSC sequences with BEL terminator
// - OSC sequences with ST terminator
// - DCS sequences
// - ESC charset sequences
// - Mode set/reset sequences
//
// Source: PTY-native requirement — TS file-mode strips nothing, but PTY output
// contains terminal control sequences that must be removed before display.
// Reuses and extends the proven regex from pkg/tui/markdown.go:678.
var ansiEscapeRe = regexp.MustCompile(
	// CSI sequences: \x1b[<params><letter>  (SGR, CUP, ED, EL, etc.)
	`\x1b\[[0-9;]*[a-zA-Z]` +
		// OSC sequences with BEL terminator: \x1b]...<BEL>
		// Covers OSC 8 hyperlinks, window title, etc.
		`|\x1b][^\x07]*\x07` +
		// OSC sequences with ST terminator: \x1b]...<ST>
		`|\x1b].*?\x1b\\` +
		// DCS sequences: \x1bP...<ST>
		`|\x1bP[^\x1b]*\x1b\\` +
		// ESC charset sequences: \x1b(, \x1b) followed by A/B/0/1/2
		`|\x1b[()][AB012]` +
		// Mode set/reset: \x1b>, \x1b<
		`|\x1b[><]` +
		// Bare ESC (incomplete/truncated)
		`|\x1b`,
)

// StripANSI removes ANSI escape sequences from PTY output.
// PTY output contains terminal control sequences (cursor movement, color codes,
// screen clearing, etc.) that must be stripped before display in the TUI.
func StripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}
