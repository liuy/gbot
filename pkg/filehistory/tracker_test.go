package filehistory

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestRecordBackup_NewFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	err := tr.RecordBackup("/tmp/newfile.go", nil, 1)
	if err != nil {
		t.Fatalf("RecordBackup failed: %v", err)
	}

	records := tr.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	if r.FilePath != "/tmp/newfile.go" {
		t.Errorf("FilePath = %q, want /tmp/newfile.go", r.FilePath)
	}
	if r.BackupName != "" {
		t.Errorf("BackupName = %q, want empty for new file", r.BackupName)
	}
	if r.Version != 1 {
		t.Errorf("Version = %d, want 1", r.Version)
	}
	if r.TurnIndex != 1 {
		t.Errorf("TurnIndex = %d, want 1", r.TurnIndex)
	}
}

func TestRecordBackup_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	content := []byte("original content")
	err := tr.RecordBackup("/tmp/existing.go", content, 2)
	if err != nil {
		t.Fatalf("RecordBackup failed: %v", err)
	}

	records := tr.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	if r.BackupName == "" {
		t.Fatal("BackupName should not be empty for existing file")
	}
	if r.Version != 1 {
		t.Errorf("Version = %d, want 1", r.Version)
	}

	// Verify backup file was created on disk
	backupPath := filepath.Join(dir, r.BackupName)
	data, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("backup file not found: %v", err)
	}
	if string(data) != "original content" {
		t.Errorf("backup content = %q, want %q", string(data), "original content")
	}
}

func TestRecordBackup_MultipleEdits(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	filePath := "/tmp/multi.go"
	for i := range 3 {
		content := []byte("version " + string(rune('0'+i)))
		if err := tr.RecordBackup(filePath, content, i+1); err != nil {
			t.Fatalf("RecordBackup %d failed: %v", i+1, err)
		}
	}

	records := tr.Records()
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}

	// Verify versions increment
	seenVersions := map[int]bool{}
	for _, r := range records {
		if r.Version < 1 || r.Version > 3 {
			t.Errorf("unexpected version %d", r.Version)
		}
		seenVersions[r.Version] = true
	}
	if len(seenVersions) != 3 {
		t.Errorf("expected 3 distinct versions, got %d", len(seenVersions))
	}

	// All should have different backup names
	seenNames := map[string]bool{}
	for _, r := range records {
		if r.BackupName == "" {
			t.Error("BackupName should not be empty")
		}
		seenNames[r.BackupName] = true
	}
	if len(seenNames) != 3 {
		t.Errorf("expected 3 distinct backup names, got %d", len(seenNames))
	}
}

func TestRestoreToIndex_RestoresPreEditState(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Simulate: file exists with content "v1" at turn 1, then edited to "v2" at turn 2
	targetFile := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(targetFile, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn 1: no backup needed (original state)
	// Turn 2: edit changes "v1" → "v2", backup "v1"
	if err := tr.RecordBackup(targetFile, []byte("v1"), 2); err != nil {
		t.Fatalf("RecordBackup failed: %v", err)
	}

	// Simulate edit
	if err := os.WriteFile(targetFile, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Rewind to turn 1: should restore "v1"
	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex failed: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}
	if restored[0] != targetFile {
		t.Errorf("restored[0] = %q, want %q", restored[0], targetFile)
	}

	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read file after restore: %v", err)
	}
	if string(data) != "v1" {
		t.Errorf("file content after restore = %q, want %q", string(data), "v1")
	}
}

func TestRestoreToIndex_DeletesCreatedFile(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Simulate: file created at turn 2 (didn't exist at turn 1)
	targetFile := filepath.Join(t.TempDir(), "new.go")
	if err := os.WriteFile(targetFile, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Record backup with nil content = file didn't exist before
	if err := tr.RecordBackup(targetFile, nil, 2); err != nil {
		t.Fatalf("RecordBackup failed: %v", err)
	}

	// Rewind to turn 1: should delete the file
	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex failed: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored (deleted) file, got %d", len(restored))
	}

	// File should be gone
	if _, err := os.Stat(targetFile); !os.IsNotExist(err) {
		t.Error("file should have been deleted after rewind")
	}
}

func TestRestoreToIndex_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	tmpDir := t.TempDir()
	file1 := filepath.Join(tmpDir, "a.go")
	file2 := filepath.Join(tmpDir, "b.go")
	file3 := filepath.Join(tmpDir, "c.go")

	// Write initial content
	mustWriteFile(t, file1, []byte("a1"))
	mustWriteFile(t, file2, []byte("b1"))
	mustWriteFile(t, file3, []byte("c1"))

	// Turn 2: edit file1 a1→a2, backup a1
	if err := tr.RecordBackup(file1, []byte("a1"), 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	mustWriteFile(t, file1, []byte("a2"))

	// Turn 3: edit file2 b1→b2, backup b1
	if err := tr.RecordBackup(file2, []byte("b1"), 3); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	mustWriteFile(t, file2, []byte("b2"))

	// Turn 4: edit file3 c1→c2, backup c1
	if err := tr.RecordBackup(file3, []byte("c1"), 4); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	mustWriteFile(t, file3, []byte("c2"))

	// Rewind to turn 2: removes turns [2..end], so all 3 edits are reverted.
	// file1 restored to a1, file2 restored to b1, file3 restored to c1
	restored, err := tr.RestoreToIndex(2)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}

	sort.Strings(restored)
	if len(restored) != 3 {
		t.Fatalf("expected 3 restored files, got %d: %v", len(restored), restored)
	}

	// file1 should be restored to "a1" (edit at turn 2 is also reverted)
	data, _ := os.ReadFile(file1)
	if string(data) != "a1" {
		t.Errorf("file1 = %q, want %q", string(data), "a1")
	}

	// file2 should be restored to "b1"
	data, _ = os.ReadFile(file2)
	if string(data) != "b1" {
		t.Errorf("file2 = %q, want %q", string(data), "b1")
	}

	// file3 should be restored to "c1"
	data, _ = os.ReadFile(file3)
	if string(data) != "c1" {
		t.Errorf("file3 = %q, want %q", string(data), "c1")
	}
}

