// Package toolresult implements TS-aligned large tool output persistence.
// TS reference: src/utils/toolResultStorage.ts
package toolresult

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"unicode/utf8"
)

const (
	DefaultMaxResultSizeChars = 50000
	PreviewSizeBytes          = 2000
	MaxPersistSizeBytes       = 64 * 1024 * 1024 // 64MB — TS MAX_PERSISTED_SIZE
	ToolResultsSubdir         = "tool-results"
	PersistedOutputTag        = "<persisted-output>"
	PersistedOutputClosingTag = "</persisted-output>"
	// ClearedMessage replaces old tool result content during microcompact.
	// TS: TOOL_RESULT_CLEARED_MESSAGE (toolResultStorage.ts:34)
	ClearedMessage = "[Old tool result content cleared]"
)

// PersistedOutputTagBytes is the byte slice form of PersistedOutputTag.
// Correction 10: package-level constant avoids per-iteration allocation in microcompact.
var PersistedOutputTagBytes = []byte(PersistedOutputTag)

// Correction 1: validate toolUseID to prevent path traversal.
var safeIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Correction 12: validate sessionID to prevent path traversal.
var safeSessionIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// PersistedToolResult holds metadata about a persisted tool result file.
type PersistedToolResult struct {
	Filepath     string
	OriginalSize int
	IsJSON       bool // TS: isJson — true when content is array/object
	Preview      string
	HasMore      bool
}

// PersistResult is the return type for MaybePersistLargeToolResult.
// Correction 18: struct instead of raw []byte so callers don't guess persistence status.
type PersistResult struct {
	Output    []byte // new output (original or preview JSON)
	Persisted bool   // whether content was persisted to disk
	FilePath  string // persisted file path (empty if not persisted)
}

// Correction 9: cache directory creation per session.
// Safe for concurrent use: dirCacheMu protects dirCache.
// The check-then-create pattern is acceptable because os.MkdirAll is idempotent —
// a concurrent caller may also create the directory without data loss.
var (
	dirCacheMu sync.Mutex
	dirCache   = make(map[string]bool) // sessionID → dir created
)

// GetPersistenceThreshold returns the effective persistence threshold.
// -1 means no limit (Read tool), never persist.
// TS: getPersistenceThreshold (toolResultStorage.ts:55-78)
func GetPersistenceThreshold(toolName string, declaredMax int) int {
	if declaredMax < 0 { // Infinity/opt-out
		return declaredMax
	}
	if declaredMax == 0 {
		return DefaultMaxResultSizeChars
	}
	if declaredMax < DefaultMaxResultSizeChars {
		return declaredMax
	}
	return DefaultMaxResultSizeChars
}

// GetSessionDir returns ~/.gbot/sessions/<sessionID>.
// Correction 12: validates sessionID format.
func GetSessionDir(sessionID string) (string, error) {
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".gbot", "sessions", sessionID), nil
}

// GetToolResultsDir returns ~/.gbot/sessions/<sessionID>/tool-results/.
func GetToolResultsDir(sessionID string) (string, error) {
	sessionDir, err := GetSessionDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionDir, ToolResultsSubdir), nil
}

// EnsureToolResultsDir creates the tool-results directory (idempotent).
// Correction 9: cached per session to avoid repeated syscalls.
func EnsureToolResultsDir(sessionID string) error {
	dirCacheMu.Lock()
	if dirCache[sessionID] {
		dirCacheMu.Unlock()
		return nil
	}
	dirCacheMu.Unlock()

	dir, err := GetToolResultsDir(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create tool-results dir: %w", err)
	}

	dirCacheMu.Lock()
	dirCache[sessionID] = true
	dirCacheMu.Unlock()
	return nil
}

// GetToolResultPath returns the file path for a tool result.
// Correction 1: sanitizes toolUseID to prevent path traversal.
// Correction 6: uses .json extension for array/object content, .txt otherwise.
func GetToolResultPath(sessionID, toolUseID string, isJSON bool) (string, error) {
	dir, err := GetToolResultsDir(sessionID)
	if err != nil {
		return "", err
	}
	safeID := sanitizeToolUseID(toolUseID)
	ext := "txt"
	if isJSON {
		ext = "json"
	}
	return filepath.Join(dir, safeID+"."+ext), nil
}

// IsToolResultPath checks whether a file path is inside the tool-results directory.
// Used by Read tool to auto-allow persisted file reads.
func IsToolResultPath(filePath string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	expectedPrefix := filepath.Join(home, ".gbot", "sessions")
	// abs = .../sessions/<id>/tool-results/<file>
	// Dir(abs) = .../sessions/<id>/tool-results
	// Dir(Dir(abs)) = .../sessions/<id>
	// Dir(Dir(Dir(abs))) = .../sessions
	return filepath.Dir(filepath.Dir(filepath.Dir(abs))) == expectedPrefix &&
		filepath.Base(filepath.Dir(abs)) == ToolResultsSubdir
}

