package web

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
)

const defaultLimit = 10

// Input is the web tool input schema.
type Input struct {
	Query string `json:"query"`           // required — search terms or URL
	Since string `json:"since,omitempty"` // optional — e.g. 3d, 1w, 2m, 1y
	Limit int    `json:"limit,omitempty"` // optional — max results, default 10
	Fetch bool   `json:"fetch,omitempty"` // optional — auto-fetch top results' content
}

// Output is the web tool result data.
type Output struct {
	Mode    string          // "search" or "fetch"
	Content string          // formatted output for LLM
	Raw     *SearchResponse // structured response
}

// Config holds provider instances for the web tool.
type Config struct {
	Providers []SearchProvider
}

// New creates the Web tool with the given provider chain.
func New(cfg Config) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query or URL to fetch. Auto-detects: URLs are fetched, other text triggers search."
			},
			"since": {
				"type": "string",
				"description": "Time filter for search results. Examples: 3d (3 days), 1w (1 week), 2m (2 months), 1y (1 year). Search only."
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of search results. Default: 10."
			},
			"fetch": {
				"type": "boolean",
				"description": "When true, automatically fetches full content of top search results. Default: false."
			}
		}
	}`)

	chain := &SearchChain{Providers: cfg.Providers}

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
			return execute(ctx, input, chain)
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

func execute(ctx context.Context, input json.RawMessage, chain *SearchChain) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// URL detection: fetch mode
	if IsURL(in.Query) {
		return executeFetch(ctx, in.Query)
	}

	// Search mode
	return executeSearch(ctx, in, chain)
}

func executeSearch(ctx context.Context, in Input, chain *SearchChain) (*tool.ToolResult, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	since, _, err := ParseSince(in.Since)
	if err != nil {
		return nil, err
	}

	params := SearchParams{
		Query: in.Query,
		Limit: limit,
		Since: since,
	}

	resp, err := chain.Search(ctx, params)
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

func executeFetch(ctx context.Context, url string) (*tool.ToolResult, error) {
	// Phase 2 will implement full fetch with scrapers.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &tool.ToolResult{
		Data: &Output{
			Mode:    "fetch",
			Content: fmt.Sprintf("URL detected: %s\nFetch not yet implemented (Phase 2).", url),
		},
	}, nil
}

func webPrompt() string {
	return `## Web Tool

Search the web or fetch URL content. Usage:
- web("golang generics") — search, returns sources list
- web("https://github.com/owner/repo") — fetch URL content (Phase 2+)
- web("golang generics", fetch=true) — search + fetch top results' content

Parameters:
- query (required): search terms or URL
- since (optional): time filter — 3d, 1w, 2m, 1y
- limit (optional): max results, default 10
- fetch (optional): auto-fetch top results' content, default false`
}
