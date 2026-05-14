package filehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Snapshot-based file history data model.
// Source: TS fileHistory.ts:31-52
//
// TS uses BackupFileName = string | null. Go uses empty string as null sentinel:
//   - BackupFileName == "" means file did not exist at this version (TS null)
//   - BackupFileName != "" is the backup file name on disk (TS string)
// ---------------------------------------------------------------------------

// FileHistoryBackup represents one file's backup state at a snapshot point.
// Source: TS fileHistory.ts:33-37
type FileHistoryBackup struct {
	BackupFileName string    // hash@vN, empty = file did not exist (TS null)
	Version        int       // per-file version counter, starts at 1
	BackupTime     time.Time // when this backup was created
}

// FileHistorySnapshot captures all tracked file states at one turn boundary.
// Each snapshot is created at the end of a turn by MakeSnapshot.
// Source: TS fileHistory.ts:39-43
type FileHistorySnapshot struct {
	MessageID          string                       // user message UUID
	TrackedFileBackups map[string]FileHistoryBackup // absolute filePath → backup
	Timestamp          time.Time                    // when this snapshot was created
}

// FileHistoryState is the full in-memory state of the file history system.
// Source: TS fileHistory.ts:45-52
type FileHistoryState struct {
	Snapshots        []FileHistorySnapshot // ordered list of turn-end snapshots
	TrackedFiles     map[string]bool       // set of absolute file paths being tracked
	SnapshotSequence int                   // monotonically-increasing counter for each snapshot
}

// MAX_SNAPSHOTS is the maximum number of snapshots retained in memory.
// Oldest snapshots are evicted when this limit is exceeded.
// Source: TS fileHistory.ts:54
const MAX_SNAPSHOTS = 100

// ---------------------------------------------------------------------------
// Tracker manages file backup snapshots for the rewind system.
// Source: TS fileHistory.ts — translated from flat record to snapshot model.
//
// The TS source uses an updateFileHistoryState callback pattern for atomic state
// updates. Go uses sync.Mutex directly, which is simpler and equivalent:
//   - TS updateFileHistoryState(updater) → Go t.mu.Lock() + defer t.mu.Unlock()
//   - TS async IO → Go synchronous IO (standard in Go)
//   - TS maybeShortenFilePath/maybeExpandFilePath → gbot uses absolute paths directly
// ---------------------------------------------------------------------------

// Tracker manages file backup records for the rewind system using snapshots.
type Tracker struct {
	dir   string           // backup storage directory (~/.gbot/file-history/{sessionID}/)
	state FileHistoryState // full in-memory snapshot state
	mu    sync.Mutex
}

// NewTracker creates a new file history tracker with an initial empty snapshot.
// dir is the base directory for storing backup files
// (typically ~/.gbot/file-history/{sessionID}/).
// Source: TS fileHistory.ts — initial state has one empty snapshot.
func NewTracker(dir string) *Tracker {
	t := &Tracker{
		dir: dir,
		state: FileHistoryState{
			Snapshots: []FileHistorySnapshot{
				{
					MessageID:          "",
					TrackedFileBackups: make(map[string]FileHistoryBackup),
					Timestamp:          time.Now(),
				},
			},
			TrackedFiles:     make(map[string]bool),
			SnapshotSequence: 0,
		},
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Warn("filehistory:new_tracker:mkdir_failed", "dir", dir, "err", err)
	}
	return t
}

// Dir returns the backup storage directory path.
func (t *Tracker) Dir() string {
	return t.dir
}

// ---------------------------------------------------------------------------
// TrackEdit — records a file edit before it happens.
// Source: TS fileHistory.ts:86-193 — fileHistoryTrackEdit
//
// TrackEdit must be called BEFORE the file is written. It reads the current
// file content from disk and creates a v1 backup. If the same file is tracked
// multiple times in the same turn (same mostRecentSnapshot), it skips the
// duplicate — the v1 backup preserves the original pre-edit content.
//
// TS Phase 1: Check if backup is needed (deduplication check).
// TS Phase 2: Create backup (async in TS, sync in Go).
// TS Phase 3: Commit to state (updateFileHistoryState → mutex in Go).
// ---------------------------------------------------------------------------

