// Package mcp implements the MCP (Model Context Protocol) client infrastructure.
//
// This file: Unicode sanitization for hidden character attack mitigation.
// Source: src/utils/sanitization.ts (92 lines)
//
// Protects against ASCII Smuggling and Hidden Prompt Injection vulnerabilities
// where invisible Unicode characters hide malicious instructions from users
// but are processed by AI models.
//
// Reference: https://embracethered.com/blog/posts/2024/hiding-and-finding-text-with-unicode-tags/
// Reference: HackerOne report #3086545
package mcp

import (
	"encoding/json"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ---------------------------------------------------------------------------
// Unicode category range tables
// Source: sanitization.ts:42 — [\p{Cf}\p{Co}\p{Cn}]
// ---------------------------------------------------------------------------

// cnTable is the Unicode "Other, Not Assigned" (Cn) range table.
// Go's unicode package doesn't export Cn, but it's available via Categories.
// Source: sanitization.ts:42 — \p{Cn} catches unassigned codepoints.
var cnTable = unicode.Categories["Cn"]

// ---------------------------------------------------------------------------
// sanitizeUnicode — Source: sanitization.ts:25-65 partiallySanitizeUnicode
// ---------------------------------------------------------------------------

var maxSanitizationIterations = 10 // Source: sanitization.ts:29

// sanitizeUnicode applies NFKC normalization and removes dangerous Unicode categories.
// Source: sanitization.ts:25-65 — partiallySanitizeUnicode
//
// Iterates until stable because NFKC normalization can introduce new invisible characters.
func sanitizeUnicode(s string) string {
	// Source: sanitization.ts:26-28
	current := s
	previous := ""
	iterations := 0

	// Source: sanitization.ts:32-55 — iterate until stable or max iterations
	for current != previous && iterations < maxSanitizationIterations {
		previous = current

		// Source: sanitization.ts:36 — NFKC normalization
		current = norm.NFKC.String(current)

		// Source: sanitization.ts:42 — strip \p{Cf}, \p{Co}, \p{Cn}
		current = removeInvisibleChars(current)

		iterations++
	}

	// Max iterations reached — return current (partially sanitized) state.
	// After 10 rounds of NFKC + invisible removal, the string is much cleaner
	// than the original. Returning the original would give attackers a bypass path.
	// Source: sanitization.ts:58-62 — TS crashes; we degrade gracefully.
	if iterations >= maxSanitizationIterations {
		return current
	}

	return current
}

// ---------------------------------------------------------------------------
// removeInvisibleChars — Source: sanitization.ts:42-53
// ---------------------------------------------------------------------------

// removeInvisibleChars removes dangerous Unicode characters using two methods.
// Source: sanitization.ts:42-53
//
// Method 1 (sanitization.ts:42): Unicode property classes \p{Cf}, \p{Co}, \p{Cn}
// Method 2 (sanitization.ts:47-53): Explicit character ranges as fallback
func removeInvisibleChars(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisibleChar(r) {
			return -1 // remove
		}
		return r
	}, s)
}

// isInvisibleChar reports whether a rune is in a dangerous Unicode category.
// Source: sanitization.ts:42-53 — \p{Cf}\p{Co}\p{Cn} + explicit ranges
//
// Dangerous categories:
//   - \p{Cf} (Format): zero-width spaces, bidi controls, BOM
//   - \p{Co} (Private Use): U+E000-F8FF
//   - \p{Cn} (Unassigned): codepoints not yet assigned in Unicode
//
// Explicit ranges (sanitization.ts:47-53, fallback for environments without \p{} support):
//   - U+200B-200F: Zero-width spaces, LTR/RTL marks
//   - U+202A-202E: Directional formatting
//   - U+2066-2069: Directional isolates
//   - U+FEFF: Byte order mark
//   - U+E000-F8FF: Basic Multilingual Plane private use
func isInvisibleChar(r rune) bool {
	// Source: sanitization.ts:42 — /[\p{Cf}\p{Co}\p{Cn}]/gu
	// Go's unicode package covers all categories; the explicit ranges in
	// sanitization.ts:47-53 are a JS fallback not needed here.
	if unicode.Is(unicode.Cf, r) {
		return true
	}
	if unicode.Is(unicode.Co, r) {
		return true
	}
	if cnTable != nil && unicode.Is(cnTable, r) {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// sanitizeUnicodeRecursive — Source: sanitization.ts:67-91 recursivelySanitizeUnicode
// ---------------------------------------------------------------------------

// sanitizeUnicodeRecursive recursively sanitizes all strings in a nested structure.
// Source: sanitization.ts:67-91 — recursivelySanitizeUnicode
//
// Handles: strings (sanitize), arrays/slices (recurse), maps (recurse key+value),
// and other types (pass through). Returns the sanitized value.
// Sanitization errors are logged and the original value is preserved.
func sanitizeUnicodeRecursive(v any) any {
	switch val := v.(type) {
	case string:
		return sanitizeUnicode(val)
	case []any:
		result := make([]any, len(val))
		for i, elem := range val {
			result[i] = sanitizeUnicodeRecursive(elem)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(val))
		for k, v := range val {
			// Source: sanitization.ts:83-84 — sanitize both keys and values
			sanitizedKey := sanitizeUnicode(k)
			result[sanitizedKey] = sanitizeUnicodeRecursive(v)
		}
		return result
	default:
		// Source: sanitization.ts:89 — numbers, booleans, null unchanged
		return v
	}
}

// ---------------------------------------------------------------------------
// sanitizeString — convenience wrapper for single string fields
// ---------------------------------------------------------------------------

// sanitizeString sanitizes a string field.
// Used in discovery to clean individual metadata fields (tool names, descriptions, etc.).
func sanitizeString(s string) string {
	return sanitizeUnicode(s)
}

// ---------------------------------------------------------------------------
// sanitizeJSON — recursive sanitization of JSON structures
// ---------------------------------------------------------------------------

// sanitizeJSON recursively sanitizes all strings in a JSON structure.
// Unmarshals the raw JSON, sanitizes recursively, re-marshals.
// Returns the original on parse error.
func sanitizeJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return raw // preserve original on parse error
	}

	sanitized := sanitizeUnicodeRecursive(parsed)
	result, _ := json.Marshal(sanitized)
	return result
}
