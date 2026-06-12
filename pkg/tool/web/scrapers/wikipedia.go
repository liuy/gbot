package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func HandleWikipedia(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	// Match *.wikipedia.org
	hostname := u.Hostname()
	if !strings.HasSuffix(hostname, ".wikipedia.org") {
		return nil, nil
	}

	path := u.Path
	if !strings.HasPrefix(path, "/wiki/") {
		return nil, nil
	}

	lang := strings.TrimSuffix(hostname, ".wikipedia.org")
	if lang == "" {
		return nil, nil
	}
	title := strings.TrimPrefix(path, "/wiki/")
	title = decodeURL(title)

	// Fetch summary via Wikipedia REST API.
	summaryURL := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s", lang, url.PathEscape(title))
	summaryData, err := fetchJSON(ctx, client, summaryURL)
	if err != nil {
		return nil, nil
	}

	var summary struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Extract     string `json:"extract"`
	}
	if err := json.Unmarshal(summaryData, &summary); err != nil {
		return nil, nil
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# %s\n\n", summary.Title)
	if summary.Description != "" {
		fmt.Fprintf(&md, "*%s*\n\n", summary.Description)
	}
	md.WriteString(summary.Extract)
	md.WriteString("\n\n---\n\n")

	// Fetch full article content via mobile-html API.
	contentURL := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/mobile-html/%s", lang, url.PathEscape(title))
	contentData, err := fetchBytes(ctx, client, contentURL)
	if err != nil {
		// Summary is enough.
		return &Result{
			Content:     md.String(),
			ContentType: "text/markdown",
			Method:      "wikipedia-api",
			Notes:       []string{"Fetched via Wikipedia API (summary only)"},
		}, nil
	}

	// Simple extraction: grab section headings and paragraphs via text scanning.
	// We strip HTML tags and extract meaningful sections.
	content := stripHTMLAndExtract(contentData)

	md.WriteString(content)

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "wikipedia-api",
		Notes:       []string{"Fetched via Wikipedia API"},
	}, nil
}

// decodeURL decodes a URL-encoded string (e.g. "Tea_Party" -> "Tea Party").
func decodeURL(s string) string {
	decoded := strings.ReplaceAll(s, "_", " ")
	if d, err := url.QueryUnescape(decoded); err == nil {
		return d
	}
	return decoded
}

// stripHTMLAndExtract does a basic extraction of headings and paragraphs from HTML.
func stripHTMLAndExtract(data []byte) string {
	html := string(data)

	var result strings.Builder

	// Extract <section>...</section> blocks and parse heading + paragraphs.
	sections := extractSections(html)
	for _, section := range sections {
		heading := extractTagContent(section, "h2")
		if heading == "" {
			heading = extractTagContent(section, "h3")
		}
		if heading != "" {
			// Skip reference sections.
			headingText := strings.TrimSpace(heading)
			skipSections := []string{"References", "External links", "See also", "Notes", "Further reading", "Sources"}
			shouldSkip := false
			for _, s := range skipSections {
				if strings.EqualFold(headingText, s) || strings.HasPrefix(strings.ToLower(headingText), strings.ToLower(s)) {
					shouldSkip = true
					break
				}
			}
			if shouldSkip {
				continue
			}

			level := "##"
			if strings.Contains(section, "<h3") {
				level = "###"
			}
			fmt.Fprintf(&result, "%s %s\n\n", level, headingText)
		}

		// Extract paragraphs.
		paragraphs := extractAllTagContents(section, "p")
		for _, p := range paragraphs {
			p = strings.TrimSpace(p)
			if len(p) > 20 { // Skip short fragments.
				result.WriteString(p)
				result.WriteString("\n\n")
			}
		}
	}

	return result.String()
}

// extractTagContent finds the first occurrence of a tag and returns its text content.
func extractTagContent(html, tag string) string {
	openTag := "<" + tag
	closeTag := "</" + tag + ">"

	start := strings.Index(html, openTag)
	if start < 0 {
		return ""
	}
	// Skip past opening tag.
	rest := html[start:]
	end := strings.Index(rest, closeTag)
	if end < 0 {
		return ""
	}
	// Find the start of content (after the opening >).
	contentStart := strings.IndexByte(rest, '>')
	if contentStart < 0 {
		return ""
	}
	contentStart++ // past '>'
	if contentStart >= end {
		return ""
	}
	content := rest[contentStart:end]
	// Strip any tags in content.
	return strings.TrimSpace(stripTags(content))
}

// extractAllTagContents finds all occurrences of a tag and returns their text content.
func extractAllTagContents(html, tag string) []string {
	var results []string
	remaining := html
	for {
		content := extractTagContent(remaining, tag)
		if content == "" {
			break
		}
		results = append(results, content)
		// Advance past this tag.
		closeTag := "</" + tag + ">"
		idx := strings.Index(remaining, closeTag)
		if idx < 0 {
			break
		}
		remaining = remaining[idx+len(closeTag):]
	}
	return results
}

// extractSections splits HTML into section elements.
func extractSections(html string) []string {
	var sections []string
	remaining := html
	for {
		start := strings.Index(remaining, "<section")
		if start < 0 {
			break
		}
		rest := remaining[start:]
		end := strings.Index(rest, "</section>")
		if end < 0 {
			break
		}
		section := rest[:end+len("</section>")]
		sections = append(sections, section)
		remaining = rest[end+len("</section>"):]
	}
	if len(sections) == 0 {
		// Fallback: whole body.
		body := extractTagContent(html, "body")
		if body != "" {
			sections = append(sections, body)
		}
	}
	return sections
}

// stripTags removes HTML tags, keeping only text content.
func stripTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}