// TrackEdit records a file edit by creating a backup of its current contents.
// Must be called BEFORE the file is actually edited, so the pre-edit content
// is preserved. Same file same turn (same snapshot) is deduplicated.
// Source: TS fileHistory.ts:86-193 — fileHistoryTrackEdit
func (t *Tracker) TrackEdit(filePath string) error {
	return t.trackEditLocked(filePath, func() (FileHistoryBackup, error) {
		return t.createBackup(filePath, 1)
	})
}

// TrackEditFromContent records a file edit using provided content instead of
// reading from disk. Used for Bash-detected changes where BeforeContent is
// available from the pre-execution snapshot.
// nil content means the file did not exist (null backup).
func (t *Tracker) TrackEditFromContent(filePath string, content []byte) error {
	return t.trackEditLocked(filePath, func() (FileHistoryBackup, error) {
		return t.createBackupFromContent(filePath, 1, content)
	})
}

// trackEditLocked is the shared implementation for TrackEdit and TrackEditFromContent.
// backupFn is called inside the mutex to create the v1 backup.
func (t *Tracker) trackEditLocked(filePath string, backupFn func() (FileHistoryBackup, error)) error {
	if filePath == "" {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// TS Phase 1: check if backup is needed.
	// Speculative writes would overwrite the deterministic {hash}@v1 backup
	// on every repeat call — a second trackEdit after an edit would corrupt
	// v1 with post-edit content.
	mostRecent := t.mostRecentSnapshotLocked()
	if mostRecent == nil {
		slog.Error("filehistory:track_edit:missing_snapshot")
		return fmt.Errorf("filehistory: no snapshots available")
	}
	if _, alreadyTracked := mostRecent.TrackedFileBackups[filePath]; alreadyTracked {
		return nil
	}

	// TS Phase 2: create backup.
	backup, err := backupFn()
	if err != nil {
		slog.Error("filehistory:track_edit:backup_failed", "file", filePath, "err", err)
		return fmt.Errorf("filehistory: track edit backup: %w", err)
	}

	// TS Phase 3: commit to state.
	// Re-check tracked (another trackEdit may have raced — defensive in Go
	// since mutex serializes, but matches TS pattern).
	mostRecent = t.mostRecentSnapshotLocked()
	if mostRecent == nil {
		return fmt.Errorf("filehistory: snapshot disappeared during track edit")
	}
	if _, alreadyTracked := mostRecent.TrackedFileBackups[filePath]; alreadyTracked {
		return nil
	}

	t.state.TrackedFiles[filePath] = true
	mostRecent.TrackedFileBackups[filePath] = backup
	return nil
}

// Source: TS fileHistory.ts:198-342 — fileHistoryMakeSnapshot
//
// Called at the end of each turn by the Engine. For each tracked file:
//  1. Stat the file to check if it exists
//  2. If exists and unchanged since last backup → reuse latest backup reference
//  3. If exists and changed → create new backup with next version
//  4. If deleted → record null backup (BackupFileName == "")
//
// Creates a new FileHistorySnapshot and appends to state.
// Evicts oldest snapshot if total exceeds MAX_SNAPSHOTS.
// ---------------------------------------------------------------------------

// MakeSnapshot creates a turn-end snapshot of all tracked files.
// Source: TS fileHistory.ts:198-342 — fileHistoryMakeSnapshot
func (t *Tracker) MakeSnapshot(messageID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// TS Phase 2: do all IO for tracked file backups.
	trackedFileBackups := make(map[string]FileHistoryBackup)
	mostRecentSnapshot := t.mostRecentSnapshotLocked()
	if mostRecentSnapshot == nil {
		return nil // no snapshots to reference
	}

	for filePath := range t.state.TrackedFiles {
		latestBackup := mostRecentSnapshot.TrackedFileBackups[filePath]
		nextVersion := latestBackup.Version + 1
		if latestBackup.Version == 0 {
			nextVersion = 1
		}

		// Stat the file once; ENOENT means the tracked file was deleted.
		fileInfo, statErr := os.Stat(filePath)
		if statErr != nil && !os.IsNotExist(statErr) {
			slog.Error("filehistory:make_snapshot:stat_failed", "file", filePath, "err", statErr)
			continue
		}

		if os.IsNotExist(statErr) {
			// File does not exist — record null backup.
			// TS: backupFileName: null (Go: BackupFileName == "")
			trackedFileBackups[filePath] = FileHistoryBackup{
				BackupFileName: "",
				Version:        nextVersion,
				BackupTime:     time.Now(),
			}
			continue
		}

		// File exists — check if it needs to be backed up.
		if latestBackup.BackupFileName != "" {
			changed, err := t.checkOriginFileChanged(filePath, latestBackup.BackupFileName, fileInfo)
			if err != nil {
				slog.Error("filehistory:make_snapshot:check_changed_failed", "file", filePath, "err", err)
				continue
			}
			if !changed {
				// File hasn't been modified since the latest version, reuse it.
				trackedFileBackups[filePath] = latestBackup
				continue
			}
		}

		// File is newer than the latest backup, create a new backup.
		backup, err := t.createBackup(filePath, nextVersion)
		if err != nil {
			slog.Error("filehistory:make_snapshot:backup_failed", "file", filePath, "err", err)
			continue
		}
		trackedFileBackups[filePath] = backup
	}

	// TS Phase 3: commit the new snapshot to state.
	// Inherit trackedFileBackups from lastSnapshot for any files not yet processed
	// (if fileHistoryTrackEdit added a file during our processing).
	lastSnapshot := t.mostRecentSnapshotLocked()
	if lastSnapshot != nil {
		for filePath := range t.state.TrackedFiles {
			if _, ok := trackedFileBackups[filePath]; ok {
				continue
			}
			if inherited, ok := lastSnapshot.TrackedFileBackups[filePath]; ok {
				trackedFileBackups[filePath] = inherited
			}
		}
	}

	newSnapshot := FileHistorySnapshot{
		MessageID:          messageID,
		TrackedFileBackups: trackedFileBackups,
		Timestamp:          time.Now(),
	}

	allSnapshots := append(t.state.Snapshots, newSnapshot)
	if len(allSnapshots) > MAX_SNAPSHOTS {
		allSnapshots = allSnapshots[len(allSnapshots)-MAX_SNAPSHOTS:]
	}

	t.state.Snapshots = allSnapshots
	t.state.SnapshotSequence++

	return nil
}

// ---------------------------------------------------------------------------
// Rewind — restores files to their state at a given snapshot.
// Source: TS fileHistory.ts:347-397 — fileHistoryRewind
//
// Finds the target snapshot by messageID, then applies it to all tracked files.
// ---------------------------------------------------------------------------

// Rewind restores all tracked files to their state at the snapshot for messageID.
// Returns list of restored file paths.
// Source: TS fileHistory.ts:347-397 — fileHistoryRewind
func (t *Tracker) Rewind(messageID string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	targetSnapshot := t.findLastSnapshotLocked(messageID)
	if targetSnapshot == nil {
		return nil, fmt.Errorf("filehistory: snapshot for %s not found", messageID)
	}

	restored := t.applySnapshotLocked(targetSnapshot)
	return restored, nil
}

// RewindFilesOnly restores files to their state at the snapshot for messageID
// without truncating snapshots. Used by "Restore code only" /rewind option.
func (t *Tracker) RewindFilesOnly(messageID string) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	targetSnapshot := t.findLastSnapshotLocked(messageID)
	if targetSnapshot == nil {
		return nil, fmt.Errorf("filehistory: snapshot for %s not found", messageID)
	}

	return t.applySnapshotLocked(targetSnapshot), nil
}

