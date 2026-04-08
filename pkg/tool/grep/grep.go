// Package grep implements the Grep tool for searching file contents using ripgrep.
//
// Source reference: tools/GrepTool/GrepTool.ts
// 1:1 port from the TypeScript source.
package grep

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Default cap on grep results when head_limit is unspecified.
const DefaultHeadLimit = 250

// VCS directories excluded from searches to avoid noise from version control metadata.
var vcsDirsToExclude = []string{
	".git",
	".svn",
	".hg",
	".bzr",
	".jj",
	".sl",
}

// Input is the grep tool input schema.
// Source: GrepTool.ts — Zod schema for grep input.
type Input struct {
	Pattern        string `json:"pattern" validate:"required"`
	Path           string `json:"path,omitempty"`
	Glob           string `json:"glob,omitempty"`            // file glob filter (e.g. "*.go") — maps to rg --glob
	OutputMode     string `json:"output_mode,omitempty"`    // "content" | "files_with_matches" | "count"
	ContextBefore  int    `json:"-B,omitempty"`             // lines before match (rg -B)
	ContextAfter   int    `json:"-A,omitempty"`             // lines after match (rg -A)
	Context        int    `json:"context,omitempty"`        // lines before and after (rg -C)
	ContextC       int    `json:"-C,omitempty"`             // alias for context (TS compatibility)
	LineNumbers    *bool  `json:"-n,omitempty"`             // show line numbers (default: true for content mode)
	CaseInsensitive *bool `json:"-i,omitempty"`             // case insensitive search (rg -i)
	Type           string `json:"type,omitempty"`           // file type filter (e.g. "go", "py")
	HeadLimit      *int   `json:"head_limit,omitempty"`     // limit output to N results (nil → 250, explicit 0 → unlimited)
	Offset         int    `json:"offset,omitempty"`         // skip first N results
	Multiline      *bool  `json:"multiline,omitempty"`      // enable multiline mode (rg -U --multiline-dotall)
}

// Output is the grep tool output.
// Source: GrepTool.ts — tool result data.
type Output struct {
	Mode         string   `json:"mode,omitempty"`           // "content" | "files_with_matches" | "count"
	NumFiles     int      `json:"numFiles,omitempty"`        // number of matching files
	Filenames    []string `json:"filenames,omitempty"`       // list of matching file paths (files_with_matches)
	Content      string   `json:"content,omitempty"`         // matching content lines (content mode)
	NumLines     int      `json:"numLines,omitempty"`        // number of content lines (content mode)
	NumMatches   int      `json:"numMatches,omitempty"`     // total matches (count mode)
	Matches      []Match `json:"matches"`                   // parsed match list
	Count        int     `json:"count"`                     // total match count
	AppliedLimit  *int    `json:"appliedLimit,omitempty"`    // the limit that was applied (if any)
	AppliedOffset *int    `json:"appliedOffset,omitempty"`  // the offset that was applied
}

