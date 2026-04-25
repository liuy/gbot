package skills

import (
	"regexp"
	"strings"
)

// Pre-compiled regexes for argument substitution.
// Source: argumentSubstitution.ts:123-133
var (
	reArgumentsIndexed = regexp.MustCompile(`\$ARGUMENTS\[(\d+)\]`)
	reShorthandIndexed = regexp.MustCompile(`\$(\d+)`)
)

// ---------------------------------------------------------------------------
// Argument substitution
// Source: src/utils/argumentSubstitution.ts
// ---------------------------------------------------------------------------

// SubstituteArguments replaces placeholders in skill content.
// Source: argumentSubstitution.ts:94-145 — substituteArguments
//
// Supported placeholders:
//   - $ARGUMENTS       → full arguments string
//   - $ARGUMENTS[N]    → Nth positional argument
//   - $0, $1, ...      → shorthand for positional arguments
//   - ${name}          → named argument (from arguments frontmatter)
//
// If no placeholders are found and appendIfNoPlaceholder is true and args is non-empty,
// appends "\n\nARGUMENTS: {args}" to content.
func SubstituteArguments(content, args string, argumentNames []string, appendIfNoPlaceholder bool) string {
	// Empty args means no substitution needed
	if args == "" {
		return content
	}

	parsedArgs := ParseArguments(args)
	originalContent := content

	// Replace named arguments: ${name} → parsedArgs[i]
	// Source: argumentSubstitution.ts:111-121
	for i, name := range argumentNames {
		if name == "" {
			continue
		}
		val := ""
		if i < len(parsedArgs) {
			val = parsedArgs[i]
		}
		// Match ${name} — simple, unambiguous
		content = strings.ReplaceAll(content, "${"+name+"}", val)

		// Match $name but NOT $name[...] or $nameXxx (word boundary)
		// Source: argumentSubstitution.ts:117-118 — \$${name}(?![\[\w])
		// Go regexp lacks lookahead, so we use manual index-based replacement.
		content = replaceNamedArg(content, name, val)
	}

	// Replace indexed arguments: $ARGUMENTS[N]
	// Source: argumentSubstitution.ts:123-127
	content = reArgumentsIndexed.ReplaceAllStringFunc(content, func(match string) string {
		sub := reArgumentsIndexed.FindStringSubmatch(match)
		if len(sub) < 2 {
			return ""
		}
		idx := parseIndex(sub[1])
		if idx < len(parsedArgs) {
			return parsedArgs[idx]
		}
		return ""
	})

	// Replace shorthand indexed arguments: $0, $1, ...
	// Source: argumentSubstitution.ts:130-133 — \$(\d+)(?!\w)
	// Go regexp lacks lookahead; we check the character after the match manually.
	content = replaceShorthandIndexed(content, parsedArgs)

	// Replace $ARGUMENTS with full args string
	// Source: argumentSubstitution.ts:136
	content = strings.ReplaceAll(content, "$ARGUMENTS", args)

	// If no placeholders were found and appendIfNoPlaceholder, append
	// Source: argumentSubstitution.ts:140-142
	if content == originalContent && appendIfNoPlaceholder && args != "" {
		content = content + "\n\nARGUMENTS: " + args
	}

	return content
}

// replaceNamedArg replaces $name occurrences that are NOT followed by [ or word chars.
// Simulates TS negative lookahead: \$name(?![\[\w])
func replaceNamedArg(content, name, value string) string {
	prefix := "$" + name
	prefixLen := len(prefix)
	var b strings.Builder
	b.Grow(len(content))

	i := 0
	for i <= len(content)-prefixLen {
		if content[i:i+prefixLen] == prefix {
			// Check character after the match
			after := i + prefixLen
			if after < len(content) {
				ch := content[after]
				if ch == '[' || isWordChar(ch) {
					// Not a standalone $name — skip
					b.WriteByte(content[i])
					i++
					continue
				}
			}
			// Replace
			b.WriteString(value)
			i = after
			continue
		}
		b.WriteByte(content[i])
		i++
	}
	b.WriteString(content[i:])
	return b.String()
}

// replaceShorthandIndexed replaces $0, $1, etc. NOT followed by word chars.
// Simulates TS: \$(\d+)(?!\w)
func replaceShorthandIndexed(content string, parsedArgs []string) string {
	matches := reShorthandIndexed.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}

	var b strings.Builder
	b.Grow(len(content))
	lastEnd := 0

	for _, m := range matches {
		fullStart, fullEnd := m[0], m[1]
		groupStart, groupEnd := m[2], m[3]

		// Check character after the match
		if fullEnd < len(content) && isWordChar(content[fullEnd]) {
			// Skip — followed by word char (e.g., $1abc)
			continue
		}

		// Write content before this match
		b.WriteString(content[lastEnd:fullStart])

		// Get index from captured group
		idx := parseIndex(content[groupStart:groupEnd])
		if idx < len(parsedArgs) {
			b.WriteString(parsedArgs[idx])
		} else {
			// Out of bounds — write original
			b.WriteString(content[fullStart:fullEnd])
		}
		lastEnd = fullEnd
	}
	b.WriteString(content[lastEnd:])
	return b.String()
}

// isWordChar returns true if the byte is a word character [a-zA-Z0-9_].
func isWordChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_'
}

// parseIndex parses a decimal integer string.
func parseIndex(s string) int {
	idx := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		idx = idx*10 + int(c-'0')
	}
	return idx
}

// ParseArguments splits raw args string into positional parts.
// Handles simple quoted strings (double and single quotes).
// Source: argumentSubstitution.ts:24-40 — parseArguments
func ParseArguments(args string) []string {
	if strings.TrimSpace(args) == "" {
		return nil
	}

	var result []string
	var current strings.Builder
	inDoubleQuote := false
	inSingleQuote := false

	for i := 0; i < len(args); i++ {
		ch := args[i]

		switch {
		case inDoubleQuote:
			if ch == '"' {
				inDoubleQuote = false
			} else {
				current.WriteByte(ch)
			}
		case inSingleQuote:
			if ch == '\'' {
				inSingleQuote = false
			} else {
				current.WriteByte(ch)
			}
		case ch == '"':
			inDoubleQuote = true
		case ch == '\'':
			inSingleQuote = true
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// ParseArgumentNames parses frontmatter arguments field.
// Accepts space-separated string or string array.
// Source: argumentSubstitution.ts:50-68 — parseArgumentNames
func ParseArgumentNames(raw any) []string {
	if raw == nil {
		return nil
	}

	isValidName := func(name string) bool {
		trimmed := strings.TrimSpace(name)
		return trimmed != "" && !isOnlyDigits(trimmed)
	}

	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		parts := strings.Fields(v)
		var result []string
		for _, p := range parts {
			if isValidName(p) {
				result = append(result, p)
			}
		}
		return result
	case []any:
		var result []string
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				continue
			}
			if isValidName(s) {
				result = append(result, strings.TrimSpace(s))
			}
		}
		return result
	default:
		return nil
	}
}

// GenerateProgressiveArgumentHint returns hint for tab completion.
// Source: argumentSubstitution.ts:76-83 — generateProgressiveArgumentHint
func GenerateProgressiveArgumentHint(argNames, typedArgs []string) string {
	if len(argNames) <= len(typedArgs) {
		return ""
	}
	remaining := argNames[len(typedArgs):]
	parts := make([]string, len(remaining))
	for i, name := range remaining {
		parts[i] = "[" + name + "]"
	}
	return strings.Join(parts, " ")
}

// isOnlyDigits checks if a string contains only digit characters.
func isOnlyDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}
