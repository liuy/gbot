package bash

import (
	"syscall"
	"testing"
)

func TestGetTerminalSize(t *testing.T) {
	t.Parallel()

	rows, cols, err := GetTerminalSize()
	if err != nil {
		t.Errorf("GetTerminalSize() error: %v", err)
	}
	if rows != 24 && rows < 1 {
		t.Errorf("GetTerminalSize() rows = %d, want 24 (fallback) or positive", rows)
	}
	if cols != 80 && cols < 1 {
		t.Errorf("GetTerminalSize() cols = %d, want 80 (fallback) or positive", cols)
	}
}

func TestGetTerminalSize_WithPTY(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	_ = setPTYWindowSize(master.Fd())
	rows, cols, err := GetTerminalSize()
	if err != nil {
		t.Errorf("GetTerminalSize() error: %v", err)
	}
	_ = rows
	_ = cols
}

func TestIsLinux(t *testing.T) {
	t.Parallel()

	result := isLinux()
	_ = result
}

func TestSetPTYWindowSize_InvalidFd(t *testing.T) {
	err := setPTYWindowSize(uintptr(syscall.Stdin))
	if err == nil {
		_ = err
	}
}

func TestSetPTYWindowSize_WithPTY(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	_ = setPTYWindowSize(master.Fd())
}

// --- fd-parametrized function tests ---

func TestGetTerminalSizeFd_Fallback(t *testing.T) {
	t.Parallel()

	// Non-existent fd → term.GetSize fails → fallback 24x80
	rows, cols, err := getTerminalSizeFd(999)
	if err != nil {
		t.Errorf("getTerminalSizeFd() error: %v", err)
	}
	if rows != 24 || cols != 80 {
		t.Errorf("getTerminalSizeFd() = %d, %d, want 24, 80 (fallback)", rows, cols)
	}
}

func TestSetPTYWindowSizeFd_InvalidFd(t *testing.T) {
	t.Parallel()

	// Invalid ctlFd → IoctlGetWinsize fails
	err := setPTYWindowSizeFd(999, 0)
	if err == nil {
		t.Error("expected error with invalid ctlFd")
	}
}

func TestSetPTYWindowSizeFd_InvalidPtyFd(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	// Valid ctlFd (master), invalid ptyFd → IoctlSetWinsize fails
	err = setPTYWindowSizeFd(int(master.Fd()), 999)
	if err == nil {
		t.Error("expected error with invalid ptyFd")
	}
}

func TestGetTerminalSizeFd_WithPTY(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	// PTY slave is a terminal → term.GetSize should succeed
	rows, cols, err := getTerminalSizeFd(int(slave.Fd()))
	if err != nil {
		t.Errorf("getTerminalSizeFd() error: %v", err)
	}
	_ = rows
	_ = cols
}

func TestSetPTYWindowSizeFd_WithPTY(t *testing.T) {
	if !isPTYAvailable() {
		t.Skip("PTY not available")
	}

	master, slave, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	// Use master as both ctlFd and ptyFd — both are valid PTY fds
	err = setPTYWindowSizeFd(int(master.Fd()), master.Fd())
	// May or may not succeed depending on window state, just verify no panic
	_ = err
}
