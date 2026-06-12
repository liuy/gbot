package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/tool"
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
	Raw     *SearchResponse
}

type Config struct {
	Providers []SearchProvider
	Client    *http.Client
}

func New(cfg Config) tool.Tool {
	chain := &SearchChain{Providers: cfg.Providers}

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
			return execute(ctx, input, chain, cfg.Client)
		},
		IsReadOnly_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            webPrompt(chain),
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

func execute(ctx context.Context, input json.RawMessage, chain *SearchChain, client *http.Client) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if IsURL(in.Query) {
		return executeFetch(ctx, in.Query, in.JS, client)
	}

	return executeSearch(ctx, in, chain)
}

func executeSearch(ctx context.Context, in Input, chain *SearchChain) (*tool.ToolResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	params := SearchParams{
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

func executeFetch(ctx context.Context, url string, js bool, client *http.Client) (*tool.ToolResult, error) {
	if js {
		slog.Info("web:fetch JS mode", "url", url)
		return fetchWithChrome(ctx, url, client)
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
