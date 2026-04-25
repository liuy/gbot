// Package glob implements the Glob tool for file pattern matching.
//
// Source reference: tools/GlobTool/GlobTool.ts
// 1:1 port from the TypeScript source.
package glob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// Maximum number of files returned by a single glob call.
// Source: GlobTool.ts — globLimits.maxResults.
const MaxGlobResults = 100

// Input is the glob tool input schema.
// Source: GlobTool.ts — Zod schema for glob input.
type Input struct {
	Pattern string `json:"pattern" validate:"required"`
	Path    string `json:"path,omitempty"`
}

// Output is the glob tool output.
// Source: GlobTool.ts — tool result data.
type Output struct {
	Files      []string `json:"filenames"`
	Count      int      `json:"numFiles"`
	DurationMs int64    `json:"durationMs"`
	Truncated  bool     `json:"truncated"`
}

// New creates the Glob tool.
// Source: tools/GlobTool/GlobTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["pattern"],
		"properties": {
			"pattern": {
				"type": "string",
				"description": "The glob pattern to match files against (e.g. '**/*.go', 'src/**/*.ts')."
			},
			"path": {
				"type": "string",
				"description": "The directory to search in. Defaults to current working directory."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "Glob",
		Aliases_: []string{"glob"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Find files matching a glob pattern", nil
			}
			return in.Pattern, nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true // glob is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // glob is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		MaxResultSizeChars:   100000,
		Prompt_: globPrompt(),
		RenderResult_: func(data any) string {
			out, ok := data.(*Output)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			if len(out.Files) == 0 {
				return "No files matched"
			}
			return strings.Join(out.Files, "\n")
		},
	})
}

// Execute finds files matching a glob pattern.
// Source: GlobTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	start := time.Now()

	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Determine base path
	basePath := in.Path
	if basePath == "" {
		if tctx != nil && tctx.WorkingDir != "" {
			basePath = tctx.WorkingDir
		} else {
			basePath, _ = os.Getwd()
		}
	}

	// Verify base path exists
	info, err := os.Stat(basePath)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %s", basePath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", basePath)
	}

	// Use doublestar.Glob for pattern matching
	matches, err := doublestar.Glob(os.DirFS(basePath), in.Pattern)
	if err != nil {
		return nil, fmt.Errorf("glob pattern error: %w", err)
	}

	// Sort matches for deterministic output
	sort.Strings(matches)

	// Apply truncation limit (same as TS: globLimits.maxResults = 100)
	truncated := false
	if len(matches) > MaxGlobResults {
		matches = matches[:MaxGlobResults]
		truncated = true
	}

	return &tool.ToolResult{Data: &Output{
		Files:      matches,
		Count:      len(matches),
		DurationMs: time.Since(start).Milliseconds(),
		Truncated:  truncated,
	}}, nil
}
