//go:build !windows

package agent

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentityForFile returns a unique identifier for a file based on device
// ID and inode. Returns empty string if the file can't be identified (fail open).
// Source: markdownConfigLoader.ts:159-172 — getFileIdentity
func fileIdentityForFile(path string) string {
	stat, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sys, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	// Skip unreliable identities (NFS, FUSE report dev=0, ino=0)
	// Source: markdownConfigLoader.ts:164-167
	if sys.Dev == 0 && sys.Ino == 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", sys.Dev, sys.Ino)
}
