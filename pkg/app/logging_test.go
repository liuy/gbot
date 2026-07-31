package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetupLogFile_NewFile: no existing log → creates file, returns non-nil
func TestSetupLogFile_NewFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	f := setupLogFile(logPath, 0)

	if f == nil {
		t.Fatal("expected non-nil file handle")
	}
	defer f.Close()

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log file not created: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("new log file should be empty, got %d bytes", info.Size())
	}
}

// TestSetupLogFile_AppendExisting: existing log → O_APPEND, old content preserved
func TestSetupLogFile_AppendExisting(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	// Write some initial content
	oldContent := []byte("previous log line\n")
	if err := os.WriteFile(logPath, oldContent, 0644); err != nil {
		t.Fatal(err)
	}

	f := setupLogFile(logPath, 0)
	if f == nil {
		t.Fatal("expected non-nil file handle")
	}
	defer f.Close()

	// Write new content
	if _, err := f.Write([]byte("new log line\n")); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "previous log line\nnew log line\n" {
		t.Fatalf("expected appended content, got %q", string(data))
	}
}

// TestSetupLogFile_RotateOnLimit: log > maxBytes → renamed to .old, new file created
func TestSetupLogFile_RotateOnLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	// Create a file exceeding the limit
	bigContent := make([]byte, maxLogSize+1)
	if err := os.WriteFile(logPath, bigContent, 0644); err != nil {
		t.Fatal(err)
	}

	f := setupLogFile(logPath, 0)
	if f == nil {
		t.Fatal("expected non-nil file handle")
	}
	defer f.Close()

	// .old should exist with old content
	oldPath := logPath + ".old"
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatalf(".old file not created: %v", err)
	}
	if len(oldData) != len(bigContent) {
		t.Fatalf(".old file size mismatch: got %d, want %d", len(oldData), len(bigContent))
	}

	// New log file should be empty
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("new log file not created: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("new log file should be empty after rotation, got %d bytes", info.Size())
	}
}

// TestSetupLogFile_NoRotateUnderLimit: log < maxBytes → no rotation
func TestSetupLogFile_NoRotateUnderLimit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	// Create a file under the limit
	smallContent := []byte("small log\n")
	if err := os.WriteFile(logPath, smallContent, 0644); err != nil {
		t.Fatal(err)
	}

	f := setupLogFile(logPath, 0)
	if f == nil {
		t.Fatal("expected non-nil file handle")
	}
	defer f.Close()

	// .old should NOT exist
	oldPath := logPath + ".old"
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf(".old file should not exist, got err: %v", err)
	}

	// Content should be preserved (appended)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "small log\n" {
		t.Fatalf("content should be preserved, got %q", string(data))
	}
}

// TestSetupLogFile_RotateReplacesOldBackup: rotate twice → .old is always the latest backup
func TestSetupLogFile_RotateReplacesOldBackup(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gbot.log")

	// First rotation: write big content, trigger rotate
	firstBatch := []byte("first rotation content")
	if err := os.WriteFile(logPath, firstBatch, 0644); err != nil {
		t.Fatal(err)
	}
	// Force rotation by exceeding size
	bigContent := make([]byte, maxLogSize+1)
	if err := os.WriteFile(logPath, bigContent, 0644); err != nil {
		t.Fatal(err)
	}

	f1 := setupLogFile(logPath, 0)
	if f1 == nil {
		t.Fatal("expected non-nil file handle on first rotation")
	}
	f1.Close()

	// Verify .old has bigContent
	oldData, _ := os.ReadFile(logPath + ".old")
	if len(oldData) != len(bigContent) {
		t.Fatalf("first rotation .old size mismatch: got %d, want %d", len(oldData), len(bigContent))
	}

	// Second rotation
	if err := os.WriteFile(logPath, bigContent, 0644); err != nil {
		t.Fatal(err)
	}
	f2 := setupLogFile(logPath, 0)
	if f2 == nil {
		t.Fatal("expected non-nil file handle on second rotation")
	}
	f2.Close()

	// .old should now have the second big content, not the first
	oldData2, _ := os.ReadFile(logPath + ".old")
	if len(oldData2) != len(bigContent) {
		t.Fatalf("second rotation .old size mismatch: got %d, want %d", len(oldData2), len(bigContent))
	}
}
