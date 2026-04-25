package skills

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ParseSkill — full frontmatter parsing tests
// Source: src/skills/loadSkillsDir.ts:191-265 — parseSkillFrontmatterFields
// ---------------------------------------------------------------------------

func TestParseSkill_AllFields(t *testing.T) {
	t.Parallel()

	input := `---
name: My Skill
description: "A test skill"
when_to_use: Use this when testing
allowed-tools: Bash,Read,Write
argument-hint: <file> <pattern>
arguments: file pattern
model: haiku
effort: high
context: fork
agent: code-reviewer
shell: bash
user-invocable: true
disable-model-invocation: false
aliases:
  - test
  - check
version: "1.0"
paths:
  - "**/*.go"
  - "src/**"
---
This is the skill body.
Multiple lines.
`
	cmd := ParseSkill("my-skill", "/path/to/my-skill/SKILL.md", input, types.SkillSourceUser)

	// Identity
	if cmd.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", cmd.Name, "my-skill")
	}
	if cmd.DisplayName != "My Skill" {
		t.Errorf("DisplayName = %q, want %q", cmd.DisplayName, "My Skill")
	}
	if cmd.Description != "A test skill" {
		t.Errorf("Description = %q, want %q", cmd.Description, "A test skill")
	}
	if cmd.WhenToUse != "Use this when testing" {
		t.Errorf("WhenToUse = %q, want %q", cmd.WhenToUse, "Use this when testing")
	}

	// Execution control
	if cmd.Model != "haiku" {
		t.Errorf("Model = %q, want %q", cmd.Model, "haiku")
	}
	if cmd.Effort != "high" {
		t.Errorf("Effort = %q, want %q", cmd.Effort, "high")
	}
	if cmd.Context != "fork" {
		t.Errorf("Context = %q, want %q", cmd.Context, "fork")
	}
	if cmd.AgentType != "code-reviewer" {
		t.Errorf("AgentType = %q, want %q", cmd.AgentType, "code-reviewer")
	}
	if cmd.Shell == nil || *cmd.Shell != "bash" {
		t.Errorf("Shell = %v, want 'bash'", cmd.Shell)
	}
	if !cmd.IsUserInvocable {
		t.Error("IsUserInvocable should be true")
	}
	if cmd.DisableModelInvocation {
		t.Error("DisableModelInvocation should be false")
	}

	// Tools
	if len(cmd.AllowedTools) != 3 {
		t.Fatalf("AllowedTools len = %d, want 3: %v", len(cmd.AllowedTools), cmd.AllowedTools)
	}
	if cmd.AllowedTools[0] != "Bash" {
		t.Errorf("AllowedTools[0] = %q, want %q", cmd.AllowedTools[0], "Bash")
	}

	// Arguments
	if cmd.ArgumentHint != "<file> <pattern>" {
		t.Errorf("ArgumentHint = %q, want %q", cmd.ArgumentHint, "<file> <pattern>")
	}
	if len(cmd.Arguments) != 2 {
		t.Fatalf("Arguments len = %d, want 2", len(cmd.Arguments))
	}
	if cmd.Arguments[0].Name != "file" {
		t.Errorf("Arguments[0].Name = %q, want %q", cmd.Arguments[0].Name, "file")
	}

	// Paths
	if len(cmd.Paths) != 2 {
		t.Fatalf("Paths len = %d, want 2: %v", len(cmd.Paths), cmd.Paths)
	}
	// Paths should have /** suffix stripped
	if cmd.Paths[0] != "**/*.go" {
		t.Errorf("Paths[0] = %q, want %q", cmd.Paths[0], "**/*.go")
	}

	// Aliases
	if len(cmd.Aliases) != 2 {
		t.Fatalf("Aliases len = %d, want 2", len(cmd.Aliases))
	}
	if cmd.Aliases[0] != "test" {
		t.Errorf("Aliases[0] = %q, want %q", cmd.Aliases[0], "test")
	}

	// Version
	if cmd.Version != "1.0" {
		t.Errorf("Version = %q, want %q", cmd.Version, "1.0")
	}

	// Content
	if !strings.Contains(cmd.Content, "This is the skill body.") {
		t.Errorf("Content should contain body text, got %q", cmd.Content)
	}

	// Metadata
	if cmd.Type != "prompt" {
		t.Errorf("Type = %q, want %q", cmd.Type, "prompt")
	}
	if cmd.Source != types.SkillSourceUser {
		t.Errorf("Source = %v, want %v", cmd.Source, types.SkillSourceUser)
	}
	if cmd.LoadedFrom != "skills" {
		t.Errorf("LoadedFrom = %q, want %q", cmd.LoadedFrom, "skills")
	}
	if cmd.ContentLength == 0 {
		t.Error("ContentLength should be non-zero")
	}
	if cmd.ProgressMessage != "running" {
		t.Errorf("ProgressMessage = %q, want %q", cmd.ProgressMessage, "running")
	}
}

