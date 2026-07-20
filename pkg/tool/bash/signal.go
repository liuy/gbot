package bash

import (
	"runtime"

	"golang.org/x/term"
)

// GetTerminalSize returns (rows, cols) of the controlling terminal.
// Falls back to 80x24 on error.
//
// Source: ink.tsx:226 — process.stdout.columns/rows.
// Uses golang.org/x/term.GetSize (platform-agnostic TIOCGWINSZ wrapper).
func GetTerminalSize() (rows, cols int, err error) {
	return getTerminalSizeFd(0)
}

// getTerminalSizeFd returns (rows, cols) for the given fd.
// Extracted for testability — callers use GetTerminalSize() which passes fd 0.
func getTerminalSizeFd(fd int) (rows, cols int, err error) {
	cols, rows, err = term.GetSize(fd)
	if err != nil {
		return 24, 80, nil // fallback
	}
	return rows, cols, nil
}

// isLinux returns true on Linux. Used by the checkIsLinux test hook.
// PTY allocation (via go-pty) and SIGWINCH are gated to Linux.
func isLinux() bool {
	return runtime.GOOS == "linux"
}
