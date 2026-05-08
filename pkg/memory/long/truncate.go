package long

import (
	"fmt"
	"strings"
)

// EntrypointTruncation holds the result of truncating MEMORY.md content.
// TS: EntrypointTruncation (memdir.ts:41-47)
type EntrypointTruncation struct {
	Content          string
	LineCount        int
	ByteCount        int
	WasLineTruncated bool
	WasByteTruncated bool
}

// TruncateEntrypoint truncates MEMORY.md content to the line AND byte caps,
// appending a warning that names which cap fired.
// Line-truncates first (natural boundary), then byte-truncates at the last
// newline before the cap. If a single line exceeds the byte cap, hard-truncates
// at the byte cap (TS: cutAt > 0 ? cutAt : MAX_ENTRYPOINT_BYTES).
// TS: truncateEntrypointContent (memdir.ts:57-103)
func TruncateEntrypoint(raw string) EntrypointTruncation {
	trimmed := trimWhitespace(raw)
	if trimmed == "" {
		return EntrypointTruncation{}
	}

	lines := splitLines(trimmed)
	lineCount := len(lines)
	byteCount := len(trimmed)

	wasLineTruncated := lineCount > MaxEntrypointLines
	wasByteTruncated := byteCount > MaxEntrypointBytes

	if !wasLineTruncated && !wasByteTruncated {
		return EntrypointTruncation{
			Content:          trimmed,
			LineCount:        lineCount,
			ByteCount:        byteCount,
			WasLineTruncated: false,
			WasByteTruncated: false,
		}
	}

	// Step 1: line-truncate (natural boundary)
	content := trimmed
	if wasLineTruncated {
		content = joinLines(lines[:MaxEntrypointLines])
	}

	// Step 2: byte-truncate at last newline before cap
	if len(content) > MaxEntrypointBytes {
		cutAt := lastIndexByte(content, '\n', MaxEntrypointBytes)
		if cutAt > 0 {
			content = content[:cutAt]
		} else {
			// Single line >25KB: hard-truncate at byte cap
			// TS: truncated.slice(0, cutAt > 0 ? cutAt : MAX_ENTRYPOINT_BYTES)
			content = content[:MaxEntrypointBytes]
		}
	}

	reason := formatTruncationReason(wasLineTruncated, wasByteTruncated, lineCount, byteCount)
	warning := fmt.Sprintf(
		"\n\n> WARNING: %s is %s. Only part of it was loaded. Keep index entries to one line under ~200 chars; move detail into topic files.",
		EntrypointName, reason,
	)

	return EntrypointTruncation{
		Content:          content + warning,
		LineCount:        lineCount,
		ByteCount:        byteCount,
		WasLineTruncated: wasLineTruncated,
		WasByteTruncated: wasByteTruncated,
	}
}

func formatTruncationReason(lineTrunc, byteTrunc bool, lineCount, byteCount int) string {
	switch {
	case byteTrunc && !lineTrunc:
		return fmt.Sprintf("%d bytes (limit: %d bytes) — index entries are too long", byteCount, MaxEntrypointBytes)
	case lineTrunc && !byteTrunc:
		return fmt.Sprintf("%d lines (limit: %d)", lineCount, MaxEntrypointLines)
	default:
		return fmt.Sprintf("%d lines and %d bytes", lineCount, byteCount)
	}
}

// trimWhitespace trims leading/trailing whitespace.
func trimWhitespace(s string) string {
	return strings.TrimSpace(s)
}

// splitLines splits on newlines.
func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

// joinLines joins lines with newlines.
func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// lastIndexByte finds the last occurrence of byte c before position n in s.
func lastIndexByte(s string, c byte, n int) int {
	end := min(n, len(s))
	for i := end - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
