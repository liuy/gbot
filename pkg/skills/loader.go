package skills

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Skill Registry — main discovery engine
// Source: src/skills/loadSkillsDir.ts — getSkillDirCommands + loadSkillsDir
// ---------------------------------------------------------------------------

// Registry manages skill discovery, loading, and lookup.
type Registry struct {
	mu  sync.RWMutex
	cwd string

	// Loaded skills
	skills []types.SkillCommand

	// Conditional skills (with paths frontmatter, awaiting activation)
	conditional map[string]types.SkillCommand

	// Dynamic skills (discovered at runtime)
	dynamicSkills map[string]types.SkillCommand

	// Negative cache for dynamic discovery (checked paths)
	dynamicDirCache map[string]bool

	// Per-session activation memory
	activatedNames map[string]bool

	// Invoked skills for compaction protection
	invokedSkills map[string]types.InvokedSkillInfo

	// Callbacks for skill change notifications
	onSkillsLoadedCallbacks []func()
}

// NewRegistry creates a new skill registry.
func NewRegistry(cwd string) *Registry {
	return &Registry{
		cwd:             cwd,
		conditional:     make(map[string]types.SkillCommand),
		dynamicSkills:   make(map[string]types.SkillCommand),
		dynamicDirCache: make(map[string]bool),
		activatedNames:  make(map[string]bool),
		invokedSkills:   make(map[string]types.InvokedSkillInfo),
	}
}

// Load discovers and loads skills from all sources.
// Source: loadSkillsDir.ts:638-803 — getSkillDirCommands
func (r *Registry) Load() error {
	var allSkills []types.SkillCommand

	// Source 1: Bundled skills (embed.FS)
	// TS: bundledSkills — registered via registerBundledSkill
	bundled := r.loadBundledSkills()
	allSkills = append(allSkills, bundled...)

	// Source 2: Managed skills (policy)
	// TS: managedSkills — loadSkillsDir.ts:686-688
	managed := r.loadManagedSkills()
	allSkills = append(allSkills, managed...)

	// Source 3: User skills (~/.gbot/skills/)
	// TS: userSkills — loadSkillsDir.ts:689-691
	user := r.loadUserSkills()
	allSkills = append(user, allSkills...)

	// Source 4: Project skills (walk to git root)
	// TS: projectSkillsNested — loadSkillsDir.ts:693-697
	project := r.loadProjectSkills()
	allSkills = append(project, allSkills...)

	// Deduplicate by resolved file path (first-wins)
	// Source: loadSkillsDir.ts:725-763
	allSkills = r.deduplicateSkills(allSkills)

	// Separate conditional skills (with paths frontmatter) from unconditional
	// Source: loadSkillsDir.ts:771-785
	var unconditional []types.SkillCommand
	for _, skill := range allSkills {
		if len(skill.Paths) > 0 && !r.activatedNames[skill.Name] {
			r.conditional[skill.Name] = skill
		} else {
			unconditional = append(unconditional, skill)
		}
	}

	r.mu.Lock()
	r.skills = unconditional
	r.mu.Unlock()

	slog.Info("skills: loaded",
		"total", len(unconditional),
		"conditional", len(r.conditional),
		"bundled", len(bundled),
		"managed", len(managed),
		"user", len(user),
		"project", len(project),
	)
	return nil
}

// GetAllSkills returns all loaded skills (unconditional only).
func (r *Registry) GetAllSkills() []types.SkillCommand {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Merge static + dynamic skills
	result := make([]types.SkillCommand, len(r.skills))
	copy(result, r.skills)
	for _, s := range r.dynamicSkills {
		result = append(result, s)
	}
	return result
}

// GetSkillToolSkills returns skills visible to the SkillTool.
// Source: commands.ts:563-581 — getSkillToolCommands filter
func (r *Registry) GetSkillToolSkills() []types.SkillCommand {
	all := r.GetAllSkills()

	var result []types.SkillCommand
	for _, cmd := range all {
		// Filter: type == "prompt"
		if cmd.Type != "prompt" {
			continue
		}
		// Filter: !DisableModelInvocation
		if cmd.DisableModelInvocation {
			continue
		}
		// Filter: source != "builtin" (gbot doesn't have builtin source)
		// Filter: loadedFrom == "bundled" || "skills" || hasUserSpecifiedDesc || whenToUse
		if cmd.LoadedFrom == "bundled" || cmd.LoadedFrom == "skills" {
			result = append(result, cmd)
			continue
		}
		if cmd.HasUserSpecifiedDesc || cmd.WhenToUse != "" {
			result = append(result, cmd)
		}
	}
	return result
}

