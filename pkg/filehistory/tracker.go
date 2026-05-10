package filehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// BackupRecord represents one file backup (pre-edit content saved to disk).
type BackupRecord struct {
	FilePath   string `json:"filePath"`   // absolute path
	BackupName string `json:"backupName"` // {hash16}@v{N}, empty = file didn't exist before edit
	Version    int    `json:"version"`    // per-file version counter
	TurnIndex  int    `json:"turnIndex"`  // engine message index of the user message
}

// Tracker manages file backup records for the rewind system.
// Source: TS fileHistory.ts — simplified to flat backup record model.
//
// The TS source uses a snapshot system (fileHistoryMakeSnapshot at each turn).
// gbot uses flat BackupRecord + on-demand restore, which is simpler and equivalent:
//   - TS snapshot: "at turn N, file F is at version V"
//   - gbot record: "at turn N, file F was backed up from pre-edit content"
//   - Restore finds the earliest record with turnIndex >= targetIndex.
type Tracker struct {
	dir      string         // backup storage directory
	versions map[string]int // filePath -> current version counter
	records  []BackupRecord // all backup records (in-memory, persisted to store)
	mu       sync.Mutex
}

// NewTracker creates a new file history tracker.
// dir is the base directory for storing backup files
// (typically ~/.gbot/file-history/{sessionID}/).
func NewTracker(dir string) *Tracker {
	return &Tracker{
		dir:      dir,
		versions: make(map[string]int),
		records:  make([]BackupRecord, 0),
	}
}

// RecordBackup saves the pre-edit file content as a backup on disk.
// originalContent is nil if the file didn't exist before the edit.
// turnIndex is the engine message index of the user message that triggered the edit.
func (t *Tracker) RecordBackup(filePath string, originalContent []byte, turnIndex int) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	version := t.versions[filePath] + 1
	t.versions[filePath] = version

	var backupName string
	if originalContent != nil {
		// File existed before — save backup to disk
		hash := sha256.Sum256([]byte(filePath))
		hashStr := hex.EncodeToString(hash[:])[:16]
		backupName = fmt.Sprintf("%s@v%d", hashStr, version)

		if err := os.MkdirAll(t.dir, 0o755); err != nil {
			return fmt.Errorf("filehistory: mkdir: %w", err)
		}

		backupPath := filepath.Join(t.dir, backupName)
		if err := os.WriteFile(backupPath, originalContent, 0o644); err != nil {
			return fmt.Errorf("filehistory: write backup: %w", err)
		}
	}
	// backupName == "" means file didn't exist before edit

	t.records = append(t.records, BackupRecord{
		FilePath:   filePath,
		BackupName: backupName,
		Version:    version,
		TurnIndex:  turnIndex,
	})
	return nil
}

// RestoreToIndex restores all files to their state at turnIndex.
// Returns list of restored file paths.
//
// For each file with backups at turnIndex or later:
//  1. Find the backup with the smallest turnIndex >= targetIndex
//  2. That backup contains the pre-edit content = state before that turn's edit
//  3. If backupName is empty (file was created during the rewound turns) → delete the file
//  4. Otherwise, restore backup file to original path
//
// The >= operator is correct: RewindTo(N) removes messages [N..end].
// Record(turnIndex=N) has pre-edit-of-turn-N = post-turn-(N-1) state.
// This is exactly the file state we want to restore to.
func (t *Tracker) RestoreToIndex(targetIndex int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	restored := t.restoreFilesLocked(targetIndex)
	t.truncateRecordsLocked(targetIndex)
	return restored, nil
}

// RestoreFilesOnly restores files to their state at targetIndex without
// truncating backup records. Used by "Restore code only" /rewind option.
func (t *Tracker) RestoreFilesOnly(targetIndex int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restoreFilesLocked(targetIndex), nil
}

// HasRecordsAtOrAfter returns true if any backup record has turnIndex >= targetIndex.
// Used by TUI to decide whether to show the "Restore code" option.
func (t *Tracker) HasRecordsAtOrAfter(targetIndex int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.records {
		if r.TurnIndex >= targetIndex {
			return true
		}
	}
	return false
}

// restoreFilesLocked restores all files to their state at targetIndex.
// Must be called with t.mu held.
func (t *Tracker) restoreFilesLocked(targetIndex int) []string {
	// Group records by filePath
	byFile := make(map[string][]BackupRecord)
	for _, r := range t.records {
		byFile[r.FilePath] = append(byFile[r.FilePath], r)
	}

	var restored []string

	for filePath, records := range byFile {
		// Sort by turnIndex ascending
		sort.Slice(records, func(i, j int) bool {
			return records[i].TurnIndex < records[j].TurnIndex
		})

		// Find the earliest record with turnIndex >= targetIndex
		var target *BackupRecord
		for i := range records {
			if records[i].TurnIndex >= targetIndex {
				target = &records[i]
				break
			}
		}
		if target == nil {
			continue // no records at or after targetIndex
		}

		if target.BackupName == "" {
			// File was created during the rewound turns — delete it
			if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
				slog.Warn("filehistory:restore:delete_failed", "file", filePath, "err", err)
			} else {
				restored = append(restored, filePath)
			}
		} else {
			// Restore backup to original path
			backupPath := filepath.Join(t.dir, target.BackupName)
			data, err := os.ReadFile(backupPath)
			if err != nil {
				slog.Warn("filehistory:restore:read_backup_failed", "backup", backupPath, "err", err)
				continue
			}
			if err := os.WriteFile(filePath, data, 0o644); err != nil {
				slog.Warn("filehistory:restore:write_failed", "file", filePath, "err", err)
				continue
			}
			restored = append(restored, filePath)
		}
	}

	return restored
}

// Records returns a copy of all backup records (for persistence).
func (t *Tracker) Records() []BackupRecord {
	t.mu.Lock()
	defer t.mu.Unlock()

	result := make([]BackupRecord, len(t.records))
	copy(result, t.records)
	return result
}

// LoadRecords replaces in-memory records from persisted data (crash recovery).
func (t *Tracker) LoadRecords(records []BackupRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.records = make([]BackupRecord, len(records))
	copy(t.records, records)

	// Rebuild version counters from loaded records
	t.versions = make(map[string]int)
	for _, r := range t.records {
		if r.Version > t.versions[r.FilePath] {
			t.versions[r.FilePath] = r.Version
		}
	}
}

// TruncateRecords removes records with turnIndex >= targetIndex (after rewind).
func (t *Tracker) TruncateRecords(targetIndex int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.truncateRecordsLocked(targetIndex)
}

// truncateRecordsLocked is the unlocked version of TruncateRecords.
func (t *Tracker) truncateRecordsLocked(targetIndex int) {
	filtered := t.records[:0]
	for _, r := range t.records {
		if r.TurnIndex < targetIndex {
			filtered = append(filtered, r)
		}
	}
	t.records = filtered
}

// fileHash returns the first 16 hex characters of sha256(filePath).
func fileHash(filePath string) string {
	hash := sha256.Sum256([]byte(filePath))
	return hex.EncodeToString(hash[:])[:16]
}

// skipDir returns true for directories that should be skipped during WalkDir.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "__pycache__", ".hg", ".svn":
		return true
	}
	return false
}

// IsSkippedDir exposes skipDir for testing.
func IsSkippedDir(name string) bool {
	return skipDir(name)
}

// WalkDir traverses root, calling fn for each file. Skips known large directories.
// Returns the first error from fn, or a WalkDir error.
func WalkDir(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible files
		}
		if d.IsDir() && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		return fn(path, d)
	})
}
