package scrapers

import (
	"regexp"
	"strings"
)

var (
	scriptBlockRe   = regexp.MustCompile(`(?i)<script[\s\S]*?</script>`)
	styleBlockRe    = regexp.MustCompile(`(?i)<style[\s\S]*?</style>`)
	stripTagRe      = regexp.MustCompile(`<[^>]*>`)
	collapseNewline = regexp.MustCompile(`\n{3,}`)
)

func htmlToBasicMarkdown(html string) string {
	s := scriptBlockRe.ReplaceAllString(html, "")
	s = styleBlockRe.ReplaceAllString(s, "")

	s = strings.ReplaceAll(s, "<p>", "\n\n")
	s = strings.ReplaceAll(s, "</p>", "")
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")

	s = stripTagRe.ReplaceAllString(s, "")

	s = decodeHTMLEntities(s)

	s = collapseNewline.ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}

func decodeHTMLEntities(s string) string {
	repl := []struct{ old, new string }{
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&amp;", "&"},
		{"&quot;", "\""},
		{"&#039;", "'"},
		{"&#39;", "'"},
		{"&#x27;", "'"},
		{"&#x2F;", "/"},
		{"&nbsp;", " "},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}