// HasChangesAtMessage returns true if rewinding to the snapshot for messageID
// would change any file on disk.
// Source: TS fileHistory.ts:494-531 — fileHistoryHasAnyChanges
func (t *Tracker) HasChangesAtMessage(messageID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	targetSnapshot := t.findLastSnapshotLocked(messageID)
	slog.Info("filehistory:HasChangesAtMessage", "messageID", messageID, "snapshot_found", targetSnapshot != nil, "trackedFiles", len(t.state.TrackedFiles), "totalSnapshots", len(t.state.Snapshots))
	if targetSnapshot == nil {
		return false
	}

	for filePath := range t.state.TrackedFiles {
		targetBackup := targetSnapshot.TrackedFileBackups[filePath]

		var backupFileName string
		var hasBackup bool
		if targetBackup.Version != 0 {
			backupFileName = targetBackup.BackupFileName
			hasBackup = true
		} else {
			// File not in target snapshot — look for first version fallback.
			bf, found := t.getBackupFileNameFirstVersionLocked(filePath)
			if !found {
				continue
			}
			backupFileName = bf
			hasBackup = true
		}
		_ = hasBackup

		if backupFileName == "" {
			// Backup says file did not exist; check if it exists now.
			if _, err := os.Stat(filePath); err == nil {
				return true // file exists but shouldn't
			}
			continue
		}

		changed, err := t.checkOriginFileChanged(filePath, backupFileName, nil)
		if err != nil {
			continue
		}
		if changed {
			return true
		}
	}
	return false
}

