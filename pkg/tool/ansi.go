package tool

import (
	"regexp"
	"strings"
)

// ansiEscapeRe matches ANSI escape sequences in PTY output and terminal strings.
// Covers: CSI sequences, OSC (BEL/ST), DCS, ESC charset, mode set/reset, bare ESC.
var ansiEscapeRe = regexp.MustCompile(
	// CSI sequences: \x1b[<params><letter>  (SGR, CUP, ED, EL, etc.)
	`\x1b\[[0-9;]*[a-zA-Z]` +
		// OSC sequences with BEL terminator: \x1b]...<BEL>
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

// StripANSI removes ANSI escape sequences from a string.
// Fast path: if no ESC byte exists, skip regex entirely.
func StripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return ansiEscapeRe.ReplaceAllString(s, "")
}
