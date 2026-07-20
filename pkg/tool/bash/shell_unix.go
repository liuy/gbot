//go:build !windows

package bash

// resolveShellCommand returns the shell used to run user commands.
// On Unix this is the package-level shellCommand var ("bash" by default);
// tests override the var directly to force error paths.
func resolveShellCommand() string {
	return shellCommand
}
