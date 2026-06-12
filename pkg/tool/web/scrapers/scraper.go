// Package scrapers provides site-specific content handlers that extract
// structured markdown from well-known websites via their APIs.
//
// Each scraper is a Handler: given a URL, it checks if it can handle it
// (by hostname/path pattern), fetches via the site's API, and returns
// clean markdown. If a scraper can't handle the URL it returns (nil, nil),
// letting the caller fall through to the next handler or generic fetch.

package scrapers

import (
	"context"
	"net/http"
	"net/url"
)

// JSFetcher renders a URL with JavaScript and returns the rendered HTML.
// Used by scrapers that need chromedp (e.g. WeChat). May be nil.
type JSFetcher func(ctx context.Context, url string) (string, error)

// Handler extracts structured markdown from a supported website URL.
// Returns (nil, nil) if the URL is not handled by this scraper.
type Handler func(ctx context.Context, url *url.URL, client *http.Client, js JSFetcher) (*Result, error)

// Result is the scraped content as clean markdown.
type Result struct {
	Content     string
	ContentType string // "text/markdown"
	Method      string // e.g. "github-api", "wikipedia-api"
	Notes       []string
}

// Registry holds all registered scrapers.
// Iterate in order; the first Handler that returns non-nil wins.
type Registry struct {
	handlers []Handler
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) Register(h Handler) {
	r.handlers = append(r.handlers, h)
}

func (r *Registry) Try(ctx context.Context, url *url.URL, client *http.Client, js JSFetcher) (*Result, error) {
	for _, h := range r.handlers {
		result, err := h(ctx, url, client, js)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, nil
}
