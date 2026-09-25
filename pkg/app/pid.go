package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/project"
)

// Grace window for a dying predecessor. The Android app stops the old daemon
// and respawns immediately; the old process needs a moment to release the PID
// file, and failing instantly left the phone without a daemon for minutes
// (the app's respawn backoff). Vars (not consts) so tests can shrink them.
var (
	pidWaitTotal = 10 * time.Second
	pidWaitStep  = 250 * time.Millisecond
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
		if err := waitForPIDRelease(pidPath); err != nil {
			return nil, err
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

// waitForPIDRelease blocks while the PID file names a live process, up to
// pidWaitTotal. A nil return means the file is gone, unparseable, or names a
// dead process — i.e. the lock is free. The error keeps the historical
// "already running" wording for a holder that outlives the window (a real
// second instance, as opposed to our own dying predecessor).
func waitForPIDRelease(pidPath string) error {
	deadline := time.Now().Add(pidWaitTotal)
	for {
		data, readErr := os.ReadFile(pidPath)
		if readErr != nil {
			return nil // no file: lock free
		}
		existing, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil || !isProcessAlive(existing) {
			return nil // stale or garbage: lock free
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("another gbot instance is already running (PID %d)", existing)
		}
		time.Sleep(pidWaitStep)
	}
}
