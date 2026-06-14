// Package toolsearch implements the ToolSearch tool for deferred tool discovery.
//
// Source reference: tools/ToolSearchTool/ToolSearchTool.ts
// 1:1 port from the TypeScript source.
package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
)

// ToolName is the canonical name of the ToolSearch tool.
// Source: tools/ToolSearchTool/constants.ts
const ToolName = "ToolSearch"

// DefaultMaxResults is the default maximum number of search results.
// Source: ToolSearchTool.ts — inputSchema max_results default.
const DefaultMaxResults = 5

// Input is the tool search input schema.
// Source: ToolSearchTool.ts:21-34 — inputSchema
type Input struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// Output is the tool search output schema.
// Source: ToolSearchTool.ts:37-44 — outputSchema
type Output struct {
	Matches            []string `json:"matches"`
	Query              string   `json:"query"`
	TotalDeferredTools int      `json:"total_deferred_tools"`
	PendingMCPServers  []string `json:"pending_mcp_servers,omitempty"`
}

// ToolSearchResult is the structured result type for ToolSearch.
type ToolSearchResult struct {
	Matches            []ToolMatch `json:"matches"`
	Query              string      `json:"query"`
	TotalDeferredTools int         `json:"total_deferred_tools"`
	PendingMCPServers  []string    `json:"pending_mcp_servers,omitempty"`
}

// ToolMatch represents a single matched tool.
type ToolMatch struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// camelCaseRE splits CamelCase boundaries. Source: ToolSearchTool.ts:149
var camelCaseRE = regexp.MustCompile(`([a-z])([A-Z])`)

// selectRE matches the select: prefix for direct tool selection.
var selectRE = regexp.MustCompile(`(?i)^select:(.+)$`)

// New creates the ToolSearch tool.
// Source: tools/ToolSearchTool/ToolSearchTool.ts — buildTool({...})
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["query"],
		"properties": {
			"query": {
				"type": "string",
				"description": "Query to find deferred tools. Use \"select:<tool_name>\" for direct selection, or keywords to search."
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return (default: 5)"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        ToolName,
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			// nil or invalid input (buildToolDefs scenario): return full prompt
			// so the model understands how to use ToolSearch.
			if input == nil {
				return toolPrompt, nil
			}
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return toolPrompt, nil
			}
			return in.Query, nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true // tool search is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // tool search is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars: 100000,
		Prompt_:            toolPrompt,
		RenderResult_: func(data any) string {
			out, ok := data.(*Output)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			if len(out.Matches) == 0 {
				text := "No matching deferred tools found"
				if len(out.PendingMCPServers) > 0 {
					text += fmt.Sprintf(
						". Some MCP servers are still connecting: %s. Their tools will become available shortly — try searching again.",
						strings.Join(out.PendingMCPServers, ", "),
					)
				}
				return text
			}
			return fmt.Sprintf("Found %d tools:\n- %s", len(out.Matches), strings.Join(out.Matches, "\n- "))
		},
	})
}

// parsedName holds the parsed components of a tool name.
// Source: ToolSearchTool.ts:132-161 — parseToolName()
type parsedName struct {
	Parts []string // searchable parts
	Full  string   // full lowercased name
	IsMCP bool     // true for MCP tools (mcp__ prefix)
}

// parseToolName parses a tool name into searchable parts.
// Source: ToolSearchTool.ts:132-161 — parseToolName()
//
// MCP tools (mcp__server__action): split on __ and _ after removing mcp__ prefix.
// Regular tools: split on CamelCase boundaries and underscores.
func parseToolName(name string) parsedName {
	// Check if it's an MCP tool
	if strings.HasPrefix(name, "mcp__") {
		withoutPrefix := strings.ToLower(name[5:]) // remove "mcp__"
		parts := strings.FieldsFunc(withoutPrefix, func(r rune) bool {
			return r == '_' // split on both __ and _
		})
		// Filter empty parts
		filtered := make([]string, 0, len(parts))
		for _, p := range parts {
			if p != "" {
				filtered = append(filtered, p)
			}
		}
		// Full: replace __ and _ with spaces
		full := strings.ReplaceAll(withoutPrefix, "__", " ")
		full = strings.ReplaceAll(full, "_", " ")
		return parsedName{
			Parts: filtered,
			Full:  full,
			IsMCP: true,
		}
	}

	// Regular tool - split by CamelCase and underscores
	// Source: ToolSearchTool.ts:149-154
	//   name.replace(/([a-z])([A-Z])/g, '$1 $2').replace(/_/g, ' ').toLowerCase().split(/\s+/)
	replaced := camelCaseRE.ReplaceAllString(name, `${1} ${2}`)
	replaced = strings.ReplaceAll(replaced, "_", " ")
	lowered := strings.ToLower(replaced)
	parts := strings.Fields(lowered) // Fields splits on whitespace and filters empty

	return parsedName{
		Parts: parts,
		Full:  lowered,
		IsMCP: false,
	}
}

