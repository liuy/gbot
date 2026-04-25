package agent

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ResolveSkillNames
// ---------------------------------------------------------------------------

func TestResolveSkillNames_ExactMatch(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "commit", Content: "commit skill"},
		{Name: "review", Content: "review skill"},
		{Name: "plan", Content: "plan skill"},
	}
	result := ResolveSkillNames([]string{"commit", "plan"}, allSkills, "General")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
	if result[0].Name != "commit" {
		t.Errorf("first match Name = %q, want %q", result[0].Name, "commit")
	}
	if result[1].Name != "plan" {
		t.Errorf("second match Name = %q, want %q", result[1].Name, "plan")
	}
}

func TestResolveSkillNames_PluginPrefix(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "compound-engineering:review", Content: "review skill"},
		{Name: "commit", Content: "commit skill"},
	}
	// Agent type "compound-engineering:designer" → prefix "compound-engineering"
	// Looking for "review" → tries "compound-engineering:review" → match
	result := ResolveSkillNames([]string{"review"}, allSkills, "compound-engineering:designer")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Name != "compound-engineering:review" {
		t.Errorf("match Name = %q, want %q", result[0].Name, "compound-engineering:review")
	}
}

func TestResolveSkillNames_SuffixMatch(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "plugin-a:deploy", Content: "deploy a"},
		{Name: "plugin-b:deploy", Content: "deploy b"},
	}
	// No exact match, no plugin prefix match for "General" → suffix match
	// First skill ending with ":deploy" wins
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "General")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Name != "plugin-a:deploy" {
		t.Errorf("match Name = %q, want %q (first suffix match)", result[0].Name, "plugin-a:deploy")
	}
}

func TestResolveSkillNames_NoMatch(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "commit", Content: "commit skill"},
	}
	result := ResolveSkillNames([]string{"nonexistent"}, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("expected 0 matches for nonexistent, got %d", len(result))
	}
}

func TestResolveSkillNames_EmptyNames(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{{Name: "commit", Content: "x"}}
	result := ResolveSkillNames(nil, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("expected 0 for nil names, got %d", len(result))
	}
}

func TestResolveSkillNames_PriorityExactOverPrefix(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "review", Content: "exact review"},
		{Name: "compound-engineering:review", Content: "plugin review"},
	}
	// "review" should match exact Name "review", not the plugin-prefixed one
	result := ResolveSkillNames([]string{"review"}, allSkills, "compound-engineering:agent")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Content != "exact review" {
		t.Errorf("exact match should win over plugin prefix, got content %q", result[0].Content)
	}
}

func TestResolveSkillNames_PrefixOverSuffix(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "plugin-a:deploy", Content: "a deploy"},
		{Name: "myplugin:deploy", Content: "my deploy"},
	}
	// agentType "myplugin:agent" → prefix "myplugin" → "myplugin:deploy" matches via prefix
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "myplugin:agent")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Name != "myplugin:deploy" {
		t.Errorf("prefix match should win over suffix, got Name %q", result[0].Name)
	}
}

func TestResolveSkillNames_PartialMatchSkipped(t *testing.T) {
	t.Parallel()
	allSkills := []types.SkillCommand{
		{Name: "deploy-prod", Content: "deploy prod"},
	}
	// "deploy" should NOT match "deploy-prod" (no ":" separator)
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("partial string match should not count, got %d matches", len(result))
	}
}

// ---------------------------------------------------------------------------
// BuildSkillMessages
// ---------------------------------------------------------------------------

func TestBuildSkillMessages_Empty(t *testing.T) {
	t.Parallel()
	result := BuildSkillMessages(nil)
	if result != nil {
		t.Errorf("nil skills should return nil, got %d messages", len(result))
	}
}

func TestBuildSkillMessages_Single(t *testing.T) {
	t.Parallel()
	skills := []types.SkillCommand{
		{Name: "commit", Content: "commit skill body"},
	}
	result := BuildSkillMessages(skills)
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	msg := result[0]
	if msg.Role != types.RoleUser {
		t.Errorf("Role = %q, want %q", msg.Role, types.RoleUser)
	}
	text := msg.Content[0].Text
	if !strings.Contains(text, "<command-message>commit</command-message>") {
		t.Errorf("should contain command-message tag, got %q", text)
	}
	if !strings.Contains(text, "<command-name>commit</command-name>") {
		t.Errorf("should contain command-name tag, got %q", text)
	}
	if !strings.Contains(text, "<skill-format>true</skill-format>") {
		t.Errorf("should contain skill-format tag, got %q", text)
	}
	if !strings.Contains(text, "commit skill body") {
		t.Errorf("should contain skill content, got %q", text)
	}
}

func TestBuildSkillMessages_Multiple(t *testing.T) {
	t.Parallel()
	skills := []types.SkillCommand{
		{Name: "commit", Content: "commit content"},
		{Name: "review", Content: "review content"},
	}
	result := BuildSkillMessages(skills)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	// Each message should have its own skill content
	if !strings.Contains(result[0].Content[0].Text, "commit content") {
		t.Errorf("first message should contain commit content, got %q", result[0].Content[0].Text)
	}
	if !strings.Contains(result[1].Content[0].Text, "review content") {
		t.Errorf("second message should contain review content, got %q", result[1].Content[0].Text)
	}
}
