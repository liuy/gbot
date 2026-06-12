package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
)

type goProxyResponse struct {
	Version string `json:"Version"`
	Time    string `json:"Time"`
}

// dataAttrPattern matches <tag data-test-id="UnitHeader-{field}"...>content</closeTag>
var dataAttrPattern = regexp.MustCompile(`data-test-id="UnitHeader-([^"]+)"[^>]*>([^<]*)<`)

func HandleGoPkg(ctx context.Context, u *neturl.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	if u.Hostname() != "pkg.go.dev" {
		return nil, nil
	}

	modulePath := strings.Trim(u.Path, "/")
	if modulePath == "" {
		return nil, nil
	}

	// Go proxy for version.
	proxyURL := fmt.Sprintf("https://proxy.golang.org/%s/@latest", neturl.PathEscape(modulePath))
	proxyData, _ := fetchBytes(ctx, client, proxyURL)
	var version, published string
	if proxyData != nil {
		var r goProxyResponse
		if json.Unmarshal(proxyData, &r) == nil {
			version = r.Version
			if len(r.Time) >= 10 {
				published = r.Time[:10]
			}
		}
	}

	// Fetch pkg.go.dev HTML.
	pageURL := fmt.Sprintf("https://pkg.go.dev/%s", modulePath)
	html, htmlErr := fetchBytes(ctx, client, pageURL)

	var license, imports, importedBy, synopsis, readme string
	var exports []string

	if htmlErr == nil {
		htmlStr := string(html)

		// Extract data from UnitHeader-* data-test-id markers.
		for _, match := range dataAttrPattern.FindAllStringSubmatch(htmlStr, -1) {
			if len(match) < 3 {
				continue
			}
			text := strings.TrimSpace(match[2])
			if text == "" {
				continue
			}
			switch match[1] {
			case "license":
				license = text
			case "imports":
				imports = text
			case "importedby":
				importedBy = text
			case "commitTime":
				// "Published: Feb 28, 2026 License: MIT"
				if published == "" && strings.Contains(text, "Published:") {
					ctx := text
					for _, c := range ctx {
						if c >= '0' && c <= '9' {
							break
						}
						ctx = ctx[1:]
					}
					if idx := strings.Index(ctx, " License:"); idx > 0 {
						published = ctx[:idx]
					}
				}
			}
		}

		// Synopsis: first meaningful paragraph in readme content.
		synopsis, readme, exports = extractReadmeContent(htmlStr)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", modulePath)

	if version != "" {
		fmt.Fprintf(&b, "**Version:** %s", version)
		if published != "" {
			fmt.Fprintf(&b, " · Published: %s", published)
		}
		fmt.Fprintf(&b, "\n")
	}
	if license != "" {
		fmt.Fprintf(&b, "**License:** %s\n", license)
	}
	if imports != "" {
		fmt.Fprintf(&b, "**Imports:** %s", imports)
		if importedBy != "" {
			fmt.Fprintf(&b, " · Imported by: %s", importedBy)
		}
		fmt.Fprintf(&b, "\n")
	}
	if len(exports) > 0 {
		fmt.Fprintf(&b, "**Exported:** %s\n", strings.Join(exports, ", "))
	}

	if synopsis != "" {
		fmt.Fprintf(&b, "\n%s\n", synopsis)
	}
	if readme != "" {
		fmt.Fprintf(&b, "\n---\n\n## Documentation\n\n%s\n", readme)
	}

	return &Result{
		Content:     b.String(),
		ContentType: "text/markdown",
		Method:      "gopkg-scraper",
	}, nil
}

// readmeMarkerRE matches the opening of the readme section.
var readmeMarkerRE = regexp.MustCompile(`data-test-id="Unit-readmeContent">`)

// hrefExportsRE matches <a href="#FuncName"> in the readme section.
var hrefExportsRE = regexp.MustCompile(`<a\s+href="#([^"]+)"[^>]*>([^<]+)</a>`)

func extractReadmeContent(htmlStr string) (synopsis, readme string, exports []string) {
	loc := readmeMarkerRE.FindStringIndex(htmlStr)
	if loc == nil {
		return "", "", nil
	}

	content := htmlStr[loc[1]:]
	if len(content) > 80000 {
		content = content[:80000]
	}

	// Extract exported identifiers (links with href="#...").
	exportSeen := make(map[string]bool)
	for _, m := range hrefExportsRE.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		name := strings.TrimSpace(m[2])
		if name != "" && !exportSeen[name] && name[0] >= 'A' && name[0] <= 'Z' {
			exports = append(exports, name)
			exportSeen[name] = true
		}
	}
	if len(exports) > 25 {
		exports = exports[:25]
	}

	// Extract text content: first get synopsis (first meaningful paragraph),
	// then get first chunk of readme.
	text := stripHTMLTagsSimple(content)
	text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	text = strings.TrimSpace(text)

	// Synopsis: first paragraph up to first \n\n.
	if idx := strings.Index(text, "\n\n"); idx > 0 {
		synopsis = text[:idx]
		text = text[idx+2:]
	} else if len(text) > 200 {
		synopsis = text[:200]
		text = text[200:]
	} else {
		synopsis = text
		text = ""
	}

	// Readme: first 2000 chars of remaining content.
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 2000 {
		readme = string(runes[:2000]) + "..."
	} else {
		readme = string(runes)
	}

	return synopsis, readme, exports
}

// stripHTMLTagsSimple removes HTML tags and converts common elements to text.
func stripHTMLTagsSimple(s string) string {
	// Replace block elements with newlines.
	s = regexp.MustCompile(`(?i)</?(?:p|div|section|article|h[1-6]|pre|li|ul|ol|table|tr|hr)[^>]*>`).ReplaceAllString(s, "\n")
	// Replace br with newline.
	s = regexp.MustCompile(`(?i)<br\s*/?>`).ReplaceAllString(s, "\n")
	// Remove remaining tags.
	s = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(s, "")
	// Decode entities.
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&#x27;", "'")
	s = strings.ReplaceAll(s, "&#x2F;", "/")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	// Collapse whitespace but keep paragraph breaks.
	s = regexp.MustCompile(`\n{3,}`).ReplaceAllString(s, "\n\n")
	s = regexp.MustCompile(`[^\S\n]+`).ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return s
}

// stripHTMLTags removes HTML tags and decodes basic entities, returning
// a single-line string. Used by cratesio for the README section.
func stripHTMLTags(s string) string {
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
