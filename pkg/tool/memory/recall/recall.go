// Package recall implements the recall tool: an FTS5 search across message
// history in pkg/memory/short, plus a scan of memory/archive/*.md
// (closed-out case records) in query mode.
// Queries are keyword-oriented: words are OR-matched with bm25 ranking, so
// messages matching more keywords rank first. Boolean operators in the input
// are stripped as stopwords rather than interpreted.
package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// sinceRe parses the since parameter: a count + unit (h/d/w/m/y).
var sinceRe = regexp.MustCompile(`^(\d+)([hdwmy])$`)

// ftsTermSplitRe splits an FTS5 query into candidate terms for snippet
// centering. Operators (AND, OR, NOT, NEAR) and syntax chars are delimiters.
var ftsTermSplitRe = regexp.MustCompile(`[\s()"*~^{}\[\]]+`)

// Deps holds the store recall queries and the memory directory whose
// archive/ subtree is scanned alongside it.
type Deps struct {
	Store *short.Store
	// MemoryDir is the projectspace memory directory (absolute). Empty
	// disables the archive scan — tests and callers without a projectspace
	// stay clean.
	MemoryDir string
}

// Input is the recall tool input schema.
type Input struct {
	Query string `json:"query,omitempty"`
	UUID  string `json:"uuid,omitempty"`  // read a single message by UUID
	Since string `json:"since,omitempty"` // "7d" | "12h" | "2w" | "3m" | "1y"
	Limit int    `json:"limit,omitempty"`
}

// msgHit is one message-history match. Score omits in uuid mode where there
// is no relevance to report (single deterministic hit, not a ranked search).
type msgHit struct {
	UUID    string  `json:"uuid"`
	Content string  `json:"content"`
	Date    string  `json:"date"`
	Score   float64 `json:"score,omitempty"`
}

// ArchiveHit is one memory/archive/*.md match. File is absolute so the LLM
// can pass it straight to Read (relative paths resolve against the repo
// CWD, not the projectspace).
type ArchiveHit struct {
	File     string   `json:"file"`
	Snippets []string `json:"snippets"`
}

// Output is the recall result. Empty slices serialize as `[]` (not `null`)
// so the LLM sees a stable shape. An empty search injects one hint message
// into Messages instead of a separate field — one semantic place to look.
// Archive carries closed-out case records; omitempty keeps the serialized
// shape of archive-less results byte-identical.
type Output struct {
	Messages []msgHit     `json:"messages"`
	Archive  []ArchiveHit `json:"archive,omitempty"`
}

// emptyHint is returned as the single message on an empty search: it suggests
// the retry path for keyword-exact misses. Matching is lexical, so the usual
// cause is vocabulary mismatch — synonyms or the other language usually
// recover it. Riding in messages[] (not a separate field) keeps one semantic
// place for the LLM to look.
const emptyHint = "No matches — matching is keyword-exact, not semantic. Retry with synonyms or the other language (e.g. 调研 → 调查/梳理/survey)."

