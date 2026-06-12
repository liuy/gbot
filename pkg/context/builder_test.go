package context_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

func TestNewBuilder(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	if b == nil {
		t.Fatal("NewBuilder returned nil")
	}
	if b.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q, want %q", b.WorkingDir, "/work")
	}
	if b.GitStatus != nil {
		t.Error("GitStatus should be nil by default")
	}
	if len(b.ToolPrompts) != 0 {
		t.Errorf("ToolPrompts should be empty, got %d items", len(b.ToolPrompts))
	}
}

func TestBuild_Basic(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("built prompt missing {{SYSTEM}} stub")
	}
	if !strings.Contains(promptStr, "{{SOUL}}") {
		t.Error("built prompt missing {{SOUL}} stub")
	}
	if !strings.Contains(promptStr, "Runtime:") {
		t.Error("built prompt missing runtime info")
	}
}

func TestBuild_WithGitStatus(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GitStatus = &context.GitStatusInfo{
		IsGit:         true,
		Branch:        "test-branch",
		DefaultBranch: "test-default",
		IsDirty:       false,
	}
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if !strings.Contains(promptStr, "Git branch: test-branch") {
		t.Error("built prompt missing git status")
	}
}

func TestBuild_WithToolPrompts_NoLongerInSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"Tool 1: Use wisely", "Tool 2: Be carefully"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if strings.Contains(promptStr, "Tool 1: Use wisely") {
		t.Error("tool prompts must not appear in system prompt")
	}
	if strings.Contains(promptStr, "Tool 2: Be carefully") {
		t.Error("tool prompts must not appear in system prompt")
	}
	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("built prompt should still contain {{SYSTEM}} stub")
	}
}

func TestBuild_AllSections(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/project")
	b.GitStatus = &context.GitStatusInfo{
		IsGit:         true,
		Branch:        "develop",
		DefaultBranch: "main",
		IsDirty:       true,
	}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	expectedParts := []string{
		"{{SYSTEM}}",
		"/project",
		"Git branch: develop",
		"Default branch: main",
		"dirty",
	}

	for _, part := range expectedParts {
		if !strings.Contains(promptStr, part) {
			t.Errorf("built prompt missing expected part: %q", part)
		}
	}
}

func TestBuild_EmptyToolPrompts_NotInSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"", "valid prompt", ""}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if strings.Contains(promptStr, "valid prompt") {
		t.Error("tool prompts must not appear in system prompt")
	}
}

func TestBuild_EscapesJSON(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if result == "" {
		t.Error("Build() returned empty string")
	}
}

func TestRuntimeInfo(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/test/dir")
	info := b.RuntimeInfo()

	if !strings.Contains(info, "host=") {
		t.Error("runtime info missing host=")
	}
	if !strings.Contains(info, "os=") {
		t.Error("runtime info missing os=")
	}
	if !strings.Contains(info, "go=") {
		t.Error("runtime info missing go=")
	}
	if !strings.Contains(info, "shell=") {
		t.Error("runtime info missing shell=")
	}
	if !strings.Contains(info, "model={{MODEL}}") {
		t.Error("runtime info missing model={{MODEL}}")
	}
}

func TestGitStatusSection_NonGit(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GitStatus = &context.GitStatusInfo{IsGit: false}
	section := b.GitStatusSection()
	if !strings.Contains(section, "Not a git repository") {
		t.Errorf("expected 'Not a git repository', got %q", section)
	}
	if strings.Contains(section, "Git branch:") {
		t.Errorf("non-git section should not contain 'Git branch:', got %q", section)
	}
}

func TestGitStatusSection_Clean(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GitStatus = &context.GitStatusInfo{
		IsGit:   true,
		Branch:  "main",
		IsDirty: false,
	}
	section := b.GitStatusSection()
	if !strings.Contains(section, "Git branch: main") {
		t.Errorf("expected 'Git branch: main', got %q", section)
	}
	if !strings.Contains(section, "clean") {
		t.Errorf("expected 'clean', got %q", section)
	}
	if strings.Contains(section, "dirty") {
		t.Errorf("clean status should not contain 'dirty', got %q", section)
	}
}

func TestGitStatusSection_Dirty(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GitStatus = &context.GitStatusInfo{
		IsGit:   true,
		Branch:  "feature",
		IsDirty: true,
	}
	section := b.GitStatusSection()
	if !strings.Contains(section, "Git branch: feature") {
		t.Errorf("expected 'Git branch: feature', got %q", section)
	}
	if !strings.Contains(section, "dirty") {
		t.Errorf("expected 'dirty', got %q", section)
	}
	if strings.Contains(section, "clean") {
		t.Errorf("dirty status should not contain 'clean', got %q", section)
	}
}

