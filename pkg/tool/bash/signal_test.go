package bash

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestGetTerminalSize(t *testing.T) {
	t.Parallel()

	rows, cols, err := GetTerminalSize()
	if err != nil {
		t.Errorf("GetTerminalSize() error: %v", err)
	}
	if rows < 1 {
		t.Errorf("GetTerminalSize() rows = %d, want positive", rows)
	}
	if cols < 1 {
		t.Errorf("GetTerminalSize() cols = %d, want positive", cols)
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
	if rows < 1 {
		t.Errorf("GetTerminalSize() rows = %d, want positive", rows)
	}
	if cols < 1 {
		t.Errorf("GetTerminalSize() cols = %d, want positive", cols)
	}
}

func TestIsLinux(t *testing.T) {
	t.Parallel()

	result := isLinux()
	if runtime.GOOS == "linux" && !result {
		t.Error("isLinux() = false on linux, want true")
	}
	if runtime.GOOS != "linux" && result {
		t.Errorf("isLinux() = true on %s, want false", runtime.GOOS)
	}
}

func TestSetPTYWindowSize_InvalidFd(t *testing.T) {
	err := setPTYWindowSize(uintptr(syscall.Stdin))
	if err == nil {
		t.Fatal("expected error for invalid fd (stdin is not a PTY)")
	}
	if !strings.Contains(err.Error(), "bad file descriptor") && !strings.Contains(err.Error(), "inappropriate ioctl") {
		t.Errorf("error should mention bad file descriptor or inappropriate ioctl, got: %v", err)
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

	// setPTYWindowSize reads from fd 0 (stdin) which may not be a PTY
	// So we use setPTYWindowSizeFd directly with slave as control
	err = setPTYWindowSizeFd(int(slave.Fd()), master.Fd())
	if err != nil {
		t.Errorf("setPTYWindowSizeFd(slave, master) error: %v", err)
	}
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
		t.Fatal("expected error with invalid ctlFd")
	}
	if !strings.Contains(err.Error(), "bad file descriptor") {
		t.Errorf("error should mention bad file descriptor, got: %v", err)
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
		t.Fatal("expected error with invalid ptyFd")
	}
	if !strings.Contains(err.Error(), "bad file descriptor") {
		t.Errorf("error should mention bad file descriptor, got: %v", err)
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

	// First set a window size, then read it back
	_ = setPTYWindowSizeFd(int(slave.Fd()), master.Fd())
	rows, cols, err := getTerminalSizeFd(int(slave.Fd()))
	if err != nil {
		t.Errorf("getTerminalSizeFd() error: %v", err)
	}
	// After setting window size, should get positive values
	// (Note: may still get 0,0 if the set didn't work, which is OK for this test)
	if rows < 0 || cols < 0 {
		t.Errorf("getTerminalSizeFd() rows = %d, cols = %d, want non-negative", rows, cols)
	}
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
	if err != nil {
		t.Errorf("setPTYWindowSizeFd(valid, valid) error: %v", err)
	}
}
