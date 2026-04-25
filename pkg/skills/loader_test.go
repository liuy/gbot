package skills

import (
	"os"
	"os/exec"
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

// ---------------------------------------------------------------------------
// Additional coverage tests
// ---------------------------------------------------------------------------

func TestLoad_Integration(t *testing.T) {
	// Create project skill dirs
	projectDir := t.TempDir()
	skillDir := filepath.Join(projectDir, ".gbot", "skills", "proj-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Project skill\nuser-invocable: true\n---\nProject content."), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(projectDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	all := reg.GetAllSkills()
	found := false
	for _, s := range all {
		if s.Name == "proj-skill" {
			found = true
			if s.Description != "Project skill" {
				t.Errorf("proj-skill description = %q, want %q", s.Description, "Project skill")
			}
		}
	}
	if !found {
		t.Errorf("proj-skill not found in loaded skills: %v", skillNames(all))
	}
}

func TestLoad_SeparatesConditional(t *testing.T) {
	projectDir := t.TempDir()
	skillDir := filepath.Join(projectDir, ".gbot", "skills", "cond-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Conditional\npaths:\n  - \"*.go\"\n---\nConditional content."), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(projectDir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Conditional skill should NOT be in GetAllSkills
	all := reg.GetAllSkills()
	for _, s := range all {
		if s.Name == "cond-skill" {
			t.Error("conditional skill should not appear in GetAllSkills")
		}
	}

	// Should be in conditional map
	reg.mu.RLock()
	cond, ok := reg.conditional["cond-skill"]
	reg.mu.RUnlock()
	if !ok {
		t.Fatal("cond-skill should be in conditional map")
	}
	if len(cond.Paths) != 1 || cond.Paths[0] != "*.go" {
		t.Errorf("cond-skill paths = %v, want [*.go]", cond.Paths)
	}
}

func TestRegisterBundledSkill(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.RegisterBundledSkill(types.SkillCommand{
		Name: "bundled-skill",
		Description: "Bundled skill",
		Source: types.SkillSourceBundled,
		LoadedFrom: "bundled",
	})

	all := reg.GetAllSkills()
	if len(all) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(all))
	}
	if all[0].Name != "bundled-skill" {
		t.Errorf("skill name = %q, want %q", all[0].Name, "bundled-skill")
	}
}

func TestOnSkillsLoaded_CallbackAndUnsubscribe(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	callCount := 0
	cb := func() { callCount++ }

	unsub := reg.OnSkillsLoaded(cb)

	// Fire callbacks
	reg.fireOnSkillsLoaded()
	if callCount != 1 {
		t.Errorf("expected 1 callback, got %d", callCount)
	}

	// Unsubscribe
	unsub()

	// Fire again — should not call
	reg.fireOnSkillsLoaded()
	if callCount != 1 {
		t.Errorf("expected 1 callback after unsubscribe, got %d", callCount)
	}
}

func TestFireOnSkillsLoaded_PanicRecovery(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	panicked := false
	reg.OnSkillsLoaded(func() { panicked = true; panic("test panic") })
	called := false
	reg.OnSkillsLoaded(func() { called = true })

	// Should not panic even if first callback panics
	reg.fireOnSkillsLoaded()

	if !panicked {
		t.Error("first callback should have been called")
	}
	if !called {
		t.Error("second callback should still be called after first panics")
	}
}

func TestSortDirsDeepestFirst(t *testing.T) {
	t.Parallel()

	dirs := []string{
		"/a/b",
		"/a/b/c/d/e",
		"/a/b/c",
		"/a",
	}
	sortDirsDeepestFirst(dirs)

	if dirs[0] != "/a/b/c/d/e" {
		t.Errorf("first should be deepest, got %q", dirs[0])
	}
	if dirs[len(dirs)-1] != "/a" {
		t.Errorf("last should be shallowest, got %q", dirs[len(dirs)-1])
	}
}

func TestIsPathGitignored(t *testing.T) {
	// Create a git repo with a .gitignore
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")

	// Create .gitignore
	if err := os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte("ignored_file.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create the ignored file so git check-ignore can match it
	if err := os.WriteFile(filepath.Join(repoDir, "ignored_file.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", ".gitignore")
	runGit(t, repoDir, "commit", "-m", "init")

	if !isPathGitignored("ignored_file.txt", repoDir) {
		t.Error("ignored_file.txt should be gitignored")
	}
	if isPathGitignored("not_ignored.txt", repoDir) {
		t.Error("not_ignored.txt should not be gitignored")
	}
}

func TestIsPathGitignored_NotGitRepo(t *testing.T) {
	// Not a git repo — should return false (fail open)
	if isPathGitignored("anything", t.TempDir()) {
		t.Error("non-git repo should return false (fail open)")
	}
}

func TestLoadManagedSkills_DisabledEnv(t *testing.T) {
	t.Setenv("GBOT_DISABLE_POLICY_SKILLS", "1")

	reg := NewRegistry(t.TempDir())
	skills := reg.loadManagedSkills()
	if len(skills) != 0 {
		t.Errorf("should return empty when disabled, got %d", len(skills))
	}
}

func TestLoadManagedSkills_WithDir(t *testing.T) {
	managedDir := t.TempDir()
	t.Setenv("GBOT_MANAGED_SETTINGS_PATH", managedDir)

	// Create a managed skill
	skillDir := filepath.Join(managedDir, ".gbot", "skills", "admin-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Admin skill\n---\nAdmin content."), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	skills := reg.loadManagedSkills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 managed skill, got %d", len(skills))
	}
	if skills[0].Name != "admin-skill" {
		t.Errorf("skill name = %q, want %q", skills[0].Name, "admin-skill")
	}
	if skills[0].Source != types.SkillSourceManaged {
		t.Errorf("source = %v, want %v", skills[0].Source, types.SkillSourceManaged)
	}
}

func TestCollectProjectSkillDirs(t *testing.T) {
	cwd := t.TempDir()

	// Create .gbot/skills/ at cwd level
	cwdSkillDir := filepath.Join(cwd, ".gbot", "skills")
	if err := os.MkdirAll(cwdSkillDir, 0755); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(cwd)
	dirs := reg.collectProjectSkillDirs()

	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != cwdSkillDir {
		t.Errorf("dir = %q, want %q", dirs[0], cwdSkillDir)
	}
}

func TestManagedSkillsPath_DefaultLinux(t *testing.T) {
	// Clear override to test default
	t.Setenv("GBOT_MANAGED_SETTINGS_PATH", "")
	got := managedSkillsPath()
	// On Linux (test runner), should return /etc/gbot/skills
	want := "/etc/gbot/skills"
	if got != want {
		t.Errorf("managedSkillsPath() = %q, want %q", got, want)
	}
}

func TestFindSkill_ByAlias(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "commit", Aliases: []string{"ci"}, Description: "Commit", LoadedFrom: "skills", Type: "prompt"},
	}
	reg.mu.Unlock()

	found := reg.FindSkill("ci")
	if found == nil {
		t.Fatal("FindSkill(ci) should find commit via alias")
	}
	if found.Name != "commit" {
		t.Errorf("found.Name = %q, want %q", found.Name, "commit")
	}
}

