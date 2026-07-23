//go:build !windows

package proc

import "os/exec"

func hideWindowWindows(cmd *exec.Cmd) {}