func TestRestoreToIndex_NoBackups(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex failed: %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("expected 0 restored files with no backups, got %d", len(restored))
	}
}

func TestTruncateRecords(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if err := tr.RecordBackup("/tmp/a.go", []byte("a"), 1); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr.RecordBackup("/tmp/b.go", []byte("b"), 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr.RecordBackup("/tmp/c.go", []byte("c"), 3); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	tr.TruncateRecords(2)

	records := tr.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record after truncate, got %d", len(records))
	}
	if records[0].TurnIndex != 1 {
		t.Errorf("remaining record TurnIndex = %d, want 1", records[0].TurnIndex)
	}
}

func TestTracker_PersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	tr1 := NewTracker(dir)

	if err := tr1.RecordBackup("/tmp/x.go", []byte("x-content"), 1); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr1.RecordBackup("/tmp/y.go", nil, 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr1.RecordBackup("/tmp/x.go", []byte("x-content-v2"), 3); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	records := tr1.Records()

	// Simulate crash recovery: create new tracker, load records
	tr2 := NewTracker(dir)
	tr2.LoadRecords(records)

	loaded := tr2.Records()
	if len(loaded) != 3 {
		t.Fatalf("expected 3 loaded records, got %d", len(loaded))
	}

	// Verify version counters were rebuilt
	// Next backup for /tmp/x.go should be version 3
	if err := tr2.RecordBackup("/tmp/x.go", []byte("x-content-v3"), 4); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	loaded = tr2.Records()
	lastRecord := loaded[len(loaded)-1]
	if lastRecord.Version != 3 {
		t.Errorf("version after LoadRecords = %d, want 3", lastRecord.Version)
	}
}

func TestFileHash(t *testing.T) {
	h := fileHash("/tmp/test.go")
	if len(h) != 16 {
		t.Errorf("hash length = %d, want 16", len(h))
	}
	// Same input should produce same hash
	h2 := fileHash("/tmp/test.go")
	if h != h2 {
		t.Errorf("hash not deterministic: %q vs %q", h, h2)
	}
	// Different input should produce different hash
	h3 := fileHash("/tmp/other.go")
	if h == h3 {
		t.Error("different paths should produce different hashes")
	}
}

func TestIsSkippedDir(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{".git", true},
		{"node_modules", true},
		{"vendor", true},
		{"__pycache__", true},
		{".hg", true},
		{".svn", true},
		{"src", false},
		{"pkg", false},
		{"cmd", false},
	}
	for _, tt := range tests {
		got := IsSkippedDir(tt.name)
		if got != tt.want {
			t.Errorf("IsSkippedDir(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRestoreToIndex_RecordTruncated(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	if err := tr.RecordBackup("/tmp/a.go", []byte("a"), 1); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr.RecordBackup("/tmp/b.go", []byte("b"), 2); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}
	if err := tr.RecordBackup("/tmp/c.go", []byte("c"), 3); err != nil {
		t.Fatalf("RecordBackup: %v", err)
	}

	// Restore to index 2 should truncate records with turnIndex >= 2
	if _, err := tr.RestoreToIndex(2); err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}

	records := tr.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record after restore+truncate, got %d", len(records))
	}
	if records[0].TurnIndex != 1 {
		t.Errorf("remaining record TurnIndex = %d, want 1", records[0].TurnIndex)
	}
}

func TestRecordBackup_EmptyFilePath(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	// Empty file path should still work (it's a valid record)
	err := tr.RecordBackup("", nil, 1)
	if err != nil {
		t.Fatalf("RecordBackup with empty path failed: %v", err)
	}
	records := tr.Records()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].FilePath != "" {
		t.Errorf("FilePath = %q, want empty", records[0].FilePath)
	}
}

