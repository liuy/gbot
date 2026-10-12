//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The identity string is the dedup key for symlink/hardlink detection, so both
// numbers and the separator must survive verbatim.
func TestFileIdentity_ExactDeviceAndInode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(path, []byte("---\nname: X\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("Sys() is %T, want *syscall.Stat_t", info.Sys())
	}

	want := fmt.Sprintf("%d:%d", sys.Dev, sys.Ino)
	if got := fileIdentity(path); got != want {
		t.Errorf("fileIdentity(%s) = %q, want %q", path, got, want)
	}

	// A directory is identifiable too — the guard only rejects dev=0&&ino=0.
	if got := fileIdentity(dir); got == "" {
		t.Errorf("fileIdentity(%s) = %q, want a non-empty device:inode key", dir, got)
	}
}