// Match represents a single grep match.
type Match struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// New creates the Grep tool.
// Source: tools/GrepTool/GrepTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["pattern"],
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The regular expression pattern to search for in file contents."
			},
			"path": {
				"type": "string",
				"description": "File or directory to search in. Defaults to current working directory."
			},
			"glob": {
				"type": "string",
				"description": "Glob pattern to filter files (e.g. '*.js', '*.{ts,tsx}') - maps to rg --glob."
			},
			"output_mode": {
				"type": "string",
				"enum": ["content", "files_with_matches", "count"],
				"description": "Output mode: 'content' shows matching lines with context, 'files_with_matches' shows file paths sorted by mtime, 'count' shows match counts. Defaults to 'files_with_matches'."
			},
			"-B": {
				"type": "integer",
				"description": "Number of lines to show before each match (rg -B). Requires output_mode: 'content'."
			},
			"-A": {
				"type": "integer",
				"description": "Number of lines to show after each match (rg -A). Requires output_mode: 'content'."
			},
			"context": {
				"type": "integer",
				"description": "Number of lines to show before and after each match (rg -C). Requires output_mode: 'content'."
			},
			"-C": {
				"type": "integer",
				"description": "Alias for context (rg -C). Number of lines to show before and after each match."
			},
			"-n": {
				"type": "boolean",
				"description": "Show line numbers in output (rg -n). Requires output_mode: 'content'. Defaults to true."
			},
			"-i": {
				"type": "boolean",
				"description": "Case insensitive search (rg -i)."
			},
			"type": {
				"type": "string",
				"description": "File type to search (rg --type). Common types: js, py, rust, go, java, etc."
			},
			"head_limit": {
				"type": "integer",
				"description": "Limit output to first N lines/entries. Works across all output modes. Defaults to 250. Pass 0 for unlimited."
			},
			"offset": {
				"type": "integer",
				"description": "Skip first N results before applying head_limit."
			},
			"multiline": {
				"type": "boolean",
				"description": "Enable multiline mode where . matches newlines (rg -U --multiline-dotall). Default: false."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "Grep",
		Aliases_: []string{"grep"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Search file contents with regex", nil
			}
			return fmt.Sprintf("Grep: %s", in.Pattern), nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true
		},
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "Search file contents using ripgrep (rg). Supports regex patterns, file type filtering, glob includes, and multiple output modes.",
	})
}

// Execute searches file contents using ripgrep.
// Source: GrepTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Determine search path
	searchPath := in.Path
	if searchPath == "" {
		if tctx != nil && tctx.WorkingDir != "" {
			searchPath = tctx.WorkingDir
		} else {
			searchPath, _ = os.Getwd()
		}
	}

	// Fall back to Go-based search if rg is not available
	if _, err := exec.LookPath("rg"); err != nil {
		return goGrep(ctx, in.Pattern, searchPath, in.Glob)
	}

	// Determine output mode
	mode := in.OutputMode
	if mode == "" {
		mode = "files_with_matches"
	}

	// Build rg command args
	args := []string{
		"--hidden",         // search hidden files
		"--color=never",    // no ANSI colors
		"--max-columns", "500", // prevent base64/minified content from cluttering output
	}

	// Exclude VCS directories
	for _, dir := range vcsDirsToExclude {
		args = append(args, "--glob", "!"+dir)
	}

	// Multiline mode
	if ptrVal(in.Multiline) {
		args = append(args, "-U", "--multiline-dotall")
	}

	// Case insensitive
	if ptrVal(in.CaseInsensitive) {
		args = append(args, "-i")
	}

	// Output mode flags
	switch mode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	case "content":
		// line numbers: default to true for content mode
		if !ptrSet(in.LineNumbers) || ptrVal(in.LineNumbers) {
			args = append(args, "--line-number")
		}
		args = append(args, "--no-heading")
		args = append(args, "--with-filename")
	}

	// Pattern starting with dash: use -e to prevent rg from treating it as option
	if strings.HasPrefix(in.Pattern, "-") {
		args = append(args, "-e", in.Pattern)
	} else {
		args = append(args, in.Pattern)
	}

	// Type filter
	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}

	// Glob filter
	if in.Glob != "" {
		patterns := splitGlobPatterns(in.Glob)
		for _, p := range patterns {
			if p != "" {
				args = append(args, "--glob", p)
			}
		}
	}

	// Context flags for content mode
	if mode == "content" {
		ctxVal := in.Context
		if ctxVal == 0 && in.ContextC != 0 {
			ctxVal = in.ContextC // -C alias for context
		}
		if ctxVal != 0 {
			args = append(args, "-C", strconv.Itoa(ctxVal))
		} else {
			if in.ContextBefore != 0 {
				args = append(args, "-B", strconv.Itoa(in.ContextBefore))
			}
			if in.ContextAfter != 0 {
				args = append(args, "-A", strconv.Itoa(in.ContextAfter))
			}
		}
	}

	args = append(args, searchPath)

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.Output()
	if err != nil {
		// rg returns exit code 1 when no matches — not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return emptyResult(mode), nil
		}
		return nil, fmt.Errorf("ripgrep error: %w", err)
	}

	// Resolve head_limit: nil → DefaultHeadLimit (250), explicit 0 → unlimited
	headLimit := DefaultHeadLimit
	if in.HeadLimit != nil {
		headLimit = *in.HeadLimit
	}

	return buildResult(mode, string(output), headLimit, in.Offset)
}

