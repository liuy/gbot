package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// hnItem maps the Firebase API response for a single Hacker News item.
type hnItem struct {
	ID          int    `json:"id"`
	Deleted     bool   `json:"deleted"`
	Type        string `json:"type"`
	By          string `json:"by"`
	Time        int64  `json:"time"`
	Text        string `json:"text"`
	Dead        bool   `json:"dead"`
	Kids        []int  `json:"kids"`
	URL         string `json:"url"`
	Score       int    `json:"score"`
	Title       string `json:"title"`
	Descendants int    `json:"descendants"`
}

const hnBaseURL = "https://hacker-news.firebaseio.com/v0"

func HandleHackerNews(ctx context.Context, u *url.URL, client *http.Client, _ JSFetcher) (*Result, error) {
	hostname := u.Hostname()
	if !strings.Contains(hostname, "news.ycombinator.com") {
		return nil, nil
	}

	path := u.Path
	query := u.Query()

	// Case 1: Item page — /item?id=NNN
	if path == "/item" && query.Has("id") {
		itemID := query.Get("id")
		return handleHNItem(ctx, client, itemID)
	}

	// Case 2: /newest -> newstories.json
	if path == "/newest" {
		return handleHNListing(ctx, client, "newstories.json", "New Stories")
	}

	// Case 3: /best -> beststories.json
	if path == "/best" {
		return handleHNListing(ctx, client, "beststories.json", "Best Stories")
	}

	// Case 4: front page (/ or /news) -> topstories.json
	if path == "/" || path == "/news" || path == "" {
		return handleHNListing(ctx, client, "topstories.json", "Top Stories")
	}

	return nil, nil
}

// handleHNItem fetches a single item by ID and renders it with top-level comments.
func handleHNItem(ctx context.Context, client *http.Client, itemID string) (*Result, error) {
	// Verify the ID is numeric.
	if _, err := strconv.Atoi(itemID); err != nil {
		return nil, nil
	}

	itemURL := fmt.Sprintf("%s/item/%s.json", hnBaseURL, itemID)
	data, err := fetchJSON(ctx, client, itemURL)
	if err != nil {
		return nil, fmt.Errorf("fetch HN item: %w", err)
	}

	var item hnItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, fmt.Errorf("parse HN item: %w", err)
	}

	if item.Deleted || item.Dead {
		return &Result{
			Content:     "[deleted]",
			ContentType: "text/markdown",
			Method:      "hackernews-api",
			Notes:       []string{"Item is deleted or dead"},
		}, nil
	}

	var md strings.Builder

	// Title — if the story has no URL of its own, link to HN.
	storyURL := item.URL
	if storyURL == "" {
		storyURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", item.ID)
	}
	fmt.Fprintf(&md, "# [%s](%s)\n\n", item.Title, storyURL)

	// Metadata line.
	relativeTime := formatRelativeTime(item.Time)
	fmt.Fprintf(&md, "**Posted by:** %s | **Score:** %d | **Time:** %s\n", item.By, item.Score, relativeTime)
	fmt.Fprintf(&md, "**Comments:** %d\n\n", item.Descendants)

	// Story text, if any.
	if item.Text != "" {
		md.WriteString(decodeHNText(item.Text))
		md.WriteString("\n\n")
	}

	md.WriteString("---\n\n## Comments\n\n")

	// Fetch top-level comments in parallel.
	if len(item.Kids) > 0 {
		comments := fetchHNCommentsParallel(ctx, client, item.Kids)
		for _, comment := range comments {
			md.WriteString(comment)
			md.WriteString("\n\n---\n\n")
		}
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "hackernews-api",
		Notes:       []string{"Fetched via Hacker News Firebase API"},
	}, nil
}

// fetchHNCommentsParallel fetches multiple HN comments concurrently using goroutines.
func fetchHNCommentsParallel(ctx context.Context, client *http.Client, kids []int) []string {
	type commentResult struct {
		text string
		err  error
	}
	results := make([]chan commentResult, len(kids))
	for i, kidID := range kids {
		ch := make(chan commentResult, 1)
		results[i] = ch
		go func(id int, c chan commentResult) {
			text, err := fetchHNComment(ctx, client, id)
			c <- commentResult{text, err}
		}(kidID, ch)
	}
	var comments []string
	for _, ch := range results {
		r := <-ch
		if r.err == nil && r.text != "" {
			comments = append(comments, r.text)
		}
	}
	return comments
}

// fetchHNComment fetches a single HN item and renders it as a comment block.
func fetchHNComment(ctx context.Context, client *http.Client, id int) (string, error) {
	itemURL := fmt.Sprintf("%s/item/%d.json", hnBaseURL, id)
	data, err := fetchJSON(ctx, client, itemURL)
	if err != nil {
		return "", err
	}

	var item hnItem
	if err := json.Unmarshal(data, &item); err != nil {
		return "", err
	}

	if item.Deleted || item.Dead || item.Type != "comment" {
		return "", nil
	}

	var md strings.Builder
	relativeTime := formatRelativeTime(item.Time)
	fmt.Fprintf(&md, "**%s** (%s)\n\n", item.By, relativeTime)
	md.WriteString(decodeHNText(item.Text))

	return md.String(), nil
}

