package context

import (
	"fmt"
	"os"
	"os/exec"
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
	defer func() { _ = os.Setenv("HOME", origHome) }()

	got, err := LoadSoulFile()
	if err != nil {
		t.Fatalf("LoadSoulFile returned error: %v", err)
	}
	if got != soulContent {
		t.Errorf("LoadSoulFile = %q, want %q", got, soulContent)
	}
}

func TestLoadSystemFile_NoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, err := loadSystemFileFrom(dir)
	if err != nil {
		t.Fatalf("loadSystemFileFrom returned error: %v", err)
	}
	if got != "" {
		t.Errorf("loadSystemFileFrom should return empty when no SYSTEM.md, got %q", got)
	}
}

func TestLoadSystemFile_FileExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gbotDir := filepath.Join(dir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	systemContent := "# Test System Prompt\nYou are a test assistant."
	if err := os.WriteFile(filepath.Join(gbotDir, "SYSTEM.md"), []byte(systemContent), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := loadSystemFileFrom(dir)
	if err != nil {
		t.Fatalf("loadSystemFileFrom returned error: %v", err)
	}
	if got != systemContent {
		t.Errorf("loadSystemFileFrom = %q, want %q", got, systemContent)
	}
}

func TestDefaultBasePrompt_ContainsStubs(t *testing.T) {
	t.Parallel()
	prompt := DefaultBasePrompt()
	if !strings.Contains(prompt, "# What you are") {
		t.Error("DefaultBasePrompt should contain # What you are")
	}
	if !strings.Contains(prompt, "# How you work") {
		t.Error("DefaultBasePrompt should contain # How you work")
	}
	if strings.Contains(prompt, "{{SOUL}}") {
		t.Error("DefaultBasePrompt should NOT contain {{SOUL}} — it's added by builder")
	}
}

func TestLoadSoulFile_NoFile(t *testing.T) {
	homeDir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

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
	defer func() { _ = os.Setenv("HOME", origHome) }()

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
	// lsp= was removed: its server order came from concurrent discovery, so it
	// reshuffled every restart and invalidated the prompt prefix cache.
	if strings.Contains(info, "lsp=") {
		t.Errorf("RuntimeInfo must not list lsp servers, got %q", info)
	}
}

// The Environment line is the prompt's cache-sensitive prefix: field order,
// the conditional repo/workspace/projectspace entries and the two trailing
// placeholders are all part of the shipped shape.
func TestRuntimeInfo_Exact(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")

	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	osName := detectOS()
	head := "\n\n# Environment\n\nRuntime: host=" + host + " | os=" + osName + " | go=" + runtime.Version() + " | shell=/bin/zsh"

	gitDir := t.TempDir()
	if err := exec.Command("git", "init", gitDir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := detectRepoRoot(gitDir)
	if repo == "" {
		t.Skip("git init did not report a repo root")
	}
	nonGit := t.TempDir()

	// git -C "" falls back to the process cwd, so an empty WorkingDir still
	// reports the repository the tests run inside.
	selfRepoPart := ""
	if r := detectRepoRoot(""); r != "" {
		selfRepoPart = " | repo=" + r
	}

	const tail = " | model={{MODEL}} | modalities={{MODALITIES}}"
	bullets := "\n\n- workspace: working directory for tool operations (Bash, Read, Write, Grep, Lsp, etc). Operations outside this directory require absolute paths.\n" +
		"- projectspace: gbot state directory. Layout: gbot.log + gbot.pid at root; memory/ subdir holds markdown notes + memory.db (full transcript of all sessions); also session_notes/, file-history/, history/, plans/\n" +
		fmt.Sprintf("- artifacts: interactive HTML pages shown to the user in wui (card + full view). Write them to the projectspace artifacts directory (%s/artifacts/, absolute paths) via Write/Edit; they are served in the chat as cards. Good artifacts are self-contained single-file pages (inline CSS/JS, no external scripts — the CSP blocks all cross-origin resources; localStorage works). When iterating, Edit the same file. Prefer in-line content in chat when the page adds nothing — err on the side of not creating one.", "/p")

	tests := []struct {
		name string
		b    *Builder
		want string
	}{
		{
			name: "workspace and projectspace",
			b:    &Builder{WorkingDir: "/w", ProjectDir: "/p"},
			want: head + " | workspace=/w | projectspace=/p" + tail + bullets,
		},
		{
			name: "workspace only",
			b:    &Builder{WorkingDir: nonGit},
			want: head + " | workspace=" + nonGit + tail,
		},
		{
			name: "projectspace only",
			b:    &Builder{ProjectDir: "/p"},
			want: head + selfRepoPart + " | projectspace=/p" + tail,
		},
		{
			name: "neither",
			b:    &Builder{},
			want: head + selfRepoPart + tail,
		},
		{
			name: "git repo adds the repo field",
			b:    &Builder{WorkingDir: gitDir},
			want: head + " | repo=" + repo + " | workspace=" + gitDir + tail,
		},
	}
	for _, tc := range tests {
		if got := tc.b.RuntimeInfo(); got != tc.want {
			t.Errorf("RuntimeInfo(%s) =\n%q\nwant:\n%q", tc.name, got, tc.want)
		}
	}
}

// On Linux the OS label comes from /etc/os-release's PRETTY_NAME, not the
// runtime.GOOS fallback.
func TestDetectOS_MatchesOsRelease(t *testing.T) {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Skipf("no /etc/os-release: %v", err)
	}
	var pretty string
	for line := range strings.SplitSeq(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			pretty = strings.Trim(after, `"`)
			break
		}
	}
	if pretty == "" {
		t.Skip("/etc/os-release has no PRETTY_NAME")
	}

	want := fmt.Sprintf("%s (%s)", pretty, runtime.GOARCH)
	if got := detectOS(); got != want {
		t.Errorf("detectOS = %q, want %q", got, want)
	}
}