// splitGlobPatterns splits a glob string on commas/spaces while preserving brace patterns.
func splitGlobPatterns(glob string) []string {
	var result []string
	rawPatterns := strings.Fields(glob)
	for _, raw := range rawPatterns {
		if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
			result = append(result, raw)
		} else {
			for _, p := range strings.Split(raw, ",") {
				if p != "" {
					result = append(result, p)
				}
			}
		}
	}
	return result
}

// emptyResult returns an empty result for the given output mode.
func emptyResult(mode string) *tool.ToolResult {
	switch mode {
	case "content":
		return &tool.ToolResult{Data: &Output{Mode: "content", Content: "", NumLines: 0}}
	case "count":
		return &tool.ToolResult{Data: &Output{Mode: "count", NumFiles: 0, NumMatches: 0}}
	default: // files_with_matches
		return &tool.ToolResult{Data: &Output{Mode: "files_with_matches", NumFiles: 0, Filenames: []string{}}}
	}
}

// buildResult constructs the Output based on output mode.
func buildResult(mode, rgOutput string, headLimit, offset int) (*tool.ToolResult, error) {
	switch mode {
	case "content":
		lines := strings.Split(strings.TrimSuffix(rgOutput, "\n"), "\n")
		filtered := filterEmpty(lines)
		limited, appliedLimit := applyHeadLimit(filtered, headLimit, offset)
		numLines := len(limited)
		var appliedOffset *int
		if offset > 0 {
			appliedOffset = &offset
		}
		content := strings.Join(limited, "\n")
		return &tool.ToolResult{Data: &Output{
			Mode:          "content",
			Content:       content,
			NumLines:      numLines,
			AppliedLimit:  appliedLimit,
			AppliedOffset: appliedOffset,
		}}, nil

	case "count":
		lines := strings.Split(strings.TrimSuffix(rgOutput, "\n"), "\n")
		filtered := filterEmpty(lines)
		limited, appliedLimit := applyHeadLimit(filtered, headLimit, offset)
		var appliedOffset *int
		if offset > 0 {
			appliedOffset = &offset
		}
		var totalMatches, fileCount int
		var contentLines []string
		for _, line := range limited {
			colonIdx := strings.LastIndex(line, ":")
			var countStr string
			if colonIdx > 0 {
				// Format: "filename:count"
				countStr = line[colonIdx+1:]
			} else {
				// Single file, no colon — entire line is the count
				countStr = line
			}
			count, _ := strconv.Atoi(countStr)
			totalMatches += count
			fileCount++
			contentLines = append(contentLines, line)
		}
		return &tool.ToolResult{Data: &Output{
			Mode:          "count",
			NumFiles:      fileCount,
			NumMatches:    totalMatches,
			Content:       strings.Join(contentLines, "\n"),
			AppliedLimit:  appliedLimit,
			AppliedOffset: appliedOffset,
		}}, nil

	default: // files_with_matches
		lines := strings.Split(strings.TrimSuffix(rgOutput, "\n"), "\n")
		filtered := filterEmpty(lines)
		sorted := sortByMtime(filtered)
		limited, appliedLimit := applyHeadLimitStrings(sorted, headLimit, offset)
		var relFiles []string
		for _, f := range limited {
			relFiles = append(relFiles, toRelativePath(f))
		}
		var appliedOffset *int
		if offset > 0 {
			appliedOffset = &offset
		}
		return &tool.ToolResult{Data: &Output{
			Mode:          "files_with_matches",
			Filenames:     relFiles,
			NumFiles:      len(relFiles),
			AppliedLimit:  appliedLimit,
			AppliedOffset: appliedOffset,
		}}, nil
	}
}

