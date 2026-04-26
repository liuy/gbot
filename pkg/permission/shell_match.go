package permission

import (
	"regexp"
	"strings"
)

// Null-byte sentinel placeholders for wildcard pattern escaping.
// Module-level so the RegExp objects are compiled once.
// Source: shellRuleMatching.ts:13-20
const (
	escapedStarPlaceholder     = "\x00ESCAPED_STAR\x00"
	escapedBackslashPlaceholder = "\x00ESCAPED_BACKSLASH\x00"
)

var (
	escapedStarRe     = regexp.MustCompile(regexp.QuoteMeta(escapedStarPlaceholder))
	escapedBackslashRe = regexp.MustCompile(regexp.QuoteMeta(escapedBackslashPlaceholder))
)

// ParseShellRule classifies a shell command rule pattern.
//
// Source: shellRuleMatching.ts:159-184 — parsePermissionRule
//
// No legacy :* prefix syntax per user direction. Only Exact and Wildcard.
// Pre-compiles regex for Wildcard rules.
func ParseShellRule(pattern string) ShellRule {
	if HasWildcards(pattern) {
		re := compileWildcardPattern(pattern)
		return ShellRule{
			Type:    ShellRuleWildcard,
			Pattern: pattern,
			re:      re,
		}
	}
	return ShellRule{
		Type:    ShellRuleExact,
		Pattern: pattern,
		re:      nil,
	}
}

// MatchShellCommand checks if a command matches a shell rule.
func MatchShellCommand(rule ShellRule, command string) bool {
	switch rule.Type {
	case ShellRuleExact:
		return command == rule.Pattern
	case ShellRuleWildcard:
		if rule.re == nil {
			// Should not happen — ParseShellRule always compiles for wildcards.
			// Fail-secure: treat as no-match (no deny triggered).
			return false
		}
		return rule.re.MatchString(command)
	default:
		return false
	}
}

// HasWildcards checks if a pattern contains unescaped wildcards.
//
// Source: shellRuleMatching.ts:54-78
//
// An asterisk is unescaped if it's not preceded by a backslash,
// or if it's preceded by an even number of backslashes.
func HasWildcards(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			backslashCount := 0
			for j := i - 1; j >= 0 && pattern[j] == '\\'; j-- {
				backslashCount++
			}
			if backslashCount%2 == 0 {
				return true
			}
		}
	}
	return false
}

// MatchWildcardPattern matches a command against a wildcard pattern.
//
// Source: shellRuleMatching.ts:90-154 — matchWildcardPattern
//
// Wildcards (*) match any sequence of characters.
// Use \* to match a literal asterisk character.
// Use \\ to match a literal backslash.
//
// Special behavior: if pattern ends with ' *' (space + wildcard) AND the trailing
// wildcard is the ONLY unescaped wildcard, make the trailing space-and-args
// optional so 'git *' matches both 'git add' and bare 'git'.
func MatchWildcardPattern(pattern, command string) bool {
	trimmedPattern := strings.TrimSpace(pattern)

	// Phase 1: Process escape sequences — \* and \\
	var processed strings.Builder
	i := 0
	for i < len(trimmedPattern) {
		if trimmedPattern[i] == '\\' && i+1 < len(trimmedPattern) {
			next := trimmedPattern[i+1]
			if next == '*' {
				processed.WriteString(escapedStarPlaceholder)
				i += 2
				continue
			} else if next == '\\' {
				processed.WriteString(escapedBackslashPlaceholder)
				i += 2
				continue
			}
		}
		processed.WriteByte(trimmedPattern[i])
		i++
	}
	proc := processed.String()

	// Phase 2: Escape regex special characters except *
	escaped := escapeRegexExceptStar(proc)

	// Phase 3: Convert unescaped * to .*
	withWildcards := strings.ReplaceAll(escaped, "*", ".*")

	// Phase 4: Convert placeholders back to escaped regex literals
	regexPattern := escapedStarRe.ReplaceAllString(withWildcards, `\*`)
	regexPattern = escapedBackslashRe.ReplaceAllString(regexPattern, `\\`)

	// Phase 5: Trailing ' *' optimization
	// Source: shellRuleMatching.ts:141-145
	// When pattern ends with ' .*' AND the trailing wildcard is the ONLY unescaped wildcard,
	// make the trailing space-and-args optional so 'git *' matches 'git'.
	unescapedStarCount := strings.Count(proc, "*")
	if strings.HasSuffix(regexPattern, " .*") && unescapedStarCount == 1 {
		regexPattern = regexPattern[:len(regexPattern)-3] + "( .*)?"
	}

	return regexp.MustCompile("(?s)^" + regexPattern + "$").MatchString(command)
}

// compileWildcardPattern pre-compiles a wildcard pattern into a *regexp.Regexp.
// Used at rule load time so hot-path matching uses pre-compiled regex.
func compileWildcardPattern(pattern string) *regexp.Regexp {
	trimmedPattern := strings.TrimSpace(pattern)

	var processed strings.Builder
	i := 0
	for i < len(trimmedPattern) {
		if trimmedPattern[i] == '\\' && i+1 < len(trimmedPattern) {
			next := trimmedPattern[i+1]
			if next == '*' {
				processed.WriteString(escapedStarPlaceholder)
				i += 2
				continue
			} else if next == '\\' {
				processed.WriteString(escapedBackslashPlaceholder)
				i += 2
				continue
			}
		}
		processed.WriteByte(trimmedPattern[i])
		i++
	}
	proc := processed.String()

	escaped := escapeRegexExceptStar(proc)
	withWildcards := strings.ReplaceAll(escaped, "*", ".*")

	regexPattern := escapedStarRe.ReplaceAllString(withWildcards, `\*`)
	regexPattern = escapedBackslashRe.ReplaceAllString(regexPattern, `\\`)

	unescapedStarCount := strings.Count(proc, "*")
	if strings.HasSuffix(regexPattern, " .*") && unescapedStarCount == 1 {
		regexPattern = regexPattern[:len(regexPattern)-3] + "( .*)?"
	}

	return regexp.MustCompile("(?s)^" + regexPattern + "$")
}

// escapeRegexExceptStar escapes regex special characters except *.
// Source: shellRuleMatching.ts:126
func escapeRegexExceptStar(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '.', '+', '?', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\', '\'', '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
