package utils

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path atomically by writing to a temp file
// in the same directory then renaming. This prevents partial writes from
// producing a corrupted file — if the process crashes mid-write, the target
// file either has the old content or the full new content, never a truncated
// or corrupted intermediate state.
//
// The parent directory must already exist. File permissions are set to perm.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmpPath := filepath.Join(dir, "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