// compileTermPatterns pre-compiles word-boundary regexes for all search terms.
// Source: ToolSearchTool.ts:167-175 — compileTermPatterns()
//
// Called once per search instead of tools*terms*2 times.
func compileTermPatterns(terms []string) map[string]*regexp.Regexp {
	patterns := make(map[string]*regexp.Regexp, len(terms))
	for _, term := range terms {
		if _, exists := patterns[term]; !exists {
			// \b{escapeRegExp(term)}\b — Go equivalent: regexp.QuoteMeta for escaping
			pattern := `\b` + regexp.QuoteMeta(term) + `\b`
			if re, err := regexp.Compile(pattern); err == nil {
				patterns[term] = re
			}
		}
	}
	return patterns
}

// scoredTool holds a tool name and its search score.
type scoredTool struct {
	Name  string
	Score int
}

// searchToolsWithKeywords performs keyword-based search over tool names and descriptions.
// Source: ToolSearchTool.ts:186-302 — searchToolsWithKeywords
//
// Scoring algorithm (must match TS exactly):
//  1. Exact name match fast path (deferred set -> full set fallback)
//  2. mcp__ prefix match
//  3. Keyword scoring:
//     - name part exact match: MCP 12 / non-MCP 10
//     - name part contains match: MCP 6 / non-MCP 5
//     - full name fallback: 3 (only when score==0)
//     - searchHint match: 4
//     - description match: 2
//  4. +required syntax (terms starting with + must all match)
//  5. max_results limit
func searchToolsWithKeywords(
	query string,
	deferredTools map[string]tool.Tool,
	allTools map[string]tool.Tool,
	maxResults int,
) []string {
	queryLower := strings.ToLower(strings.TrimSpace(query))

	// Fast path: if query matches a tool name exactly, return it directly.
	// Source: ToolSearchTool.ts:194-204
	// Checks deferred first, then falls back to the full tool set.
	for name := range deferredTools {
		if strings.ToLower(name) == queryLower {
			return []string{name}
		}
	}
	// Fallback: check full tool set
	for name := range allTools {
		if strings.ToLower(name) == queryLower {
			return []string{name}
		}
	}

	// If query looks like an MCP tool prefix (mcp__server), find matching tools.
	// Source: ToolSearchTool.ts:207-216
	if strings.HasPrefix(queryLower, "mcp__") && len(queryLower) > 5 {
		var prefixMatches []string
		for name := range deferredTools {
			if strings.HasPrefix(strings.ToLower(name), queryLower) {
				prefixMatches = append(prefixMatches, name)
				if len(prefixMatches) >= maxResults {
					break
				}
			}
		}
		if len(prefixMatches) > 0 {
			sort.Strings(prefixMatches) // deterministic ordering
			return prefixMatches
		}
	}

	// Parse query into terms
	queryTerms := strings.Fields(queryLower) // split on whitespace, filter empty

	// Partition into required (+prefixed) and optional terms
	// Source: ToolSearchTool.ts:220-229
	var requiredTerms []string
	var optionalTerms []string
	for _, term := range queryTerms {
		if strings.HasPrefix(term, "+") && len(term) > 1 {
			requiredTerms = append(requiredTerms, term[1:])
		} else {
			optionalTerms = append(optionalTerms, term)
		}
	}

	// Build all scoring terms
	// Source: ToolSearchTool.ts:231-233
	var allScoringTerms []string
	if len(requiredTerms) > 0 {
		allScoringTerms = make([]string, 0, len(requiredTerms)+len(optionalTerms))
		allScoringTerms = append(allScoringTerms, requiredTerms...)
		allScoringTerms = append(allScoringTerms, optionalTerms...)
	} else {
		allScoringTerms = queryTerms
	}

	termPatterns := compileTermPatterns(allScoringTerms)

	// Pre-filter to tools matching ALL required terms in name or description
	// Source: ToolSearchTool.ts:236-257
	candidateTools := deferredTools
	if len(requiredTerms) > 0 {
		filtered := make(map[string]tool.Tool)
		for name, t := range deferredTools {
			parsed := parseToolName(name)
			desc := toolDescription(t)
			descNormalized := strings.ToLower(desc)
			hintNormalized := strings.ToLower(tool.SearchHint(t))

			matchesAll := true
			for _, term := range requiredTerms {
				pattern, hasPattern := termPatterns[term]
				if !hasPattern {
					// If we can't compile a pattern for this term, skip the requirement
					continue
				}
				// Check: parsed.parts includes term, or any part contains term,
				// or description matches pattern, or hint matches pattern
				partExactMatch := false
				partContainsMatch := false
				for _, part := range parsed.Parts {
					if part == term {
						partExactMatch = true
						break
					}
					if strings.Contains(part, term) {
						partContainsMatch = true
					}
				}
				descMatch := pattern.MatchString(descNormalized)
				hintMatch := hintNormalized != "" && pattern.MatchString(hintNormalized)

				if !partExactMatch && !partContainsMatch && !descMatch && !hintMatch {
					matchesAll = false
					break
				}
			}
			if matchesAll {
				filtered[name] = t
			}
		}
		candidateTools = filtered
	}

	// Score each candidate tool
	// Source: ToolSearchTool.ts:259-295
	var scored []scoredTool
	for name, t := range candidateTools {
		parsed := parseToolName(name)
		desc := toolDescription(t)
		descNormalized := strings.ToLower(desc)
		hintNormalized := strings.ToLower(tool.SearchHint(t))

		score := 0
		for _, term := range allScoringTerms {
			pattern, hasPattern := termPatterns[term]
			if !hasPattern {
				continue
			}

			// Exact part match (high weight for MCP server names, tool name parts)
			// Source: ToolSearchTool.ts:271-275
			partExactMatch := false
			partContainsMatch := false
			for _, part := range parsed.Parts {
				if part == term {
					partExactMatch = true
					break
				}
				if strings.Contains(part, term) {
					partContainsMatch = true
				}
			}

			if partExactMatch {
				if parsed.IsMCP {
					score += 12
				} else {
					score += 10
				}
			} else if partContainsMatch {
				if parsed.IsMCP {
					score += 6
				} else {
					score += 5
				}
			}

			// Full name fallback (for edge cases)
			// Source: ToolSearchTool.ts:278-279 — only when score==0
			if score == 0 && strings.Contains(parsed.Full, term) {
				score += 3
			}

			// searchHint match — curated capability phrase, higher signal than prompt
			// Source: ToolSearchTool.ts:282-284
			if hintNormalized != "" && pattern.MatchString(hintNormalized) {
				score += 4
			}

			// Description match — use word boundary to avoid false positives
			// Source: ToolSearchTool.ts:287-289
			if pattern.MatchString(descNormalized) {
				score += 2
			}
		}

		scored = append(scored, scoredTool{Name: name, Score: score})
	}

	// Sort by score descending, take top maxResults
	// Source: ToolSearchTool.ts:297-301
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Name < scored[j].Name // tiebreak by name for determinism
	})

	var result []string
	for i, s := range scored {
		if s.Score > 0 && i < maxResults {
			result = append(result, s.Name)
		}
	}
	return result
}

