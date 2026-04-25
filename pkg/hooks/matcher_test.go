package hooks

import (
	"testing"
)

// ---------------------------------------------------------------------------
// CompileMatcher — source: hooks.ts:1346-1381 — matchesPattern
// ---------------------------------------------------------------------------

func TestCompileMatcherEmptyMatchesAll(t *testing.T) {
	// Source: hooks.ts:1347-1348 — !matcher
	m := CompileMatcher("")
	if !m("Bash") {
		t.Error("empty pattern should match everything")
	}
	if !m("Read") {
		t.Error("empty pattern should match everything")
	}
}

func TestCompileMatcherWildcardMatchesAll(t *testing.T) {
	// Source: hooks.ts:1347-1348 — matcher === '*'
	m := CompileMatcher("*")
	for _, name := range []string{"Bash", "Read", "Write", "Edit", "Grep", "Glob"} {
		if !m(name) {
			t.Errorf("'*' should match %q", name)
		}
	}
}

func TestCompileMatcherExactMatch(t *testing.T) {
	// Source: hooks.ts:1360 — simple exact match
	m := CompileMatcher("Bash")
	if !m("Bash") {
		t.Error("'Bash' should match 'Bash'")
	}
	if m("Read") {
		t.Error("'Bash' should not match 'Read'")
	}
	if m("bash") {
		t.Error("'Bash' should not match 'bash' (case sensitive)")
	}
}

func TestCompileMatcherPipeSeparated(t *testing.T) {
	// Source: hooks.ts:1353-1357 — pipe-separated exact matches
	m := CompileMatcher("Bash|Write|Edit")
	for _, name := range []string{"Bash", "Write", "Edit"} {
		if !m(name) {
			t.Errorf("'Bash|Write|Edit' should match %q", name)
		}
	}
	for _, name := range []string{"Read", "Grep", "Glob", "Agent"} {
		if m(name) {
			t.Errorf("'Bash|Write|Edit' should not match %q", name)
		}
	}
}

func TestCompileMatcherPipeWithSpaces(t *testing.T) {
	// TS uses /^[a-zA-Z0-9_|]+$/ — spaces are NOT in the character class,
	// so "Bash | Write" is treated as a regex, not a simple pattern.
	// Users should write "Bash|Write" without spaces (TS convention).
	m := CompileMatcher("Bash | Write")
	// This falls through to regex path, which won't match tool names.
	if m("Bash") {
		t.Error("'Bash | Write' is regex path, should not match 'Bash' exactly")
	}
}

func TestCompileMatcherRegex(t *testing.T) {
	// Source: hooks.ts:1363-1381 — regex match
	m := CompileMatcher("^Ba.*")
	if !m("Bash") {
		t.Error("'^Ba.*' should match 'Bash'")
	}
	if m("Read") {
		t.Error("'^Ba.*' should not match 'Read'")
	}
}

func TestCompileMatcherRegexCaseInsensitive(t *testing.T) {
	m := CompileMatcher("(?i)^bash$")
	if !m("Bash") {
		t.Error("case-insensitive regex should match 'Bash'")
	}
	if !m("bash") {
		t.Error("case-insensitive regex should match 'bash'")
	}
}

func TestCompileMatcherRegexOrPattern(t *testing.T) {
	m := CompileMatcher("^(Bash|Write|Edit)$")
	for _, name := range []string{"Bash", "Write", "Edit"} {
		if !m(name) {
			t.Errorf("regex should match %q", name)
		}
	}
	if m("Read") {
		t.Error("regex should not match 'Read'")
	}
}

func TestCompileMatcherInvalidRegex(t *testing.T) {
	// Source: hooks.ts:1376-1380 — invalid regex → return false
	m := CompileMatcher("[invalid")
	if m("anything") {
		t.Error("invalid regex should never match")
	}
	if m("") {
		t.Error("invalid regex should never match even empty string")
	}
}

func TestCompileMatcherSpecialCharsNotSimple(t *testing.T) {
	// Patterns with regex special chars should go through regex path, not simple match
	m := CompileMatcher("Bash.*")
	if !m("Bash") {
		t.Error("'Bash.*' regex should match 'Bash'")
	}
	if !m("BashRun") {
		t.Error("'Bash.*' regex should match 'BashRun'")
	}
	if m("Read") {
		t.Error("'Bash.*' regex should not match 'Read'")
	}
}

// ---------------------------------------------------------------------------
// Matcher used as function (integration with CompileMatcher output)
// ---------------------------------------------------------------------------

func TestCompileMatcherReturnedFuncIsIdempotent(t *testing.T) {
	// The returned function should produce consistent results across calls
	m := CompileMatcher("Bash|Read")
	for i := range 10 {
		if !m("Bash") {
			t.Errorf("call %d: should match Bash", i)
		}
		if m("Write") {
			t.Errorf("call %d: should not match Write", i)
		}
	}
}

func TestCompileMatcherPipeTrimSpace(t *testing.T) {
	// Pipe-separated parts are trimmed — "Bash| Write" has space before Write
	// but the overall pattern "Bash| Write" contains a space → fails simplePatternRe
	// So it goes to regex path instead.
	m := CompileMatcher("Bash| Write")
	// "Bash| Write" has a space, so it's NOT a simple pattern
	// It becomes a regex. As regex, "Bash| Write" means "Bash" OR " Write"
	if !m("Bash") {
		t.Error("regex 'Bash| Write' should match 'Bash'")
	}
}

func TestCompileMatcherEmptyStringNotMatchedByRegex(t *testing.T) {
	// Empty pattern matches everything (Rule 1), but explicit regex should not match empty
	m := CompileMatcher("^Bash$")
	if m("") {
		t.Error("regex '^Bash$' should not match empty string")
	}
}