func TestRuntimeInfo_SHELLNotSet(t *testing.T) {
	origShell := os.Getenv("SHELL")
	_ = os.Unsetenv("SHELL")
	defer func() { _ = os.Setenv("SHELL", origShell) }()

	tmpDir := t.TempDir()
	b := &Builder{WorkingDir: tmpDir}

	info := b.RuntimeInfo()
	if !strings.Contains(info, "shell=/bin/bash") {
		t.Errorf("RuntimeInfo should default shell to /bin/bash when SHELL is unset, got %q", info)
	}
}

// TestLoadSystemFile_Public covers the exported LoadSystemFile — exercises
// both happy path (SYSTEM.md present) and missing-file path.
func TestLoadSystemFile_Public(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()
	got, err := LoadSystemFile()
	if err != nil {
		t.Fatalf("LoadSystemFile returned error: %v", err)
	}
	if got != "" {
		t.Errorf("LoadSystemFile should return empty when no SYSTEM.md, got %q", got)
	}

	content := "# System\nYou are gbot."
	if err := os.WriteFile(filepath.Join(gbotDir, "SYSTEM.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = LoadSystemFile()
	if err != nil {
		t.Fatalf("LoadSystemFile returned error: %v", err)
	}
	if got != content {
		t.Errorf("LoadSystemFile = %q, want %q", got, content)
	}
}

func TestDetectOS(t *testing.T) {
	osName := detectOS()
	if osName == "" {
		t.Fatal("detectOS returned empty string")
	}
	if !strings.Contains(osName, runtime.GOARCH) {
		t.Errorf("detectOS = %q, expected to contain %q", osName, runtime.GOARCH)
	}
}

func TestDetectRepoRoot(t *testing.T) {
	tmpDir := t.TempDir()
	if err := exec.Command("git", "init", tmpDir).Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	got := detectRepoRoot(tmpDir)
	if got == "" {
		t.Error("detectRepoRoot on a git repo should return non-empty")
	}

	noGit := t.TempDir()
	if got := detectRepoRoot(noGit); got != "" {
		t.Errorf("detectRepoRoot outside a git repo = %q, want empty", got)
	}
}

// wantArtifactsBullet pins the finalized artifacts Environment entry
// verbatim — any rewording of the shipped prompt fails this test. The
// projectspace path is baked in concretely so the model never has to
// assemble it from the projectspace= line.
const wantArtifactsBullet = "- artifacts: interactive HTML pages shown to the user in wui (card + full view). Write them to the projectspace artifacts directory (/p/artifacts/, absolute paths) via Write/Edit; they are served in the chat as cards. Good artifacts are self-contained single-file pages (inline CSS/JS, no external scripts — the CSP blocks all cross-origin resources; localStorage works). When iterating, Edit the same file. Prefer in-line content in chat when the page adds nothing — err on the side of not creating one."

func TestRuntimeInfo_ArtifactsBullet(t *testing.T) {
	b := &Builder{WorkingDir: "/w", ProjectDir: "/p"}
	info := b.RuntimeInfo()
	if !strings.Contains(info, wantArtifactsBullet) {
		t.Errorf("RuntimeInfo missing the finalized artifacts bullet; got:\n%s", info)
	}

	noProject := &Builder{WorkingDir: "/w"}
	if got := noProject.RuntimeInfo(); strings.Contains(got, "- artifacts:") {
		t.Errorf("RuntimeInfo without ProjectDir must not mention artifacts; got:\n%s", got)
	}
}
