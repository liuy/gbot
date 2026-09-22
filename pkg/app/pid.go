package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/project"
)

// acquirePID writes the current PID to the project's PID file and returns a
// cleanup function that removes it. Returns an error if another live process
// already holds the PID file — unless skipLiveGuard is set: a tableflip
// upgraded child boots while its parent still holds the file (the parent
// overwrote nothing; the child takes ownership by overwriting it), so the
// liveness check is skipped there while the file still gets rewritten with
// the child's PID.
func acquirePID(projectDir string, skipLiveGuard bool) (cleanup func(), err error) {
	if mkErr := os.MkdirAll(projectDir, 0755); mkErr != nil {
		return nil, fmt.Errorf("create project dir: %w", mkErr)
	}

	pidPath := project.PIDFile(projectDir)

	if !skipLiveGuard {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			existing, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && isProcessAlive(existing) {
				return nil, fmt.Errorf("another gbot instance is already running (PID %d)", existing)
			}
		}
	}

	if writeErr := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644); writeErr != nil {
		return nil, fmt.Errorf("write PID file: %w", writeErr)
	}

	cleanup = func() {
		_ = os.Remove(pidPath)
	}
	return cleanup, nil
}

// isProcessAlive checks whether a process with the given PID exists.
// Platform-specific: see pid_unix.go and pid_windows.go.
func isProcessAlive(pid int) bool {
	return isProcessAliveImpl(pid)
}