// FindSkill finds a skill by name, alias, or display name (first match).
// Source: commands.ts:688-698 — findCommand
func (r *Registry) FindSkill(name string) *types.SkillCommand {
	all := r.GetAllSkills()
	for _, cmd := range all {
		if cmd.Name == name {
			return &cmd
		}
		if cmd.UserFacingName() == name {
			return &cmd
		}
		if slices.Contains(cmd.Aliases, name) {
			return &cmd
		}
	}
	return nil
}

// HasSkill checks if a skill exists by name.
// Source: commands.ts:700-702 — hasCommand
func (r *Registry) HasSkill(name string) bool {
	return r.FindSkill(name) != nil
}

// ---------------------------------------------------------------------------
// Discovery sources
// ---------------------------------------------------------------------------

// loadBundledSkills loads skills from the bundled (embedded) skill set.
// TS: bundledSkills — registered at startup, not from filesystem.
// gbot: returns empty for now; bundled skills will be added via RegisterBundledSkill.
func (r *Registry) loadBundledSkills() []types.SkillCommand {
	// Bundled skills are registered separately via RegisterBundledSkill.
	return nil
}

// loadManagedSkills loads skills from the policy directory.
// Source: loadSkillsDir.ts:686-688 — managedSkillsDir
func (r *Registry) loadManagedSkills() []types.SkillCommand {
	dir := managedSkillsPath()
	if dir == "" {
		return nil
	}
	// Check for disable env var
	// TS: CLAUDE_CODE_DISABLE_POLICY_SKILLS → GBOT_DISABLE_POLICY_SKILLS
	if os.Getenv("GBOT_DISABLE_POLICY_SKILLS") != "" {
		return nil
	}
	return r.loadSkillsFromDir(dir, types.SkillSourceManaged)
}

// loadUserSkills loads skills from ~/.gbot/skills/.
// Source: loadSkillsDir.ts:640,689-691 — userSkillsDir
func (r *Registry) loadUserSkills() []types.SkillCommand {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(home, ".gbot", "skills")
	return r.loadSkillsFromDir(dir, types.SkillSourceUser)
}

// loadProjectSkills loads skills from project directories (walk to git root).
// Source: loadSkillsDir.ts:642,693-697 — projectSkillsDirs
func (r *Registry) loadProjectSkills() []types.SkillCommand {
	var allSkills []types.SkillCommand

	// Walk from cwd upward, collecting .gbot/skills/ directories
	dirs := r.collectProjectSkillDirs()
	for _, dir := range dirs {
		skills := r.loadSkillsFromDir(dir, types.SkillSourceProject)
		allSkills = append(allSkills, skills...)
	}
	return allSkills
}

// collectProjectSkillDirs walks from cwd upward to home, finding .gbot/skills/ dirs.
// Source: utils/markdownConfigLoader.ts — getProjectDirsUpToHome
func (r *Registry) collectProjectSkillDirs() []string {
	var dirs []string
	current := r.cwd

	// Walk up to home directory or root
	home, _ := os.UserHomeDir()
	for {
		skillDir := filepath.Join(current, ".gbot", "skills")
		if _, err := os.Stat(skillDir); err == nil {
			dirs = append(dirs, skillDir)
		}

		parent := filepath.Dir(current)
		if parent == current {
			break // reached root
		}
		if home != "" && parent == home {
			// Check home dir too, then stop
			skillDir = filepath.Join(home, ".gbot", "skills")
			if _, err := os.Stat(skillDir); err == nil {
				dirs = append(dirs, skillDir)
			}
			break
		}
		current = parent
	}
	return dirs
}

// ---------------------------------------------------------------------------
// Core loading from a skills directory
// Source: loadSkillsDir.ts:407-480 — loadSkillsFromSkillsDir
// ---------------------------------------------------------------------------

// loadSkillsFromDir loads all skills from a directory.
// Only supports directory format: <name>/SKILL.md (no flat .md files).
// Source: loadSkillsDir.ts:424-428
func (r *Registry) loadSkillsFromDir(dir string, source types.SkillSource) []types.SkillCommand {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // fail open
	}

	skills := make([]types.SkillCommand, 0, len(entries))
	for _, entry := range entries {
		// Only support directory format: skill-name/SKILL.md
		// Source: loadSkillsDir.ts:425-428
		if !entry.IsDir() {
			continue
		}

		skillDirPath := filepath.Join(dir, entry.Name())
		skillFilePath := filepath.Join(skillDirPath, "SKILL.md")

		// Check file size before reading
		info, err := os.Stat(skillFilePath)
		if err != nil {
			continue // SKILL.md doesn't exist, skip
		}
		if info.Size() > maxFrontmatterFileSize {
			slog.Warn("skills: skipping file: too large", "path", skillFilePath, "size", info.Size())
			continue
		}

		rawContent, err := os.ReadFile(skillFilePath)
		if err != nil {
			continue
		}

		skillName := entry.Name()
		cmd := ParseSkill(skillName, skillFilePath, string(rawContent), source)
		skills = append(skills, *cmd)
	}
	return skills
}

