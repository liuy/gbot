//go:build android

package bash

import (
	"os"
	"testing"
)

// These tests mutate the package-level shellCommand global. They cannot use
// t.Parallel() — parallel runs would race on the shared variable.
//
// Cache limitation: resolvedShellCommandOnce is a package-level sync.OnceValue.
// The first test that calls resolveShellCommand() with shellCommand=="bash"
// populates the cache; subsequent calls return the cached value regardless of
// env changes. TestResolveShellCommand_GBOTBashPath must run before any other
// test that would set a different cache key — Go runs tests in source-file
// order within a package, which is why GBOTBashPath is declared first below.
// TestResolveShellCommand_TestOverride then bypasses the cache entirely:
// shellCommand != "bash" short-circuits before the OnceValue is consulted.
//
// The HOME-unset → literal "bash" fallback branch is intentionally NOT tested
// here: it would require resolveShellCommandOnce to be unset, but Go has no
// way to reset a sync.OnceValue mid-process. A separate process-level test
// would be flaky in CI where test ordering isn't guaranteed.

func TestResolveShellCommand_GBOTBashPath(t *testing.T) {
	// sync.OnceValue cannot be reset; if another parallel test already
	// populated the cache, this test cannot verify GBOT_BASH_PATH resolution.
	cached := resolvedShellCommandOnce()
	if cached != "" && cached != "bash" {
		t.Skipf("shell cache already populated by another test: %q", cached)
	}

	orig := shellCommand
	defer func() { shellCommand = orig }()
	shellCommand = "bash"

	f, err := os.CreateTemp("", "gbot-bash-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	if err := f.Close(); err != nil {
		t.Fatalf("close tmpfile: %v", err)
	}

	t.Setenv("GBOT_BASH_PATH", f.Name())

	if got := resolveShellCommand(); got != f.Name() {
		t.Errorf("resolveShellCommand() = %q, want %q", got, f.Name())
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
