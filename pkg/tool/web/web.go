package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
	"os"
)

const defaultLimit = 10

type Input struct {
	Query    string `json:"query"`
	Provider string `json:"provider,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	JS       bool   `json:"js,omitempty"`
}

type Output struct {
	Mode    string
	Content string
	Raw     *providers.SearchResponse
}

// New constructs the web tool with default search providers and scrapers.
// Pass nil to use http.DefaultClient.
func New(client *http.Client) tool.Tool {
	if client == nil {
		client = http.DefaultClient
	}
	chain := searchChain(client)
	reg := scraperRegistry()

	avail := chain.AvailableProviders()
	providerDesc := `Search provider. \"auto\" uses the first available provider.`
	if len(avail) > 0 {
		providerDesc = fmt.Sprintf(`Search provider. Available: auto, %s. Default: auto.`, strings.Join(avail, ", "))
	}

	schema := json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query or URL to fetch. Auto-detects: URLs are fetched, other text triggers search."
			},
			"provider": {
				"type": "string",
				"description": "%s"
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of search results. Default: 10."
			},
			"js": {
				"type": "boolean",
				"description": "Fetch URL with headless Chrome (JS rendering + stealth). Use when the page requires JavaScript or returns empty/blocked content."
			}
		}
	}`, providerDesc))

	return tool.BuildTool(tool.ToolDef{
		Name_:   "Web",
		Aliases_: []string{"web"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Search the web or fetch URLs", nil
			}
			return in.Query, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, chain, client, reg)
		},
		IsReadOnly_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            webPrompt(),
		RenderResult_: func(data any) string {
			switch v := data.(type) {
			case *Output:
				return v.Content
			case string:
				return v
			default:
				b, _ := json.Marshal(data)
				return string(b)
			}
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsSearch: true}
		},
	})
}

func execute(ctx context.Context, input json.RawMessage, chain *providers.SearchChain, client *http.Client, reg *scrapers.Registry) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if IsURL(in.Query) {
		return executeFetch(ctx, in.Query, in.JS, client, reg)
	}

	return executeSearch(ctx, in, chain)
}

func executeSearch(ctx context.Context, in Input, chain *providers.SearchChain) (*tool.ToolResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	params := providers.SearchParams{
		Query: in.Query,
		Limit: limit,
	}

	resp, err := chain.SearchWithProvider(ctx, params, in.Provider)
	if err != nil {
		return nil, fmt.Errorf("web search failed: %w", err)
	}

	content := formatForLLM(resp)

	return &tool.ToolResult{
		Data: &Output{
			Mode:    "search",
			Content: content,
			Raw:     resp,
		},
	}, nil
}

func extractProxyURL(client *http.Client) string {
	if client == nil || client.Transport == nil {
		return ""
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		return ""
	}
	req, err := http.NewRequest("GET", "https://example.com", nil)
	if err != nil {
		return ""
	}
	p, err := tr.Proxy(req)
	if err != nil || p == nil {
		return ""
	}
	return p.String()
}

func executeFetch(ctx context.Context, url string, js bool, client *http.Client, reg *scrapers.Registry) (*tool.ToolResult, error) {
	// Explicit JS mode: skip scrapers, go straight to chromedp.
	if js {
		slog.Info("web:fetch JS mode", "url", url)
		return fetchWithChrome(ctx, url, client)
	}

	// Try site-specific scrapers first.
	jsFetcher := func(ctx context.Context, u string) (string, error) {
		return chromedpFetch(ctx, u, 25*time.Second, extractProxyURL(client))
	}
	if reg != nil {
		parsed, err := neturl.Parse(url)
		if err == nil {
			result, err := reg.Try(ctx, parsed, client, jsFetcher)
			if err != nil {
				return nil, err
			}
			if result != nil {
				slog.Info("web:fetch scraper matched", "method", result.Method, "url", url)
				notes := result.Notes
				if notes == nil {
					notes = []string{}
				}
				fetchResult := BuildResult(result.Content, FetchResultOptions{
					URL:         url,
					FinalURL:    url,
					ContentType: result.ContentType,
					Notes:       notes,
				})
				return &tool.ToolResult{
					Data: &Output{
						Mode:    "fetch",
						Content: fetchResult.Content,
					},
				}, nil
			}
		}
	}

	result := LoadPage(ctx, url, LoadPageOptions{Client: client})

	if result.Error != "" {
		slog.Info("web:fetch failed", "url", url, "status", result.Status, "error", result.Error)
		return nil, fmt.Errorf("fetch failed: %s", result.Error)
	}

	slog.Info("web:fetch completed via HTTP", "url", url, "status", result.Status, "content_len", len(result.Content))

	content := result.Content
	if LooksLikeHTML(content) {
		md, err := HTMLToMarkdown(content)
		if err == nil {
			content = md
		}
	}

	var notes []string
	if result.FinalURL != url {
		notes = append(notes, fmt.Sprintf("redirected to %s", result.FinalURL))
	}
	if result.Truncated {
		notes = append(notes, "output truncated")
	}

	fetchResult := BuildResult(content, FetchResultOptions{
		URL:         url,
		FinalURL:    result.FinalURL,
		ContentType: result.ContentType,
		Notes:       notes,
	})

	return &tool.ToolResult{
		Data: &Output{
			Mode:    "fetch",
			Content: fetchResult.Content,
		},
	}, nil
}

func fetchWithChrome(ctx context.Context, url string, client *http.Client) (*tool.ToolResult, error) {
	html, err := chromedpFetch(ctx, url, 20*time.Second, extractProxyURL(client))
	if err != nil {
		return nil, fmt.Errorf("JS fetch failed: %w", err)
	}

	content := html
	if LooksLikeHTML(content) {
		md, mdErr := HTMLToMarkdown(content)
		if mdErr == nil {
			content = md
		}
	}

	// BuildResult already calls FinalizeOutput internally.
	fetchResult := BuildResult(content, FetchResultOptions{
		URL:         url,
		FinalURL:    url,
		ContentType: "text/html",
		Notes:       []string{"fetched with JS rendering"},
	})

	slog.Info("web:fetch JS mode succeeded", "url", url, "content_len", len(fetchResult.Content))
	return &tool.ToolResult{
		Data: &Output{
			Mode:    "fetch",
			Content: fetchResult.Content,
		},
	}, nil
}

// searchChain returns a search chain pre-populated with built-in
// providers (zhipu if ZHIPU_API_KEY is set, then anysearch, then duckduckgo).
func searchChain(client *http.Client) *providers.SearchChain {
	var ps []providers.SearchProvider
	if key := os.Getenv("ZHIPU_API_KEY"); key != "" {
		ps = append(ps, &providers.ZhipuProvider{Client: client, APIKey: key})
	}
	ps = append(ps, &providers.AnySearchProvider{Client: client})
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
