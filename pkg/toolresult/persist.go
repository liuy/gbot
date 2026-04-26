package toolresult

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// MaybePersistLargeToolResult checks if tool output exceeds the threshold.
// If so, it persists the output to disk and returns a preview.
// Returns a PersistResult with the new output and persistence status.
//
// TS: maybePersistLargeToolResult (toolResultStorage.ts:272-334)
func MaybePersistLargeToolResult(
	output []byte,
	toolName string,
	declaredMaxResultSize int,
	toolUseID string,
	sessionID string,
) PersistResult {
	// Step 9: Empty result handling (TS: isToolResultContentEmpty + empty message)
	if IsToolResultContentEmpty(output) {
		emptyMsg := fmt.Sprintf("(%s completed with no output)", toolName)
		b, _ := json.Marshal(emptyMsg)
		return PersistResult{Output: b}
	}

	// Correction 3: degradation when sessionID is empty.
	if sessionID == "" {
		slog.Warn("toolresult: skipping persistence, empty sessionID", "tool", toolName)
		return PersistResult{Output: output}
	}

	// Check threshold.
	threshold := GetPersistenceThreshold(toolName, declaredMaxResultSize)
	if threshold < 0 { // -1 = no limit (Read tool)
		return PersistResult{Output: output}
	}
	if len(output) <= threshold {
		return PersistResult{Output: output}
	}

	// Step 6.5: skip persistence for image content.
	if HasImageBlock(output) {
		return PersistResult{Output: output}
	}

	// Decode double-wrapped JSON to extract the raw string content.
	// Correction 4: if decode fails, persist raw bytes directly.
	var content string
	decoded := false
	if len(output) >= 2 && output[0] == '"' {
		if err := json.Unmarshal(output, &content); err == nil {
			decoded = true
		}
	}
	if !decoded {
		content = string(output)
	}

	// Correction 7: cap at MaxPersistSizeBytes (64MB).
	if len(content) > MaxPersistSizeBytes {
		slog.Warn("toolresult: output exceeds max persist size", "size", len(content), "max", MaxPersistSizeBytes, "tool", toolName)
		// Return error hint instead of persisting.
		hint := fmt.Sprintf("[Output too large to persist (%s). Consider using a more targeted command.]",
			FormatFileSize(len(content)))
		b, _ := json.Marshal(hint)
		return PersistResult{Output: b}
	}

	// Persist to disk.
	result, err := PersistToolResult(sessionID, toolUseID, []byte(content))
	if err != nil {
		slog.Warn("toolresult: persist failed, returning original output", "error", err)
		return PersistResult{Output: output}
	}

	// Build preview message and re-encode as JSON string.
	msg := BuildLargeToolResultMessage(result)

	// Correction 11: safe JSON re-encoding.
	newOutput, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("toolresult: marshal preview failed", "error", err)
		return PersistResult{Output: output}
	}

	return PersistResult{
		Output:    newOutput,
		Persisted: true,
		FilePath:  result.Filepath,
	}
}

// IsToolResultContentEmpty checks if tool result content is empty or whitespace-only.
// TS: isToolResultContentEmpty (toolResultStorage.ts:250-265)
func IsToolResultContentEmpty(content []byte) bool {
	if len(content) == 0 {
		return true
	}

	// Try to decode as JSON string first.
	if content[0] == '"' {
		var s string
		if json.Unmarshal(content, &s) == nil {
			return len(s) == 0 || isOnlyWhitespace(s)
		}
	}

	// Try as JSON array (content blocks).
	if content[0] == '[' {
		var blocks []json.RawMessage
		if json.Unmarshal(content, &blocks) != nil {
			return false
		}
		if len(blocks) == 0 {
			return true
		}
		// All blocks must be empty text blocks.
		for _, b := range blocks {
			var partial struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(b, &partial) != nil || partial.Type != "text" {
				return false
			}
			if !isOnlyWhitespace(partial.Text) {
				return false
			}
		}
		return true
	}

	return false
}

func isOnlyWhitespace(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