// TruncateSnapshotsFrom removes snapshots with the given messageID and all
// snapshots after it. Used for message-only rewind to clean up snapshot state.
func (t *Tracker) TruncateSnapshotsFrom(messageID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Find the index of the snapshot with messageID.
	idx := -1
	for i, snap := range t.state.Snapshots {
		if snap.MessageID == messageID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	t.state.Snapshots = t.state.Snapshots[:idx]
}

// State returns a copy of the full file history state (for persistence).
func (t *Tracker) State() FileHistoryState {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.copyStateLocked()
}

// LoadState replaces in-memory state from persisted data (crash recovery).
func (t *Tracker) LoadState(state FileHistoryState) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.state = FileHistoryState{
		Snapshots:        make([]FileHistorySnapshot, len(state.Snapshots)),
		TrackedFiles:     make(map[string]bool),
		SnapshotSequence: state.SnapshotSequence,
	}
	copy(t.state.Snapshots, state.Snapshots)
	maps.Copy(t.state.TrackedFiles, state.TrackedFiles)
	// Deep-copy snapshot trackedFileBackups maps.
	for i, snap := range t.state.Snapshots {
		original := state.Snapshots[i]
		snap.TrackedFileBackups = make(map[string]FileHistoryBackup, len(original.TrackedFileBackups))
		maps.Copy(snap.TrackedFileBackups, original.TrackedFileBackups)
		t.state.Snapshots[i] = snap
	}
}

// ---------------------------------------------------------------------------
// Internal methods
// ---------------------------------------------------------------------------

// mostRecentSnapshotLocked returns the last snapshot. Must be called with t.mu held.
func (t *Tracker) mostRecentSnapshotLocked() *FileHistorySnapshot {
	if len(t.state.Snapshots) == 0 {
		return nil
	}
	return &t.state.Snapshots[len(t.state.Snapshots)-1]
}

// findLastSnapshotLocked finds the last snapshot with the given messageID.
// Source: TS Array.findLast(predicate) — Go equivalent.
// Must be called with t.mu held.
func (t *Tracker) findLastSnapshotLocked(messageID string) *FileHistorySnapshot {
	for i := len(t.state.Snapshots) - 1; i >= 0; i-- {
		if t.state.Snapshots[i].MessageID == messageID {
			return &t.state.Snapshots[i]
		}
	}
	return nil
}