func TestParseSkill_MinimalFrontmatter(t *testing.T) {
	t.Parallel()

	input := "---\ndescription: Minimal skill\n---\nDo something."
	cmd := ParseSkill("minimal", "/path/minimal/SKILL.md", input, types.SkillSourceProject)

	if cmd.Name != "minimal" {
		t.Errorf("Name = %q, want %q", cmd.Name, "minimal")
	}
	if cmd.DisplayName != "" {
		t.Errorf("DisplayName should be empty for minimal, got %q", cmd.DisplayName)
	}
	if cmd.Description != "Minimal skill" {
		t.Errorf("Description = %q, want %q", cmd.Description, "Minimal skill")
	}
	// Defaults
	if cmd.Model != "" {
		t.Errorf("Model should default empty, got %q", cmd.Model)
	}
	if cmd.Context != "" {
		t.Errorf("Context should default empty (inline), got %q", cmd.Context)
	}
	if cmd.Shell != nil {
		t.Errorf("Shell should default nil, got %v", cmd.Shell)
	}
	if !cmd.IsUserInvocable {
		t.Error("IsUserInvocable should default true")
	}
	if cmd.HasUserSpecifiedDesc != true {
		t.Error("HasUserSpecifiedDesc should be true when description is in frontmatter")
	}
}

func TestParseSkill_NoFrontmatter(t *testing.T) {
	t.Parallel()

	input := "# Just markdown\nSome content here."
	cmd := ParseSkill("nofm", "/path/nofm/SKILL.md", input, types.SkillSourceBundled)

	// Description should be extracted from first markdown line
	if !strings.Contains(cmd.Description, "Just markdown") {
		t.Errorf("Description should be extracted from markdown, got %q", cmd.Description)
	}
	if cmd.Content != input {
		t.Errorf("Content should be original input when no frontmatter")
	}
}

func TestParseSkill_ModelInherit(t *testing.T) {
	t.Parallel()

	input := "---\nmodel: inherit\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if cmd.Model != "" {
		t.Errorf("model: inherit should map to empty string, got %q", cmd.Model)
	}
}

func TestParseSkill_ContextFork(t *testing.T) {
	t.Parallel()

	input := "---\ncontext: fork\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if cmd.Context != "fork" {
		t.Errorf("context: fork should be 'fork', got %q", cmd.Context)
	}
}

func TestParseSkill_ContextInline(t *testing.T) {
	t.Parallel()

	input := "---\ncontext: inline\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	// Correction 25: only "fork" maps to fork, everything else is empty (inline)
	if cmd.Context != "" {
		t.Errorf("context: inline should map to empty (not store 'inline'), got %q", cmd.Context)
	}
}

func TestParseSkill_AllowedToolsArray(t *testing.T) {
	t.Parallel()

	input := "---\nallowed-tools:\n  - Bash\n  - Read\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if len(cmd.AllowedTools) != 2 {
		t.Fatalf("AllowedTools len = %d, want 2", len(cmd.AllowedTools))
	}
	if cmd.AllowedTools[1] != "Read" {
		t.Errorf("AllowedTools[1] = %q, want %q", cmd.AllowedTools[1], "Read")
	}
}

