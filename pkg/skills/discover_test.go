package skills

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Dynamic discovery tests
// ---------------------------------------------------------------------------

func TestMatchesPatterns_DirectMatch(t *testing.T) {
	t.Parallel()

	if !matchesPatterns("src/main.go", []string{"src/*.go"}) {
		t.Error("should match src/*.go against src/main.go")
	}
}

func TestMatchesPatterns_PrefixMatch(t *testing.T) {
	t.Parallel()

	if !matchesPatterns("src/deep/file.go", []string{"src"}) {
		t.Error("should match prefix 'src' against 'src/deep/file.go'")
	}
}

func TestMatchesPatterns_FilenameMatch(t *testing.T) {
	t.Parallel()

	// Pattern without "/" should match just the filename
	if !matchesPatterns("any/path/test.go", []string{"*.go"}) {
		t.Error("should match *.go against filename test.go")
	}
}

func TestMatchesPatterns_NoMatch(t *testing.T) {
	t.Parallel()

	if matchesPatterns("src/main.rs", []string{"*.go", "docs"}) {
		t.Error("should not match *.go or docs against src/main.rs")
	}
}

func TestMatchesPatterns_EmptyPatterns(t *testing.T) {
	t.Parallel()

	if matchesPatterns("anything", nil) {
		t.Error("empty patterns should not match")
	}
}

