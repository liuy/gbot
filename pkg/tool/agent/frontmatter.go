// Package agent provides built-in agent definitions and tool filtering for the Agent tool.
//
// Source reference: utils/frontmatterParser.ts
package agent

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Frontmatter parser
// Source: utils/frontmatterParser.ts
// ---------------------------------------------------------------------------

// ParsedMarkdown holds the result of parsing a markdown file with frontmatter.
// Source: frontmatterParser.ts:61-64 — ParsedMarkdown
type ParsedMarkdown struct {
	Frontmatter map[string]any // parsed YAML key-value pairs
	Content     string                 // body after the --- delimiter
}

// frontmatterRegex matches YAML frontmatter between --- delimiters.
// Source: frontmatterParser.ts:123 — FRONTMATTER_REGEX = /^---\s*\n([\s\S]*?)---\s*\n?/
var frontmatterRegex = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n?`)

// yamlSpecialChars matches characters that require quoting in YAML values.
// Source: frontmatterParser.ts:77 — YAML_SPECIAL_CHARS
var yamlSpecialChars = regexp.MustCompile(`[{}[\]*&#!|>%@` + "`" + `]|: `)

// maxFrontmatterFileSize is the maximum size of an agent .md file (1MB).
const maxFrontmatterFileSize = 1 << 20

// ParseFrontmatter extracts YAML frontmatter from markdown content.
// Source: frontmatterParser.ts:130-175 — parseFrontmatter
//
// If no frontmatter found, returns empty frontmatter map and original content.
// On YAML parse failure, retries with quoteProblematicValues, then fails open.
func ParseFrontmatter(markdown string, sourcePath string) ParsedMarkdown {
	// Strip BOM if present
	markdown = strings.TrimPrefix(markdown, "\xEF\xBB\xBF")

	match := frontmatterRegex.FindStringSubmatchIndex(markdown)
	if match == nil {
		return ParsedMarkdown{
			Frontmatter: make(map[string]any),
			Content:     markdown,
		}
	}

	frontmatterText := markdown[match[2]:match[3]]
	content := markdown[match[1]:]

	frontmatter, err := parseYAML(frontmatterText)
	if err != nil {
		// Retry with quoting problematic values
		// Source: frontmatterParser.ts:157-165
		quoted := quoteProblematicValues(frontmatterText)
		frontmatter, err = parseYAML(quoted)
		if err != nil {
			// Still failed — fail open
			// Source: frontmatterParser.ts:166-175
			location := ""
			if sourcePath != "" {
				location = " in " + sourcePath
			}
			slog.Warn("frontmatter: failed to parse YAML", "location", location, "error", err)
			return ParsedMarkdown{
				Frontmatter: make(map[string]any),
				Content:     content,
			}
		}
	}

	return ParsedMarkdown{
		Frontmatter: frontmatter,
		Content:     content,
	}
}

// parseYAML parses a YAML string into a map.
// Returns the parsed map or an error.
func parseYAML(text string) (map[string]any, error) {
	var result map[string]any
	if err := yaml.Unmarshal([]byte(text), &result); err != nil {
		return nil, err
	}
	if result == nil {
		return make(map[string]any), nil
	}
	return result, nil
}

// quoteProblematicValues pre-processes frontmatter text to quote values that
// contain special YAML characters.
// This allows glob patterns like **/*.{ts,tsx} to be parsed correctly.
//
// Source: frontmatterParser.ts:85-121 — quoteProblematicValues
func quoteProblematicValues(frontmatterText string) string {
	lines := strings.Split(frontmatterText, "\n")
	result := make([]string, 0, len(lines))

	// Match simple key: value lines (not indented, not list items, not block scalars)
	keyValueRe := regexp.MustCompile(`^([a-zA-Z_-]+):\s+(.+)$`)

	for _, line := range lines {
		match := keyValueRe.FindStringSubmatch(line)
		if len(match) == 3 {
			key := match[1]
			value := match[2]
			// regex guarantees key and value are non-empty

			// Skip if already quoted
			// Source: frontmatterParser.ts:100-106
			if (strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`)) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				result = append(result, line)
				continue
			}

			// Quote if contains special YAML characters
			// Source: frontmatterParser.ts:108-114
			if yamlSpecialChars.MatchString(value) {
				// Use double quotes and escape any existing double quotes
				escaped := strings.ReplaceAll(value, `\`, `\\`)
				escaped = strings.ReplaceAll(escaped, `"`, `\"`)
				result = append(result, fmt.Sprintf("%s: \"%s\"", key, escaped))
				continue
			}
		}

		result = append(result, line)
	}

	return strings.Join(result, "\n")
}