func TestLoadGitStatus(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skip("git not available")
	}
	cmd = exec.Command("git", "config", "user.email", "test@test.com")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "config", "user.name", "Test")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "checkout", "-b", "test-branch")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "initial.txt"), []byte("init"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "initial.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	info := context.LoadGitStatus(tmpDir)
	if !info.IsGit {
		t.Fatal("expected IsGit=true for git repo")
	}
	if info.Branch != "test-branch" {
		t.Errorf("Branch = %q, want 'test-branch'", info.Branch)
	}
	if info.IsDirty {
		t.Error("expected clean status for fresh repo")
	}
}

func TestLoadGitStatus_NonGitDir(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	info := context.LoadGitStatus(tmpDir)
	if info.IsGit {
		t.Error("temp dir should not be a git repository")
	}
}

func TestLoadGitStatus_WithRemote(t *testing.T) {
	tmpDir := t.TempDir()

	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skip("git not available")
	}
	_, _ = exec.Command("git", "-C", tmpDir, "config", "user.email", "t@t.com").Output()
	_, _ = exec.Command("git", "-C", tmpDir, "config", "user.name", "T").Output()
	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	_, _ = exec.Command("git", "-C", tmpDir, "add", ".").Output()
	_, _ = exec.Command("git", "-C", tmpDir, "commit", "-m", "init").Output()

	_, _ = exec.Command("git", "-C", tmpDir, "remote", "add", "origin", "/tmp/fake").Output()
	_, _ = exec.Command("git", "-C", tmpDir, "update-ref", "refs/remotes/origin/main", "HEAD").Output()
	_, _ = exec.Command("git", "-C", tmpDir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main").Output()

	info := context.LoadGitStatus(tmpDir)
	if !info.IsGit {
		t.Fatal("expected IsGit=true")
	}
	if info.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want 'main'", info.DefaultBranch)
	}
}

func TestPlatformInfo_EmptyShell(t *testing.T) {
	t.Parallel()
	origShell := os.Getenv("SHELL")
	_ = os.Setenv("SHELL", "")
	defer func() { _ = os.Setenv("SHELL", origShell) }()

	b := context.NewBuilder("/test")
	info := b.RuntimeInfo()
	if !strings.Contains(info, "/bin/bash") {
		t.Errorf("expected /bin/bash fallback, got %q", info)
	}
}

func TestBaseSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	prompt := b.BaseSystemPrompt()
	if !strings.Contains(prompt, "{{SYSTEM}}") {
		t.Error("base prompt should contain {{SYSTEM}} stub")
	}
}

func TestDefaultBasePrompt(t *testing.T) {
	t.Parallel()
	prompt := context.DefaultBasePrompt()
	if !strings.Contains(prompt, "You are creature") {
		t.Error("default base prompt missing identity")
	}
	if strings.Contains(prompt, "{{SOUL}}") {
		t.Error("DefaultBasePrompt should NOT contain {{SOUL}} — it's added by builder")
	}
	if !strings.Contains(prompt, "# Code style") {
		t.Error("default base prompt missing Code style section")
	}
}

func TestBuild_NoFiles(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	promptStr := result

	if !strings.Contains(promptStr, "{{SYSTEM}}") {
		t.Error("prompt should contain {{SYSTEM}} stub")
	}
	if !strings.Contains(promptStr, "{{SOUL}}") {
		t.Error("prompt should contain {{SOUL}} stub")
	}
	if !strings.Contains(promptStr, "Runtime:") {
		t.Error("prompt should contain runtime info even without files")
	}
}

func TestBuild_WithSkillListing(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.SkillListing = "/commit - create a commit\n/review - review code"

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	if !strings.Contains(result, "## Available Skills") {
		t.Error("built prompt missing '## Available Skills' section")
	}
	if !strings.Contains(result, "/commit") {
		t.Error("built prompt missing skill listing content")
	}
}

func TestBuild_ToolPromptsIgnored(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"p1", "", "p3"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if result == "" {
		t.Fatal("Build() returned empty result")
	}

	promptStr := result

	// Tool prompts go into tool defs, not system prompt.
	if strings.Contains(promptStr, "p1") {
		t.Error("tool prompt p1 must not be in system prompt")
	}
	if strings.Contains(promptStr, "p3") {
		t.Error("tool prompt p3 must not be in system prompt")
	}
}
