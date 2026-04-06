package fileread

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecute_ReadFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm.txt")
	if err := os.WriteFile(fp, []byte("secret"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Restore permissions for cleanup
	defer func() { _ = os.Chmod(fp, 0o644) }()

	// Reading a file with no permissions should fail
	input := json.RawMessage(`{"file_path":"` + fp + `"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error for unreadable file")
	}
}

func TestExecute_OpenFileError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fp := filepath.Join(dir, "noperm2.txt")
	if err := os.WriteFile(fp, []byte("secret"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer func() { _ = os.Chmod(fp, 0o644) }()

	// Reading with offset/limit triggers os.Open path
	input := json.RawMessage(`{"file_path":"` + fp + `","offset":1,"limit":1}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error for unreadable file")
	}
}
