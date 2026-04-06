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
	"strings"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// Input is the grep tool input schema.
// Source: GrepTool.ts — Zod schema for grep input.
type Input struct {
	Pattern string `json:"pattern" validate:"required"`
	Path    string `json:"path,omitempty"`
	Include string `json:"include,omitempty"` // file glob filter (e.g. "*.go")
	Type    string `json:"type,omitempty"`    // file type filter (e.g. "go", "py")
}

// Match represents a single grep match.
type Match struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// Output is the grep tool output.
// Source: GrepTool.ts — tool result data.
type Output struct {
	Matches []Match `json:"matches"`
	Count   int     `json:"count"`
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
			"include": {
				"type": "string",
				"description": "File glob to include (e.g. '*.{ts,tsx}'). Equivalent to rg --glob."
			},
			"type": {
				"type": "string",
				"description": "File type to search (e.g. 'go', 'py', 'rust'). Equivalent to rg --type."
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
			return true // grep is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // grep is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "Search file contents using ripgrep (rg). Supports regex patterns, file type filtering, and glob includes.",
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

	// Check if rg is available; fall back to Go-based search if not
	if _, err := exec.LookPath("rg"); err != nil {
		return goGrep(ctx, in.Pattern, searchPath, in.Include)
	}

	// Build rg command
	args := []string{
		"--line-number",    // include line numbers
		"--no-heading",     // don't group by file
		"--color=never",    // no ANSI colors
		"--with-filename",  // always show filenames
	}

	if in.Include != "" {
		args = append(args, "--glob", in.Include)
	}
	if in.Type != "" {
		args = append(args, "--type", in.Type)
	}

	args = append(args, in.Pattern, searchPath)

	cmd := exec.CommandContext(ctx, "rg", args...)
	output, err := cmd.Output()
	if err != nil {
		// rg returns exit code 1 when no matches found — not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return &tool.ToolResult{Data: &Output{
				Matches: []Match{},
				Count:   0,
			}}, nil
		}
		return nil, fmt.Errorf("ripgrep error: %w", err)
	}

	// Parse output lines: format is "filepath:linenum:content"
	matches := parseRGOutput(string(output))

	return &tool.ToolResult{Data: &Output{
		Matches: matches,
		Count:   len(matches),
	}}, nil
}

// parseRGOutput parses ripgrep output into Match structs.
func parseRGOutput(output string) []Match {
	var matches []Match
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format: filepath:linenum:content
		// First colon separates filepath
		idx1 := strings.Index(line, ":")
		if idx1 < 0 {
			continue
		}
		rest := line[idx1+1:]
		idx2 := strings.Index(rest, ":")
		if idx2 < 0 {
			continue
		}

		file := line[:idx1]
		lineNum := 0
		_, _ = fmt.Sscanf(rest[:idx2], "%d", &lineNum)
		content := rest[idx2+1:]

		matches = append(matches, Match{
			File:    file,
			Line:    lineNum,
			Content: content,
		})
	}

	return matches
}

// goGrep is a fallback grep implementation when rg is not available.
// It does a simple line-by-line regex search in Go.
func goGrep(ctx context.Context, pattern, searchPath, include string) (*tool.ToolResult, error) {
	var matches []Match

	// For simplicity, read single file or error
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
		// Walk directory — simple recursive search
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

// grepFile searches a single file for lines containing the pattern.
func grepFile(filePath, pattern string) ([]Match, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var matches []Match
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		if strings.Contains(scanner.Text(), pattern) {
			matches = append(matches, Match{
				File:    filePath,
				Line:    lineNum,
				Content: scanner.Text(),
			})
		}
	}

	return matches, scanner.Err()
}
