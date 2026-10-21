package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	ddgUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	ddgTimeout   = 20 * time.Second
	// The render phase gets its own window from the parent ctx: a cold
	// Chrome start (5-10s) inside the shared 20s fetch budget would
	// starve the render.
	ddgBrowseTimeout = 25 * time.Second
)

// DDGSearchURL is the cross-package test seam for the real-Chrome
// integration test in the web package.
var DDGSearchURL = "https://html.duckduckgo.com/html/"

type DuckDuckGoProvider struct {
	Client *http.Client

	// Browse renders a URL with headless Chrome. Nil = no escalation channel.
	Browse func(ctx context.Context, url string, timeout time.Duration) (string, error)
}

func (d *DuckDuckGoProvider) ID() string        { return "duckduckgo" }
func (d *DuckDuckGoProvider) IsAvailable() bool { return true }

func (d *DuckDuckGoProvider) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return http.DefaultClient
}

func (d *DuckDuckGoProvider) Search(ctx context.Context, params SearchParams) (*SearchResponse, error) {
	searchURL := DDGSearchURL + "?q=" + url.QueryEscape(params.Query)
	return d.searchWithClient(ctx, params, searchURL)
}

// searchViaBrowser renders searchURL headlessly and parses it with the
// same parser as the plain path. Fresh deadline from the parent ctx:
// the plain-fetch budget must not starve a cold Chrome start.
func (d *DuckDuckGoProvider) searchViaBrowser(ctx context.Context, searchURL string, params SearchParams) (*SearchResponse, error) {
	browseCtx, cancel := context.WithTimeout(ctx, ddgBrowseTimeout)
	defer cancel()

	html, err := d.Browse(browseCtx, searchURL, ddgBrowseTimeout)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("DuckDuckGo browser fallback failed: %v", err),
			Status:   503,
		}
	}
	if isAnomalyResponse(html) {
		return nil, ddgBlockedError()
	}
	sources, err := parseDDGHTML(strings.NewReader(html), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("ddg: parse: %w", err)
	}
	return &SearchResponse{Provider: "duckduckgo", Sources: sources}, nil
}

func (d *DuckDuckGoProvider) searchWithClient(ctx context.Context, params SearchParams, searchURL string) (*SearchResponse, error) {

	reqCtx, cancel := context.WithTimeout(ctx, ddgTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ddg: build request: %w", err)
	}
	req.Header.Set("User-Agent", ddgUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := d.client().Do(req)
	if err != nil {
		// Escalation is reserved for bot-block signals; a parent ctx that is
		// already canceled must surface, not spawn a doomed render.
		if d.Browse != nil && ctx.Err() == nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	// Read the body up front: the anomaly check needs it even on non-2xx,
	// so one read rule covers both branches.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("read response: %v", err),
			Status:   resp.StatusCode,
		}
	}
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		if d.Browse != nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("HTTP %d", resp.StatusCode),
			Status:   resp.StatusCode,
		}
	}

	if isAnomalyResponse(body) {
		if d.Browse != nil {
			return d.searchViaBrowser(ctx, searchURL, params)
		}
		return nil, ddgBlockedError()
	}

	sources, err := parseDDGHTML(strings.NewReader(body), params.Limit)
	if err != nil {
		return nil, fmt.Errorf("ddg: parse: %w", err)
	}

	return &SearchResponse{
		Provider: "duckduckgo",
		Sources:  sources,
	}, nil
}

// isAnomalyResponse reports whether the page DDG returned is the
// bot-challenge modal instead of real results. DDG mixes status codes
// (200 vs 202) on challenge pages, so the body check is the only
// reliable signal; empty results is a legitimate no-match outcome.
func isAnomalyResponse(html string) bool {
	return strings.Contains(html, "anomaly-modal") || strings.Contains(html, "anomaly.js")
}

// ddgBlockedError is shared by the plain and browser-escalation paths so the
// message never drifts between them.
func ddgBlockedError() *SearchProviderError {
	return &SearchProviderError{
		Provider: "duckduckgo",
		Message: "DuckDuckGo blocked the request with a bot-detection challenge. " +
			"DuckDuckGo throttles automated HTML searches from datacenter/shared-egress IPs; " +
			"configure a credentialed provider such as Jina or Tavily for reliable web search.",
		Status: 429,
	}
}

func parseDDGHTML(body io.Reader, limit int) ([]SearchSource, error) {
	doc, err := goquery.NewDocumentFromReader(body)
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}

	var sources []SearchSource
	doc.Find(".result").Each(func(i int, s *goquery.Selection) {
		if len(sources) >= limit {
			return
		}

		link := s.Find(".result__a")
		title := strings.TrimSpace(link.Text())
		href, _ := link.Attr("href")

		actualURL := extractDDGURL(href)

		snippetEl := s.Find(".result__snippet")
		snippet := strings.TrimSpace(snippetEl.Text())

		if title == "" && actualURL == "" {
			return
		}
		if title == "" {
			title = actualURL
		}

		sources = append(sources, SearchSource{
			Title:   title,
			URL:     actualURL,
			Snippet: snippet,
		})
	})

	return sources, nil
}

func extractDDGURL(href string) string {
	if href == "" {
		return ""
	}

	if strings.Contains(href, "uddg=") {
		u, err := url.Parse(href)
		if err != nil {
			return href
		}
		encoded := u.Query().Get("uddg")
		if encoded != "" {
			decoded, err := url.QueryUnescape(encoded)
			if err == nil {
				return decoded
			}
			return encoded
		}
	}

	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}

	return href
}
