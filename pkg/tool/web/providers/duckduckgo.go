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
	ddgDefaultLimit = 10
	ddgUserAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	ddgTimeout      = 20 * time.Second
)

type DuckDuckGoProvider struct {
	Client *http.Client
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
	if params.Limit <= 0 {
		params.Limit = ddgDefaultLimit
	}

	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(params.Query))

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
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("request failed: %v", err),
			Status:   0,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return nil, &SearchProviderError{
			Provider: "duckduckgo",
			Message:  fmt.Sprintf("HTTP %d", resp.StatusCode),
			Status:   resp.StatusCode,
		}
	}

	sources, err := parseDDGHTML(resp.Body, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("ddg: parse: %w", err)
	}

	return &SearchResponse{
		Provider: "duckduckgo",
		Sources:  sources,
	}, nil
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
