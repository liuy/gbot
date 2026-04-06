package filewrite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecute_MkdirAllError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a file where a directory should be
	blocker := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Try to write to a path that requires "blocked" as a directory
	target := filepath.Join(blocker, "sub", "file.txt")
	input := json.RawMessage(`{"file_path":"` + target + `","content":"test"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when MkdirAll fails")
	}
}

func TestExecute_WriteFileError(t *testing.T) {
	dir := t.TempDir()

	// Create a read-only directory
	roDir := filepath.Join(dir, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write a file there first, then make dir read-only
	target := filepath.Join(roDir, "file.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_ = os.Chmod(roDir, 0o444)
	defer func() { _ = os.Chmod(roDir, 0o755) }()

	// Now try to overwrite — this should fail due to directory permissions
	input := json.RawMessage(`{"file_path":"` + target + `","content":"new content"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when write fails")
	}
}
