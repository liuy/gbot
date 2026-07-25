//go:build android

package bash

import (
	"os"
	"path/filepath"
	"sync"
)

// resolveShellCommand returns the bash executable path on Android.
//
// Resolution order mirrors shell_windows.go:
//  1. Test override: if shellCommand has been changed from "bash" (default),
//     honor it. Preserves the cross-platform test hook used by
//     bash_internal_test.go and pty_test.go to force error paths.
//  2. GBOT_BASH_PATH env var (set by nativeSetDataPath in gui_android.go).
//  3. $HOME/usr/bin/bash — default bootstrap install location.
//  4. Literal "bash" — defer to the OS to surface a clear error.
//
// Result is cached via sync.OnceValue after first resolution.
func resolveShellCommand() string {
	if shellCommand != "bash" {
		return shellCommand
	}
	return resolvedShellCommandOnce()
}

var resolvedShellCommandOnce = sync.OnceValue(func() string {
	if p := os.Getenv("GBOT_BASH_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if home := os.Getenv("HOME"); home != "" {
		candidate := filepath.Join(home, "usr", "bin", "bash")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "bash"
})
