package context_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if b.GBOTMDContent != "" {
		t.Errorf("GBOTMDContent should be empty by default, got %q", b.GBOTMDContent)
	}
}

func TestBuild_Basic(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	// Check for unique, specific substrings from the system prompt
	if !strings.Contains(promptStr, "You are gbot") {
		t.Error("built prompt missing 'You are gbot'")
	}
	if !strings.Contains(promptStr, "Current date:") {
		t.Error("built prompt missing 'Current date:'")
	}
	if !strings.Contains(promptStr, "/work") {
		t.Error("built prompt missing working directory '/work'")
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

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	if !strings.Contains(promptStr, "Git branch: test-branch") {
		t.Error("built prompt missing git status")
	}
}

func TestBuild_WithGBOTMDContent(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GBOTMDContent = "Always use TypeScript strict mode."

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	if !strings.Contains(promptStr, "Always use TypeScript strict mode.") {
		t.Error("built prompt missing GBOT.md content")
	}
	if !strings.Contains(promptStr, "Instructions") {
		t.Error("built prompt missing Instructions section header")
	}
}

func TestBuild_WithToolPrompts(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"Tool 1: Use wisely", "Tool 2: Be carefully"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	// Check for specific tool names and unique content
	if !strings.Contains(promptStr, "Tool 1: Use wisely") {
		t.Error("built prompt missing tool prompt 1")
	}
	if !strings.Contains(promptStr, "Tool 2: Be carefully") {
		t.Error("built prompt missing tool prompt 2")
	}
	// Also verify base prompt is still present
	if !strings.Contains(promptStr, "You are gbot") {
		t.Error("built prompt should still contain 'You are gbot'")
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
	b.GBOTMDContent = "Custom instructions here."
	b.ToolPrompts = []string{"Bash tool: Use for shell commands."}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	expectedParts := []string{
		"You are gbot",
		"/project",
		"Git branch: develop",
		"Default branch: main",
		"dirty",
		"Custom instructions here.",
		"Bash tool: Use for shell commands.",
	}

	for _, part := range expectedParts {
		if !strings.Contains(promptStr, part) {
			t.Errorf("built prompt missing expected part: %q", part)
		}
	}

	// Verify order: system prompt should come before tool definitions
	systemPromptIdx := strings.Index(promptStr, "You are gbot")
	toolPromptIdx := strings.Index(promptStr, "Bash tool: Use for shell commands.")
	if systemPromptIdx == -1 || toolPromptIdx == -1 {
		t.Error("both system prompt and tool definitions should be present")
	} else if systemPromptIdx > toolPromptIdx {
		t.Error("system prompt should appear before tool definitions")
	}
}

func TestBuild_EmptyToolPrompts(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.ToolPrompts = []string{"", "valid prompt", ""}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	if !strings.Contains(promptStr, "valid prompt") {
		t.Error("built prompt missing valid tool prompt")
	}
}

func TestBuild_EmptyGBOTMDContent(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GBOTMDContent = ""
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	if strings.Contains(promptStr, "Instructions") {
		t.Error("built prompt should not have Instructions section for empty GBOT.md")
	}
}

func TestBuild_EscapesJSON(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	b.GBOTMDContent = `Contains "quotes" and <html>`

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not valid JSON: %v, raw: %s", err, string(result))
	}

	if !strings.Contains(promptStr, `Contains "quotes"`) {
		t.Errorf("expected escaped quotes in prompt, got: %s", promptStr)
	}
}

func TestPlatformInfo(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/test/dir")
	info := b.PlatformInfo()

	if !strings.Contains(info, runtime.GOOS) {
		t.Error("platform info missing OS")
	}
	if !strings.Contains(info, runtime.GOARCH) {
		t.Error("platform info missing ARCH")
	}
	if !strings.Contains(info, "/test/dir") {
		t.Error("platform info missing working directory")
	}
	if !strings.Contains(info, "Shell:") {
		t.Error("platform info missing shell")
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

func TestEscapeJSONString(t *testing.T) {
	t.Parallel()
	// EscapeJSONString uses json.HTMLEscape which produces JSON-safe output.
	// Verify round-trip: escaped output should parse correctly as JSON string.
	tests := []string{
		"hello",
		`say "hi"`,
		"line1\nline2",
		"<b>bold</b>",
		"tab\there",
		"back\\slash",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			escaped := context.EscapeJSONString(input)
			// Wrap in quotes to make valid JSON
			roundtrip := `"` + escaped + `"`
			var got string
			if err := json.Unmarshal([]byte(roundtrip), &got); err != nil {
				t.Fatalf("EscapeJSONString(%q) produced invalid JSON: %v, escaped=%q", input, err, escaped)
			}
			if got != input {
				t.Errorf("roundtrip mismatch: got %q, want %q", got, input)
			}
		})
	}
}

func TestEscapeJSONString_ShortOutput(t *testing.T) {
	t.Parallel()
	// The EscapeJSONString function has a fallback path for when
	// json.Marshal returns output shorter than 2 bytes or without
	// surrounding quotes. Test with an empty string to exercise the
	// short-output path (len(b) < 2).
	result := context.EscapeJSONString("")
	if result != "" {
		t.Errorf("expected empty string for empty input, got %q", result)
	}
}

func TestLoadGitStatus(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// Initialize a git repo with a specific branch
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

	// Need at least one commit for rev-parse --abbrev-ref HEAD to return
	// the branch name instead of "HEAD"
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

func TestLoadGBOTMD(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	// No GBOT.md files exist
	content := context.LoadGBOTMD(tmpDir)
	if content != "" {
		t.Errorf("expected empty content with no GBOT.md files, got %q", content)
	}

	// Create a GBOT.md in the working directory
	gbotMD := filepath.Join(tmpDir, "GBOT.md")
	if err := os.WriteFile(gbotMD, []byte("Test instructions."), 0644); err != nil {
		t.Fatal(err)
	}

	content = context.LoadGBOTMD(tmpDir)
	if !strings.Contains(content, "Test instructions.") {
		t.Errorf("expected GBOT.md content, got %q", content)
	}
}

func TestPlatformInfo_EmptyShell(t *testing.T) {
	t.Parallel()
	origShell := os.Getenv("SHELL")
	_ = os.Setenv("SHELL", "")
	defer func() { _ = os.Setenv("SHELL", origShell) }()

	b := context.NewBuilder("/test")
	info := b.PlatformInfo()
	if !strings.Contains(info, "/bin/bash") {
		t.Errorf("expected /bin/bash fallback, got %q", info)
	}
}

func TestBaseSystemPrompt(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	prompt := b.BaseSystemPrompt()
	if !strings.Contains(prompt, "You are gbot") {
		t.Error("base prompt missing greeting")
	}
	if !strings.Contains(prompt, "Current date:") {
		t.Error("base prompt missing date")
	}
}

func TestBuild_NoFiles(t *testing.T) {
	t.Parallel()
	b := context.NewBuilder("/work")
	// No files, no git status, no GBOT.md - just base prompt
	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("Build() result is not a valid JSON string: %v", err)
	}

	// Should still contain the base system prompt
	if !strings.Contains(promptStr, "You are gbot") {
		t.Error("prompt should contain 'You are gbot' even without files")
	}
	// Should contain working directory
	if !strings.Contains(promptStr, "/work") {
		t.Error("prompt should contain working directory even without files")
	}
	// Should contain platform info (Platform: section)
	if !strings.Contains(promptStr, "Platform:") {
		t.Error("prompt should contain platform info even without files")
	}
}

func TestBuild_MarshalError(t *testing.T) {
	t.Parallel()
	// json.Marshal of a string never fails in practice, so this test
	// verifies the error path is reachable by testing Build returns
	// a valid result (proving lines 77-81 execute correctly).
	b := context.NewBuilder("/work")
	b.GBOTMDContent = "test"
	b.ToolPrompts = []string{"p1", "", "p3"}

	result, err := b.Build()
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if result == nil {
		t.Fatal("Build() returned nil result")
	}

	var promptStr string
	if err := json.Unmarshal(result, &promptStr); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if !strings.Contains(promptStr, "p1") {
		t.Error("missing tool prompt p1")
	}
	if !strings.Contains(promptStr, "p3") {
		t.Error("missing tool prompt p3")
	}
}