// New creates the recall tool. store must be non-nil — if the store is
// unavailable, recall should not be registered at all. memoryDir is the
// projectspace memory directory; its archive/ subtree is searched in query
// mode (empty skips the scan).
func New(store *short.Store, memoryDir string) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Keywords describing what you are looking for (e.g. 'alice blue', '数据库 migration'). Separate multiple keywords with spaces; messages matching more keywords rank first. Do not use boolean operators. Also searches memory/archive/*.md (closed-out case records); archive hits render as a file path + snippet lines — Read the file for full details."
			},
			"uuid": {
				"type": "string",
				"description": "Read a single message by its UUID (returned from a previous search). Returns full content, not a snippet. Mutually exclusive with query."
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
				return "Search conversation history", nil
			}
			if in.UUID != "" {
				return "uuid: " + in.UUID, nil
			}
			s := in.Query
			if in.Since != "" {
				s += " (" + in.Since + ")"
			}
			return s, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			return execute(ctx, input, Deps{Store: store, MemoryDir: memoryDir})
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
			hintOnly := len(out.Messages) == 1 && out.Messages[0].Content == emptyHint
			if len(out.Messages) == 0 && len(out.Archive) == 0 {
				return "No matches found."
			}
			// The empty-search hint rides in messages[] for the LLM; for
			// humans render it bare instead of as a numbered block with an
			// empty date line. Match on content (not UUID=="") because
			// uuid-mode hits also carry an empty UUID.
			if hintOnly && len(out.Archive) == 0 {
				return out.Messages[0].Content
			}
			sections := make([]string, 0, 2)
			if len(out.Archive) > 0 {
				sections = append(sections, archiveSectionText(out.Archive))
			}
			if len(out.Messages) > 0 && !hintOnly {
				blocks := make([]string, 0, len(out.Messages))
				for i, m := range out.Messages {
					var b strings.Builder
					// Score 0 means uuid mode (no relevance concept) — the
					// score prefix is search-mode-only to avoid "0.00" noise.
					if m.Score != 0 {
						fmt.Fprintf(&b, "%d. [%.2f] %s\n", i+1, m.Score, m.Date)
					} else {
						fmt.Fprintf(&b, "%d. %s\n", i+1, m.Date)
					}
					// Indent every line so multi-line snippets stay one visual
					// block instead of bleeding into the next entry's header.
					for line := range strings.SplitSeq(m.Content, "\n") {
						b.WriteString("   " + line + "\n")
					}
					// Trim spaces too: empty or trailing-newline content would
					// otherwise leave a stray 3-space line after the trim of \n.
					blocks = append(blocks, strings.TrimRight(b.String(), " \n"))
				}
				sections = append(sections, strings.Join(blocks, "\n\n"))
			}
			return strings.Join(sections, "\n\n")
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
			// Wire text that happens to be a JSON object decodes into an
			// all-zero Output (unknown fields ignored), which replay would
			// render as "No matches found." instead of falling back to the
			// wire text. Uniform rule across wire-plaintext tools. An
			// archive-only hit list (Messages empty, Archive non-empty) is a
			// legal shape and must decode.
			//
			// Accepted and recorded false negatives — two reachable paths
			// produce an all-zero Output: a search-storage error (execute
			// logs it and keeps Messages empty) and a uuid lookup miss. Both
			// degrade legacy replay to showing the raw JSON, which loses no
			// information.
			if len(o.Messages) == 0 && len(o.Archive) == 0 {
				return nil, fmt.Errorf("recall: decoded output lacks identifying fields (not a legacy JSON result)")
			}
			return &o, nil
		},
		IsSearchOrRead_: func(json.RawMessage) tool.SearchReadKind {
			return tool.SearchReadKind{IsSearch: true}
		},
		FormatWireBlocks_: func(data any) []types.ContentBlock {
			out, ok := data.(*Output)
			if !ok {
				raw, _ := json.Marshal(data)
				return []types.ContentBlock{types.NewTextBlock(string(raw))}
			}
			return []types.ContentBlock{types.NewTextBlock(wireText(out))}
		},
	})
}

// wireText renders the LLM-facing plain-text form: numbered entries with a
// date header, 3-space-indented content lines, entries separated by a blank
// line. UUID and score ride on the header because the LLM needs the UUID
// for uuid-mode follow-up reads (the schema documents that flow) and the
// score to judge match confidence — the old JSON wire carried both, so the
// plaintext wire keeps them. Archive hits render first: a closed-out case
// record is more authoritative than a conversation fragment.
func wireText(out *Output) string {
	hintOnly := len(out.Messages) == 1 && out.Messages[0].Content == emptyHint
	if len(out.Messages) == 0 && len(out.Archive) == 0 {
		return "No matches found."
	}
	if hintOnly && len(out.Archive) == 0 {
		return emptyHint
	}
	sections := make([]string, 0, 2)
	if len(out.Archive) > 0 {
		sections = append(sections, archiveSectionText(out.Archive))
	}
	if len(out.Messages) > 0 && !hintOnly {
		blocks := make([]string, 0, len(out.Messages))
		for i, m := range out.Messages {
			var b strings.Builder
			header := fmt.Sprintf("%d. %s", i+1, m.Date)
			if m.Score != 0 {
				// Score 0 means uuid mode (no relevance concept) — the score
				// prefix is search-mode-only to avoid "score 0.00" noise.
				header = fmt.Sprintf("score %.2f  %s  uuid %s", m.Score, header, m.UUID)
			} else {
				header = fmt.Sprintf("%s  uuid %s", header, m.UUID)
			}
			b.WriteString(header + "\n")
			// Indent every line so multi-line content stays one visual block
			// instead of bleeding into the next entry's header. Trailing
			// whitespace is trimmed so a trailing newline in content does not
			// leave a stray 3-space line (same reason the render trims).
			for line := range strings.SplitSeq(m.Content, "\n") {
				b.WriteString("   " + line + "\n")
			}
			blocks = append(blocks, strings.TrimRight(b.String(), " \n"))
		}
		sections = append(sections, strings.Join(blocks, "\n\n"))
	}
	return strings.Join(sections, "\n\n")
}

// archiveSectionText renders the archive hit list: one path bullet per hit
// with 4-space-indented snippet lines under it.
func archiveSectionText(hits []ArchiveHit) string {
	var b strings.Builder
	b.WriteString("## Archive\n")
	for _, h := range hits {
		b.WriteString("- " + h.File + "\n")
		for _, s := range h.Snippets {
			b.WriteString("    " + s + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// execute runs the recall query against message history.
func execute(ctx context.Context, input json.RawMessage, deps Deps) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	// UUID mode: read single message by UUID, return full content (not snippet).
	if in.UUID != "" {
		msg, err := deps.Store.GetMessageByUUID(in.UUID)
		if err != nil {
			return nil, fmt.Errorf("get message: %w", err)
		}
		if msg == nil {
			return &tool.ToolResult{Data: &Output{Messages: []msgHit{}}}, nil
		}
		text := short.ExtractSearchableText(msg.Content)
		return &tool.ToolResult{Data: &Output{
			Messages: []msgHit{{
				UUID:    msg.UUID,
				Content: text,
				// Scanned CreatedAt carries UTC location (driver _timezone=UTC); render local wall clock — store UTC, convert on read.
				Date: msg.CreatedAt.Local().Format("2006-01-02 15:04"),
			}},
		}}, nil
	}

	if strings.TrimSpace(in.Query) == "" {
		return nil, fmt.Errorf("either query or uuid is required")
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
		Messages: []msgHit{},
	}

	// Archive scan runs concurrently with the DB search — local file reads
	// independent of the FTS query.
	var archiveCh chan []ArchiveHit
	if deps.MemoryDir != "" {
		archiveCh = make(chan []ArchiveHit, 1)
		go func() { archiveCh <- scanArchive(deps.MemoryDir, in.Query) }()
	}

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
				UUID:    r.TranscriptMessage.UUID,
				Content: makeSnippet(text, in.Query, 50),
				Date:    r.TranscriptMessage.CreatedAt.Local().Format("2006-01-02 15:04"),
				Score:   r.Score,
			})
		}
	}

	if archiveCh != nil {
		out.Archive = <-archiveCh
	}

	if len(out.Messages) == 0 && len(out.Archive) == 0 && err == nil {
		out.Messages = []msgHit{{Content: emptyHint}}
	}

	return &tool.ToolResult{Data: out}, nil
}

// Archive scan tuning and exclusions.
const (
	archiveMaxSnippetLines = 3
	archiveSnippetRunes    = 50
)

// scanArchive searches memory/archive/*.md for any-term (OR,
// case-insensitive) matches — the same term semantics as the FTS message
// search. A file hit aggregates up to archiveMaxSnippetLines matching lines,
// each windowed through makeSnippet. Missing directory or unreadable files
// are a silent empty result: the archive layer is additive, never fatal.
func scanArchive(memoryDir, query string) []ArchiveHit {
	dir := filepath.Join(memoryDir, "archive")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	terms := archiveTerms(query)
	if len(terms) == 0 {
		return nil
	}
	var hits []ArchiveHit
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if !containsAnyFold(content, terms) {
			continue
		}
		hit := ArchiveHit{File: filepath.Join(dir, e.Name())}
		for line := range strings.SplitSeq(content, "\n") {
			if len(hit.Snippets) >= archiveMaxSnippetLines {
				break
			}
			if containsAnyFold(line, terms) {
				hit.Snippets = append(hit.Snippets, makeSnippet(line, query, archiveSnippetRunes))
			}
		}
		hits = append(hits, hit)
	}
	return hits
}

// archiveTerms splits a query into lowercase search terms, skipping FTS
// operators — a query that ignored the no-operators guidance must not
// degenerate into matching every file containing the English word "and".
func archiveTerms(query string) []string {
	var terms []string
	// Same split as the FTS5 side (ftsTermSplitRe) so quoted/punctuated
	// terms like "migration" match on both layers.
	for _, f := range ftsTermSplitRe.Split(query, -1) {
		if f == "" {
			continue
		}
		switch strings.ToUpper(f) {
		case "AND", "OR", "NOT", "NEAR":
			continue
		}
		terms = append(terms, strings.ToLower(f))
	}
	return terms
}

// containsAnyFold reports whether any term appears in s, case-insensitively.
func containsAnyFold(s string, terms []string) bool {
	lower := strings.ToLower(s)
	for _, t := range terms {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
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
