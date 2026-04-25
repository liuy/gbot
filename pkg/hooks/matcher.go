package hooks

import (
	"regexp"
	"strings"
)

// ---------------------------------------------------------------------------
// CompileMatcher — source: hooks.ts:1346-1381 — matchesPattern
// ---------------------------------------------------------------------------

// simplePatternRe matches strings containing only [a-zA-Z0-9_|].
// Source: hooks.ts:1351 — /^[a-zA-Z0-9_|]+$/
var simplePatternRe = regexp.MustCompile(`^[a-zA-Z0-9_|]+$`)

// CompileMatcher builds a matcher function from a pattern string.
// Source: hooks.ts:1346-1381 — matchesPattern.
//
// Matching rules (aligned with TS):
//  1. Empty string or "*" → match everything
//  2. Simple pattern (only [a-zA-Z0-9_|]) → exact match or pipe-separated list
//  3. Everything else → regex match
func CompileMatcher(pattern string) func(string) bool {
	// Rule 1: empty or "*" matches everything
	// Source: hooks.ts:1347-1349
	if pattern == "" || pattern == "*" {
		return func(string) bool { return true }
	}

	// Rule 2: simple pattern (exact match or pipe-separated)
	// Source: hooks.ts:1351-1361
	if simplePatternRe.MatchString(pattern) {
		if strings.Contains(pattern, "|") {
			// Pipe-separated exact matches
			// Source: hooks.ts:1353-1357
			parts := strings.Split(pattern, "|")
			set := make(map[string]bool, len(parts))
			for _, p := range parts {
				set[strings.TrimSpace(p)] = true
			}
			return func(name string) bool {
				return set[name]
			}
		}
		// Simple exact match
		// Source: hooks.ts:1360
		return func(name string) bool {
			return name == pattern
		}
	}

	// Rule 3: regex match
	// Source: hooks.ts:1363-1381
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Invalid regex → never match (TS: log + return false)
		return func(string) bool { return false }
	}
	return func(name string) bool {
		defer func() { _ = recover() }()
		return re.MatchString(name)
	}
}