// Execute runs the ToolSearch tool.
// Source: ToolSearchTool.ts:328-434 — call()
func Execute(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}

	// Get all tools from context
	allTools := tctx.Options.Tools

	// Filter to deferred tools only
	// Source: ToolSearchTool.ts:331 — const deferredTools = tools.filter(isDeferredTool)
	deferredTools := make(map[string]tool.Tool)
	for name, t := range allTools {
		if tool.IsDeferred(t) {
			deferredTools[name] = t
		}
	}

	// Helper to get pending server names
	// Source: ToolSearchTool.ts:335-339
	getPendingServerNames := func() []string {
		if len(tctx.Options.PendingMCPServers) > 0 {
			return tctx.Options.PendingMCPServers
		}
		return nil
	}

	// Check for select: prefix — direct tool selection.
	// Source: ToolSearchTool.ts:363-406
	// Supports comma-separated multi-select: select:A,B,C.
	selectMatch := selectRE.FindStringSubmatch(in.Query)
	if selectMatch != nil {
		requested := strings.Split(selectMatch[1], ",")
		// Trim and filter empty
		var cleaned []string
		for _, s := range requested {
			s = strings.TrimSpace(s)
			if s != "" {
				cleaned = append(cleaned, s)
			}
		}

		var found []string
		for _, toolName := range cleaned {
			// Try deferred set first, then full set
			if _, ok := deferredTools[toolName]; ok {
				if !slices.Contains(found, toolName) {
					found = append(found, toolName)
				}
			} else if _, ok := allTools[toolName]; ok {
				if !slices.Contains(found, toolName) {
					found = append(found, toolName)
				}
			}
		}

		if len(found) == 0 {
			pendingServers := getPendingServerNames()
			return &tool.ToolResult{Data: &Output{
				Matches:            []string{},
				Query:              in.Query,
				TotalDeferredTools: len(deferredTools),
				PendingMCPServers:  pendingServers,
			}}, nil
		}

		return &tool.ToolResult{Data: &Output{
			Matches:            found,
			Query:              in.Query,
			TotalDeferredTools: len(deferredTools),
		}}, nil
	}

	// Keyword search
	// Source: ToolSearchTool.ts:409-431
	matches := searchToolsWithKeywords(in.Query, deferredTools, allTools, maxResults)

	// Include pending server info when search finds no matches
	// Source: ToolSearchTool.ts:423-431
	if len(matches) == 0 {
		pendingServers := getPendingServerNames()
		return &tool.ToolResult{Data: &Output{
			Matches:            matches,
			Query:              in.Query,
			TotalDeferredTools: len(deferredTools),
			PendingMCPServers:  pendingServers,
		}}, nil
	}

	return &tool.ToolResult{Data: &Output{
		Matches:            matches,
		Query:              in.Query,
		TotalDeferredTools: len(deferredTools),
	}}, nil
}

// toolDescription returns the description for a tool.
// In TS, this uses getToolDescriptionMemoized which calls tool.prompt().
// In Go, we use the Description method directly.
func toolDescription(t tool.Tool) string {
	desc, err := t.Description(nil)
	if err != nil {
		return ""
	}
	return desc
}