// applySnapshotLocked applies the given snapshot to all tracked files on disk.
// Returns list of file paths that were changed.
// Source: TS fileHistory.ts:537-591 — applySnapshot
// Must be called with t.mu held.
func (t *Tracker) applySnapshotLocked(targetSnapshot *FileHistorySnapshot) []string {
	var filesChanged []string

	for filePath := range t.state.TrackedFiles {
		targetBackup := targetSnapshot.TrackedFileBackups[filePath]

		// Determine backupFileName: from target snapshot, or first version fallback.
		var backupFileName string
		var hasBackup bool
		if targetBackup.Version != 0 {
			backupFileName = targetBackup.BackupFileName
			hasBackup = true
		} else {
			// File not in target snapshot — search for first version.
			bf, found := t.getBackupFileNameFirstVersionLocked(filePath)
			if !found {
				// Cannot determine backup state — don't touch the file.
				slog.Error("filehistory:apply:backup_not_found", "file", filePath)
				continue
			}
			backupFileName = bf
			hasBackup = true
		}

		if !hasBackup {
			continue
		}

		if backupFileName == "" {
			// File did not exist at the target version; delete it if present.
			// TS: backupFileName === null → unlink(filePath)
			err := os.Remove(filePath)
			if err != nil && !os.IsNotExist(err) {
				slog.Error("filehistory:apply:delete_failed", "file", filePath, "err", err)
				continue
			}
			if err == nil {
				filesChanged = append(filesChanged, filePath)
			}
			continue
		}

		// Restore file from backup only if it differs from backup.
		// Source: TS fileHistory.ts:575-577 — conditional restore via checkOriginFileChanged.
		changed, checkErr := t.checkOriginFileChanged(filePath, backupFileName, nil)
		if checkErr != nil || !changed {
			continue
		}
		if err := t.restoreBackup(filePath, backupFileName); err != nil {
			slog.Error("filehistory:apply:restore_failed", "file", filePath, "err", err)
			continue
		}
		filesChanged = append(filesChanged, filePath)
	}

	return filesChanged
}

// ---------------------------------------------------------------------------
// createBackup — creates a backup file on disk.
// Source: TS fileHistory.ts:748-798 — createBackup
//
// If the file does not exist (ENOENT), records a null backup (BackupFileName == "").
// Uses lazy mkdir: tries copyFile first, creates directory on ENOENT.
// ---------------------------------------------------------------------------

// createBackup creates a backup of the file at filePath with the given version.
// If the file does not exist, returns a backup with empty BackupFileName.
// Source: TS fileHistory.ts:748-798 — createBackup
func (t *Tracker) createBackup(filePath string, version int) (FileHistoryBackup, error) {
	if filePath == "" {
		return FileHistoryBackup{BackupFileName: "", Version: version, BackupTime: time.Now()}, nil
	}

	backupFileName := getBackupFileName(filePath, version)
	backupPath := filepath.Join(t.dir, backupFileName)

	// Stat first: if the source is missing, record a null backup and skip copy.
	// Source: TS L763-771 — separate "source missing" from "backup dir missing".
	fileInfo, statErr := os.Stat(filePath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return FileHistoryBackup{BackupFileName: "", Version: version, BackupTime: time.Now()}, nil
		}
		return FileHistoryBackup{}, fmt.Errorf("filehistory: stat source: %w", statErr)
	}

	// Copy source file to backup using io.Copy (kernel-level, no full-content buffering).
	// Source: TS L776-783 — copyFile with lazy mkdir.
	if err := t.copyFileData(filePath, backupPath, fileInfo.Mode()); err != nil {
		return FileHistoryBackup{}, fmt.Errorf("filehistory: copy backup: %w", err)
	}

	return FileHistoryBackup{
		BackupFileName: backupFileName,
		Version:        version,
		BackupTime:     time.Now(),
	}, nil
}

