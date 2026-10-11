package filehistory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func setMtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// checkOriginFileChanged decides whether a restore is needed; every branch of
// the size/mode/mtime/content ladder is pinned.
func TestCheckOriginFileChanged_Matrix(t *testing.T) {
	histDir := t.TempDir()
	workDir := t.TempDir()
	tr := NewTracker(histDir)

	orig := filepath.Join(workDir, "f.txt")
	backup := "f@v1"
	backupPath := filepath.Join(histDir, backup)

	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	older := time.Now().Add(-time.Hour)
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	newer := time.Now().Add(time.Hour)

	// Same size and mode, original older than the backup: the mtime shortcut
	// reports "unchanged" without reading contents.
	writeFileMode(t, orig, "aaaa", 0o644)
	writeFileMode(t, backupPath, "bbbb", 0o644)
	setMtime(t, orig, older)
	setMtime(t, backupPath, newer)
	if changed, err := tr.checkOriginFileChanged(orig, backup, nil); err != nil || changed {
		t.Errorf("mtime shortcut: changed=%v err=%v, want false/nil", changed, err)
	}

	// Same size, original newer: content comparison runs and finds a diff.
	setMtime(t, orig, newer)
	setMtime(t, backupPath, older)
	if changed, err := tr.checkOriginFileChanged(orig, backup, nil); err != nil || !changed {
		t.Errorf("differing content: changed=%v err=%v, want true/nil", changed, err)
	}

	// Identical content, newer original: no change.
	writeFileMode(t, orig, "bbbb", 0o644)
	setMtime(t, orig, newer)
	if changed, err := tr.checkOriginFileChanged(orig, backup, nil); err != nil || changed {
		t.Errorf("identical content: changed=%v err=%v, want false/nil", changed, err)
	}

	// Different size short-circuits to changed.
	writeFileMode(t, orig, "cc", 0o644)
	if changed, err := tr.checkOriginFileChanged(orig, backup, nil); err != nil || !changed {
		t.Errorf("size mismatch: changed=%v err=%v, want true/nil", changed, err)
	}

	// Same size, different mode.
	writeFileMode(t, orig, "bbbb", 0o600)
	if changed, err := tr.checkOriginFileChanged(orig, backup, nil); err != nil || !changed {
		t.Errorf("mode mismatch: changed=%v err=%v, want true/nil", changed, err)
	}

	// Original missing → changed (the file must be recreated from the backup).
	missing := filepath.Join(workDir, "missing.txt")
	if changed, err := tr.checkOriginFileChanged(missing, backup, nil); err != nil || !changed {
		t.Errorf("missing original: changed=%v err=%v, want true/nil", changed, err)
	}

	// Backup missing → changed.
	writeFileMode(t, orig, "bbbb", 0o644)
	if changed, err := tr.checkOriginFileChanged(orig, "no-such-backup@v1", nil); err != nil || !changed {
		t.Errorf("missing backup: changed=%v err=%v, want true/nil", changed, err)
	}

	// A stats hint replaces the stat of the original: pointing it at a file
	// that predates the backup must report "unchanged" even though the path
	// argument itself does not exist.
	hint, err := os.Stat(orig)
	if err != nil {
		t.Fatal(err)
	}
	setMtime(t, orig, older)
	setMtime(t, backupPath, newer)
	if changed, err := tr.checkOriginFileChanged(missing, backup, hint); err != nil || changed {
		t.Errorf("stats hint path: changed=%v err=%v, want false/nil (hint must replace the stat)", changed, err)
	}
}

// restoreBackup copies the backup content and mode back over the file.
func TestRestoreBackup(t *testing.T) {
	histDir := t.TempDir()
	workDir := t.TempDir()
	tr := NewTracker(histDir)

	target := filepath.Join(workDir, "restored.txt")
	writeFileMode(t, filepath.Join(histDir, "r@v1"), "original", 0o600)

	// Fresh destination: created with the backup's mode.
	if err := tr.restoreBackup(target, "r@v1"); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("restored content = %q, want original", data)
	}
	st, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("restored mode = %o, want the backup mode 600", st.Mode().Perm())
	}

	// Overwriting an existing file replaces its content.
	writeFileMode(t, target, "current", 0o644)
	if err := tr.restoreBackup(target, "r@v1"); err != nil {
		t.Fatalf("restoreBackup over existing: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "original" {
		t.Errorf("content after re-restore = %q err=%v, want original", data, err)
	}

	err = tr.restoreBackup(target, "absent@v1")
	if err == nil {
		t.Fatal("restoring a missing backup must fail")
	}
	if err.Error() != "filehistory: backup file not found: "+filepath.Join(histDir, "absent@v1") {
		t.Errorf("err = %q, want the backup-not-found message", err.Error())
	}
}

