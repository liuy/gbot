// Package bash implements shell command quoting for eval.
//
// Source reference: utils/bash/shellQuoting.ts
// 1:1 port from the TypeScript source.
package bash

import (
	"regexp"
	"strings"

	shellescape "al.essio.dev/pkg/shellescape"
)

// ---------------------------------------------------------------------------
// containsHeredoc — detects heredoc patterns in a command
// Source: shellQuoting.ts:7-22 — containsHeredoc
// ---------------------------------------------------------------------------

// bitShiftArithmetic matches arithmetic bit-shift: $((1 << 2))
var bitShiftArithmetic = regexp.MustCompile(`\$\(\(.*<<.*\)\)`)

// bitShiftTest matches test-style bit-shift: [[ 1 << 2 ]]
var bitShiftTest = regexp.MustCompile(`\[\[\s*\d+\s*<<\s*\d+\s*\]\]`)

// bitShiftSimple matches simple numeric bit-shift: 1 << 2
var bitShiftSimple = regexp.MustCompile(`\d\s*<<\s*\d`)

// heredocPattern matches heredoc: <<EOF, <<'EOF', <<"EOF", <<-EOF, <<-'EOF', <<\EOF
// Source: shellQuoting.ts:20 — /<<-?\s*(?:(['"]?)(\w+)\1|\\(\w+))/
// Go regexp lacks backreferences, so we use alternation for quote styles.
var heredocPattern = regexp.MustCompile(`<<-?\s*(?:\w+|'\w+'|"\w+"|\\\w+)`)

// containsHeredoc detects if a command contains a heredoc pattern.
// Excludes bit-shift operators first.
// Source: shellQuoting.ts:7-22
func containsHeredoc(command string) bool {
	// Exclude bit-shift operators
	// Source: shellQuoting.ts:11-17
	if bitShiftSimple.MatchString(command) ||
		bitShiftTest.MatchString(command) ||
		bitShiftArithmetic.MatchString(command) {
		return false
	}
	return heredocPattern.MatchString(command)
}

// ---------------------------------------------------------------------------
// hasStdinRedirect — checks if a command already has a stdin redirect
// Source: shellQuoting.ts:81-86
// ---------------------------------------------------------------------------

// hasStdinRedirect detects if a command already has a stdin redirect.
// Match patterns like: < file, </path/to/file, < /dev/null
// But NOT: <<EOF (heredoc), << (bit shift), <(process substitution)
// Source: shellQuoting.ts:85 — /(?:^|[\s;&|])<(?![<(])\s*\S+/
// Go regexp lacks lookahead, so we use character-by-character scanning.
func hasStdinRedirect(command string) bool {
	for i := 0; i < len(command); i++ {
		if command[i] != '<' {
			continue
		}
		// Exclude: << (heredoc/bit shift) and <( (process substitution)
		if i+1 < len(command) && (command[i+1] == '<' || command[i+1] == '(') {
			continue
		}
		// Must be preceded by start-of-string, whitespace, or separator
		if i > 0 {
			prev := command[i-1]
			if prev != ' ' && prev != '\t' && prev != ';' && prev != '&' && prev != '|' && prev != '\n' {
				continue
			}
		}
		// Skip whitespace after <
		j := i + 1
		for j < len(command) && (command[j] == ' ' || command[j] == '\t') {
			j++
		}
		// Must have a non-empty target (not another < or ()
		if j < len(command) && command[j] != '<' && command[j] != '(' {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// shouldAddStdinRedirect — decides whether to add < /dev/null
// Source: shellQuoting.ts:93-106
// ---------------------------------------------------------------------------

// shouldAddStdinRedirect checks if stdin redirect can be safely added.
// Source: shellQuoting.ts:93-106
func shouldAddStdinRedirect(command string) bool {
	// Don't add for heredocs — they provide their own input
	// Source: shellQuoting.ts:95-97
	if containsHeredoc(command) {
		return false
	}
	// Don't add if command already has one
	// Source: shellQuoting.ts:100-102
	if hasStdinRedirect(command) {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// rewriteWindowsNullRedirect — rewrites >nul to >/dev/null
// Source: shellQuoting.ts:124-128
// ---------------------------------------------------------------------------

// rewriteWindowsNullRedirect rewrites Windows CMD-style >nul redirects to POSIX /dev/null.
// Matches: >nul, 2>nul, &>nul, >>nul (case-insensitive)
// Does NOT match: >null, >nul.txt, >nullable
// Source: shellQuoting.ts:124-128
func rewriteWindowsNullRedirect(command string) string {
	// Source: shellQuoting.ts:124 — /(\d?&?>+\s*)[Nn][Uu][Ll](?=\s|$|[|&;)\n])/g
	// Go regexp lacks lookahead, so we scan and replace manually.
	var b strings.Builder
	b.Grow(len(command) + 32)
	i := 0
	for i < len(command) {
		// Look for redirect pattern: optional digit, optional &, one or more >, optional whitespace
		start := i
		// Check for optional digit (0-2)
		if i < len(command) && command[i] >= '0' && command[i] <= '2' {
			i++
		}
		// Check for optional &
		if i < len(command) && command[i] == '&' {
			i++
		}
		// Check for one or more >
		if i >= len(command) || command[i] != '>' {
			// Not a redirect pattern, reset and advance
			i = start + 1
			if start < len(command) {
				b.WriteByte(command[start])
			}
			continue
		}
		gtCount := 0
		for i < len(command) && command[i] == '>' {
			gtCount++
			i++
		}
		// Skip whitespace
		wsStart := i
		for i < len(command) && (command[i] == ' ' || command[i] == '\t') {
			i++
		}
		// Check for [Nn][Uu][Ll] followed by boundary
		if i+3 <= len(command) &&
			(command[i] == 'N' || command[i] == 'n') &&
			(command[i+1] == 'U' || command[i+1] == 'u') &&
			(command[i+2] == 'L' || command[i+2] == 'l') &&
			(i+3 == len(command) || isNulBoundary(command[i+3])) {
			// Match! Write the redirect prefix + /dev/null
			b.WriteString(command[start:wsStart])
			b.WriteString("/dev/null")
			i += 3
			continue
		}
		// Not a nul redirect, write everything as-is
		b.WriteString(command[start:i])
	}
	return b.String()
}

// isNulBoundary checks if a character is a valid boundary after "nul".
// Source: shellQuoting.ts:124 — lookahead (?=\s|$|[|&;)\n])
func isNulBoundary(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '|' || ch == '&' || ch == ';' || ch == ')' || ch == '\n'
}

// ---------------------------------------------------------------------------
// quoteShellCommand — main quoting function
// Source: shellQuoting.ts:46-74
// ---------------------------------------------------------------------------

// quoteShellCommand quotes a shell command for use with eval.
// Uses single-quote wrapping (shellescape.Quote) which preserves:
//   - $VAR references for eval to expand
//   - Literal newlines for heredocs
//   - All special characters
//
// Source: shellQuoting.ts:46-74 — quoteShellCommand
func quoteShellCommand(command string, addStdinRedirect bool) string {
	quoted := shellescape.Quote(command)
	if addStdinRedirect {
		quoted += " < /dev/null"
	}
	return quoted
}
