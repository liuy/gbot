package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"github.com/liuy/gbot/pkg/markitdown"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/web/providers"
	"github.com/liuy/gbot/pkg/tool/web/scrapers"
)

const defaultLimit = 10

// Option configures the web tool.
type Option func(*webConfig)

type webConfig struct {
	apiKeys map[string]string // provider name → API key
}

// WithAPIKeys sets API keys for search providers.
// Empty value = anonymous mode. Omitted provider = not registered.
func WithAPIKeys(keys map[string]string) Option {
	return func(c *webConfig) { c.apiKeys = keys }
}

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
func New(client *http.Client, opts ...Option) tool.Tool {
	if client == nil {
		client = http.DefaultClient
	}
	var cfg webConfig
	for _, o := range opts {
		o(&cfg)
	}
	chain := searchChain(client, cfg.apiKeys)
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
		Name_:        "Web",
		Aliases_:     []string{"web"},
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
			// Web is read-only HTTP; concurrent searches/fetches are safe.
			return true
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
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			text, err := tool.UnmarshalSingleBlock(raw)
			if err != nil {
				return nil, err
			}
			var o Output
			if err := json.Unmarshal([]byte(text), &o); err != nil {
				return nil, err
			}
			return &o, nil
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

	// Binary document conversion (PDF, DOCX, PPTX, etc.).
	if md, err := fetchAndConvertDocument(ctx, url, client); err == nil && md != "" {
		slog.Info("web:fetch document converted", "url", url, "content_len", len(md))
		fetchResult := BuildResult(md, FetchResultOptions{
			URL:      url,
			FinalURL: url,
			Notes:    []string{"converted from binary document"},
		})
		return &tool.ToolResult{
			Data: &Output{
				Mode:    "fetch",
				Content: fetchResult.Content,
			},
		}, nil
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

// IsConvertibleDocument returns true if the MIME type or file extension indicates
// a binary document that markitdown can convert to markdown.
func IsConvertibleDocument(mimeType, ext string) bool {
	if documentContentTypes[mimeType] {
		return true
	}
	// Explicit non-document MIME types (text/html, application/json, etc.) are never convertible.
	if mimeType != "" && mimeType != "application/octet-stream" && !documentTextMimes[mimeType] {
		return false
	}
	return documentExtensions[ext]
}

// MIME types that are technically text but should still go through markitdown
// because they need conversion (e.g. ipynb JSON → markdown).
var documentTextMimes = map[string]bool{
	"text/plain":       true,
	"application/json": true,
	"text/csv":         true,
}

var documentExtensions = map[string]bool{
	".pdf": true, ".docx": true, ".doc": true,
	".pptx": true, ".ppt": true,
	".xlsx": true, ".xls": true,
	".epub": true, ".ipynb": true,
	".csv": true,
}

var documentContentTypes = map[string]bool{
	"application/pdf": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   true,
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": true,
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         true,
	"application/vnd.ms-excel": true,
	"application/epub+zip":     true,
}

// fetchAndConvertDocument detects binary document URLs and converts them to markdown.
// Returns ("", nil) if the URL is not a document type — caller should fall through to HTTP fetch.
func fetchAndConvertDocument(ctx context.Context, url string, client *http.Client) (string, error) {
	parsed, err := neturl.Parse(url)
	if err != nil {
		return "", nil
	}

	ext := strings.ToLower(getExt(parsed.Path))

	// Quick check: skip URLs that clearly aren't documents by extension.
	// URLs with matching extensions proceed directly; others need a HEAD check.
	extMatch := IsConvertibleDocument("", ext)
	if !extMatch {
		// Extension doesn't match — do a HEAD request to check Content-Type.
		if client == nil {
			client = http.DefaultClient
		}
		headReq, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
		if err != nil {
			return "", nil
		}
		headReq.Header.Set("User-Agent", "curl/8.0")
		headResp, err := client.Do(headReq)
		if err != nil {
			return "", nil
		}
		_, _ = io.Copy(io.Discard, headResp.Body)
		_ = headResp.Body.Close()

		if headResp.StatusCode < 200 || headResp.StatusCode >= 300 {
			return "", nil
		}

		ct := strings.ToLower(strings.SplitN(headResp.Header.Get("Content-Type"), ";", 2)[0])
		ct = strings.TrimSpace(ct)
		if !IsConvertibleDocument(ct, "") {
			return "", nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", nil
	}
	req.Header.Set("User-Agent", "curl/8.0")

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil
	}

	ct := strings.ToLower(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0])
	ct = strings.TrimSpace(ct)

	// For extension-matched URLs, verify Content-Type isn't explicitly non-document.
	if extMatch && ct != "" && ct != "application/octet-stream" && !IsConvertibleDocument(ct, "") && !documentTextMimes[ct] {
		return "", nil
	}

	// Derive extension from redirect URL if original had no match.
	finalExt := ext
	if !extMatch {
		finalParsed := resp.Request.URL
		if finalExt = strings.ToLower(getExt(finalParsed.Path)); !IsConvertibleDocument("", finalExt) {
			finalExt = extByMime(ct)
		}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024*1024))
	if err != nil {
		return "", nil
	}

	m := markitdown.New()
	result, err := m.ConvertReader(bytes.NewReader(data), markitdown.StreamInfo{
		Extension: finalExt,
		MIMEType:  ct,
		URL:       url,
	})
	if err != nil {
		return "", nil
	}

	return result.Markdown, nil
}

// extByMime returns a file extension for common document MIME types.
func extByMime(mime string) string {
	switch mime {
	case "application/pdf":
		return ".pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.ms-excel":
		return ".xls"
	case "application/epub+zip":
		return ".epub"
	default:
		return ""
	}
}

// getExt returns the file extension from a URL path.
func getExt(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			break
		}
	}
	return ""
}

// searchChain builds a search chain from config keys.
// Providers with keys configured (or supporting anonymous mode) are registered.
func searchChain(client *http.Client, keys map[string]string) *providers.SearchChain {
	var ps []providers.SearchProvider
	if _, ok := keys["anysearch"]; ok {
		ps = append(ps, &providers.AnySearchProvider{Client: client, APIKey: keys["anysearch"]})
	}
	if key, ok := keys["zhipu"]; ok && key != "" {
		ps = append(ps, &providers.ZhipuProvider{Client: client, APIKey: key})
	}
	// DuckDuckGo always available (no key needed).
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
