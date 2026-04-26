package permission

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Dangerous files that should be protected from auto-editing.
// Source: filesystem.ts:57-68 — DANGEROUS_FILES
//
// These files can be used for code execution or data exfiltration.
// Returns ask (not deny) when matched, aligned with TS behavior.
var dangerousFiles = []string{
	".gitconfig",
	".gitmodules",
	".bashrc",
	".bash_profile",
	".zshrc",
	".zprofile",
	".profile",
	".ripgreprc",
	".mcp.json",
	".claude.json",
}

// Dangerous directories that should be protected from auto-editing.
// Source: filesystem.ts:74-79 — DANGEROUS_DIRECTORIES
var dangerousDirectories = []string{
	".git",
	".vscode",
	".idea",
	".claude",
}

// MatchFilePath checks if a file path matches a permission rule pattern.
//
// Uses doublestar for gitignore-style matching (already in go.mod).
// Aligned with TS's use of the 'ignore' npm package.
//
// Security protections: rejects .. traversal and absolute paths.
// Symlink resolution: EvalSymlinks + ancestor fallback for new files.
// Root-relative matching: uses Rule.ConfigRoot.
func MatchFilePath(rule Rule, filePath string) (bool, error) {
	// Normalize: strip ./ prefix, use /
	filePath = normalizePath(filePath)
	pattern := rule.Value.RuleContent
	if pattern == nil {
		// Bare tool name — matches everything
		return true, nil
	}
	patternStr := normalizePath(*pattern)

	// Security check: path traversal
	if containsPathTraversal(filePath) {
		return false, fmt.Errorf("path traversal detected: %q", filePath)
	}

	// Root-relative matching:
	// When ConfigRoot is set, resolve both paths relative to ConfigRoot.
	// Pattern is already relative to ConfigRoot (as stored in config).
	// FilePath gets resolved to absolute via ConfigRoot, then made relative for matching.
	if rule.ConfigRoot != "" {
		// Make filePath absolute relative to ConfigRoot
		absFilePath := filePath
		if !filepath.IsAbs(filePath) {
			absFilePath = filepath.Join(rule.ConfigRoot, filePath)
		}

		// Symlink resolution
		resolvedAbs, err := resolvePath(absFilePath)
		if err != nil {
			return false, fmt.Errorf("symlink resolution failed: %w", err)
		}

		// Make resolved path relative to ConfigRoot
		relPath, err := filepath.Rel(rule.ConfigRoot, resolvedAbs)
		if err != nil {
			return false, fmt.Errorf("failed to make path relative to ConfigRoot: %w", err)
		}
		relPath = normalizePath(relPath)

		// Match: pattern is relative to ConfigRoot, path is now also relative
		matched, err := doublestar.Match(patternStr, relPath)
		if err != nil {
			return false, fmt.Errorf("pattern match error: %w", err)
		}
		return matched, nil
	}

	// No ConfigRoot: match as-is (absolute path check applies)
	if filepath.IsAbs(filePath) && !filepath.IsAbs(patternStr) {
		return false, fmt.Errorf("absolute path not allowed with relative pattern: %q", filePath)
	}

	// Symlink resolution
	resolvedPath, err := resolvePath(filePath)
	if err != nil {
		return false, fmt.Errorf("symlink resolution failed: %w", err)
	}

	matched, err := doublestar.Match(patternStr, resolvedPath)
	if err != nil {
		return false, fmt.Errorf("pattern match error: %w", err)
	}
	return matched, nil
}

// IsDangerousFilePath checks if a file path is in the dangerous list.
// Source: filesystem.ts:435-488 — isDangerousFilePathToAutoEdit
//
// Returns true if the file should trigger an ask decision.
// Case-insensitive comparison to prevent bypasses.
func IsDangerousFilePath(path string) bool {
	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Clean(path)
	}

	segments := splitPathSegments(absPath)
	fileName := ""
	if len(segments) > 0 {
		fileName = segments[len(segments)-1]
	}

	// Check dangerous directories (case-insensitive)
	for _, seg := range segments {
		lowerSeg := strings.ToLower(seg)
		if slices.Contains(dangerousDirectories, lowerSeg) {
			return true
		}
	}

	// Check dangerous files (case-insensitive)
	if fileName != "" {
		lowerName := strings.ToLower(fileName)
		if slices.Contains(dangerousFiles, lowerName) {
			return true
		}
	}

	return false
}

// ValidateFilePattern validates a file path pattern at load time.
// Returns error if the pattern is malformed.
func ValidateFilePattern(pattern string) error {
	normalized := normalizePath(pattern)
	// Try matching against empty string — validates pattern syntax
	_, err := doublestar.Match(normalized, "")
	if err != nil {
		return fmt.Errorf("invalid file pattern %q: %w", pattern, err)
	}
	return nil
}

// resolvePath resolves symlinks in the path.
// For new files (path doesn't exist), walks up parent directories.
// Aligned with TS resolveDeepestExistingAncestorSync.
func resolvePath(path string) (string, error) {
	// Try full resolution first
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return normalizePath(resolved), nil
	}

	// Walk up parent directories until we find one that exists
	dir := filepath.Dir(path)
	remaining := filepath.Base(path)
	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			result := filepath.Join(resolved, remaining)
			return normalizePath(result), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root, can't resolve — fail-secure
			return normalizePath(path), nil
		}
		remaining = filepath.Join(filepath.Base(dir), remaining)
		dir = parent
	}
}

// normalizePath strips ./ prefix and normalizes separators.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	p = strings.ReplaceAll(p, string(filepath.Separator), "/")
	// Strip leading ./
	p = strings.TrimPrefix(p, "./")
	return p
}

// containsPathTraversal checks for .. in path segments.
func containsPathTraversal(p string) bool {
	for part := range strings.SplitSeq(p, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// splitPathSegments splits a path into its segments.
func splitPathSegments(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

