package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBashExe creates <dir>/bin/bash.exe as an empty file. Existence is
// what injectPortablePaths checks, not contents.
func writeBashExe(t *testing.T, dir string) {
	t.Helper()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	bashExe := filepath.Join(binDir, "bash.exe")
	if err := os.WriteFile(bashExe, nil, 0644); err != nil {
		t.Fatalf("write %s: %v", bashExe, err)
	}
}

// TestInjectPortablePaths_BundlePresent verifies that when bin/bash.exe
// exists, PATH is prepended with the absolute bin/ and GBOT_BASH_PATH
// is set to bin/bash.exe.
func TestInjectPortablePaths_BundlePresent(t *testing.T) {
	dir := t.TempDir()
	writeBashExe(t, dir)

	t.Setenv("PATH", "original")
	t.Setenv("GBOT_BASH_PATH", "")

	injectPortablePaths(dir)

	absBin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatalf("abs bin: %v", err)
	}
	sep := string(os.PathListSeparator)
	wantPath := absBin + sep + "original"
	if got := os.Getenv("PATH"); got != wantPath {
		t.Errorf("PATH = %q, want %q", got, wantPath)
	}
	wantBash := filepath.Join(absBin, "bash.exe")
	if got := os.Getenv("GBOT_BASH_PATH"); got != wantBash {
		t.Errorf("GBOT_BASH_PATH = %q, want %q", got, wantBash)
	}
}

// TestInjectPortablePaths_NoBundle verifies that when bin/bash.exe is
// absent, both PATH and GBOT_BASH_PATH are left unchanged.
func TestInjectPortablePaths_NoBundle(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("PATH", "original")
	t.Setenv("GBOT_BASH_PATH", "existing")

	injectPortablePaths(dir)

	if got := os.Getenv("PATH"); got != "original" {
		t.Errorf("PATH = %q, want %q (unchanged)", got, "original")
	}
	if got := os.Getenv("GBOT_BASH_PATH"); got != "existing" {
		t.Errorf("GBOT_BASH_PATH = %q, want %q (unchanged)", got, "existing")
	}
}

// TestInjectPortablePaths_Idempotent verifies that calling twice does
// not double-prepend. PATH must contain the bin/ directory exactly once.
func TestInjectPortablePaths_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeBashExe(t, dir)

	t.Setenv("PATH", "original")
	t.Setenv("GBOT_BASH_PATH", "")

	injectPortablePaths(dir)
	injectPortablePaths(dir)

	absBin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatalf("abs bin: %v", err)
	}
	sep := string(os.PathListSeparator)
	wantPath := absBin + sep + "original"
	if got := os.Getenv("PATH"); got != wantPath {
		t.Errorf("PATH = %q, want %q (no double-prepend)", got, wantPath)
	}
	// bin/ must appear exactly once in PATH.
	if n := strings.Count(os.Getenv("PATH"), absBin); n != 1 {
		t.Errorf("absBin appears %d times in PATH, want 1; PATH=%q", n, os.Getenv("PATH"))
	}
}

// TestInjectPortablePaths_UserBashPathHonored verifies that an explicit
// GBOT_BASH_PATH is NOT overwritten — user/env override wins.
func TestInjectPortablePaths_UserBashPathHonored(t *testing.T) {
	dir := t.TempDir()
	writeBashExe(t, dir)

	t.Setenv("PATH", "original")
	t.Setenv("GBOT_BASH_PATH", "/custom/bash.exe")

	injectPortablePaths(dir)

	if got := os.Getenv("GBOT_BASH_PATH"); got != "/custom/bash.exe" {
		t.Errorf("GBOT_BASH_PATH = %q, want %q (user override must win)", got, "/custom/bash.exe")
	}
}

// TestInjectPortablePaths_PathAlreadyPrefixed verifies that when PATH
// already starts with the bundle's bin/, it is not double-prepended.
func TestInjectPortablePaths_PathAlreadyPrefixed(t *testing.T) {
	dir := t.TempDir()
	writeBashExe(t, dir)

	absBin, err := filepath.Abs(filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatalf("abs bin: %v", err)
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", absBin+sep+"other")
	t.Setenv("GBOT_BASH_PATH", "")

	injectPortablePaths(dir)

	if got := os.Getenv("PATH"); got != absBin+sep+"other" {
		t.Errorf("PATH = %q, want %q (already prefixed, no double-prepend)", got, absBin+sep+"other")
	}
}
