package context_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/context"
)

// ---------------------------------------------------------------------------
// LoadContextFiles
// ---------------------------------------------------------------------------

func TestLoadContextFiles_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()
	result := context.LoadContextFiles(tmpDir)
	if len(result) != 0 {
		t.Errorf("expected empty map with no files, got %d keys", len(result))
	}
}

func TestLoadContextFiles_UserGlobal(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "CLAUDE.md"), []byte("Use strict mode."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Override home dir for test
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	workDir := t.TempDir()
	result := context.LoadContextFiles(workDir)
	content, ok := result[context.KeyClaudeMd]
	if !ok {
		t.Fatal("expected claudeMd key in result")
	}
	if !strings.Contains(content, "Use strict mode.") {
		t.Errorf("claudeMd should contain CLAUDE.md content, got %q", content)
	}
}

func TestLoadContextFiles_UserGlobalAgentsMd(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "AGENTS.md"), []byte("Agent rules."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "CLAUDE.md"), []byte("Claude rules."), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	workDir := t.TempDir()
	result := context.LoadContextFiles(workDir)
	content, ok := result[context.KeyClaudeMd]
	if !ok {
		t.Fatal("expected claudeMd key")
	}
	if !strings.Contains(content, "Agent rules.") {
		t.Error("claudeMd should contain AGENTS.md content")
	}
	if !strings.Contains(content, "Claude rules.") {
		t.Error("claudeMd should contain CLAUDE.md content")
	}
	// AGENTS.md should appear before CLAUDE.md (AGENTS.md read first)
	agentsIdx := strings.Index(content, "Agent rules.")
	claudeIdx := strings.Index(content, "Claude rules.")
	if agentsIdx >= claudeIdx {
		t.Error("AGENTS.md content should appear before CLAUDE.md content")
	}
}

func TestLoadContextFiles_ProjectOnly(t *testing.T) {
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "/nonexistent")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte("Project instructions."), 0o644); err != nil {
		t.Fatal(err)
	}

	result := context.LoadContextFiles(tmpDir)
	if _, ok := result[context.KeyClaudeMd]; ok {
		t.Error("should not have claudeMd when only project files exist")
	}
	content, ok := result[context.KeyProjectClaudeMd]
	if !ok {
		t.Fatal("expected projectClaudeMd key")
	}
	if !strings.Contains(content, "Project instructions.") {
		t.Errorf("projectClaudeMd should contain project content, got %q", content)
	}
}

func TestLoadContextFiles_ProjectPriority(t *testing.T) {
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "/nonexistent")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	// Create structure: root/sub/cwd
	root := t.TempDir()
	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Root has CLAUDE.md
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Root level."), 0o644); err != nil {
		t.Fatal(err)
	}
	// Subdir has CLAUDE.md
	if err := os.WriteFile(filepath.Join(subDir, "CLAUDE.md"), []byte("Sub level."), 0o644); err != nil {
		t.Fatal(err)
	}

	result := context.LoadContextFiles(subDir)
	content := result[context.KeyProjectClaudeMd]

	rootIdx := strings.Index(content, "Root level.")
	subIdx := strings.Index(content, "Sub level.")
	if rootIdx == -1 || subIdx == -1 {
		t.Fatalf("expected both root and sub content, got %q", content)
	}
	// Root should appear before sub (root first, CWD last = highest priority)
	if rootIdx > subIdx {
		t.Error("root content should appear before sub content (root→CWD ordering)")
	}
}

