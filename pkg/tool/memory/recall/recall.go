// Package recall implements the recall tool: a combined FTS5 search across
// structured facts and message history (both in pkg/memory/short).
// The query is passed raw to both stores so FTS5 boolean operators (AND, OR,
// NOT) and parentheses are honored — the LLM uses these to express precise
// queries. Malformed queries degrade to empty results rather than failing.
package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

// Deps holds the store recall queries.
type Deps struct {
	Store *short.Store
}

// Input is the recall tool input schema.
type Input struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// factHit is one structured-fact match.
type factHit struct {
	FactID  int64  `json:"fact_id"`
	Content string `json:"content"`
	Date    string `json:"date"`
}

// msgHit is one message-history match.
type msgHit struct {
	Content string `json:"content"`
	Date    string `json:"date"`
}

// Output is the combined recall result. Empty slices serialize as `[]` (not
// `null`) so the LLM sees a stable shape.
type Output struct {
	Facts    []factHit `json:"facts"`
	Messages []msgHit  `json:"messages"`
}

// New creates the recall tool. store must be non-nil — if the store is
// unavailable, recall should not be registered at all.
func New(store *short.Store) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "FTS5 query expression (supports AND/OR/NOT and parentheses, e.g. 'alice AND bob', 'blue OR red'). Separate multi-word terms with spaces or operators."			},
			"limit": {
				"type": "integer",
				"default": 50,
				"minimum": 1,
				"maximum": 200
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Recall",
		Aliases_:     []string{"recall"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Search structured facts and message history", nil
			}
			return in.Query, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, Deps{Store: store})
		},
		IsReadOnly_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 50000,
		Prompt_:            prompt,
		RenderResult_: func(data any) string {
			out, ok := data.(*Output)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			if len(out.Facts) == 0 && len(out.Messages) == 0 {
				return "No matches found."
			}
			var b strings.Builder
			for _, f := range out.Facts {
				fmt.Fprintf(&b, "[fact %d] %s (%s)\n", f.FactID, f.Content, f.Date)
			}
			for _, m := range out.Messages {
				fmt.Fprintf(&b, "[msg] %s (%s)\n", m.Content, m.Date)
			}
			return strings.TrimRight(b.String(), "\n")
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

// execute runs the recall query against both stores and merges results.
func execute(ctx context.Context, input json.RawMessage, deps Deps) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	out := &Output{
		Facts:    []factHit{},
		Messages: []msgHit{},
	}

	facts, err := deps.Store.SearchFacts(in.Query, limit)
	if err != nil {
		slog.Warn("recall: facts search failed, continuing with messages", "error", err)
	} else {
		for _, f := range facts {
			out.Facts = append(out.Facts, factHit{
				FactID:  f.ID,
				Content: f.Content,
				Date:    f.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	}

	results, err := deps.Store.SearchMessages(in.Query, &short.SearchOptions{Limit: limit})
	if err != nil {
		slog.Warn("recall: messages search failed", "error", err)
	} else {
		for _, r := range results {
			out.Messages = append(out.Messages, msgHit{
				Content: short.ExtractTextFromJSON(r.TranscriptMessage.Content),
				Date:    r.TranscriptMessage.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	}

	return &tool.ToolResult{Data: out}, nil
}
