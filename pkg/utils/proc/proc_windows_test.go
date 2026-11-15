//go:build windows

package proc

import (
	"os/exec"
	"syscall"
	"testing"
)

const createNoWindow = 0x08000000

func TestHideWindowWindowsSetsCreateNoWindow(t *testing.T) {
	cmd := exec.Command("cmd")
	hideWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr = nil, want CREATE_NO_WINDOW attrs set")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow = false, want true")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NO_WINDOW bit set", cmd.SysProcAttr.CreationFlags)
	}
}

func TestHideWindowWindowsPreservesExistingAttrs(t *testing.T) {
	cmd := exec.Command("cmd")
	existing := &syscall.SysProcAttr{CreationFlags: 0x00000200}
	cmd.SysProcAttr = existing
	hideWindow(cmd)
	if cmd.SysProcAttr != existing {
		t.Errorf("SysProcAttr replaced (%+v), want existing attrs extended in place", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.CreationFlags != 0x00000200|createNoWindow {
		t.Errorf("CreationFlags = %#x, want %#x", cmd.SysProcAttr.CreationFlags, 0x00000200|createNoWindow)
	}
}
