//go:build windows

package bash

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// killProcessTree kills the process tree rooted at pid using taskkill.
// taskkill /T walks the process tree via CreateToolhelp32Snapshot server-side;
// /F forces termination. This is the documented escalation path for tree-kill
// on Windows and matches hermes-agent's kill path.
//
// If taskkill fails (PID already gone, taskkill unavailable), fall back to
// os.FindProcess + Signal(SIGKILL) which Go translates to TerminateProcess.
//
// Always returns nil — the caller (cancellation goroutine, background job
// kill) treats kill as best-effort.
func killProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
	if err != nil {
		// Fall back to TerminateProcess for the root PID.
		if proc, e := os.FindProcess(pid); e == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}
	return nil
}

// killProcess is the Windows equivalent of the Unix killProcess.
// Reuses killProcessTree (tree kill has no downside here — there's no
// graceful signal ladder on Windows).
func killProcess(pid int) {
	_ = killProcessTree(pid)
}

// setSysProcAttrForGroup is a no-op on Windows. Windows has no process-group
// concept analogous to Unix's setpgid; tree kills go through taskkill /T
// from killProcessTree instead. Non-PTY fallback is rare on Windows when
// ConPTY is available (detectPTYSupport returns true at init).
func setSysProcAttrForGroup(_ *exec.Cmd) {}
