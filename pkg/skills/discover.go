package skills

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Dynamic skill discovery
// Source: src/skills/loadSkillsDir.ts:818-975
// ---------------------------------------------------------------------------

// DiscoverSkillDirsForPaths walks from each file path up to cwd,
// discovering .gbot/skills/ directories. Returns newly discovered dirs.
// Source: loadSkillsDir.ts:861-915
func (r *Registry) DiscoverSkillDirsForPaths(filePaths []string) []string {
	var newDirs []string
	resolvedCwd := strings.TrimSuffix(r.cwd, string(filepath.Separator))

	for _, fp := range filePaths {
		currentDir := filepath.Dir(fp)

		// Walk up to cwd but NOT including cwd itself
		// Source: loadSkillsDir.ts:876
		for strings.HasPrefix(currentDir, resolvedCwd+string(filepath.Separator)) {
			skillDir := filepath.Join(currentDir, ".gbot", "skills")

			// Skip if already checked (hit or miss) — negative cache
			// Source: loadSkillsDir.ts:882-883
			if r.dynamicDirCache[skillDir] {
				currentDir = filepath.Dir(currentDir)
				if currentDir == filepath.Dir(currentDir) {
					break
				}
				continue
			}
			r.dynamicDirCache[skillDir] = true

			if _, err := os.Stat(skillDir); err != nil {
				// Directory doesn't exist — recorded in cache above
			} else {
				// Check gitignore before loading
				// Source: loadSkillsDir.ts:892-897
				if isPathGitignored(currentDir, resolvedCwd) {
					slog.Debug("skills: skipped gitignored dir", "dir", skillDir)
					currentDir = filepath.Dir(currentDir)
					continue
				}
				newDirs = append(newDirs, skillDir)
			}

			parent := filepath.Dir(currentDir)
			if parent == currentDir {
				break // reached root
			}
			currentDir = parent
		}
	}

	// Sort deepest first — skills closer to the file take precedence
	// Source: loadSkillsDir.ts:912-914
	sortDirsDeepestFirst(newDirs)
	return newDirs
}

// AddSkillDirectories loads skills from discovered directories.
// Skills from deeper paths override shallower ones (last-writer-wins within dynamic).
// Source: loadSkillsDir.ts:923-975
func (r *Registry) AddSkillDirectories(dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}

	// Load skills from all directories
	var allLoaded []types.SkillCommand
	for _, dir := range dirs {
		skills := r.loadSkillsFromDir(dir, types.SkillSourceProject)
		allLoaded = append(allLoaded, skills...)
	}

	// Process in reverse order (shallower first) so deeper paths override
	// Source: loadSkillsDir.ts:945-951
	r.mu.Lock()
	for i := len(allLoaded) - 1; i >= 0; i-- {
		skill := allLoaded[i]
		r.dynamicSkills[skill.Name] = skill
	}
	r.mu.Unlock()

	if len(allLoaded) > 0 {
		slog.Info("skills: dynamically discovered",
			"count", len(allLoaded),
			"dirs", len(dirs),
		)
		r.fireOnSkillsLoaded()
	}
	return nil
}

// ActivateConditionalSkillsForPaths activates conditional skills
// whose paths match the given file paths. Uses gitignore-style matching.
// Source: loadSkillsDir.ts:997-1058
func (r *Registry) ActivateConditionalSkillsForPaths(filePaths []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.conditional) == 0 {
		return nil
	}

	var activated []string

	for name, skill := range r.conditional {
		if len(skill.Paths) == 0 {
			continue
		}

		for _, fp := range filePaths {
			relPath := fp
			if filepath.IsAbs(fp) {
				rel, err := filepath.Rel(r.cwd, fp)
				if err != nil {
					continue
				}
				relPath = rel
			}

			// Skip paths outside cwd or absolute
			// Source: loadSkillsDir.ts:1021-1027
			if relPath == "" || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
				continue
			}

			if matchesPatterns(relPath, skill.Paths) {
				// Activate: move to dynamic skills
				r.dynamicSkills[name] = skill
				delete(r.conditional, name)
				r.activatedNames[name] = true
				activated = append(activated, name)
				slog.Debug("skills: activated conditional skill", "name", name, "matched", relPath)
				break
			}
		}
	}

	if len(activated) > 0 {
		// Fire callbacks outside lock
		go r.fireOnSkillsLoaded()
	}
	return activated
}

// matchesPatterns checks if a path matches any of the glob patterns.
// Uses simple glob matching (filepath.Match) as an approximation of gitignore matching.
// Source: loadSkillsDir.ts:1012-1029 — uses ignore library for gitignore-style matching.
func matchesPatterns(path string, patterns []string) bool {
	for _, pattern := range patterns {
		// Try direct match
		matched, _ := filepath.Match(pattern, path)
		if matched {
			return true
		}
		// Try prefix match (pattern matches directory and all contents)
		if strings.HasPrefix(path, strings.TrimSuffix(pattern, "*")) {
			return true
		}
		// Try matching just the filename
		if !strings.Contains(pattern, "/") {
			matched, _ = filepath.Match(pattern, filepath.Base(path))
			if matched {
				return true
			}
		}
	}
	return false
}