func TestRecordBackup_MkdirAllError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	parent := t.TempDir()
	readOnlyDir := filepath.Join(parent, "readonly")
	mustMkdirAll(t, readOnlyDir)
	mustWriteFile(t, filepath.Join(readOnlyDir, "placeholder"), []byte("x"))
	if err := os.Chmod(readOnlyDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o755) })

	// Target dir is inside read-only dir → MkdirAll fails
	tr := NewTracker(filepath.Join(readOnlyDir, "nested", "sub"))
	err := tr.RecordBackup("/tmp/test.go", []byte("content"), 1)
	if err == nil {
		t.Fatal("expected error when backup dir creation fails")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error should mention mkdir, got: %v", err)
	}
}

func TestRecordBackup_WriteBackupError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// dir exists but is read-only → WriteFile fails
	tr := NewTracker(dir)
	err := tr.RecordBackup("/tmp/test.go", []byte("content"), 1)
	if err == nil {
		t.Fatal("expected error when writing backup fails")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("error should mention backup, got: %v", err)
	}
}

func TestRestoreToIndex_SameFileMultipleRecords(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)
	targetFile := filepath.Join(t.TempDir(), "multi.go")

	// Turn 1: backup "v1"
	if err := tr.RecordBackup(targetFile, []byte("v1"), 1); err != nil {
		t.Fatal(err)
	}
	// Turn 3: backup "v3" (records out of order forces sort comparison)
	if err := tr.RecordBackup(targetFile, []byte("v3"), 3); err != nil {
		t.Fatal(err)
	}

	// Restore to turn 2: finds turnIndex=3 (earliest >= 2), restores "v3"
	restored, err := tr.RestoreToIndex(2)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored, got %d", len(restored))
	}
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v3" {
		t.Errorf("file = %q, want %q", string(data), "v3")
	}
}

func TestRestoreToIndex_RemoveFailed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	dir := t.TempDir()
	tr := NewTracker(dir)

	subDir := filepath.Join(t.TempDir(), "sub")
	mustMkdirAll(t, subDir)
	targetFile := filepath.Join(subDir, "created.go")
	mustWriteFile(t, targetFile, []byte("new"))

	// Record: file was created (nil originalContent)
	if err := tr.RecordBackup(targetFile, nil, 1); err != nil {
		t.Fatal(err)
	}

	// Make parent dir read-only so os.Remove fails
	if err := os.Chmod(subDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(subDir, 0o755) })

	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}
	// Delete failed → not in restored list
	for _, f := range restored {
		if f == targetFile {
			t.Error("file should not be in restored list when delete fails")
		}
	}
}

func TestRestoreToIndex_ReadBackupFailed(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir)

	targetFile := filepath.Join(t.TempDir(), "test.go")
	if err := tr.RecordBackup(targetFile, []byte("original"), 1); err != nil {
		t.Fatal(err)
	}

	// Delete the backup file from disk
	records := tr.Records()
	if err := os.Remove(filepath.Join(dir, records[0].BackupName)); err != nil {
		t.Fatal(err)
	}

	// Restore should fail to read backup (logged, not error)
	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}
	if len(restored) != 0 {
		t.Errorf("expected 0 restored (backup missing), got %d", len(restored))
	}
}

func TestRestoreToIndex_WriteRestoreFailed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	dir := t.TempDir()
	tr := NewTracker(dir)

	targetFile := filepath.Join(t.TempDir(), "test.go")
	mustWriteFile(t, targetFile, []byte("modified"))

	if err := tr.RecordBackup(targetFile, []byte("original"), 1); err != nil {
		t.Fatal(err)
	}

	// Make target file read-only so WriteFile restore fails
	if err := os.Chmod(targetFile, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(targetFile, 0o644) })

	restored, err := tr.RestoreToIndex(1)
	if err != nil {
		t.Fatalf("RestoreToIndex: %v", err)
	}
	// WriteFile to read-only file → fails, not in restored list
	for _, f := range restored {
		if f == targetFile {
			t.Error("file should not be in restored list when write fails")
		}
	}
}

func TestWalkDir_InaccessibleEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: test requires non-root user")
	}
	root := t.TempDir()
	subDir := filepath.Join(root, "sub")
	mustMkdirAll(t, subDir)
	mustWriteFile(t, filepath.Join(subDir, "file.txt"), []byte("content"))

	// Make subdirectory inaccessible → WalkDir passes error to callback
	if err := os.Chmod(subDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(subDir, 0o755) })

	var visited []string
	err := WalkDir(root, func(path string, d fs.DirEntry) error {
		visited = append(visited, path)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	// Root should be visited, but files inside inaccessible sub should not
	for _, p := range visited {
		if filepath.Base(p) == "file.txt" {
			t.Error("should not visit files in inaccessible directory")
		}
	}
}
