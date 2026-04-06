// Package fileedit implements the FileEdit tool for making targeted string replacements in files.
//
// Source reference: tools/FileEditTool/FileEditTool.ts
// 1:1 port from the TypeScript source.
package fileedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/user/gbot/pkg/tool"
	"github.com/user/gbot/pkg/types"
)

// Input is the file edit tool input schema.
// Source: FileEditTool.ts — Zod schema for file edit input.
type Input struct {
	FilePath   string `json:"file_path" validate:"required"`
	OldString  string `json:"old_string" validate:"required"`
	NewString  string `json:"new_string" validate:"required"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Output is the file edit tool output.
// Source: FileEditTool.ts — tool result data.
type Output struct {
	Success      bool   `json:"success"`
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

// New creates the FileEdit tool.
// Source: tools/FileEditTool/FileEditTool.ts
func New() tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path", "old_string", "new_string"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "The absolute path to the file to edit."
			},
			"old_string": {
				"type": "string",
				"description": "The text to replace. Must be unique in the file unless replace_all is true."
			},
			"new_string": {
				"type": "string",
				"description": "The text to replace it with."
			},
			"replace_all": {
				"type": "boolean",
				"description": "Replace all occurrences of old_string. Default: false."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:  "FileEdit",
		Aliases_: []string{"fileedit", "edit"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Edit a file with string replacement", nil
			}
			return fmt.Sprintf("Edit file: %s", in.FilePath), nil
		},
		Call_: Execute,
		IsReadOnly_: func(json.RawMessage) bool {
			return false // editing is never read-only
		},
		IsDestructive_: func(json.RawMessage) bool {
			return false // targeted edits, not destructive
		},
		IsConcurrencySafe_: func(json.RawMessage) bool {
			return false // modifies files, not concurrency-safe
		},
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_: "Make targeted string replacements in files. old_string must be unique unless replace_all is true.",
	})
}

// Execute performs a string replacement in a file.
// Source: FileEditTool.ts:call() — 1:1 port.
func Execute(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
	var in Input
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	if in.FilePath == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	if in.OldString == "" {
		return nil, fmt.Errorf("old_string is required")
	}

	// Read the file
	data, err := os.ReadFile(in.FilePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	content := string(data)

	// Count occurrences
	count := strings.Count(content, in.OldString)
	if count == 0 {
		return nil, fmt.Errorf("old_string not found in file: %s", in.FilePath)
	}

	// Enforce uniqueness unless replace_all
	if !in.ReplaceAll && count > 1 {
		return nil, fmt.Errorf("old_string is not unique in file (found %d occurrences). Either provide more context to make it unique, or set replace_all to true", count)
	}

	// Perform replacement
	var newContent string
	var replacements int
	if in.ReplaceAll {
		newContent = strings.ReplaceAll(content, in.OldString, in.NewString)
		replacements = count
	} else {
		newContent = strings.Replace(content, in.OldString, in.NewString, 1)
		replacements = 1
	}

	// Write back
	err = os.WriteFile(in.FilePath, []byte(newContent), 0)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	return &tool.ToolResult{Data: &Output{
		Success:      true,
		Path:         in.FilePath,
		Replacements: replacements,
	}}, nil
}