func TestDiscoverSkillDirsForPaths_FindsNewDir(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	// Create .gbot/skills/ inside a subdirectory
	skillDir := filepath.Join(cwd, "src", ".gbot", "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(cwd)
	filePath := filepath.Join(cwd, "src", "main.go")

	dirs := reg.DiscoverSkillDirsForPaths([]string{filePath})
	if len(dirs) != 1 {
		t.Fatalf("expected 1 discovered dir, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != skillDir {
		t.Errorf("dir = %q, want %q", dirs[0], skillDir)
	}
}

func TestDiscoverSkillDirsForPaths_NegativeCache(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	reg := NewRegistry(cwd)

	// First call — no .gbot/skills/ exists
	filePath := filepath.Join(cwd, "src", "main.go")
	dirs := reg.DiscoverSkillDirsForPaths([]string{filePath})
	if len(dirs) != 0 {
		t.Errorf("expected 0 dirs on first call, got %d", len(dirs))
	}

	// Create the dir AFTER first call
	skillDir := filepath.Join(cwd, "src", ".gbot", "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Second call — should still return 0 because of negative cache
	dirs = reg.DiscoverSkillDirsForPaths([]string{filePath})
	if len(dirs) != 0 {
		t.Errorf("negative cache should prevent re-discovery, got %d dirs", len(dirs))
	}
}

func TestDiscoverSkillDirsForPaths_SortsDeepestFirst(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	// Create two .gbot/skills/ at different depths
	deepDir := filepath.Join(cwd, "src", "pkg", ".gbot", "skills")
	shallowDir := filepath.Join(cwd, "src", ".gbot", "skills")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shallowDir, 0755); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(cwd)
	deepFile := filepath.Join(cwd, "src", "pkg", "util.go")

	dirs := reg.DiscoverSkillDirsForPaths([]string{deepFile})
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
	// Deepest first
	if dirs[0] != deepDir {
		t.Errorf("first dir should be deepest %q, got %q", deepDir, dirs[0])
	}
	if dirs[1] != shallowDir {
		t.Errorf("second dir should be shallower %q, got %q", shallowDir, dirs[1])
	}
}

func TestDiscoverSkillDirsForPaths_StopsAtCwd(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	// Create .gbot/skills/ AT cwd level — should NOT be discovered (already loaded at startup)
	cwdSkillDir := filepath.Join(cwd, ".gbot", "skills")
	if err := os.MkdirAll(cwdSkillDir, 0755); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(cwd)
	filePath := filepath.Join(cwd, "main.go")

	dirs := reg.DiscoverSkillDirsForPaths([]string{filePath})
	// Should not discover cwd's own skills dir
	if len(dirs) != 0 {
		t.Errorf("should not discover cwd-level dir (already loaded), got %d: %v", len(dirs), dirs)
	}
}

func TestAddSkillDirectories_Empty(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	err := reg.AddSkillDirectories(nil)
	if err != nil {
		t.Errorf("empty dirs should succeed, got %v", err)
	}
}

func TestAddSkillDirectories_LoadsSkills(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Create a skill in the dir
	skillDir := filepath.Join(dir, "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\ndescription: Test\n---\nTest content."), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	err := reg.AddSkillDirectories([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check dynamic skills
	reg.mu.RLock()
	dynamic := reg.dynamicSkills
	reg.mu.RUnlock()
	if len(dynamic) != 1 {
		t.Fatalf("expected 1 dynamic skill, got %d", len(dynamic))
	}
	if _, ok := dynamic["test-skill"]; !ok {
		t.Error("expected 'test-skill' in dynamic skills")
	}
}

func TestAddSkillDirectories_TriggersCallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "cb-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\n---\nBody."), 0644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry(t.TempDir())
	called := false
	reg.OnSkillsLoaded(func() {
		called = true
	})

	err := reg.AddSkillDirectories([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("OnSkillsLoaded callback should have been called")
	}
}

func TestActivateConditionalSkillsForPaths_NoConditionals(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	result := reg.ActivateConditionalSkillsForPaths([]string{"some/file.go"})
	if result != nil {
		t.Errorf("no conditionals should return nil, got %v", result)
	}
}

func TestActivateConditionalSkillsForPaths_Matches(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	reg := NewRegistry(cwd)

	// Set up a conditional skill with paths
	reg.mu.Lock()
	reg.conditional["go-skill"] = types.SkillCommand{
		Name:   "go-skill",
		Paths:  []string{"*.go"},
		Source: types.SkillSourceProject,
	}
	reg.mu.Unlock()

	activated := reg.ActivateConditionalSkillsForPaths([]string{filepath.Join(cwd, "main.go")})
	if len(activated) != 1 {
		t.Fatalf("expected 1 activated skill, got %d: %v", len(activated), activated)
	}
	if activated[0] != "go-skill" {
		t.Errorf("activated[0] = %q, want %q", activated[0], "go-skill")
	}

	// Verify moved to dynamicSkills
	reg.mu.RLock()
	_, inDynamic := reg.dynamicSkills["go-skill"]
	_, inConditional := reg.conditional["go-skill"]
	reg.mu.RUnlock()
	if !inDynamic {
		t.Error("go-skill should be in dynamicSkills after activation")
	}
	if inConditional {
		t.Error("go-skill should be removed from conditional after activation")
	}
}

func TestActivateConditionalSkillsForPaths_OutsideCwd(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	reg := NewRegistry(cwd)

	reg.mu.Lock()
	reg.conditional["ext-skill"] = types.SkillCommand{
		Name:  "ext-skill",
		Paths: []string{"*.go"},
	}
	reg.mu.Unlock()

	// Path outside cwd should not match
	outsidePath := filepath.Join(t.TempDir(), "other.go")
	activated := reg.ActivateConditionalSkillsForPaths([]string{outsidePath})
	if len(activated) != 0 {
		t.Errorf("path outside cwd should not activate, got %d", len(activated))
	}
}

func TestActivateConditionalSkillsForPaths_NoMatch(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	reg := NewRegistry(cwd)

	reg.mu.Lock()
	reg.conditional["go-skill"] = types.SkillCommand{
		Name:  "go-skill",
		Paths: []string{"*.go"},
	}
	reg.mu.Unlock()

	// Non-matching extension
	activated := reg.ActivateConditionalSkillsForPaths([]string{filepath.Join(cwd, "main.rs")})
	if len(activated) != 0 {
		t.Errorf("non-matching file should not activate, got %d", len(activated))
	}
}

func TestDiscoverSkillDirsForPaths_Gitignored(t *testing.T) {
	cwd := t.TempDir()

	// Set up git repo
	runGitCmd(t, cwd, "init")
	runGitCmd(t, cwd, "config", "user.email", "test@test.com")
	runGitCmd(t, cwd, "config", "user.name", "Test")

	// Create a .gitignore that ignores the 'build' directory
	if err := os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("build/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create .gbot/skills/ inside the gitignored build dir
	skillDir := filepath.Join(cwd, "build", ".gbot", "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, cwd, "add", ".gitignore")
	runGitCmd(t, cwd, "commit", "-m", "init")

	reg := NewRegistry(cwd)
	filePath := filepath.Join(cwd, "build", "main.go")

	dirs := reg.DiscoverSkillDirsForPaths([]string{filePath})
	// The gitignored dir should be skipped
	for _, d := range dirs {
		if d == skillDir {
			t.Errorf("gitignored .gbot/skills/ should not be discovered: %q", d)
		}
	}
}

func TestDiscoverSkillDirsForPaths_EmptyFilePaths(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(t.TempDir())
	dirs := reg.DiscoverSkillDirsForPaths(nil)
	if len(dirs) != 0 {
		t.Errorf("empty paths should return empty, got %d", len(dirs))
	}
}

// runGitCmd executes a git command in the given directory.
func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

// ---------------------------------------------------------------------------
// Walk-up cache, gitignore skip, activation guards
// ---------------------------------------------------------------------------

func TestDiscoverSkillDirsForPaths_CacheHitKeepsWalkingUp(t *testing.T) {
	cwd := t.TempDir()
	deep := filepath.Join(cwd, "a", "b")
	if err := os.MkdirAll(filepath.Join(cwd, "a", ".gbot", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	reg := NewRegistry(cwd)
	reg.dynamicDirCache[filepath.Join(deep, ".gbot", "skills")] = true
	got := reg.DiscoverSkillDirsForPaths([]string{filepath.Join(deep, "f.go")})
	want := []string{filepath.Join(cwd, "a", ".gbot", "skills")}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("new dirs = %v, want %v (a cached miss must not stop the walk)", got, want)
	}
	if !reg.dynamicDirCache[want[0]] {
		t.Error("the visited skill dir was not recorded in the negative cache")
	}
}

func TestDiscoverSkillDirsForPaths_GitignoredDirDoesNotStopTheWalk(t *testing.T) {
	logs := captureSkillsLogs(t)
	cwd := t.TempDir()
	runGitCmd(t, cwd, "init", "-q")
	if err := os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("sub/ignored\n"), 0o644); err != nil {
		t.Fatalf("gitignore: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "sub", "ignored", ".gbot", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "sub", ".gbot", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	reg := NewRegistry(cwd)
	got := reg.DiscoverSkillDirsForPaths([]string{filepath.Join(cwd, "sub", "ignored", "f.go")})
	want := []string{filepath.Join(cwd, "sub", ".gbot", "skills")}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("new dirs = %v, want %v (the gitignored dir must not end the walk)", got, want)
	}
	recs := logs.find("skills: skipped gitignored dir")
	if len(recs) != 1 {
		t.Fatalf("gitignore skip logged %d times, want 1", len(recs))
	}
	if recs[0].attrs["dir"] != filepath.Join(cwd, "sub", "ignored", ".gbot", "skills") {
		t.Errorf("logged dir = %v, want the ignored skill dir", recs[0].attrs["dir"])
	}
}

func TestAddSkillDirectories_EmptyLoadStaysSilent(t *testing.T) {
	logs := captureSkillsLogs(t)
	cwd := t.TempDir()
	reg := NewRegistry(cwd)
	var fired int
	reg.OnSkillsLoaded(func() { fired++ })
	if err := reg.AddSkillDirectories([]string{filepath.Join(cwd, "missing")}); err != nil {
		t.Fatalf("AddSkillDirectories: %v", err)
	}
	if fired != 0 {
		t.Errorf("onSkillsLoaded fired %d times with no skills loaded, want 0", fired)
	}
	if got := logs.find("skills: dynamically discovered"); len(got) != 0 {
		t.Errorf("empty discovery logged %d times, want 0", len(got))
	}
}

func TestAddSkillDirectories_LogsAndFiresOnce(t *testing.T) {
	logs := captureSkillsLogs(t)
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".gbot", "skills")
	if err := os.MkdirAll(filepath.Join(dir, "disc"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\ndescription: discovered skill\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "disc", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	reg := NewRegistry(cwd)
	var fired int
	reg.OnSkillsLoaded(func() { fired++ })
	if err := reg.AddSkillDirectories([]string{dir}); err != nil {
		t.Fatalf("AddSkillDirectories: %v", err)
	}
	if fired != 1 {
		t.Errorf("onSkillsLoaded fired %d times, want 1", fired)
	}
	recs := logs.find("skills: dynamically discovered")
	if len(recs) != 1 {
		t.Fatalf("discovery logged %d times, want 1", len(recs))
	}
	if n, _ := recs[0].attrs["count"].(int64); n != 1 {
		t.Errorf("count attr = %v, want int64(1)", recs[0].attrs["count"])
	}
	if n, _ := recs[0].attrs["dirs"].(int64); n != 1 {
		t.Errorf("dirs attr = %v, want int64(1)", recs[0].attrs["dirs"])
	}
}

func TestActivateConditionalSkillsForPaths_Guards(t *testing.T) {
	cwd := t.TempDir()
	newReg := func() *Registry {
		reg := NewRegistry(cwd)
		reg.mu.Lock()
		// A pathless conditional must not stop the scan of later skills.
		reg.conditional["pathless"] = types.SkillCommand{Name: "pathless", Source: types.SkillSourceProject}
		reg.conditional["go-skill"] = types.SkillCommand{Name: "go-skill", Paths: []string{"*.go"}}
		reg.mu.Unlock()
		return reg
	}

	// Empty file path: nothing can match an empty relative path, not even "*".
	catchAll := func() *Registry {
		reg := NewRegistry(cwd)
		reg.mu.Lock()
		reg.conditional["catch-all"] = types.SkillCommand{Name: "catch-all", Paths: []string{"*"}}
		reg.mu.Unlock()
		return reg
	}
	if got := catchAll().ActivateConditionalSkillsForPaths([]string{""}); len(got) != 0 {
		t.Errorf("activation for an empty path = %v, want none", got)
	}
	// Path outside cwd must be rejected before pattern matching.
	if got := newReg().ActivateConditionalSkillsForPaths([]string{"../outside/main.go"}); len(got) != 0 {
		t.Errorf("activation for a path outside cwd = %v, want none", got)
	}
	// A rejected path must not end the scan of the remaining paths.
	if got := newReg().ActivateConditionalSkillsForPaths([]string{"../outside/main.go", filepath.Join(cwd, "a.go")}); len(got) != 1 || got[0] != "go-skill" {
		t.Errorf("activation with a leading out-of-cwd path = %v, want [go-skill]", got)
	}
	// One matching path activates each skill exactly once.
	reg := newReg()
	got := reg.ActivateConditionalSkillsForPaths([]string{filepath.Join(cwd, "a.go"), filepath.Join(cwd, "b.go")})
	if len(got) != 1 || got[0] != "go-skill" {
		t.Fatalf("activated = %v, want [go-skill] exactly once", got)
	}
	if !reg.activatedNames["go-skill"] {
		t.Error("activatedNames was not marked for go-skill")
	}
	if reg.activatedNames["pathless"] {
		t.Error("pathless conditional was marked activated")
	}
}

func TestActivateConditionalSkillsForPaths_CallbackOnlyOnActivation(t *testing.T) {
	cwd := t.TempDir()
	fired := make(chan struct{}, 4)
	newReg := func() *Registry {
		reg := NewRegistry(cwd)
		reg.mu.Lock()
		reg.conditional["go-skill"] = types.SkillCommand{Name: "go-skill", Paths: []string{"*.go"}}
		reg.mu.Unlock()
		reg.OnSkillsLoaded(func() { fired <- struct{}{} })
		return reg
	}
	reg := newReg()
	if got := reg.ActivateConditionalSkillsForPaths([]string{filepath.Join(cwd, "main.rs")}); len(got) != 0 {
		t.Fatalf("activated = %v, want none", got)
	}
	select {
	case <-fired:
		t.Error("onSkillsLoaded fired with nothing activated")
	case <-time.After(200 * time.Millisecond):
	}
	reg2 := newReg()
	if got := reg2.ActivateConditionalSkillsForPaths([]string{filepath.Join(cwd, "main.go")}); len(got) != 1 {
		t.Fatalf("activated = %v, want [go-skill]", got)
	}
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Error("onSkillsLoaded did not fire after an activation")
	}
}
