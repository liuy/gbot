package wechat

import (
	"regexp"
	"strings"
	"unicode/utf8"
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
		maxContent := width - indent
		breakAt := strings.LastIndex(current[:maxContent], " ")
		if breakAt <= 0 {
			breakAt = runeBoundary(current, maxContent)
		}
		if breakAt <= 0 {
			breakAt = 1
		}
		result = append(result, prefix+current[:breakAt])
		current = strings.TrimLeft(current[breakAt:], " ")
	}
	if current != "" {
		result = append(result, prefix+current)
	}
	return result
}

func runeBoundary(s string, maxBytes int) int {
	if maxBytes >= len(s) {
		return len(s)
	}
	for i := maxBytes; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return i
		}
	}
	return 0
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

// wechatMaxMessageLen is the maximum message length WeChat accepts per send.
// iLink silently truncates or drops messages beyond this; we split proactively.
const wechatMaxMessageLen = 2000

// splitForWeChat splits a long formatted message into chunks, each fitting
// within WeChat's per-message limit. Split order: paragraph boundary → line
// boundary → hard rune split. Code blocks are kept together when possible;
// when a code block exceeds the limit, the fence is closed at chunk end and
// reopened (with the original language tag) at the next chunk start so each
// chunk renders as valid markdown.
func splitForWeChat(text string) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= wechatMaxMessageLen {
		return []string{text}
	}

	var chunks []string
	var current []string
	currentLen := 0
	inCodeBlock := false
	codeLang := "" // language tag of the current open code block

	flush := func() {
		if len(current) > 0 {
			chunk := strings.TrimSpace(strings.Join(current, "\n"))
			if chunk != "" {
				// If we're mid-code-block, close the fence so this chunk
				// renders standalone.
				if inCodeBlock {
					chunk += "\n```"
				}
				chunks = append(chunks, chunk)
			}
			current = nil
			currentLen = 0
		}
	}

	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		isFence := fenceRe.MatchString(trimmed)
		if isFence {
			if !inCodeBlock {
				// Opening fence — capture language tag for reopen.
				codeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			}
			inCodeBlock = !inCodeBlock
			// If we just closed a code block that was hard-split (current is
			// empty because flush ran in the hard-split path), skip emitting
			// a lone closing-fence chunk.
			if !inCodeBlock && len(current) == 0 && len(chunks) > 0 {
				continue
			}
		}

		lineRunes := []rune(line)
		lineLen := len(lineRunes) + 1 // +1 for newline

		// Hard-split lines with no break points that exceed the limit.
		if lineLen > wechatMaxMessageLen {
			flush()
			reopenPrefix := ""
			closeSuffix := ""
			if inCodeBlock {
				reopenPrefix = "```" + codeLang + "\n"
				closeSuffix = "\n```"
			}
			overhead := len([]rune(reopenPrefix)) + len([]rune(closeSuffix))
			budget := wechatMaxMessageLen - overhead
			if budget < 1 {
				budget = wechatMaxMessageLen / 2
			}
			for start := 0; start < len(lineRunes); start += budget {
				end := min(start+budget, len(lineRunes))
				chunks = append(chunks, reopenPrefix+string(lineRunes[start:end])+closeSuffix)
			}
			continue
		}

		if currentLen+lineLen > wechatMaxMessageLen && len(current) > 0 {
			flush()
			// Reopen code fence in the new chunk if we were mid-block.
			if inCodeBlock {
				reopen := "```" + codeLang
				current = append(current, reopen)
				currentLen = len([]rune(reopen)) + 1
			}
		}

		current = append(current, line)
		currentLen += lineLen
	}
	flush()

	return chunks
}

// formatMessage combines both normalization and wrapping for outbound messages.
func formatMessage(content string) string {
	if content == "" {
		return content
	}
	return wrapCopyFriendlyLines(normalizeMarkdownBlocks(content))
}
