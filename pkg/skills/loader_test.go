package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Loader tests — discovery, dedup, FindSkill, GetSkillToolSkills
// ---------------------------------------------------------------------------

// setupSkillDir creates a temporary skill directory structure for testing.
func setupSkillDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create skill-name/SKILL.md structure
	skills := map[string]string{
		"commit/SKILL.md": "---\ndescription: Git commit\nuser-invocable: true\n---\nCreate a commit.",
		"review/SKILL.md": "---\ndescription: Code review\nuser-invocable: true\n---\nReview code.",
		"hidden/SKILL.md": "---\ndescription: Agent only\nuser-invocable: false\n---\nInternal skill.",
		"nodesc/SKILL.md": "---\n---\nNo description skill.",
	}

	for name, content := range skills {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a file that should be skipped (not a directory)
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("readme"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadSkillsFromDir(t *testing.T) {
	t.Parallel()
	dir := setupSkillDir(t)

	reg := NewRegistry(t.TempDir())
	skills := reg.loadSkillsFromDir(dir, types.SkillSourceUser)

	if len(skills) != 4 {
		t.Fatalf("expected 4 skills, got %d: %v", len(skills), skillNames(skills))
	}

	// Verify names
	names := make(map[string]bool)
	for _, s := range skills {
		names[s.Name] = true
	}
	for _, want := range []string{"commit", "review", "hidden", "nodesc"} {
		if !names[want] {
			t.Errorf("missing skill %q", want)
		}
	}
}

func TestLoadSkillsFromDir_SkipsFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a flat .md file (not a directory) — should be skipped
	if err := os.WriteFile(filepath.Join(dir, "flat.md"), []byte("---\n---\nflat"), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	skills := reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	if len(skills) != 0 {
		t.Errorf("flat .md files should be skipped, got %d skills", len(skills))
	}
}

func TestLoadSkillsFromDir_NonexistentDir(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	skills := reg.loadSkillsFromDir("/nonexistent/path", types.SkillSourceUser)
	if len(skills) != 0 {
		t.Errorf("nonexistent dir should return empty, got %d", len(skills))
	}
}

func TestLoadSkillsFromDir_OversizedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "big")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	bigContent := "---\nname: big\n---\n" + string(make([]byte, maxFrontmatterFileSize+1))
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	skills := reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	if len(skills) != 0 {
		t.Errorf("oversized file should be skipped, got %d skills", len(skills))
	}
}

func TestRegistry_FindSkill(t *testing.T) {
	t.Parallel()

	dir := setupSkillDir(t)
	reg := NewRegistry(t.TempDir())
	reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	// Manually set skills (bypass Load which requires full setup)
	reg.mu.Lock()
	skills := reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	reg.skills = skills
	reg.mu.Unlock()

	// Find by name
	found := reg.FindSkill("commit")
	if found == nil {
		t.Fatal("FindSkill(commit) should find the skill")
	}
	if found.Description != "Git commit" {
		t.Errorf("found.Description = %q, want %q", found.Description, "Git commit")
	}

	// Not found
	if reg.FindSkill("nonexistent") != nil {
		t.Error("FindSkill(nonexistent) should return nil")
	}
}

func TestRegistry_HasSkill(t *testing.T) {
	t.Parallel()

	dir := setupSkillDir(t)
	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	reg.mu.Unlock()

	if !reg.HasSkill("commit") {
		t.Error("HasSkill(commit) should return true")
	}
	if reg.HasSkill("nonexistent") {
		t.Error("HasSkill(nonexistent) should return false")
	}
}

func TestRegistry_GetSkillToolSkills(t *testing.T) {
	t.Parallel()

	dir := setupSkillDir(t)
	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	reg.mu.Unlock()

	filtered := reg.GetSkillToolSkills()

	// "hidden" has disableModelInvocation=false and user-invocable=false but source=user
	// which means loadedFrom="skills", so it passes the filter
	// "nodesc" has no description but loadedFrom="skills", so it passes
	// All skills in this test have loadedFrom="skills" so all should pass
	if len(filtered) != 4 {
		t.Errorf("expected all 4 skills to pass filter (loadedFrom='skills'), got %d", len(filtered))
	}
}

func TestDeduplicateSkills(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())

	skill1 := types.SkillCommand{Name: "a", SourcePath: "/path/a/SKILL.md"}
	skill2 := types.SkillCommand{Name: "b", SourcePath: "/path/b/SKILL.md"}
	skill3 := types.SkillCommand{Name: "a2", SourcePath: "/path/a/SKILL.md"} // same file as skill1

	result := reg.deduplicateSkills([]types.SkillCommand{skill1, skill2, skill3})
	if len(result) != 2 {
		t.Errorf("expected 2 unique skills (first-wins), got %d", len(result))
	}
	if result[0].Name != "a" {
		t.Errorf("first skill should be 'a', got %q", result[0].Name)
	}
	if result[1].Name != "b" {
		t.Errorf("second skill should be 'b', got %q", result[1].Name)
	}
}

func TestRegistry_GetAllSkills_WithDynamic(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "static", Source: types.SkillSourceUser, LoadedFrom: "skills", Type: "prompt"},
	}
	reg.dynamicSkills = map[string]types.SkillCommand{
		"dynamic": {Name: "dynamic", Source: types.SkillSourceProject, LoadedFrom: "skills", Type: "prompt"},
	}
	reg.mu.Unlock()

	all := reg.GetAllSkills()
	if len(all) != 2 {
		t.Fatalf("expected 2 skills (static + dynamic), got %d", len(all))
	}
	names := make(map[string]bool)
	for _, s := range all {
		names[s.Name] = true
	}
	if !names["static"] || !names["dynamic"] {
		t.Errorf("missing skills: static=%v dynamic=%v", names["static"], names["dynamic"])
	}
}

func TestManagedSkillsPath(t *testing.T) {

	// Test override
	t.Setenv("GBOT_MANAGED_SETTINGS_PATH", "/custom/path")
	if got := managedSkillsPath(); got != "/custom/path/.gbot/skills" {
		t.Errorf("override: got %q, want %q", got, "/custom/path/.gbot/skills")
	}
}

// skillNames returns a slice of skill names for debugging.
func skillNames(skills []types.SkillCommand) []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}
