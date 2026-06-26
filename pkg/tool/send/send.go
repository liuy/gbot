// Package send implements the Send tool for delivering local files to the
// WeChat user over the iLink CDN upload pipeline. WeChat-engine-only: the tool
// is registered into the WeChat engine's registry, absent from the TUI engine.
package send

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/liuy/gbot/pkg/tool"
)

// FileSender is implemented by connectors that can deliver a local file.
// Satisfied by *wechat.WeChatConnector (SendFile).
type FileSender interface {
	SendFile(ctx context.Context, filePath, caption string) error
}

// Input is the send tool input schema.
type Input struct {
	FilePath string `json:"file_path" validate:"required"`
	Caption  string `json:"caption,omitempty"`
}

const staticDescription = "Send a file (image, document, or video) to the user."

// New creates the Send tool bound to the given FileSender.
func New(sender FileSender) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "Path to the local file to send."
			},
			"caption": {
				"type": "string",
				"description": "Optional message to send with the file."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Send",
		Aliases_:     []string{"send"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return staticDescription, nil
			}
			if in.FilePath != "" {
				return in.FilePath, nil
			}
			return staticDescription, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("send: invalid input: %w", err)
			}
			if in.FilePath == "" {
				return nil, fmt.Errorf("send: file_path is required")
			}
			// Fail fast on a missing file so the LLM gets a clear error instead
			// of a raw CDN read failure mid-upload.
			if _, err := os.Stat(in.FilePath); err != nil {
				return nil, fmt.Errorf("send: file not found: %s", in.FilePath)
			}
			if err := sender.SendFile(ctx, in.FilePath, in.Caption); err != nil {
				return nil, err
			}
			return &tool.ToolResult{
				Data: map[string]any{
					"file_path": in.FilePath,
					"status":    "sent",
				},
			}, nil
		},
		IsReadOnly_:        func(json.RawMessage) bool { return false },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_:            "Send a file (image, document, or video) to the user.",
		RenderResult_: func(data any) string {
			m, ok := data.(map[string]any)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			fp, _ := m["file_path"].(string)
			if fp == "" {
				return "Sent"
			}
			return fmt.Sprintf("Sent %s", fp)
		},
	})
}