// HasChangesAtMessage must consult the target snapshot, the v1 fallback, and
// the null-backup "file should not exist" case.
func TestHasChangesAtMessage_Cases(t *testing.T) {
	histDir := t.TempDir()
	workDir := t.TempDir()
	tr := NewTracker(histDir)

	changedFile := filepath.Join(workDir, "changed.txt")
	steadyFile := filepath.Join(workDir, "steady.txt")
	fallbackFile := filepath.Join(workDir, "fallback.txt")
	ghostFile := filepath.Join(workDir, "ghost.txt")

	writeFileMode(t, changedFile, "now", 0o644)
	writeFileMode(t, steadyFile, "same", 0o644)
	writeFileMode(t, fallbackFile, "now", 0o644)
	writeFileMode(t, ghostFile, "exists", 0o644)

	for name, content := range map[string]string{
		"changed@v1":  "then",
		"steady@v1":   "same",
		"fallback@v1": "then",
	} {
		writeFileMode(t, filepath.Join(histDir, name), content, 0o644)
	}
	// The steady backup is newer than the file: the mtime shortcut reports
	// unchanged.
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	setMtime(t, steadyFile, time.Now().Add(-time.Hour))
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	setMtime(t, filepath.Join(histDir, "steady@v1"), time.Now().Add(time.Hour))

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{MessageID: "m1", TrackedFileBackups: map[string]FileHistoryBackup{
				changedFile:  {BackupFileName: "changed@v1", Version: 1},
				steadyFile:   {BackupFileName: "steady@v1", Version: 1},
				fallbackFile: {BackupFileName: "fallback@v1", Version: 1},
			}},
			{MessageID: "m2", TrackedFileBackups: map[string]FileHistoryBackup{
				changedFile:  {BackupFileName: "changed@v1", Version: 2},
				steadyFile:   {BackupFileName: "steady@v1", Version: 2},
				fallbackFile: {}, // not present at m2 → v1 fallback
				ghostFile:    {BackupFileName: "", Version: 3},
			}},
		},
		TrackedFiles: map[string]bool{
			changedFile: true, steadyFile: true, fallbackFile: true, ghostFile: true,
		},
	})

	if !tr.HasChangesAtMessage("m2") {
		t.Error("HasChangesAtMessage(m2) = false, want true (changed + fallback + ghost all differ)")
	}
	if !tr.HasChangesAtMessage("m1") {
		t.Error("HasChangesAtMessage(m1) = false, want true")
	}
	if tr.HasChangesAtMessage("unknown") {
		t.Error("HasChangesAtMessage(unknown) = true, want false")
	}

	// Only steady-state files: nothing to rewind.
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "only",
			TrackedFileBackups: map[string]FileHistoryBackup{steadyFile: {BackupFileName: "steady@v1", Version: 1}},
		}},
		TrackedFiles: map[string]bool{steadyFile: true},
	})
	if tr.HasChangesAtMessage("only") {
		t.Error("HasChangesAtMessage = true, want false when the file matches its backup")
	}

	// Null backup and the file is genuinely absent → no change.
	if err := os.Remove(ghostFile); err != nil {
		t.Fatal(err)
	}
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "ghost",
			TrackedFileBackups: map[string]FileHistoryBackup{ghostFile: {BackupFileName: "", Version: 1}},
		}},
		TrackedFiles: map[string]bool{ghostFile: true},
	})
	if tr.HasChangesAtMessage("ghost") {
		t.Error("HasChangesAtMessage = true, want false when the file is already absent")
	}
}

// TruncateSnapshotsFrom drops the named snapshot and everything after it.
func TestTruncateSnapshotsFrom_DropsNamedAndLater(t *testing.T) {
	tr := NewTracker(t.TempDir())
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{MessageID: "a", TrackedFileBackups: map[string]FileHistoryBackup{}},
			{MessageID: "b", TrackedFileBackups: map[string]FileHistoryBackup{}},
			{MessageID: "c", TrackedFileBackups: map[string]FileHistoryBackup{}},
		},
		TrackedFiles: map[string]bool{},
	})
	tr.TruncateSnapshotsFrom("b")
	st := tr.State()
	if len(st.Snapshots) != 1 || st.Snapshots[0].MessageID != "a" {
		t.Errorf("snapshots after truncate = %v, want only [a]", snapshotIDs(st.Snapshots))
	}
	tr.TruncateSnapshotsFrom("a")
	if remaining := tr.State().Snapshots; len(remaining) != 0 {
		t.Errorf("snapshots = %d, want 0 after truncating the first", len(remaining))
	}
}