func TestFindSkill_ByDisplayName(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "commit", DisplayName: "Git Commit", Description: "Commit", LoadedFrom: "skills", Type: "prompt"},
	}
	reg.mu.Unlock()

	found := reg.FindSkill("Git Commit")
	if found == nil {
		t.Fatal("FindSkill(Git Commit) should find skill by display name")
	}
}

func TestGetSkillToolSkills_FiltersDisableModelInvocation(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "visible", Description: "Visible", Type: "prompt", LoadedFrom: "skills"},
		{Name: "hidden", Description: "Hidden", Type: "prompt", LoadedFrom: "skills", DisableModelInvocation: true},
	}
	reg.mu.Unlock()

	filtered := reg.GetSkillToolSkills()
	if len(filtered) != 1 {
		t.Fatalf("expected 1 (disabled filtered), got %d", len(filtered))
	}
	if filtered[0].Name != "visible" {
		t.Errorf("filtered[0].Name = %q, want %q", filtered[0].Name, "visible")
	}
}

func TestGetSkillToolSkills_FiltersNonPromptType(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "prompt-skill", Description: "Prompt", Type: "prompt", LoadedFrom: "skills"},
		{Name: "other-skill", Description: "Other", Type: "other", LoadedFrom: "skills"},
	}
	reg.mu.Unlock()

	filtered := reg.GetSkillToolSkills()
	if len(filtered) != 1 {
		t.Fatalf("expected 1 (non-prompt filtered), got %d", len(filtered))
	}
	if filtered[0].Name != "prompt-skill" {
		t.Errorf("filtered[0].Name = %q, want %q", filtered[0].Name, "prompt-skill")
	}
}

func TestGetSkillToolSkills_RequiresDescOrWhenToUse(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	reg.mu.Lock()
	reg.skills = []types.SkillCommand{
		{Name: "no-desc-no-when", Type: "prompt", LoadedFrom: "other"},
		{Name: "has-when", Type: "prompt", LoadedFrom: "other", WhenToUse: "Use when needed"},
		{Name: "has-desc", Type: "prompt", LoadedFrom: "other", HasUserSpecifiedDesc: true},
	}
	reg.mu.Unlock()

	filtered := reg.GetSkillToolSkills()
	if len(filtered) != 2 {
		t.Fatalf("expected 2 (has-when + has-desc), got %d: %v", len(filtered), skillNames(filtered))
	}
}

// runGit executes a git command in the given directory.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func TestCollectProjectSkillDirs_WalksUpToParent(t *testing.T) {
	// Create nested structure: root/.gbot/skills/ at parent level
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	cwdSkillDir := filepath.Join(root, ".gbot", "skills")
	if err := os.MkdirAll(cwdSkillDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Registry cwd is deep inside the tree
	reg := NewRegistry(nested)
	dirs := reg.collectProjectSkillDirs()
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir from parent, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != cwdSkillDir {
		t.Errorf("dir = %q, want %q", dirs[0], cwdSkillDir)
	}
}

func TestCollectProjectSkillDirs_NoSkillDirs(t *testing.T) {
	cwd := t.TempDir()
	reg := NewRegistry(cwd)
	dirs := reg.collectProjectSkillDirs()
	if len(dirs) != 0 {
		t.Errorf("expected no dirs when none exist, got %d: %v", len(dirs), dirs)
	}
}

func TestLoadSkillsFromDir_SkillMDReadError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "unreadable")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create SKILL.md as a directory (not a file) — ReadFile will fail
	skillMD := filepath.Join(skillDir, "SKILL.md")
	if err := os.MkdirAll(skillMD, 0755); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	skills := reg.loadSkillsFromDir(dir, types.SkillSourceUser)
	if len(skills) != 0 {
		t.Errorf("unreadable SKILL.md should be skipped, got %d", len(skills))
	}
}

func TestLoadUserSkills_HomeDirExists(t *testing.T) {
	// Test that loadUserSkills doesn't panic when home dir exists
	reg := NewRegistry(t.TempDir())
	// This should succeed or return nil — either is fine
	skills := reg.loadUserSkills()
	// Just verify it doesn't panic
	_ = skills
}

func TestLoadBundledSkills(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	skills := reg.loadBundledSkills()
	if skills != nil {
		t.Errorf("loadBundledSkills should return nil, got %d", len(skills))
	}
}