func TestLoadContextFiles_Both(t *testing.T) {
	homeDir := t.TempDir()
	gbotDir := filepath.Join(homeDir, ".gbot")
	if err := os.MkdirAll(gbotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gbotDir, "CLAUDE.md"), []byte("Global instructions."), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("Project instructions."), 0o644); err != nil {
		t.Fatal(err)
	}

	result := context.LoadContextFiles(workDir)
	globalContent, ok := result[context.KeyClaudeMd]
	if !ok {
		t.Fatal("expected claudeMd key")
	}
	if !strings.Contains(globalContent, "Global instructions.") {
		t.Error("claudeMd should contain global content")
	}

	projectContent, ok := result[context.KeyProjectClaudeMd]
	if !ok {
		t.Fatal("expected projectClaudeMd key")
	}
	if !strings.Contains(projectContent, "Project instructions.") {
		t.Error("projectClaudeMd should contain project content")
	}
}

func TestLoadContextFiles_EmptyFile(t *testing.T) {
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", "/nonexistent")
	defer func() { _ = os.Setenv("HOME", origHome) }()

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte("   "), 0o644); err != nil {
		t.Fatal(err)
	}

	result := context.LoadContextFiles(tmpDir)
	if len(result) != 0 {
		t.Errorf("whitespace-only file should be skipped, got %d keys", len(result))
	}
}

// ---------------------------------------------------------------------------
// BuildPrependUserContext
// ---------------------------------------------------------------------------

func TestBuildPrependUserContext_Empty(t *testing.T) {
	result := context.BuildPrependUserContext(map[string]string{})
	if result != "" {
		t.Errorf("expected empty string for empty map, got %q", result)
	}
}

func TestBuildPrependUserContext_SingleKey(t *testing.T) {
	result := context.BuildPrependUserContext(map[string]string{
		context.KeyCurrentDate: "Today's date is 2026/01/01.",
	})
	if !strings.Contains(result, "<system-reminder>") {
		t.Error("expected <system-reminder> wrapper")
	}
	if !strings.Contains(result, "# currentDate") {
		t.Error("expected # currentDate header")
	}
	if !strings.Contains(result, "Today's date is 2026/01/01.") {
		t.Error("expected currentDate value")
	}
	if !strings.Contains(result, "IMPORTANT: this context may or may not be relevant") {
		t.Error("expected IMPORTANT footer")
	}
	if !strings.Contains(result, "</system-reminder>") {
		t.Error("expected closing </system-reminder>")
	}
}

func TestBuildPrependUserContext_MultipleKeys(t *testing.T) {
	result := context.BuildPrependUserContext(map[string]string{
		context.KeyClaudeMd:        "Global rules.",
		context.KeyProjectClaudeMd: "Project rules.",
		context.KeyCurrentDate:     "Today's date is 2026/05/14.",
	})
	if !strings.Contains(result, "# claudeMd") {
		t.Error("expected # claudeMd header")
	}
	if !strings.Contains(result, "# projectClaudeMd") {
		t.Error("expected # projectClaudeMd header")
	}
	if !strings.Contains(result, "# currentDate") {
		t.Error("expected # currentDate header")
	}
	if !strings.Contains(result, "Global rules.") {
		t.Error("expected global content")
	}
	if !strings.Contains(result, "Project rules.") {
		t.Error("expected project content")
	}

	// Key ordering: claudeMd before projectClaudeMd before currentDate
	claudeIdx := strings.Index(result, "# claudeMd")
	projectIdx := strings.Index(result, "# projectClaudeMd")
	dateIdx := strings.Index(result, "# currentDate")
	if claudeIdx >= projectIdx {
		t.Error("claudeMd should appear before projectClaudeMd")
	}
	if projectIdx >= dateIdx {
		t.Error("projectClaudeMd should appear before currentDate")
	}
}

func TestBuildPrependUserContext_FormatMatchesTS(t *testing.T) {
	m := map[string]string{
		context.KeyClaudeMd:    "User global content.",
		context.KeyCurrentDate: "Today's date is 2026/05/14.",
	}
	result := context.BuildPrependUserContext(m)

	expected := "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# claudeMd\nUser global content.\n# currentDate\nToday's date is 2026/05/14.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>"

	if result != expected {
		t.Errorf("output does not match TS format.\ngot:\n%s\n\nwant:\n%s", result, expected)
	}
}
