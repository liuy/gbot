package main

import (
	"os"
	"path/filepath"
	"strings"
)

// injectPortablePaths turns a directory into a "bundle" by prepending its
// bin/ subdirectory to PATH and pointing GBOT_BASH_PATH at bin/bash.exe.
// No-op when bin/bash.exe is absent (dev mode, non-bundle install,
// non-Windows host). Idempotent: safe to call repeatedly.
//
// GBOT_BASH_PATH is only set when unset — explicit user/env override wins,
// matching pkg/tool/bash/shell_windows.go's resolver priority.
func injectPortablePaths(execDir string) {
	binDir := filepath.Join(execDir, "bin")
	bashExe := filepath.Join(binDir, "bash.exe")
	if _, err := os.Stat(bashExe); err != nil {
		return
	}
	absBin, err := filepath.Abs(binDir)
	if err != nil {
		return
	}
	// Use absBin for both PATH and GBOT_BASH_PATH so behavior is consistent
	// regardless of whether execDir was passed as relative or absolute.
	current := os.Getenv("PATH")
	if current == "" {
		_ = os.Setenv("PATH", absBin)
	} else if !pathHasPrefix(current, absBin) {
		_ = os.Setenv("PATH", absBin+string(os.PathListSeparator)+current)
	}
	if os.Getenv("GBOT_BASH_PATH") == "" {
		_ = os.Setenv("GBOT_BASH_PATH", filepath.Join(absBin, "bash.exe"))
	}
}

// pathHasPrefix reports whether dir is the first segment of the OS-specific
// PATH list. Avoids double-prepending on repeated calls.
func pathHasPrefix(path, dir string) bool {
	sep := string(os.PathListSeparator)
	return path == dir || strings.HasPrefix(path, dir+sep)
}
