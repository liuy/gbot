//go:build !windows

package cron

import (
	"errors"
	"syscall"
)

// processAlive probes pid liveness with signal 0: nil (our own process class)
// or EPERM (alive, owned by another user) mean alive; ESRCH means dead.
// pid <= 0 never counts as alive (a malformed lock body must not signal the
// whole process group).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
