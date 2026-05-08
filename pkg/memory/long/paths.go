package long

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	EntrypointName     = "MEMORY.md"
	MaxEntrypointLines = 200
	MaxEntrypointBytes = 25000
)

// maxSanitizedLength matches TS's MAX_SANITIZED_LENGTH (200 chars).
const maxSanitizedLength = 200

// IsAutoMemoryEnabled checks if the memory system is enabled.
// Priority: GBOT_AUTO_MEMORY_ENABLED env var (1/true → OFF, 0/false → ON,
// unset → ON default enabled).
// TS: isAutoMemoryEnabled (paths.ts:30-55)
func IsAutoMemoryEnabled() bool {
	val := os.Getenv("GBOT_AUTO_MEMORY_ENABLED")
	if val == "1" || strings.EqualFold(val, "true") {
		return false
	}
	if val == "0" || strings.EqualFold(val, "false") {
		return true
	}
	return true // default: enabled
}

// ValidateMemoryPath normalizes and validates a memory directory path.
// SECURITY: rejects relative paths, root/near-root (<3 chars), null bytes,
// UNC paths. Returns empty string on rejection.
// TS: validateMemoryPath (paths.ts:109-150)
func ValidateMemoryPath(raw string) string {
	if raw == "" {
		return ""
	}

	// Reject null bytes (survives normalization, can truncate in syscalls)
	if strings.Contains(raw, "\x00") {
		return ""
	}

	// Expand ~/ prefix
	candidate := raw
	if strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, "~\\") {
		rest := raw[2:]
		restNorm := filepath.Clean(rest)
		if restNorm == "." || restNorm == ".." || restNorm == "" {
			return "" // would expand to $HOME or ancestor
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		candidate = filepath.Join(home, rest)
	}

	normalized := filepath.Clean(candidate)
	// Remove trailing separator for consistent comparison
	normalized = strings.TrimRight(normalized, string(filepath.Separator))

	if !filepath.IsAbs(normalized) {
		return ""
	}
	if len(normalized) < 3 {
		return "" // root or near-root (e.g., "/" or "C:")
	}
	// Reject UNC paths (\\server\share)
	if strings.HasPrefix(normalized, `\\`) || strings.HasPrefix(normalized, `//`) {
		return ""
	}
	// Reject Windows drive root (C:)
	if runtime.GOOS == "windows" && len(normalized) == 2 && normalized[1] == ':' {
		return ""
	}

	return normalized + string(filepath.Separator)
}

// sanitizePath makes a string safe for use as a directory name.
// Replaces non-alphanumeric with hyphens. Truncates to maxSanitizedLength
// and appends a hash suffix for uniqueness if needed.
// TS: sanitizePath (sessionStoragePortable.ts:311-319)
func sanitizePath(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	sanitized := b.String()

	if len(sanitized) <= maxSanitizedLength {
		return sanitized
	}

	hash := djb2Hash(name)
	prefix := sanitized[:maxSanitizedLength]
	return prefix + "-" + hash
}

// djb2Hash implements the DJB2 hash algorithm.
// TS: djb2Hash (hash.ts) → Math.abs(result).toString(36)
func djb2Hash(str string) string {
	hash := uint32(5381)
	for _, c := range str {
		hash = hash*33 + uint32(c)
	}
	// Convert to signed int32 for absolute value, matching TS Math.abs
	signed := int32(hash)
	if signed < 0 {
		signed = -signed
	}
	return uintToString(uint64(signed), 36)
}

func uintToString(n uint64, base int) string {
	if n == 0 {
		return "0"
	}
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [64]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = digits[n%uint64(base)]
		n /= uint64(base)
	}
	return string(buf[pos:])
}

// findGitRoot walks up from dir to find a directory containing .git.
// Returns empty string if not in a git repo.
// TS: findGitRoot (git.ts)
func findGitRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// findCanonicalGitRoot finds the git root and resolves worktrees to the
// main repository. This ensures all worktrees of the same repo share one
// memory directory.
// TS: findCanonicalGitRoot (git.ts:185-195)
func findCanonicalGitRoot(workingDir string) string {
	root := findGitRoot(workingDir)
	if root == "" {
		return workingDir
	}

	// Check if .git is a file (worktree) — resolve to main repo
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return root
	}
	if !info.IsDir() {
		// .git is a file → worktree. Read "gitdir: /path/to/main/.git/worktrees/xxx"
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return root
		}
		content := strings.TrimSpace(string(data))
		if strings.HasPrefix(content, "gitdir: ") {
			gitdir := content[len("gitdir: "):]
			// gitdir is like /path/to/main/.git/worktrees/xxx
			// Walk up to find the main repo root
			mainGitDir := filepath.Clean(filepath.Join(gitdir, "..", ".."))
			if _, err := os.Stat(mainGitDir); err == nil {
				return filepath.Dir(mainGitDir)
			}
		}
		return root
	}

	return root
}

// GetMemoryPath returns the memory directory path.
// Returns ~/.gbot/projects/{sanitized-git-root}/memory/
// TS: getAutoMemPath (paths.ts:223-235)
func GetMemoryPath(workingDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	base := findCanonicalGitRoot(workingDir)
	slug := sanitizePath(base)
	return filepath.Join(home, ".gbot", "projects", slug, "memory") + string(filepath.Separator)
}

// GetMemoryEntrypoint returns the MEMORY.md path.
// TS: getAutoMemEntrypoint (paths.ts:257-259)
func GetMemoryEntrypoint(workingDir string) string {
	return filepath.Join(GetMemoryPath(workingDir), EntrypointName)
}

// EnsureMemoryDir creates the memory directory and all parents. Idempotent.
// TS: ensureMemoryDirExists (memdir.ts:129-147)
func EnsureMemoryDir(workingDir string) error {
	return os.MkdirAll(GetMemoryPath(workingDir), 0o755)
}

// IsMemoryPath checks if absPath is within the memory directory.
// SECURITY: normalizes path before comparison to prevent traversal bypass.
// TS: isAutoMemPath (paths.ts:274-278)
func IsMemoryPath(workingDir, absPath string) bool {
	normalizedPath := filepath.Clean(absPath)
	memDir := GetMemoryPath(workingDir)
	// memDir has trailing separator; Clean removes it, so add back for prefix match
	return strings.HasPrefix(normalizedPath+string(filepath.Separator), memDir)
}
