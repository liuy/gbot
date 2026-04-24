package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ResolveSkillNames
// ---------------------------------------------------------------------------

func TestResolveSkillNames_ExactMatch(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "commit", Content: "commit skill"},
		{ID: "review", Content: "review skill"},
		{ID: "plan", Content: "plan skill"},
	}
	result := ResolveSkillNames([]string{"commit", "plan"}, allSkills, "General")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
	if result[0].ID != "commit" {
		t.Errorf("first match ID = %q, want %q", result[0].ID, "commit")
	}
	if result[1].ID != "plan" {
		t.Errorf("second match ID = %q, want %q", result[1].ID, "plan")
	}
}

func TestResolveSkillNames_PluginPrefix(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "compound-engineering:review", Content: "review skill"},
		{ID: "commit", Content: "commit skill"},
	}
	// Agent type "compound-engineering:designer" → prefix "compound-engineering"
	// Looking for "review" → tries "compound-engineering:review" → match
	result := ResolveSkillNames([]string{"review"}, allSkills, "compound-engineering:designer")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].ID != "compound-engineering:review" {
		t.Errorf("match ID = %q, want %q", result[0].ID, "compound-engineering:review")
	}
}

func TestResolveSkillNames_SuffixMatch(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "plugin-a:deploy", Content: "deploy a"},
		{ID: "plugin-b:deploy", Content: "deploy b"},
	}
	// No exact match, no plugin prefix match for "General" → suffix match
	// First skill ending with ":deploy" wins
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "General")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].ID != "plugin-a:deploy" {
		t.Errorf("match ID = %q, want %q (first suffix match)", result[0].ID, "plugin-a:deploy")
	}
}

func TestResolveSkillNames_NoMatch(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "commit", Content: "commit skill"},
	}
	result := ResolveSkillNames([]string{"nonexistent"}, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("expected 0 matches for nonexistent, got %d", len(result))
	}
}

func TestResolveSkillNames_EmptyNames(t *testing.T) {
	allSkills := []SkillInfo{{ID: "commit", Content: "x"}}
	result := ResolveSkillNames(nil, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("expected 0 for nil names, got %d", len(result))
	}
}

func TestResolveSkillNames_PriorityExactOverPrefix(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "review", Content: "exact review"},
		{ID: "compound-engineering:review", Content: "plugin review"},
	}
	// "review" should match exact ID "review", not the plugin-prefixed one
	result := ResolveSkillNames([]string{"review"}, allSkills, "compound-engineering:agent")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].Content != "exact review" {
		t.Errorf("exact match should win over plugin prefix, got content %q", result[0].Content)
	}
}

func TestResolveSkillNames_PrefixOverSuffix(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "plugin-a:deploy", Content: "a deploy"},
		{ID: "myplugin:deploy", Content: "my deploy"},
	}
	// agentType "myplugin:agent" → prefix "myplugin" → "myplugin:deploy" matches via prefix
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "myplugin:agent")
	if len(result) != 1 {
		t.Fatalf("expected 1 match, got %d", len(result))
	}
	if result[0].ID != "myplugin:deploy" {
		t.Errorf("prefix match should win over suffix, got ID %q", result[0].ID)
	}
}

func TestResolveSkillNames_PartialMatchSkipped(t *testing.T) {
	allSkills := []SkillInfo{
		{ID: "deploy-prod", Content: "deploy prod"},
	}
	// "deploy" should NOT match "deploy-prod" (no ":" separator)
	result := ResolveSkillNames([]string{"deploy"}, allSkills, "General")
	if len(result) != 0 {
		t.Errorf("partial string match should not count, got %d matches", len(result))
	}
}

// ---------------------------------------------------------------------------
// LoadSkills
// ---------------------------------------------------------------------------

func TestLoadSkills_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	result := LoadSkills(tmpDir)
	if len(result) != 0 {
		t.Errorf("empty skills dir should return 0, got %d", len(result))
	}
}

func TestLoadSkills_OnlyMDFiles(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".gbot", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(skillsDir, "commit.md"), []byte("commit skill content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "review.md"), []byte("review skill content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "notaskill.txt"), []byte("not a skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "README.md"), []byte("# Skills"), 0644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(dir)
	if len(result) != 3 {
		t.Fatalf("expected 3 .md files, got %d", len(result))
	}

	// Verify content of specific skills
	found := make(map[string]string)
	for _, si := range result {
		found[si.ID] = si.Content
	}
	if found["commit"] != "commit skill content" {
		t.Errorf("commit content = %q, want %q", found["commit"], "commit skill content")
	}
	if found["review"] != "review skill content" {
		t.Errorf("review content = %q, want %q", found["review"], "review skill content")
	}
	if found["README"] != "# Skills" {
		t.Errorf("README content = %q, want %q", found["README"], "# Skills")
	}
	if _, ok := found["notaskill"]; ok {
		t.Error("non-.md files should not be loaded")
	}
}

func TestLoadSkills_LocalOverridesGlobal(t *testing.T) {
	// Create global skills dir
	homeDir := t.TempDir()
	globalDir := filepath.Join(homeDir, ".gbot", "skills")
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "commit.md"), []byte("global commit"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create local skills dir
	localDir := t.TempDir()
	localSkillsDir := filepath.Join(localDir, ".gbot", "skills")
	if err := os.MkdirAll(localSkillsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localSkillsDir, "commit.md"), []byte("local commit"), 0644); err != nil {
		t.Fatal(err)
	}

	// Override HOME to control global skills path
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", homeDir)
	defer func() { _ = os.Setenv("HOME", origHome) }()

	result := LoadSkills(localDir)
	if len(result) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(result))
	}
	if result[0].Content != "local commit" {
		t.Errorf("local should override global, got content %q", result[0].Content)
	}
}

func TestLoadSkills_NestedDirIgnored(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, ".gbot", "skills")
	if err := os.MkdirAll(filepath.Join(skillsDir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "nested", "deep.md"), []byte("deep skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "top.md"), []byte("top skill"), 0644); err != nil {
		t.Fatal(err)
	}

	result := LoadSkills(dir)
	if len(result) != 1 {
		t.Fatalf("expected 1 (nested dirs ignored), got %d", len(result))
	}
	if result[0].ID != "top" {
		t.Errorf("ID = %q, want %q", result[0].ID, "top")
	}
}

func TestLoadSkills_NoDirExists(t *testing.T) {
	// Temp dir with no .gbot/skills subdirectory — should not error
	tmpDir := t.TempDir()
	result := LoadSkills(tmpDir)
	if len(result) != 0 {
		t.Errorf("nonexistent skills dir should return 0, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// BuildSkillMessages
// ---------------------------------------------------------------------------

func TestBuildSkillMessages_Empty(t *testing.T) {
	result := BuildSkillMessages(nil)
	if result != nil {
		t.Errorf("nil skills should return nil, got %d messages", len(result))
	}
}

func TestBuildSkillMessages_Single(t *testing.T) {
	skills := []SkillInfo{
		{ID: "commit", Content: "commit skill body"},
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
	skills := []SkillInfo{
		{ID: "commit", Content: "commit content"},
		{ID: "review", Content: "review content"},
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
