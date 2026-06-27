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

func TestAtomicWriteFile_TempFileCleanedUpOnRenameFail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Make the target path a directory — rename to an existing directory
	// fails with "file exists" on Linux when the target is a non-empty dir.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := AtomicWriteFile(target, []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected rename error (target is a directory)")
	}
	// Temp file should have been cleaned up.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".tmp") {
			t.Errorf("temp file %s not cleaned up after rename failure", name)
		}
	}
}
