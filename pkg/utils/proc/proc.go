package proc

import "os/exec"

// HideWindow configures cmd to not pop up a console window when spawned
// from a GUI application (e.g. Wails -H windowsgui binary).
// No-op on non-Windows platforms.
func HideWindow(cmd *exec.Cmd) {
	hideWindow(cmd)
}