// filterEmpty removes empty strings from a slice.
func filterEmpty(lines []string) []string {
	var result []string
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

// applyHeadLimit applies head_limit with offset to a string slice.
// Returns the limited slice and the applied limit (if truncation occurred).
// Source: GrepTool.ts — applyHeadLimit function.
func applyHeadLimit(items []string, limit, offset int) ([]string, *int) {
	// Explicit 0 = unlimited escape hatch
	if limit == 0 {
		if offset == 0 {
			return items, nil
		}
		if offset >= len(items) {
			return []string{}, nil
		}
		return items[offset:], nil
	}
	effectiveLimit := limit
	end := offset + effectiveLimit
	if end > len(items) {
		end = len(items)
	}
	if offset >= len(items) {
		return []string{}, nil
	}
	result := items[offset:end]
	// Only report appliedLimit when truncation actually occurred
	if len(items)-offset > effectiveLimit {
		return result, &effectiveLimit
	}
	return result, nil
}

// applyHeadLimitStrings applies head_limit to string slice (files_with_matches mode).
func applyHeadLimitStrings(items []string, limit, offset int) ([]string, *int) {
	return applyHeadLimit(items, limit, offset)
}

// sortByMtime sorts file paths by modification time, newest first.
// Files that fail to stat are sorted last (mtime=0).
// Source: GrepTool.ts — mtime-based sorting.
func sortByMtime(paths []string) []string {
	type fileInfo struct {
		path    string
		mtimeMs int64
	}
	var infos []fileInfo
	for _, p := range paths {
		info, err := os.Stat(p)
		mtime := int64(0)
		if err == nil {
			mtime = info.ModTime().UnixMilli()
		}
		infos = append(infos, fileInfo{path: p, mtimeMs: mtime})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].mtimeMs != infos[j].mtimeMs {
			return infos[i].mtimeMs > infos[j].mtimeMs // newest first
		}
		return infos[i].path < infos[j].path // tiebreak by name
	})
	result := make([]string, len(infos))
	for i, info := range infos {
		result[i] = info.path
	}
	return result
}

// toRelativePath converts an absolute path to a relative path (relative to cwd).
func toRelativePath(absPath string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return absPath
	}
	rel, err := filepath.Rel(cwd, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// ptrVal safely dereferences a bool pointer, returning false for nil.
func ptrVal(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// ptrSet returns true if the pointer was explicitly set (not nil).
func ptrSet(p *bool) bool {
	return p != nil
}

// goGrep is a fallback grep implementation when rg is not available.
func goGrep(ctx context.Context, pattern, searchPath, glob string) (*tool.ToolResult, error) {
	var matches []Match

	info, err := os.Stat(searchPath)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %s", searchPath)
	}

	if !info.IsDir() {
		fileMatches, err := grepFile(searchPath, pattern)
		if err != nil {
			return nil, err
		}
		matches = fileMatches
	} else {
		entries, err := os.ReadDir(searchPath)
		if err != nil {
			return nil, fmt.Errorf("read directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			filePath := searchPath + "/" + entry.Name()
			fileMatches, err := grepFile(filePath, pattern)
			if err != nil {
				continue
			}
			matches = append(matches, fileMatches...)
		}
	}

	if matches == nil {
		matches = []Match{}
	}

	return &tool.ToolResult{Data: &Output{
		Matches: matches,
		Count:   len(matches),
	}}, nil
}

// grepFile searches a single file for lines matching the pattern.
func grepFile(filePath, pattern string) ([]Match, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var matches []Match
	scanner := bufio.NewScanner(f)
	lineNum := 0

	// Try to compile as regex; fall back to contains if invalid
	re, reErr := regexp.Compile(pattern)

	for scanner.Scan() {
		lineNum++
		text := scanner.Text()
		var ok bool
		if reErr == nil {
			ok = re.MatchString(text)
		} else {
			ok = strings.Contains(text, pattern)
		}
		if ok {
			matches = append(matches, Match{
				File:    filePath,
				Line:    lineNum,
				Content: text,
			})
		}
	}

	return matches, scanner.Err()
}