// Rewind paths, one tracked file per scenario so an early exit from the
// apply loop cannot hide behind map order.
func TestRewind_NullBackupDeletes(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(t.TempDir())
	present := filepath.Join(dir, "present.txt")
	writeFileMode(t, present, "content", 0o644)

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "m1",
			TrackedFileBackups: map[string]FileHistoryBackup{present: {BackupFileName: "", Version: 1}},
		}},
		TrackedFiles: map[string]bool{present: true},
	})
	changed, err := tr.Rewind("m1")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if _, err := os.Stat(present); !os.IsNotExist(err) {
		t.Errorf("file with a null backup was not deleted (stat err = %v)", err)
	}
	if len(changed) != 1 || changed[0] != present {
		t.Errorf("changed = %v, want [%s]", changed, present)
	}
}

func TestRewind_NullBackupMissingFileNotReported(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(t.TempDir())
	gone := filepath.Join(dir, "gone.txt") // never created

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "m1",
			TrackedFileBackups: map[string]FileHistoryBackup{gone: {BackupFileName: "", Version: 1}},
		}},
		TrackedFiles: map[string]bool{gone: true},
	})
	changed, err := tr.Rewind("m1")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want no entries for an already-absent file", changed)
	}
}

func TestRewind_UndeletableFileSkipped(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(t.TempDir())
	blocker := filepath.Join(dir, "dirblock")
	if err := os.MkdirAll(filepath.Join(blocker, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "m1",
			TrackedFileBackups: map[string]FileHistoryBackup{blocker: {BackupFileName: "", Version: 1}},
		}},
		TrackedFiles: map[string]bool{blocker: true},
	})
	changed, err := tr.Rewind("m1")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want the failed delete excluded", changed)
	}
	if _, err := os.Stat(blocker); err != nil {
		t.Errorf("undeletable directory vanished: %v", err)
	}
}

// A file missing from the target snapshot falls back to its first version.
func TestRewind_FirstVersionFallback(t *testing.T) {
	dir := t.TempDir()
	histDir := t.TempDir()
	tr := NewTracker(histDir)

	file := filepath.Join(dir, "f.txt")
	writeFileMode(t, file, "current", 0o644)
	writeFileMode(t, filepath.Join(histDir, "f@v1"), "first", 0o644)

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{MessageID: "m1", TrackedFileBackups: map[string]FileHistoryBackup{
				file: {BackupFileName: "f@v1", Version: 1},
			}},
			{MessageID: "m2", TrackedFileBackups: map[string]FileHistoryBackup{}},
		},
		TrackedFiles: map[string]bool{file: true},
	})
	changed, err := tr.Rewind("m2")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Errorf("content after rewind = %q, want the v1 backup content", data)
	}
	if len(changed) != 1 {
		t.Errorf("changed = %v, want the restored file reported", changed)
	}
}

// With no backup anywhere the file must be left alone.
func TestRewind_NoBackupAnywhereLeavesFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(t.TempDir())
	file := filepath.Join(dir, "untouched.txt")
	writeFileMode(t, file, "keepme", 0o644)

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{{
			MessageID:          "m1",
			TrackedFileBackups: map[string]FileHistoryBackup{},
		}},
		TrackedFiles: map[string]bool{file: true},
	})
	changed, err := tr.Rewind("m1")
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("changed = %v, want nothing touched", changed)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keepme" {
		t.Errorf("file was modified: %q err=%v", data, err)
	}
}

// The target snapshot's own entry wins over the first-version fallback.
func TestRewind_TargetEntryWinsOverFallback(t *testing.T) {
	dir := t.TempDir()
	histDir := t.TempDir()
	tr := NewTracker(histDir)

	file := filepath.Join(dir, "f.txt")
	writeFileMode(t, file, "current", 0o644)
	writeFileMode(t, filepath.Join(histDir, "f@v1"), "first", 0o644)
	writeFileMode(t, filepath.Join(histDir, "f@v2"), "second", 0o644)

	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{MessageID: "m1", TrackedFileBackups: map[string]FileHistoryBackup{
				file: {BackupFileName: "f@v1", Version: 1},
			}},
			{MessageID: "m2", TrackedFileBackups: map[string]FileHistoryBackup{
				file: {BackupFileName: "f@v2", Version: 1},
			}},
		},
		TrackedFiles: map[string]bool{file: true},
	})
	if _, err := tr.Rewind("m2"); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Errorf("content after rewind = %q, want the target snapshot's backup (second)", data)
	}
}

func snapshotIDs(snaps []FileHistorySnapshot) []string {
	var ids []string
	for _, s := range snaps {
		ids = append(ids, s.MessageID)
	}
	return ids
}