func TestParseSkill_ArgumentsArray(t *testing.T) {
	t.Parallel()

	input := "---\narguments:\n  - file\n  - output\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if len(cmd.Arguments) != 2 {
		t.Fatalf("Arguments len = %d, want 2", len(cmd.Arguments))
	}
	if cmd.Arguments[1].Name != "output" {
		t.Errorf("Arguments[1].Name = %q, want %q", cmd.Arguments[1].Name, "output")
	}
}

func TestParseSkill_PathsStripsGlobSuffix(t *testing.T) {
	t.Parallel()

	input := "---\npaths:\n  - \"src/**\"\n  - \"*.go\"\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if len(cmd.Paths) != 2 {
		t.Fatalf("Paths len = %d, want 2: %v", len(cmd.Paths), cmd.Paths)
	}
	// /** should be stripped
	if cmd.Paths[0] != "src" {
		t.Errorf("Paths[0] = %q, want %q (/** stripped)", cmd.Paths[0], "src")
	}
	if cmd.Paths[1] != "*.go" {
		t.Errorf("Paths[1] = %q, want %q", cmd.Paths[1], "*.go")
	}
}

func TestParseSkill_PathsMatchAllSkipped(t *testing.T) {
	t.Parallel()

	input := "---\npaths:\n  - \"*\"\n  - \"**\"\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if len(cmd.Paths) != 0 {
		t.Errorf("match-all patterns should be filtered, got %d: %v", len(cmd.Paths), cmd.Paths)
	}
}

func TestParseSkill_UserInvocableDefault(t *testing.T) {
	t.Parallel()

	// Default: true when not specified
	input := "---\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if !cmd.IsUserInvocable {
		t.Error("IsUserInvocable should default to true")
	}

	// Explicit false
	input2 := "---\nuser-invocable: false\n---\nBody."
	cmd2 := ParseSkill("test2", "/test2/SKILL.md", input2, types.SkillSourceUser)
	if cmd2.IsUserInvocable {
		t.Error("IsUserInvocable should be false when set to false")
	}
	if !cmd2.IsHidden() {
		t.Error("IsHidden() should return true when IsUserInvocable is false")
	}
}

func TestParseSkill_ShellPowershell(t *testing.T) {
	t.Parallel()

	input := "---\nshell: powershell\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if cmd.Shell == nil || *cmd.Shell != "powershell" {
		t.Errorf("Shell = %v, want 'powershell'", cmd.Shell)
	}
}

func TestParseSkill_DisableModelInvocation(t *testing.T) {
	t.Parallel()

	input := "---\ndisable-model-invocation: true\n---\nBody."
	cmd := ParseSkill("test", "/test/SKILL.md", input, types.SkillSourceUser)
	if !cmd.DisableModelInvocation {
		t.Error("DisableModelInvocation should be true")
	}
}

func TestParseSkill_SourceDir(t *testing.T) {
	t.Parallel()

	input := "---\n---\nBody."
	cmd := ParseSkill("test", "/path/to/test/SKILL.md", input, types.SkillSourceUser)
	if cmd.SourceDir != "/path/to/test" {
		t.Errorf("SourceDir = %q, want %q", cmd.SourceDir, "/path/to/test")
	}
}

func TestParseSkill_SourceToLoadedFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source   types.SkillSource
		expected string
	}{
		{types.SkillSourceBundled, "bundled"},
		{types.SkillSourceMCP, "mcp"},
		{types.SkillSourcePlugin, "plugin"},
		{types.SkillSourceUser, "skills"},
		{types.SkillSourceProject, "skills"},
		{types.SkillSourceManaged, "skills"},
	}
	for _, tt := range tests {
		input := "---\n---\nBody."
		cmd := ParseSkill("test", "/test/SKILL.md", input, tt.source)
		if cmd.LoadedFrom != tt.expected {
			t.Errorf("source=%v: LoadedFrom = %q, want %q", tt.source, cmd.LoadedFrom, tt.expected)
		}
	}
}
