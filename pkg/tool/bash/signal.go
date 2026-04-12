package bash

import (
	"runtime"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// GetTerminalSize returns (rows, cols) of the controlling terminal.
// Falls back to 80x24 on error.
//
// Source: ink.tsx:226 — process.stdout.columns/rows.
// Uses golang.org/x/term.GetSize (platform-agnostic ioctl TIOCGWINSZ wrapper).
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

// setPTYWindowSize sets the PTY window size to match the controlling terminal.
// Source: ink.tsx:226 — IoctlSetWinsize for SIGWINCH handling.
// x/term has GetSize but no SetSize, so we use x/sys/unix for the write.
func setPTYWindowSize(ptyFd uintptr) error {
	return setPTYWindowSizeFd(0, ptyFd)
}

// setPTYWindowSizeFd reads window size from ctlFd and applies to ptyFd.
// Extracted for testability — callers use setPTYWindowSize() which passes fd 0.
func setPTYWindowSizeFd(ctlFd int, ptyFd uintptr) error {
	ws, err := unix.IoctlGetWinsize(ctlFd, unix.TIOCGWINSZ)
	if err != nil {
		return err
	}
	return unix.IoctlSetWinsize(int(ptyFd), unix.TIOCSWINSZ, ws)
}

// isLinux returns true on Linux.
// SIGWINCH and PTY allocation are Linux-specific.
func isLinux() bool {
	return runtime.GOOS == "linux"
}
