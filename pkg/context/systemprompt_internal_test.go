package context

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadSoulFile_FileExists(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	soulContent := "# Soul\nBe helpful."
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte(soulContent), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { os.Setenv("HOME", origHome) }()

	got, err := LoadSoulFile()
	if err != nil {
		t.Fatalf("LoadSoulFile returned error: %v", err)
	}
	if got != soulContent {
		t.Errorf("LoadSoulFile = %q, want %q", got, soulContent)
	}
}

func TestLoadSoulFile_NoFile(t *testing.T) {
	homeDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { os.Setenv("HOME", origHome) }()

	got, err := LoadSoulFile()
	if err != nil {
		t.Fatalf("LoadSoulFile returned error: %v", err)
	}
	if got != "" {
		t.Errorf("LoadSoulFile should return empty when no SOUL.md, got %q", got)
	}
}

func TestLoadSoulFile_EmptyFile(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "SOUL.md"), []byte("   \n\n  "), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { os.Setenv("HOME", origHome) }()

	got, err := LoadSoulFile()
	if err != nil {
		t.Fatalf("LoadSoulFile returned error: %v", err)
	}
	if got != "" {
		t.Errorf("LoadSoulFile should return empty for whitespace-only file, got %q", got)
	}
}

func TestDetectOS_HasEtcOSRelease(t *testing.T) {
	// On Linux CI, /etc/os-release exists — verify it returns something with arch
	result := detectOS()
	if result == "" {
		t.Error("detectOS returned empty string")
	}
	// On this machine it should contain the arch
	if !strings.Contains(result, runtime.GOARCH) {
		t.Errorf("detectOS should contain arch %q, got %q", runtime.GOARCH, result)
	}
}

func TestDetectRepoRoot_InGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	// Not a git repo — should return ""
	result := detectRepoRoot(tmpDir)
	if result != "" {
		t.Errorf("detectRepoRoot in non-git dir should return empty, got %q", result)
	}
}

func TestDetectWorkspace_NoGbotDir(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpDir)
	defer func() { os.Setenv("HOME", origHome) }()

	result := detectWorkspace()
	if result != "" {
		t.Errorf("detectWorkspace should return empty when ~/.gbot doesn't exist, got %q", result)
	}
}

func TestDetectWorkspace_HasGbotDir(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { os.Setenv("HOME", origHome) }()

	result := detectWorkspace()
	if result != gbotDir {
		t.Errorf("detectWorkspace = %q, want %q", result, gbotDir)
	}
}

func TestRuntimeInfo_IncludesAllFields(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Builder{WorkingDir: tmpDir}

	info := b.RuntimeInfo()

	if !strings.Contains(info, "host=") {
		t.Error("RuntimeInfo missing host=")
	}
	if !strings.Contains(info, "os=") {
		t.Error("RuntimeInfo missing os=")
	}
	if !strings.Contains(info, "go=") {
		t.Error("RuntimeInfo missing go=")
	}
	if !strings.Contains(info, "shell=") {
		t.Error("RuntimeInfo missing shell=")
	}
	if !strings.Contains(info, "model={{MODEL}}") {
		t.Error("RuntimeInfo missing model={{MODEL}}")
	}
}

func TestRuntimeInfo_SHELLNotSet(t *testing.T) {
	origShell := os.Getenv("SHELL")
	os.Unsetenv("SHELL")
	defer func() { os.Setenv("SHELL", origShell) }()

	tmpDir := t.TempDir()
	b := &Builder{WorkingDir: tmpDir}

	info := b.RuntimeInfo()
	if !strings.Contains(info, "shell=/bin/bash") {
		t.Errorf("RuntimeInfo should default shell to /bin/bash when SHELL is unset, got %q", info)
	}
}