// PersistToolResult writes content to disk.
// Correction 1: O_EXCL prevents overwrite (replay safety).
// Correction 13: 0600 permissions protect sensitive tool output.
// TS: persistToolResult (toolResultStorage.ts:137-184)
func PersistToolResult(sessionID, toolUseID string, content []byte) (*PersistedToolResult, error) {
	isJSON := len(content) > 0 && (content[0] == '[' || content[0] == '{')

	if err := EnsureToolResultsDir(sessionID); err != nil {
		return nil, fmt.Errorf("ensure dir: %w", err)
	}

	path, err := GetToolResultPath(sessionID, toolUseID, isJSON)
	if err != nil {
		return nil, err
	}

	// Exclusive create: skip if already exists (replay from microcompact).
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Already persisted on a prior turn, fall through to preview.
			slog.Info("toolresult: file already exists, skipping", "path", path)
		} else {
			return nil, fmt.Errorf("create file: %w", err)
		}
	} else {
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("write file: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close file: %w", err)
		}
	}

	contentStr := string(content)
	preview, hasMore := GeneratePreview(contentStr, PreviewSizeBytes)

	return &PersistedToolResult{
		Filepath:     path,
		OriginalSize: len(content),
		IsJSON:       isJSON,
		Preview:      preview,
		HasMore:      hasMore,
	}, nil
}

// GeneratePreview returns a preview of content, cutting at a newline boundary.
// Correction 8: validates UTF-8 boundary to avoid splitting multi-byte characters.
// TS: generatePreview (toolResultStorage.ts:339-356)
func GeneratePreview(content string, maxBytes int) (preview string, hasMore bool) {
	if len(content) <= maxBytes {
		return content, false
	}

	// Find last newline within the limit.
	truncated := content[:maxBytes]
	lastNewline := -1
	for i := len(truncated) - 1; i >= 0; i-- {
		if truncated[i] == '\n' {
			lastNewline = i
			break
		}
	}

	cutPoint := maxBytes
	if lastNewline > maxBytes/2 {
		cutPoint = lastNewline
	}

	// Correction 8: ensure cut point is at a valid UTF-8 boundary.
	for cutPoint > 0 && !utf8.RuneStart(content[cutPoint]) {
		cutPoint--
	}
	if cutPoint == 0 {
		cutPoint = maxBytes // fallback: use first valid rune start
	}

	return content[:cutPoint], true
}

// FormatFileSize returns a human-readable file size string.
// TS: formatFileSize (format.ts)
func FormatFileSize(size int) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.1fGB", float64(size)/float64(GB))
	case size >= MB:
		return fmt.Sprintf("%.1fMB", float64(size)/float64(MB))
	case size >= KB:
		return fmt.Sprintf("%.1fKB", float64(size)/float64(KB))
	default:
		return fmt.Sprintf("%dB", size)
	}
}

// BuildLargeToolResultMessage constructs the <persisted-output> XML message.
// TS: buildLargeToolResultMessage (toolResultStorage.ts:189-199)
func BuildLargeToolResultMessage(result *PersistedToolResult) string {
	msg := PersistedOutputTag + "\n"
	msg += fmt.Sprintf("Output too large (%s). Full output saved to: %s\n\n", FormatFileSize(result.OriginalSize), result.Filepath)
	msg += fmt.Sprintf("Preview (first %s):\n", FormatFileSize(PreviewSizeBytes))
	msg += result.Preview
	if result.HasMore {
		msg += "\n...\n"
	} else {
		msg += "\n"
	}
	msg += PersistedOutputClosingTag
	return msg
}

// sanitizeToolUseID validates toolUseID; falls back to SHA256 hash if unsafe.
// Correction 1: prevents path traversal via toolUseID.
func sanitizeToolUseID(id string) string {
	if safeIDRe.MatchString(id) {
		return id
	}
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:16])
}

// validateSessionID ensures sessionID is safe for filesystem paths.
// Correction 12: prevents path traversal via sessionID.
func validateSessionID(id string) error {
	if !safeSessionIDRe.MatchString(id) {
		return fmt.Errorf("invalid sessionID: %q", id)
	}
	return nil
}

// HasImageBlock checks whether content blocks contain an image type.
// Correction 16: accepts json.RawMessage, unmarshals internally.
// TS: hasImageBlock (toolResultStorage.ts:507-516)
func HasImageBlock(content json.RawMessage) bool {
	// Content may be a string (not an array) — skip.
	if len(content) == 0 || content[0] != '[' {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "image" {
			return true
		}
	}
	return false
}

// ResetDirCache clears the directory creation cache (for testing).
func ResetDirCache() {
	dirCacheMu.Lock()
	dirCache = make(map[string]bool)
	dirCacheMu.Unlock()
}
