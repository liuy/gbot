package web

import (
	"net/http"
	"os"

	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
)

// searchChain returns a search chain pre-populated with built-in
// providers (zhipu if ZHIPU_API_KEY is set, duckduckgo as fallback).
func searchChain(client *http.Client) *providers.SearchChain {
	var ps []providers.SearchProvider
	if key := os.Getenv("ZHIPU_API_KEY"); key != "" {
		ps = append(ps, &providers.ZhipuProvider{Client: client, APIKey: key})
	}
	ps = append(ps, &providers.DuckDuckGoProvider{Client: client})
	return &providers.SearchChain{Providers: ps}
}

// scraperRegistry returns a registry pre-populated with all built-in scrapers.
func scraperRegistry() *scrapers.Registry {
	r := scrapers.New()
	r.Register(scrapers.HandleWikipedia)
	r.Register(scrapers.HandleStackOverflow)
	r.Register(scrapers.HandleHackerNews)
	r.Register(scrapers.HandleArxiv)
	r.Register(scrapers.HandleGitHub)
	r.Register(scrapers.HandleNpm)
	r.Register(scrapers.HandlePyPI)
	r.Register(scrapers.HandleCratesIo)
	r.Register(scrapers.HandleGoPkg)
	r.Register(scrapers.HandleWeixin)
	r.Register(scrapers.HandleHuggingFace)
	return r
}
