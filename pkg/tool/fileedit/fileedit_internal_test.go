package fileedit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecute_WriteFileError(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(fp, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Make the file read-only after creation
	if err := os.Chmod(fp, 0o444); err != nil {
		t.Fatalf("Chmod 0444: %v", err)
	}
	defer func() {
		if err := os.Chmod(fp, 0o644); err != nil {
			t.Logf("Chmod restore: %v", err)
		}
	}()

	input := json.RawMessage(`{"file_path":"` + fp + `","old_string":"hello","new_string":"goodbye"}`)
	_, err := Execute(context.Background(), input, nil)
	if err == nil {
		t.Error("Execute() error = nil, want error when write fails")
	}
}
