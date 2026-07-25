//go:build android

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyAndroidEnv_SetsAllVars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("GBOT_BASH_PATH", "")
	t.Setenv("PATH", "/system/bin")

	applyAndroidEnv(dir)

	if got := os.Getenv("HOME"); got != dir {
		t.Errorf("HOME = %q, want %q", got, dir)
	}
	wantBash := filepath.Join(dir, "usr", "bin", "bash")
	if got := os.Getenv("GBOT_BASH_PATH"); got != wantBash {
		t.Errorf("GBOT_BASH_PATH = %q, want %q", got, wantBash)
	}
	wantPathPrefix := filepath.Join(dir, "usr", "bin")
	if got := os.Getenv("PATH"); !strings.HasPrefix(got, wantPathPrefix) {
		t.Errorf("PATH = %q, want prefix %q", got, wantPathPrefix)
	}
}

func TestApplyAndroidEnv_EmptyPathNoOp(t *testing.T) {
	t.Setenv("HOME", "/preset/home")
	t.Setenv("GBOT_BASH_PATH", "/preset/bash")
	t.Setenv("PATH", "/preset/path")

	applyAndroidEnv("")

	if got := os.Getenv("HOME"); got != "/preset/home" {
		t.Errorf("HOME = %q, want %q (empty path must not mutate env)", got, "/preset/home")
	}
	if got := os.Getenv("GBOT_BASH_PATH"); got != "/preset/bash" {
		t.Errorf("GBOT_BASH_PATH = %q, want %q", got, "/preset/bash")
	}
	if got := os.Getenv("PATH"); got != "/preset/path" {
		t.Errorf("PATH = %q, want %q", got, "/preset/path")
	}
}
