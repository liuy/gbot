// Package fileread implements the FileRead tool for reading file contents.
//
// Source reference: tools/FileReadTool/FileReadTool.ts
// 1:1 port from the TypeScript source.
package fileread

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// Input is the file read tool input schema.
// Source: FileReadTool.ts — Zod schema for file read input.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Offset   int    `json:"offset,omitempty"` // 1-indexed line number to start from
	Limit    int    `json:"limit,omitempty"`  // max number of lines to read
}

// Output is the file read tool output.
// Source: FileReadTool.ts — tool result data.
type Output struct {
	Content string `json:"content"`
	Path    string `json:"path"`
	Lines   int    `json:"lines"`
}

// New creates the FileRead tool.
// Source: tools/FileReadTool/FileReadTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to read."
			},
			"offset": {
				"type": "integer",
				"description": "Line number to start reading from (1-indexed). Only provide if the file is too large to read at once."
			},
			"limit": {
				"type": "integer",
				"description": "Maximum number of lines to read. Only provide if the file is too large to read at once."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "FileRead",
		Aliases_: []string{"fileread", "read", "cat"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Read a file from the filesystem", nil
			}
			return fmt.Sprintf("Read file: %s", in.FilePath), nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return true // reading is always read-only
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return true // reading is concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "Read file contents from the local filesystem. Supports line range via offset and limit parameters.",
	})
}

// Execute reads a file and returns its contents.
// Source: FileReadTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	// Check file exists and is accessible
	info, err := os.Stat(in.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file does not exist: %s", in.FilePath)
		}
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file: %s", in.FilePath)
	}

	// If no offset/limit, read the whole file
	if in.Offset == 0 && in.Limit == 0 {
		data, err := os.ReadFile(in.FilePath)
		if err != nil {
			return nil, fmt.Errorf("read file: %w", err)
		}

		content := string(data)
		lineCount := strings.Count(content, "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			lineCount++
		}

		return &tool.ToolResult{Data: &Output{
			Content: content,
			Path:    in.FilePath,
			Lines:   lineCount,
		}}, nil
	}

	// Read with line range using offset/limit
	f, err := os.Open(in.FilePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	var lines []string
	lineNum := 0
	collected := 0

	offset := in.Offset
	if offset < 1 {
		offset = 1
	}

	for scanner.Scan() {
		lineNum++
		if lineNum >= offset {
			lines = append(lines, scanner.Text())
			collected++
			if in.Limit > 0 && collected >= in.Limit {
				break
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan file: %w", err)
	}

	content := strings.Join(lines, "\n")

	return &tool.ToolResult{Data: &Output{
		Content: content,
		Path:    in.FilePath,
		Lines:   len(lines),
	}}, nil
}
