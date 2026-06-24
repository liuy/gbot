package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteFile_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := AtomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", string(data), "hello")
	}

	// Temp file must be cleaned up.
	tmpPath := filepath.Join(dir, ".test.txt.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file still exists: %v", err)
	}
}

func TestAtomicWriteFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "new" {
		t.Errorf("content = %q, want %q", string(data), "new")
	}
}

func TestAtomicWriteFile_ParentDirNotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent", "test.txt")

	err := AtomicWriteFile(path, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected error for non-existent parent dir")
	}
	if !strings.Contains(err.Error(), "write temp file") {
		t.Errorf("error = %q, want 'write temp file'", err.Error())
	}
}
