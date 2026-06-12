package web

import (
	"fmt"
	"strings"
)

func formatForLLM(resp *SearchResponse) string {
	var parts []string

	if resp.Answer != "" {
		parts = append(parts, resp.Answer)
		if len(resp.Sources) > 0 {
			parts = append(parts, fmt.Sprintf("\n## Sources\n%d %s", len(resp.Sources), pluralWord(len(resp.Sources), "source")))
		}
	}

	for i, src := range resp.Sources {
		age := formatAge(src.AgeSeconds)
		if src.PublishedDate != "" && age == "" {
			age = src.PublishedDate
		}
		title := src.Title
		if age != "" {
			title = fmt.Sprintf("%s (%s)", src.Title, age)
		}
		parts = append(parts, fmt.Sprintf("[%d] %s\n    %s", i+1, title, src.URL))
		if src.Snippet != "" {
			snippet := src.Snippet
			if len(snippet) > 240 {
				snippet = truncateRunes(snippet, 239) + "…"
			}
			parts = append(parts, fmt.Sprintf("    %s", snippet))
		}
	}

	return strings.Join(parts, "\n")
}

func formatAge(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	days := seconds / 86400
	switch {
	case days < 1:
		return "just now"
	case days == 1:
		return "1d ago"
	case days < 30:
		return fmt.Sprintf("%dd ago", days)
	case days < 365:
		return fmt.Sprintf("%dmo ago", days/30)
	default:
		return fmt.Sprintf("%dy ago", days/365)
	}
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !isRuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}
