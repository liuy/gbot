//go:build !windows

package proc

import (
	"os/exec"
	"testing"
)

func TestHideWindowLeavesCommandUsable(t *testing.T) {
	cmd := exec.Command("true")
	HideWindow(cmd)
	if cmd.SysProcAttr != nil {
		t.Errorf("SysProcAttr = %+v, want nil on non-Windows", cmd.SysProcAttr)
	}
	if err := cmd.Run(); err != nil {
		t.Errorf("cmd.Run() = %v, want success after HideWindow", err)
	}
}

func TestHideWindowStubIsNoOp(t *testing.T) {
	cmd := exec.Command("true")
	hideWindow(cmd)
	if cmd.SysProcAttr != nil {
		t.Errorf("SysProcAttr = %+v, want nil from non-Windows stub", cmd.SysProcAttr)
	}
}
