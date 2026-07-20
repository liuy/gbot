//go:build windows

package bash

import (
	"testing"
)

// Windows resolver tests are deferred — they require a real Git-for-Windows
// install or registry mock to be meaningful. The cross-compile gate
// (make build-windows) ensures the resolver compiles. Runtime coverage is
// listed in the plan under "What's testable only on Windows (deferred)".

func TestResolveShellCommand_TestOverride(t *testing.T) {
	t.Parallel()

	orig := shellCommand
	defer func() { shellCommand = orig }()
	shellCommand = "/nonexistent/shell/xyz"

	if got := resolveShellCommand(); got != "/nonexistent/shell/xyz" {
		t.Errorf("resolveShellCommand() = %q, want %q", got, "/nonexistent/shell/xyz")
	}
}
