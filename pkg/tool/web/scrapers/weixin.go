package scrapers

import (
	"context"
	"fmt"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
)

var weixinImageRe = regexp.MustCompile(`<img[^>]+data-src="([^"]+)"[^>]*>`)
var weixinImgFallbackRe = regexp.MustCompile(`<img[^>]+src="(https?://[^"]+)"[^>]*>`)
var weixinBlockTags = regexp.MustCompile(`(?i)(?:</?(?:p|section|h[1-6]|pre|ul|ol|li|blockquote|img|hr|br\s*/?)\b[^>]*>)`)

func HandleWeixin(ctx context.Context, u *neturl.URL, client *http.Client, js JSFetcher) (*Result, error) {
	host := u.Hostname()
	if !strings.HasSuffix(host, "mp.weixin.qq.com") {
		return nil, nil
	}
	if js == nil {
		return nil, nil
	}

	rendered, err := js(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("weixin fetch: %w", err)
	}

	title := extractByIDText(rendered, "activity-name")
	if title == "" {
		return nil, nil
	}
	author := extractByIDText(rendered, "js_name")
	publish := extractByIDText(rendered, "publish_time")

	bodyHTML := extractByIDInner(rendered, "js_content")
	if bodyHTML == "" {
		return nil, nil
	}

	md := weixinToMarkdown(bodyHTML)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	if author != "" {
		fmt.Fprintf(&b, "**Author:** %s\n", author)
	}
	if publish != "" {
		fmt.Fprintf(&b, "**Published:** %s\n", publish)
	}
	fmt.Fprintf(&b, "**Source:** %s\n\n", u.String())
	fmt.Fprintf(&b, "---\n\n%s\n", md)

	return &Result{
		Content:     b.String(),
		ContentType: "text/markdown",
		Method:      "weixin-js",
		Notes:       []string{"Fetched via headless Chrome (JS rendering + stealth)"},
	}, nil
}

// extractByIDText returns the text content of the first element with the given id.
func extractByIDText(html, id string) string {
	inner := extractByIDInner(html, id)
	if inner == "" {
		return ""
	}
	return strings.TrimSpace(stripXMLTags(inner))
}

// extractByIDInner returns the inner HTML of the first element with the given id.
func extractByIDInner(html, id string) string {
	marker := fmt.Sprintf(`id="%s"`, id)
	start := strings.Index(html, marker)
	if start < 0 {
		return ""
	}
	// Rewind to the opening < of this tag.
	tagStart := strings.LastIndex(html[:start], "<")
	if tagStart < 0 {
		return ""
	}
	// Find closing > of this tag.
	gt := strings.IndexByte(html[start:], '>')
	if gt < 0 {
		return ""
	}
	after := start + gt + 1

	// Determine tag name.
	tagSlice := html[tagStart : start+gt+1]
	tagName := "div"
	if sp := strings.IndexByte(tagSlice, ' '); sp > 0 {
		tagName = tagSlice[1:sp]
	} else if g := strings.IndexByte(tagSlice, '>'); g > 0 {
		tagName = tagSlice[1:g]
	}

	end := findCloseTag(html, after, tagName)
	if end < 0 {
		return ""
	}
	return html[after:end]
}

// findCloseTag finds the matching closing tag for an opening tag at `from`.
// Tracks depth for the exact tag name.
func findCloseTag(html string, from int, tag string) int {
	openMarker := "<" + tag
	closeMarker := "</" + tag + ">"
	depth := 1
	pos := from

	for depth > 0 && pos < len(html)-len(closeMarker) {
		lt := strings.IndexByte(html[pos:], '<')
		if lt < 0 {
			return -1
		}
		lt += pos

		// Skip comments.
		if lt+3 < len(html) && html[lt:lt+4] == "<!--" {
			end := strings.Index(html[lt:], "-->")
			if end < 0 {
				return -1
			}
			pos = lt + end + 3
			continue
		}

		gt := strings.IndexByte(html[lt:], '>')
		if gt < 0 {
			return -1
		}
		tagBody := html[lt : lt+gt+1]
		pos = lt + gt + 1

		if strings.HasSuffix(tagBody, "/>") {
			continue
		}

		if strings.HasPrefix(tagBody, closeMarker) {
			depth--
			continue
		}

		// Check it's the exact opening tag (not a prefix).
		if strings.HasPrefix(tagBody, openMarker) {
			suffix := tagBody[len(openMarker):]
			if len(suffix) == 0 || suffix[0] == ' ' || suffix[0] == '>' {
				depth++
				continue
			}
		}
	}
	if depth != 0 {
		return -1
	}
	return pos
}

// weixinToMarkdown converts Weixin article HTML to clean markdown.
func weixinToMarkdown(html string) string {
	html = weixinBlockTags.ReplaceAllString(html, "\n\n")
	html = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(html, "\n")
	html = weixinImageRe.ReplaceAllString(html, "\n\n![image]($1)\n\n")
	html = weixinImgFallbackRe.ReplaceAllString(html, "\n\n![image]($1)\n\n")
	html = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(html, "")
	// Decode entities.
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&ldquo;", "\u201c")
	html = strings.ReplaceAll(html, "&rdquo;", "\u201d")
	html = strings.ReplaceAll(html, "&lsquo;", "\u2018")
	html = strings.ReplaceAll(html, "&rsquo;", "\u2019")
	html = strings.ReplaceAll(html, "&mdash;", "\u2014")
	html = strings.ReplaceAll(html, "&ndash;", "\u2013")
	html = strings.ReplaceAll(html, "&hellip;", "\u2026")
	// Normalize whitespace.
	html = regexp.MustCompile(`[ \t]+\n`).ReplaceAllString(html, "\n")
	html = regexp.MustCompile(`\n{3,}`).ReplaceAllString(html, "\n\n")
	return strings.TrimSpace(html)
}

// stripTags removes HTML tags.
func stripXMLTags(s string) string {
	tagRe := regexp.MustCompile(`<[^>]+>`)
	return strings.TrimSpace(tagRe.ReplaceAllString(s, " "))
}
