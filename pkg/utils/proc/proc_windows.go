package proc

import (
	"os/exec"
	"syscall"
)

// hideWindowWindows sets CREATE_NO_WINDOW on the command's SysProcAttr
// so child processes don't flash a console window.
func hideWindowWindows(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	// CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr.CreationFlags |= 0x08000000
}
