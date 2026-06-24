package wechat

import (
	"regexp"
	"strings"
)

// WeChat copy-friendly line width.
const weixinCopyLineWidth = 120

var (
	fenceRe     = regexp.MustCompile("^```([^\n`]*)\\s*$")
	tableRuleRe = regexp.MustCompile(`^\s*\|?(?:\s*:?-{3,}:?\s*\|)+\s*:?-{3,}:?\s*\|?\s*$`)
)

// normalizeMarkdownBlocks deduplicates blank lines in markdown.
// Code blocks are preserved as-is; outside code blocks, multiple consecutive
// blank lines are collapsed into at most one blank line.
func normalizeMarkdownBlocks(content string) string {
	if content == "" {
		return content
	}

	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	inCodeBlock := false
	blankRun := 0

	for _, rawLine := range lines {
		line := strings.TrimRight(rawLine, " \t\r")

		if fenceRe.MatchString(strings.TrimSpace(line)) {
			inCodeBlock = !inCodeBlock
			result = append(result, line)
			blankRun = 0
			continue
		}

		if inCodeBlock {
			result = append(result, line)
			continue
		}

		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun <= 1 {
				result = append(result, "")
			}
			continue
		}

		blankRun = 0
		result = append(result, line)
	}

	return strings.TrimSpace(strings.Join(result, "\n"))
}

// wrapLine wraps a single line at the given width, preserving leading
// whitespace and not breaking words.
func wrapLine(line string, width int) []string {
	trimmed := strings.TrimLeft(line, " \t")
	prefix := line[:len(line)-len(trimmed)]
	indent := len(prefix)

	if len(line) <= width || len(trimmed) == 0 {
		return []string{line}
	}

	var result []string
	current := trimmed
	for len(current)+indent > width {
		// Find a good break point: try to break at a space
		maxContent := width - indent
		breakAt := strings.LastIndex(current[:maxContent], " ")
		if breakAt <= 0 {
			// No space found, force break at maxContent
			breakAt = maxContent
		}
		result = append(result, prefix+current[:breakAt])
		// Skip the space
		current = strings.TrimLeft(current[breakAt:], " ")
	}
	if current != "" {
		result = append(result, prefix+current)
	}
	return result
}

// wrapCopyFriendlyLines wraps long display lines that are hard to copy in
// WeChat clients. Code blocks and table rows are preserved as-is.
func wrapCopyFriendlyLines(content string) string {
	if content == "" {
		return content
	}

	wrapped := make([]string, 0, len(strings.Split(content, "\n")))
	inCodeBlock := false

	for rawLine := range strings.SplitSeq(content, "\n") {
		line := strings.TrimRight(rawLine, " \t\r")
		stripped := strings.TrimSpace(line)

		if fenceRe.MatchString(stripped) {
			inCodeBlock = !inCodeBlock
			wrapped = append(wrapped, line)
			continue
		}

		if inCodeBlock ||
			len(line) <= weixinCopyLineWidth ||
			stripped == "" ||
			strings.HasPrefix(stripped, "|") ||
			tableRuleRe.MatchString(stripped) {
			wrapped = append(wrapped, line)
			continue
		}

		wrappedLines := wrapLine(line, weixinCopyLineWidth)
		wrapped = append(wrapped, wrappedLines...)
	}

	return strings.TrimSpace(strings.Join(wrapped, "\n"))
}

// formatMessage combines both normalization and wrapping for outbound messages.
func formatMessage(content string) string {
	if content == "" {
		return content
	}
	return wrapCopyFriendlyLines(normalizeMarkdownBlocks(content))
}
