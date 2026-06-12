package web

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/tool"
)

const defaultLimit = 10

type Input struct {
	Query    string `json:"query"`
	Provider string `json:"provider,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type Output struct {
	Mode    string
	Content string
	Raw     *SearchResponse
}

type Config struct {
	Providers []SearchProvider
}

func New(cfg Config) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query or URL to fetch. Auto-detects: URLs are fetched, other text triggers search."
			},
			"provider": {
				"type": "string",
				"description": "Search provider. \"auto\" uses the first available provider. Available providers depend on configuration."
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of search results. Default: 10."
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

func execute(ctx context.Context, input json.RawMessage, chain *SearchChain) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	if IsURL(in.Query) {
		return executeFetch(ctx, in.Query)
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

func executeFetch(ctx context.Context, url string) (*tool.ToolResult, error) {
	result := LoadPage(ctx, url, LoadPageOptions{})

	if result.Error != "" {
		return nil, fmt.Errorf("fetch failed: %s", result.Error)
	}

	content := result.Content
	if LooksLikeHTML(content) {
		md, err := HTMLToMarkdown(content)
		if err == nil {
			content = md
		}
	}

	content, truncated := FinalizeOutput(content)
	if result.Truncated {
		truncated = true
	}

	var notes []string
	if result.FinalURL != url {
		notes = append(notes, fmt.Sprintf("redirected to %s", result.FinalURL))
	}
	if truncated {
		notes = append(notes, "output truncated")
	}

	fetchResult := BuildResult(content, struct {
		URL         string
		FinalURL    string
		Method      string
		FetchedAt   string
		Notes       []string
		ContentType string
	}{
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
