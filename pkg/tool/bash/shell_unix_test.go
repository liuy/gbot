//go:build !windows && !android

package bash

import (
	"testing"
)

// These tests mutate the package-level shellCommand global. They cannot use
// t.Parallel() — parallel runs would race on the shared variable.

func TestResolveShellCommand_Default(t *testing.T) {
	orig := shellCommand
	defer func() { shellCommand = orig }()
	shellCommand = "bash"

	if got := resolveShellCommand(); got != "bash" {
		t.Errorf("resolveShellCommand() = %q, want %q", got, "bash")
	}
}

func TestResolveShellCommand_TestOverride(t *testing.T) {
	orig := shellCommand
	defer func() { shellCommand = orig }()
	shellCommand = "/nonexistent/shell/xyz"

	if got := resolveShellCommand(); got != "/nonexistent/shell/xyz" {
		t.Errorf("resolveShellCommand() = %q, want %q", got, "/nonexistent/shell/xyz")
	}
}