// createBackupFromContent creates a backup from provided content instead of
// reading from disk. nil content → null backup (file didn't exist).
func (t *Tracker) createBackupFromContent(filePath string, version int, content []byte) (FileHistoryBackup, error) {
	if filePath == "" || content == nil {
		return FileHistoryBackup{BackupFileName: "", Version: version, BackupTime: time.Now()}, nil
	}

	backupFileName := getBackupFileName(filePath, version)
	backupPath := filepath.Join(t.dir, backupFileName)

	if err := t.writeBackupData(backupPath, content, 0o644); err != nil {
		return FileHistoryBackup{}, fmt.Errorf("filehistory: write backup: %w", err)
	}

	return FileHistoryBackup{
		BackupFileName: backupFileName,
		Version:        version,
		BackupTime:     time.Now(),
	}, nil
}

// writeBackupData writes data to path with lazy mkdir (consistent with copyFileData).
func (t *Tracker) writeBackupData(path string, data []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
			return mkdirErr
		}
		return os.WriteFile(path, data, mode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// restoreBackup — restores a file from its backup.
// Source: TS fileHistory.ts:804-837 — restoreBackup
//
// Lazy mkdir: tries copyFile first, creates directory on ENOENT.
// Preserves file permissions from the backup.
// ---------------------------------------------------------------------------

// restoreBackup restores a file from its backup file.
// Source: TS fileHistory.ts:804-837 — restoreBackup
func (t *Tracker) restoreBackup(filePath, backupFileName string) error {
	backupPath := filepath.Join(t.dir, backupFileName)

	// Stat backup first: if missing, log and bail.
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Error("filehistory:restore:backup_not_found", "backup", backupPath)
			return fmt.Errorf("filehistory: backup file not found: %s", backupPath)
		}
		return fmt.Errorf("filehistory: stat backup: %w", err)
	}

	// Copy backup to destination using io.Copy (kernel-level, no full-content buffering).
	// Source: TS L826-833 — copyFile with lazy mkdir.
	if err := t.copyFileData(backupPath, filePath, backupInfo.Mode()); err != nil {
		return fmt.Errorf("filehistory: restore copy: %w", err)
	}

	return nil
}

// copyFileData copies src to dst using io.Copy (streams, no full-content buffering).
// Source: TS L776-783, L826-833 — fs.copyFile with lazy mkdir.
func (t *Tracker) copyFileData(src, dst string, mode fs.FileMode) error {
	// Try copy first (fast path: destination dir exists).
	if err := t.copyFileDataOnce(src, dst, mode); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Slow path: destination dir missing — create and retry.
	if mkdirErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkdirErr != nil {
		return fmt.Errorf("mkdir: %w", mkdirErr)
	}
	return t.copyFileDataOnce(src, dst, mode)
}

func (t *Tracker) copyFileDataOnce(src, dst string, mode fs.FileMode) error {
	sf, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer sf.Close()

	df, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer df.Close()

	if _, err := io.Copy(df, sf); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return df.Close()
}

// ---------------------------------------------------------------------------
// checkOriginFileChanged — compares original file against backup.
// Source: TS fileHistory.ts:600-634 — checkOriginFileChanged
//
// Three-tier comparison:
//  1. Existence: one exists, one missing → changed
//  2. Stats: mode or size differ → changed
//  3. Content: actual byte comparison (most expensive, last resort)
//
// Optional originalStatsHint avoids a second stat when caller already has it.
// Source: TS fileHistory.ts:640-672 — compareStatsAndContent
// ---------------------------------------------------------------------------

