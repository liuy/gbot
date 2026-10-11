package filehistory

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The default retention window is exactly one day: 24.5h of age is pruned,
// 23.5h is kept.
func TestCleanupOldBackups_DefaultAgeBoundary(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old-session")
	fresh := filepath.Join(root, "fresh-session")
	for _, d := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Join(d, "inner"), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	stamp := func(dir string, age time.Duration) {
		// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatalf("chtimes %s: %v", dir, err)
		}
	}
	stamp(old, 24*time.Hour+30*time.Minute)
	stamp(fresh, 23*time.Hour+30*time.Minute)

	cleaned, err := CleanupOldBackups(root, DefaultCleanupAge)
	if err != nil {
		t.Fatalf("CleanupOldBackups: %v", err)
	}
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1 (only the 24.5h session is past the 24h window)", cleaned)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("old session still present (stat err = %v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh session removed early: %v", err)
	}
}

func TestCleanupConstants(t *testing.T) {
	t.Parallel()
	if DefaultCleanupAge != 24*time.Hour {
		t.Errorf("DefaultCleanupAge = %v, want 24h", DefaultCleanupAge)
	}
	if MAX_SNAPSHOTS != 100 {
		t.Errorf("MAX_SNAPSHOTS = %d, want 100", MAX_SNAPSHOTS)
	}
}

