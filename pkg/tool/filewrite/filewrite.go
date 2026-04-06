// Package filewrite implements the FileWrite tool for writing files to the filesystem.
//
// Source reference: tools/FileWriteTool/FileWriteTool.ts
// 1:1 port from the TypeScript source.
package filewrite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// Input is the file write tool input schema.
// Source: FileWriteTool.ts — Zod schema for file write input.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Content  string `json:"content" validate:"required"`
}

// Output is the file write tool output.
// Source: FileWriteTool.ts — tool result data.
type Output struct {
	Success     bool   `json:"success"`
	Path        string `json:"path"`
	BytesWritten int   `json:"bytes_written"`
}

// New creates the FileWrite tool.
// Source: tools/FileWriteTool/FileWriteTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path", "content"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to write. Will create parent directories if needed."
			},
			"content": {
				"type": "string",
				"description": "The content to write to the file."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "FileWrite",
		Aliases_: []string{"filewrite", "write"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Write content to a file", nil
			}
			return fmt.Sprintf("Write file: %s", in.FilePath), nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return false // writing is never read-only
		},
		IsDestructive_: func(json.RawMessage) bool {
			return true // can overwrite existing files
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false // modifies files, not concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "Write content to files. Creates parent directories if they do not exist. Overwrites existing files.",
	})
}

// Execute writes content to a file.
// Source: FileWriteTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}

	// Create parent directories if needed
	dir := filepath.Dir(in.FilePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create directories: %w", err)
	}

	// Write the file with standard permissions
	data := []byte(in.Content)
	if err := os.WriteFile(in.FilePath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return &tool.ToolResult{Data: &Output{
		Success:      true,
		Path:         in.FilePath,
		BytesWritten: len(data),
	}}, nil
}
