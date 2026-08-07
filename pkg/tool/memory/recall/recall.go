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
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

// sinceRe parses the since parameter: a count + unit (h/d/w/m/y).
var sinceRe = regexp.MustCompile(`^(\d+)([hdwmy])$`)

// ftsTermSplitRe splits an FTS5 query into candidate terms for snippet
// centering. Operators (AND, OR, NOT, NEAR) and syntax chars are delimiters.
var ftsTermSplitRe = regexp.MustCompile(`[\s()"*~^{}\[\]]+`)

// Deps holds the store recall queries.
type Deps struct {
	Store *short.Store
}

// Input is the recall tool input schema.
type Input struct {
	Query  string `json:"query"`
	Source string `json:"source,omitempty"` // "facts" | "messages" | "all"; default "facts"
	Since  string `json:"since,omitempty"`  // "7d" | "12h" | "2w" | "3m" | "1y"
	Limit  int    `json:"limit,omitempty"`
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
				"description": "FTS5 query expression (supports AND/OR/NOT and parentheses, e.g. 'alice AND bob', 'blue OR red'). Separate multi-word terms with spaces or operators."
			},
			"source": {
				"type": "string",
				"enum": ["facts", "messages", "all"],
				"description": "Search scope. 'facts' = structured facts only (default), 'messages' = conversation history only, 'all' = both.",
				"default": "facts"
			},
			"since": {
				"type": "string",
				"description": "Time range filter (e.g. '7d' for 7 days, '12h' for 12 hours, '2w' for 2 weeks, '3m' for 3 months, '1y' for 1 year). Omit for no limit.",
				"pattern": "^\\d+[hdwmy]$"
			},
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

// execute runs the recall query against the selected source(s) and merges
// results.
func execute(ctx context.Context, input json.RawMessage, deps Deps) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}

	source := in.Source
	if source == "" {
		source = "facts"
	}
	if source != "facts" && source != "messages" && source != "all" {
		return nil, fmt.Errorf("invalid source %q (use 'facts', 'messages', or 'all')", source)
	}

	sinceTime, err := parseSince(in.Since)
	if err != nil {
		return nil, fmt.Errorf("parse since: %w", err)
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

	if source == "facts" || source == "all" {
		facts, err := deps.Store.SearchFacts(in.Query, limit)
		if err != nil {
			slog.Warn("recall: facts search failed", "error", err)
		} else {
			for _, f := range facts {
				if !sinceTime.IsZero() && f.CreatedAt.Before(sinceTime) {
					continue
				}
				out.Facts = append(out.Facts, factHit{
					FactID:  f.ID,
					Content: f.Content,
					Date:    f.CreatedAt.Format("2006-01-02 15:04"),
				})
			}
		}
	}

	if source == "messages" || source == "all" {
		opts := &short.SearchOptions{Limit: limit}
		if !sinceTime.IsZero() {
			opts.Since = sinceTime
		}
		results, err := deps.Store.SearchMessages(in.Query, opts)
		if err != nil {
			slog.Warn("recall: messages search failed", "error", err)
		} else {
			for _, r := range results {
				text := short.ExtractSearchableText(r.TranscriptMessage.Content)
				out.Messages = append(out.Messages, msgHit{
					Content: makeSnippet(text, in.Query, 50),
					Date:    r.TranscriptMessage.CreatedAt.Format("2006-01-02 15:04"),
				})
			}
		}
	}

	return &tool.ToolResult{Data: out}, nil
}

// parseSince converts a shorthand duration string ("7d", "12h", "2w", "3m",
// "1y") into the cutoff time.Time. An empty string returns the zero value
// (no filter). Month = 30d, year = 365d — approximate, avoids calendar math.
func parseSince(since string) (time.Time, error) {
	if since == "" {
		return time.Time{}, nil
	}
	m := sinceRe.FindStringSubmatch(since)
	if m == nil {
		return time.Time{}, fmt.Errorf("invalid since format %q (use e.g. '7d', '12h', '2w', '3m', '1y')", since)
	}
	n, _ := strconv.Atoi(m[1])
	// Cap count to avoid time.Duration overflow (~290 years at hour resolution).
	if n > 100000 {
		n = 100000
	}
	var dur time.Duration
	switch m[2] {
	case "h":
		dur = time.Duration(n) * time.Hour
	case "d":
		dur = time.Duration(n) * 24 * time.Hour
	case "w":
		dur = time.Duration(n) * 7 * 24 * time.Hour
	case "m":
		dur = time.Duration(n) * 30 * 24 * time.Hour
	case "y":
		dur = time.Duration(n) * 365 * 24 * time.Hour
	}
	return time.Now().Add(-dur), nil
}

// makeSnippet extracts a maxRunes-length window from text, centered on the
// first query term found. If the text fits within maxRunes it is returned
// verbatim. If no query term is found, the window starts from the beginning.
// Ellipses mark truncation on either side.
func makeSnippet(text string, query string, maxRunes int) string {
	if text == "" || maxRunes <= 0 {
		return text
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}

	runes := []rune(text)
	half := maxRunes / 2

	start := 0
	end := maxRunes

	if term := firstQueryTerm(query); term != "" {
		lower := strings.ToLower(text)
		byteIdx := strings.Index(lower, strings.ToLower(term))
		if byteIdx >= 0 {
			runePos := utf8.RuneCountInString(text[:byteIdx])
			matchLen := utf8.RuneCountInString(term)
			center := runePos + matchLen/2
			start = max(center-half, 0)
			end = start + maxRunes
			if end > len(runes) {
				end = len(runes)
				start = max(end-maxRunes, 0)
			}
		}
	}

	if end > len(runes) {
		end = len(runes)
	}

	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return snippet
}

// firstQueryTerm extracts the first non-operator word from an FTS5 query
// string, used to center message snippets on the search term.
func firstQueryTerm(query string) string {
	fields := ftsTermSplitRe.Split(query, -1)
	for _, f := range fields {
		switch strings.ToUpper(f) {
		case "AND", "OR", "NOT", "NEAR", "":
			continue
		}
		return f
	}
	return ""
}