// handleHNListing fetches a story list (top/new/best) and renders the top 20.
func handleHNListing(ctx context.Context, client *http.Client, endpoint string, label string) (*Result, error) {
	listURL := fmt.Sprintf("%s/%s", hnBaseURL, endpoint)
	data, err := fetchJSON(ctx, client, listURL)
	if err != nil {
		return nil, fmt.Errorf("fetch HN listing: %w", err)
	}

	var ids []int
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("parse HN listing: %w", err)
	}

	limit := 20
	if len(ids) > limit {
		ids = ids[:limit]
	}

	var md strings.Builder
	fmt.Fprintf(&md, "# Hacker News - %s\n\n", label)

	for i, id := range ids {
		itemURL := fmt.Sprintf("%s/item/%d.json", hnBaseURL, id)
		itemData, err := fetchJSON(ctx, client, itemURL)
		if err != nil {
			continue
		}

		var item hnItem
		if err := json.Unmarshal(itemData, &item); err != nil {
			continue
		}

		if item.Deleted || item.Dead {
			continue
		}

		title := item.Title
		if title == "" {
			title = "[deleted]"
		}

		relativeTime := formatRelativeTime(item.Time)

		fmt.Fprintf(&md, "%d. **%s**\n", i+1, title)
		storyURL := item.URL
		if storyURL == "" {
			storyURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", item.ID)
		}
		fmt.Fprintf(&md, "   %s\n", storyURL)
		fmt.Fprintf(&md, "   %d points by %s | %s | %d comments\n", item.Score, item.By, relativeTime, item.Descendants)
		fmt.Fprintf(&md, "   https://news.ycombinator.com/item?id=%d\n\n", item.ID)
	}

	return &Result{
		Content:     md.String(),
		ContentType: "text/markdown",
		Method:      "hackernews-api",
		Notes:       []string{fmt.Sprintf("Fetched via Hacker News Firebase API (%s)", label)},
	}, nil
}

// decodeHNText converts simple HN HTML tags to Markdown equivalents.
// HN text uses: <p>, <pre><code>, <i>, <a href="...">text</a>
func decodeHNText(s string) string {
	// Replace <p> with double newline (paragraph break).
	s = strings.ReplaceAll(s, "<p>", "\n\n")
	s = strings.ReplaceAll(s, "</p>", "")

	// Replace <i> and </i> with * for italic.
	s = strings.ReplaceAll(s, "<i>", "*")
	s = strings.ReplaceAll(s, "</i>", "*")

	// Replace <pre><code> blocks with fenced code blocks.
	s = strings.ReplaceAll(s, "<pre><code>", "\n```\n")
	s = strings.ReplaceAll(s, "</code></pre>", "\n```\n")
	// Handle standalone <code> tags (inline).
	s = strings.ReplaceAll(s, "<code>", "`")
	s = strings.ReplaceAll(s, "</code>", "`")

	// Replace <a href="URL">text</a> with [text](URL).
	s = replaceAnchorTags(s)

	// Collapse multiple consecutive newlines into at most two.
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(s)
}

// replaceAnchorTags converts <a href="URL">text</a> to [text](URL).
func replaceAnchorTags(s string) string {
	var result strings.Builder
	remaining := s
	for {
		start := strings.Index(remaining, "<a ")
		if start < 0 {
			result.WriteString(remaining)
			break
		}
		result.WriteString(remaining[:start])
		remaining = remaining[start:]

		// Extract href value.
		hrefStart := strings.Index(remaining, `href="`)
		if hrefStart < 0 {
			// Malformed anchor, just strip the opening tag.
			result.WriteString(remaining[len("<a "):])
			break
		}
		hrefStart += len(`href="`)
		hrefEnd := strings.Index(remaining[hrefStart:], `"`)
		if hrefEnd < 0 {
			result.WriteString(remaining[len("<a "):])
			break
		}
		href := remaining[hrefStart : hrefStart+hrefEnd]

		// Find closing </a>.
		closeTag := "</a>"
		closeIdx := strings.Index(remaining, closeTag)
		if closeIdx < 0 {
			result.WriteString(remaining[len("<a "):])
			break
		}

		// Extract text between > and </a>.
		gtIdx := strings.IndexByte(remaining, '>')
		if gtIdx < 0 || gtIdx >= closeIdx {
			result.WriteString(remaining)
			break
		}
		text := remaining[gtIdx+1 : closeIdx]

		fmt.Fprintf(&result, "[%s](%s)", text, href)
		remaining = remaining[closeIdx+len(closeTag):]
	}
	return result.String()
}

// formatRelativeTime converts a Unix timestamp to a relative time string.
func formatRelativeTime(unixTime int64) string {
	t := time.Unix(unixTime, 0)
	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins < 1 {
			return "0m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	case diff < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}