// ---------------------------------------------------------------------------
// Deduplication
// Source: loadSkillsDir.ts:725-763 — first-wins by file identity
// ---------------------------------------------------------------------------

// deduplicateSkills removes duplicate skills based on file identity.
// First occurrence wins (same physical file via symlink resolution).
// Source: loadSkillsDir.ts:736-763
func (r *Registry) deduplicateSkills(skills []types.SkillCommand) []types.SkillCommand {
	seen := make(map[string]bool)
	var result []types.SkillCommand

	for _, skill := range skills {
		// Resolve symlinks for file identity
		resolved, err := filepath.EvalSymlinks(skill.SourcePath)
		if err != nil {
			resolved = skill.SourcePath
		}

		if seen[resolved] {
			slog.Debug("skills: skipping duplicate", "name", skill.Name, "path", skill.SourcePath)
			continue
		}
		seen[resolved] = true
		result = append(result, skill)
	}
	return result
}

// ---------------------------------------------------------------------------
// Managed skills path
// Source: utils/settings/managedPath.ts — getManagedFilePath
// ---------------------------------------------------------------------------

// managedSkillsPath returns the platform-specific managed skills directory.
// Linux: /etc/gbot/skills/
// macOS: /Library/Application Support/Gbot/skills/
// Override: GBOT_MANAGED_SETTINGS_PATH env var
func managedSkillsPath() string {
	if override := os.Getenv("GBOT_MANAGED_SETTINGS_PATH"); override != "" {
		return filepath.Join(override, ".gbot", "skills")
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/Gbot/skills"
	default: // linux and others
		return "/etc/gbot/skills"
	}
}

// maxFrontmatterFileSize is the maximum size of a skill .md file (1MB).
const maxFrontmatterFileSize = 1 << 20

// ---------------------------------------------------------------------------
// Gitignore check
// Source: utils/git/gitignore.ts — isPathGitignored
// ---------------------------------------------------------------------------

// isPathGitignored checks if a path is gitignored.
// Uses `git check-ignore <path>` with cwd as working directory.
// Returns false if not in a git repo (fails open).
func isPathGitignored(checkPath, cwd string) bool {
	// #nosec G204 — checkPath is controlled by skill discovery
	cmd := exec.Command("git", "-C", cwd, "check-ignore", "-q", checkPath)
	cmd.Dir = cwd
	err := cmd.Run()
	if err != nil {
		// Exit code 1 = not ignored, exit code 128 = not a git repo
		return false
	}
	return true // exit code 0 = ignored
}

// RegisterBundledSkill registers a bundled skill.
// TS: bundled skills are registered via registerBundledSkill in bundledSkills.ts.
func (r *Registry) RegisterBundledSkill(cmd types.SkillCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills = append(r.skills, cmd)
}

// ---------------------------------------------------------------------------
// OnSkillsLoaded callback (correction 5 + 14)
// ---------------------------------------------------------------------------

// OnSkillsLoaded registers a callback for skill changes.
// Callbacks are invoked OUTSIDE the write lock (correction 14: no deadlock).
// Returns an unsubscribe function.
func (r *Registry) OnSkillsLoaded(cb func()) func() {
	r.mu.Lock()
	r.onSkillsLoadedCallbacks = append(r.onSkillsLoadedCallbacks, cb)
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, fn := range r.onSkillsLoadedCallbacks {
			// Compare function pointers via fmt.Sprintf
			if fmt.Sprintf("%p", fn) == fmt.Sprintf("%p", cb) {
				r.onSkillsLoadedCallbacks = append(r.onSkillsLoadedCallbacks[:i], r.onSkillsLoadedCallbacks[i+1:]...)
				break
			}
		}
	}
}

// fireOnSkillsLoaded invokes callbacks outside the lock.
// Source: correction 14 — release lock before callbacks
func (r *Registry) fireOnSkillsLoaded() {
	r.mu.Lock()
	callbacks := make([]func(), len(r.onSkillsLoadedCallbacks))
	copy(callbacks, r.onSkillsLoadedCallbacks)
	r.mu.Unlock()

	for _, cb := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("skills: onSkillsLoaded callback panic", "error", r)
				}
			}()
			cb()
		}()
	}
}

// ---------------------------------------------------------------------------
// Discover — sorted by path depth helper
// ---------------------------------------------------------------------------

// sortDirsDeepestFirst sorts directories by path depth (deepest first).
// Source: loadSkillsDir.ts:912-914
func sortDirsDeepestFirst(dirs []string) {
	sort.Slice(dirs, func(i, j int) bool {
		return strings.Count(dirs[i], string(filepath.Separator)) >
			strings.Count(dirs[j], string(filepath.Separator))
	})
}