// checkOriginFileChanged returns true if the original file differs from its backup.
// Source: TS fileHistory.ts:600-634 — checkOriginFileChanged
func (t *Tracker) checkOriginFileChanged(originalFile, backupFileName string, originalStatsHint os.FileInfo) (bool, error) {
	backupPath := filepath.Join(t.dir, backupFileName)

	var originalStats os.FileInfo
	if originalStatsHint != nil {
		originalStats = originalStatsHint
	} else {
		var err error
		originalStats, err = os.Stat(originalFile)
		if err != nil {
			if os.IsNotExist(err) {
				// Original missing but backup exists → changed
				return true, nil
			}
			return true, err
		}
	}

	backupStats, err := os.Stat(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Backup missing → treat as changed
			return true, nil
		}
		return true, err
	}

	// compareStatsAndContent: TS L640-672
	// One exists, one missing → changed (both exist here, so skip)
	// Check file stats: permission mode and size.
	if originalStats.Mode() != backupStats.Mode() || originalStats.Size() != backupStats.Size() {
		return true, nil
	}

	// Optimization: if original mtime is before backup mtime, skip content comparison.
	if originalStats.ModTime().Before(backupStats.ModTime()) {
		return false, nil
	}

	// Content comparison — most expensive, last resort.
	originalContent, err := os.ReadFile(originalFile)
	if err != nil {
		return true, nil // file deleted between stat and read → treat as changed
	}
	backupContent, err := os.ReadFile(backupPath)
	if err != nil {
		return true, nil
	}
	return !bytes.Equal(originalContent, backupContent), nil
}

// ---------------------------------------------------------------------------
// getBackupFileNameFirstVersion — finds earliest backup for a file.
// Source: TS fileHistory.ts:847-862 — getBackupFileNameFirstVersion
//
// Searches all snapshots for the earliest version (v1) of a file.
// Used by applySnapshot when a file is not in the target snapshot.
// Returns ("", true) if file did not exist in v1, ("filename", true) if found,
// or ("", false) if not found at all.
// ---------------------------------------------------------------------------

// getBackupFileNameFirstVersionLocked searches all snapshots for the earliest
// backup of filePath.
// Source: TS fileHistory.ts:847-862 — getBackupFileNameFirstVersion
// Must be called with t.mu held.
func (t *Tracker) getBackupFileNameFirstVersionLocked(filePath string) (backupFileName string, found bool) {
	for _, snapshot := range t.state.Snapshots {
		backup, ok := snapshot.TrackedFileBackups[filePath]
		if ok && backup.Version == 1 {
			return backup.BackupFileName, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// getBackupFileName generates a deterministic backup file name.
// Format: {sha256_first16hex}@v{version}
// Source: TS fileHistory.ts:725-731 — getBackupFileName
func getBackupFileName(filePath string, version int) string {
	hash := sha256.Sum256([]byte(filePath))
	hashStr := hex.EncodeToString(hash[:])[:16]
	return fmt.Sprintf("%s@v%d", hashStr, version)
}

// copyStateLocked returns a deep copy of the current state.
// Must be called with t.mu held.
func (t *Tracker) copyStateLocked() FileHistoryState {
	result := FileHistoryState{
		Snapshots:        make([]FileHistorySnapshot, len(t.state.Snapshots)),
		TrackedFiles:     make(map[string]bool, len(t.state.TrackedFiles)),
		SnapshotSequence: t.state.SnapshotSequence,
	}
	maps.Copy(result.TrackedFiles, t.state.TrackedFiles)
	for i, snap := range t.state.Snapshots {
		result.Snapshots[i] = FileHistorySnapshot{
			MessageID:          snap.MessageID,
			TrackedFileBackups: make(map[string]FileHistoryBackup, len(snap.TrackedFileBackups)),
			Timestamp:          snap.Timestamp,
		}
		maps.Copy(result.Snapshots[i].TrackedFileBackups, snap.TrackedFileBackups)
	}
	return result
}

// ---------------------------------------------------------------------------
// Directory walking utilities (kept from original tracker.go)
// ---------------------------------------------------------------------------

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
