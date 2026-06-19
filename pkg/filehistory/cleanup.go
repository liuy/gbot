// Package filehistory provides cleanup of old backup session directories.
//
// Source: TS cleanup.ts:305-348 — cleanupOldFileHistoryBackups
package filehistory

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// DefaultCleanupAge is the default maximum age for backup session directories.
// Rewind is only useful within the current session, so 1 day covers all
// realistic use cases. Git history handles anything older.
const DefaultCleanupAge = 24 * time.Hour

// CleanupOldBackups removes backup session directories older than maxAge under
// the given fileHistoryDir (typically ~/.gbot/file-history/).
// Source: TS cleanup.ts:305-348 — cleanupOldFileHistoryBackups
//
// Each subdirectory represents one session's backups. If a session directory's
// mtime is older than the cutoff, the entire directory is removed recursively.
func CleanupOldBackups(fileHistoryDir string, maxAge time.Duration) (cleaned int, err error) {
	dirents, readErr := os.ReadDir(fileHistoryDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return 0, nil
		}
		return 0, readErr
	}

	cutoff := time.Now().Add(-maxAge)

	for _, d := range dirents {
		if !d.IsDir() {
			continue
		}
		sessionDir := filepath.Join(fileHistoryDir, d.Name())
		info, statErr := os.Stat(sessionDir)
		if statErr != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if rmErr := os.RemoveAll(sessionDir); rmErr != nil {
				slog.Warn("filehistory:cleanup:remove_failed", "dir", sessionDir, "err", rmErr)
				continue
			}
			cleaned++
		}
	}

	// Try to remove the parent directory if empty (matches TS tryRmdir behavior).
	_ = os.Remove(fileHistoryDir)

	return cleaned, nil
}
