package types

import (
	"testing"
)

func TestSkillCommand_IsHidden(t *testing.T) {
	t.Parallel()

	// Default: IsUserInvocable=true → not hidden
	cmd := &SkillCommand{IsUserInvocable: true}
	if cmd.IsHidden() {
		t.Error("user-invocable skill should not be hidden")
	}

	// Agent-only: IsUserInvocable=false → hidden
	cmd2 := &SkillCommand{IsUserInvocable: false}
	if !cmd2.IsHidden() {
		t.Error("agent-only skill should be hidden")
	}
}

func TestSkillCommand_UserFacingName(t *testing.T) {
	t.Parallel()

	// DisplayName takes priority
	cmd := &SkillCommand{Name: "commit", DisplayName: "Git Commit"}
	if got := cmd.UserFacingName(); got != "Git Commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "Git Commit")
	}

	// Falls back to Name when DisplayName is empty
	cmd2 := &SkillCommand{Name: "commit"}
	if got := cmd2.UserFacingName(); got != "commit" {
		t.Errorf("UserFacingName() = %q, want %q", got, "commit")
	}
}

func TestSkillCommand_MeetsAvailabilityRequirement(t *testing.T) {
	t.Parallel()

	// No availability restriction → passes
	cmd := &SkillCommand{}
	if !cmd.MeetsAvailabilityRequirement() {
		t.Error("skill with no availability should meet requirement")
	}

	// With availability restriction — gbot has no auth tiers, always passes
	cmd2 := &SkillCommand{Availability: []string{"claude-ai"}}
	if !cmd2.MeetsAvailabilityRequirement() {
		t.Error("gbot has no auth tiers, should always pass")
	}
}

func TestSkillSource_Constants(t *testing.T) {
	t.Parallel()

	sources := map[SkillSource]string{
		SkillSourceBundled: "bundled",
		SkillSourceUser:    "user",
		SkillSourceProject: "project",
		SkillSourceManaged: "managed",
		SkillSourceMCP:     "mcp",
		SkillSourcePlugin:  "plugin",
	}
	for src, want := range sources {
		if string(src) != want {
			t.Errorf("SkillSource %q = %q, want %q", src, string(src), want)
		}
	}
}

func TestSkillCommand_Defaults(t *testing.T) {
	t.Parallel()

	// Zero-value SkillCommand has IsUserInvocable=false (Go bool zero value).
	// This means IsHidden() returns true for uninitialized structs.
	// The parser sets IsUserInvocable=true by default when loading from frontmatter.
	cmd := &SkillCommand{}
	if !cmd.IsHidden() {
		t.Error("zero-value SkillCommand should be hidden (IsUserInvocable defaults to false)")
	}
}

func TestSkillCommand_Context(t *testing.T) {
	t.Parallel()

	// Empty string = inline (default)
	cmd := &SkillCommand{Context: ""}
	if cmd.Context != "" {
		t.Error("empty Context should mean inline")
	}

	// "fork" = fork execution
	cmd2 := &SkillCommand{Context: "fork"}
	if cmd2.Context != "fork" {
		t.Error("fork Context should be 'fork'")
	}
}

func TestCommandPermissionsAttachment(t *testing.T) {
	t.Parallel()

	att := CommandPermissionsAttachment{
		AllowedTools: []string{"Bash", "Read", "Write"},
		Model:        "haiku",
	}
	if len(att.AllowedTools) != 3 {
		t.Errorf("expected 3 allowed tools, got %d", len(att.AllowedTools))
	}
	if att.Model != "haiku" {
		t.Errorf("Model = %q, want %q", att.Model, "haiku")
	}
}

func TestInvokedSkillInfo(t *testing.T) {
	t.Parallel()

	info := InvokedSkillInfo{
		SkillName: "commit",
		SkillPath: "project:commit",
		Content:   "skill content here",
		AgentID:   "agent-1",
	}
	if info.SkillName != "commit" {
		t.Errorf("SkillName = %q, want %q", info.SkillName, "commit")
	}
	if info.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", info.AgentID, "agent-1")
	}
}
