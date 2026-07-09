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
}

const staticDescription = "Send a file (image, document, or video) to the user."

// SendResult is the success response for the Send tool.
type SendResult struct {
	FilePath string `json:"file_path"`
	Status   string `json:"status"`
}

// New creates the Send tool bound to the given FileSender.
func New(sender FileSender) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["file_path"],
		"properties": {
			"file_path": {
				"type": "string",
				"description": "Path to the local file to send."
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
			if err := sender.SendFile(ctx, in.FilePath, ""); err != nil {
				return nil, err
			}
			return &tool.ToolResult{
				Data: &SendResult{FilePath: in.FilePath, Status: "sent"},
			}, nil
		},
		IsReadOnly_:        func(json.RawMessage) bool { return false },
		IsDestructive_:     func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return true },
		InterruptBehavior_: tool.InterruptCancel,
		Prompt_:            "Send a file (image, document, or video) to the user.",
		RenderResult_: func(data any) string {
			s, ok := data.(*SendResult)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			if s.FilePath == "" {
				return "Sent"
			}
			return fmt.Sprintf("Sent %s", s.FilePath)
		},
		DecodeResult_: func(raw json.RawMessage) (any, error) {
			var s SendResult
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, err
			}
			return &s, nil
		},
	})
}
