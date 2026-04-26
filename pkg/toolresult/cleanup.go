package toolresult

import (
	"os"
	"path/filepath"
	"time"
)

// CleanupSession deletes the tool-results directory for a session.
// Safe to call multiple times; returns nil if the directory does not exist.
func CleanupSession(sessionID string) error {
	dir, err := GetToolResultsDir(sessionID)
	if err != nil {
		return err
	}
	// Remove the tool-results subdir only, not the entire session dir.
	err = os.RemoveAll(dir)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// CleanupOldSessions removes tool-results directories for sessions whose
// modification time is older than cutoff. Returns the number of sessions
// cleaned and any error encountered.
//
// It scans the sessions root (~/.gbot/sessions/) and removes the
// tool-results/ subdirectory of any session directory whose mtime is before
// cutoff.
func CleanupOldSessions(cutoff time.Time) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	sessionsDir := filepath.Join(home, ".gbot", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	cleaned := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		trDir := filepath.Join(sessionsDir, entry.Name(), ToolResultsSubdir)
		info, err := os.Stat(trDir)
		if err != nil {
			continue // doesn't exist or not accessible
		}
		if info.ModTime().Before(cutoff) {
			if os.RemoveAll(trDir) == nil {
				cleaned++
			}
		}
	}
	return cleaned, nil
}
