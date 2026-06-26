package long

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/liuy/gbot/pkg/project"
)

const (
	EntrypointName     = "MEMORY.md"
	MaxEntrypointLines = 200
	MaxEntrypointBytes = 25000
)

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

// GetMemoryPath returns the memory directory path.
// Returns ~/.gbot/projects/{slug}/memory/ where slug is derived from workingDir.
// TS: getAutoMemPath (paths.ts:223-235)
func GetMemoryPath(workingDir string) string {
	return filepath.Join(project.Dir(workingDir), "memory") + string(filepath.Separator)
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