// Eviction past MAX_SNAPSHOTS trims the in-memory slice only. Backup files are
// shared between evicted and surviving snapshots, so disk reclamation is left
// to CleanupOldBackups — eviction must never delete what survivors reference.
func TestMakeSnapshot_EvictsPastMaxSnapshots(t *testing.T) {
	dir := t.TempDir()
	histDir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	tr := NewTracker(histDir)

	for i := 0; i <= MAX_SNAPSHOTS; i++ {
		if err := os.WriteFile(file, []byte("v"+strings.Repeat("x", i%3)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tr.TrackEdit(file); err != nil {
			t.Fatalf("TrackEdit: %v", err)
		}
		if err := tr.MakeSnapshot("msg-" + itoa(i)); err != nil {
			t.Fatalf("MakeSnapshot: %v", err)
		}
	}
	st := tr.State()
	if len(st.Snapshots) != MAX_SNAPSHOTS {
		t.Fatalf("snapshots = %d, want exactly %d", len(st.Snapshots), MAX_SNAPSHOTS)
	}

	// Every backup still referenced by a surviving snapshot must exist on disk.
	var live int
	seen := map[string]bool{}
	for _, snap := range st.Snapshots {
		for _, b := range snap.TrackedFileBackups {
			if b.BackupFileName == "" || seen[b.BackupFileName] {
				continue
			}
			seen[b.BackupFileName] = true
			if _, err := os.Stat(filepath.Join(histDir, b.BackupFileName)); err != nil {
				t.Errorf("backup %q referenced by a surviving snapshot is missing: %v", b.BackupFileName, err)
			}
			live++
		}
	}
	if live == 0 {
		t.Error("no surviving snapshot references a backup file")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// The first-version fallback must return the v1 name, not a later version.
func TestGetBackupFileNameFirstVersion_PrefersV1(t *testing.T) {
	tr := NewTracker(t.TempDir())
	tr.LoadState(FileHistoryState{
		Snapshots: []FileHistorySnapshot{
			{MessageID: "m2", TrackedFileBackups: map[string]FileHistoryBackup{
				"/x/f": {BackupFileName: "hash@v2", Version: 2},
			}},
			{MessageID: "m1", TrackedFileBackups: map[string]FileHistoryBackup{
				"/x/f": {BackupFileName: "hash@v1", Version: 1},
			}},
		},
		TrackedFiles: map[string]bool{"/x/f": true},
	})
	name, found := tr.getBackupFileNameFirstVersionLocked("/x/f")
	if !found {
		t.Fatal("first-version lookup found nothing")
	}
	if name != "hash@v1" {
		t.Errorf("first version = %q, want hash@v1", name)
	}
	if _, found := tr.getBackupFileNameFirstVersionLocked("/x/absent"); found {
		t.Error("lookup for an untracked path must report found=false")
	}
}

// createBackup on an empty path records a null backup with the requested version.
func TestCreateBackup_EmptyPathNullBackup(t *testing.T) {
	tr := NewTracker(t.TempDir())
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	before := time.Now().Add(-time.Second)
	b, err := tr.createBackup("", 7)
	if err != nil {
		t.Fatalf("createBackup: %v", err)
	}
	if b.BackupFileName != "" {
		t.Errorf("BackupFileName = %q, want the null backup", b.BackupFileName)
	}
	if b.Version != 7 {
		t.Errorf("Version = %d, want 7", b.Version)
	}
	// REAL-TIME: wall-clock fixture; the code under test compares against the system clock.
	if b.BackupTime.Before(before) || b.BackupTime.After(time.Now().Add(time.Second)) {
		t.Errorf("BackupTime = %v, want approximately now", b.BackupTime)
	}
}

// The lazy-mkdir path reports mkdir failures wrapped, and a copy failure
// keeps the underlying OS error reachable.
func TestCopyFileData_Errors(t *testing.T) {
	tr := NewTracker(t.TempDir())
	src := filepath.Join(t.TempDir(), "src.txt")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Destination dir is missing under a read-only parent: the fast copy fails
	// with ENOENT, then the lazy MkdirAll fails with EACCES.
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	err := tr.copyFileData(src, filepath.Join(ro, "nested", "dst.txt"), 0o644)
	if err == nil {
		t.Fatal("copyFileData into an uncreatable directory must fail")
	}
	if !strings.HasPrefix(err.Error(), "mkdir: ") {
		t.Errorf("err = %q, want it to start with %q", err.Error(), "mkdir: ")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("err %v must keep the permission cause reachable via errors.Is", err)
	}

	// Copying from a directory fails inside io.Copy: the "copy:" wrap must
	// keep the OS error reachable.
	dirSrc := t.TempDir()
	copyErr := tr.copyFileData(dirSrc, filepath.Join(t.TempDir(), "dst.txt"), 0o644)
	if copyErr == nil {
		t.Fatal("copyFileData from a directory must fail")
	}
	if !strings.HasPrefix(copyErr.Error(), "copy: ") {
		t.Errorf("err = %q, want it to start with %q", copyErr.Error(), "copy: ")
	}
}

// NewTracker warns when the history directory cannot be created.
func TestNewTracker_MkdirWarns(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	NewTracker(filepath.Join(blocker, "history"))
	if !strings.Contains(buf.String(), "msg=filehistory:new_tracker:mkdir_failed") {
		t.Errorf("mkdir failure was not logged; got:\n%s", buf.String())
	}
}

// MakeSnapshot reuses the previous backup reference for an unchanged file, so
// evicted snapshots share backup files with surviving ones. Eviction must not
// delete what the survivors still point at, otherwise Rewind restores nothing.
func TestEviction_KeepsBackupsReferencedBySurvivingSnapshots(t *testing.T) {
	dir := t.TempDir()
	histDir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	tr := NewTracker(histDir)

	const content = "unchanged content"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(file); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}

	for i := 0; i <= MAX_SNAPSHOTS; i++ {
		if err := tr.MakeSnapshot("msg-" + itoa(i)); err != nil {
			t.Fatalf("MakeSnapshot %d: %v", i, err)
		}
	}

	st := tr.State()
	if len(st.Snapshots) != MAX_SNAPSHOTS {
		t.Fatalf("snapshots = %d, want %d", len(st.Snapshots), MAX_SNAPSHOTS)
	}

	// Every surviving snapshot references the same backup; all of them must exist.
	var referenced string
	for _, snap := range st.Snapshots {
		b := snap.TrackedFileBackups[file]
		if b.BackupFileName == "" {
			t.Fatalf("surviving snapshot %q has no backup for %s", snap.MessageID, file)
		}
		if referenced == "" {
			referenced = b.BackupFileName
		}
		if b.BackupFileName != referenced {
			t.Fatalf("snapshot %q references %q, want shared %q", snap.MessageID, b.BackupFileName, referenced)
		}
		if _, err := os.Stat(filepath.Join(histDir, b.BackupFileName)); err != nil {
			t.Fatalf("backup %q referenced by surviving snapshot %q is missing: %v",
				b.BackupFileName, snap.MessageID, err)
		}
	}

	// The newest surviving snapshot must still be restorable.
	if err := os.WriteFile(file, []byte("clobbered"), 0o644); err != nil {
		t.Fatal(err)
	}
	restored, err := tr.Rewind("msg-" + itoa(MAX_SNAPSHOTS))
	if err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	if len(restored) != 1 || restored[0] != file {
		t.Fatalf("restored = %v, want [%s]", restored, file)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("file after rewind = %q, want %q", string(got), content)
	}
}

// A snapshot whose backup file was removed out from under the tracker must
// surface the failure instead of silently restoring nothing.
func TestRewind_MissingBackupReturnsError(t *testing.T) {
	dir := t.TempDir()
	histDir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	tr := NewTracker(histDir)

	if err := os.WriteFile(file, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.TrackEdit(file); err != nil {
		t.Fatalf("TrackEdit: %v", err)
	}
	if err := tr.MakeSnapshot("msg-1"); err != nil {
		t.Fatalf("MakeSnapshot: %v", err)
	}

	backup := tr.State().Snapshots[0].TrackedFileBackups[file].BackupFileName
	if backup == "" {
		t.Fatal("expected a backup for the tracked file")
	}
	if err := os.Remove(filepath.Join(histDir, backup)); err != nil {
		t.Fatalf("remove backup: %v", err)
	}
	if err := os.WriteFile(file, []byte("clobbered"), 0o644); err != nil {
		t.Fatal(err)
	}

	restored, err := tr.Rewind("msg-1")
	if err == nil {
		t.Fatalf("Rewind must report the missing backup, got nil error and restored=%v", restored)
	}
	if !strings.Contains(err.Error(), "backup file not found") {
		t.Errorf("error = %v, want it to mention backup file not found", err)
	}
}
