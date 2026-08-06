// Package remember implements the remember tool, which stores or updates a
// fact in the structured facts table. Registered for both the dream agent and
// the main agent so the main agent can honor explicit user requests to
// remember or forget facts.
package remember

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

// Input is the remember tool input schema.
type Input struct {
	// FactID set = update an existing fact atomically; nil = create.
	FactID  *int64 `json:"fact_id,omitempty"`
	Content string `json:"content"`
}

// Output is the remember tool result.
type Output struct {
	Stored    bool   `json:"stored"`
	FactID    int64  `json:"fact_id"`
	Duplicate bool   `json:"duplicate"`
	Message   string `json:"message"`
}

// New creates the remember tool.
func New(store *short.Store) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["content"],
		"properties": {
			"content": {
				"type": "string",
				"description": "The fact to remember. Must be self-contained: subject + (time if not permanent) + relation/condition as needed."
			},
			"fact_id": {
				"type": "integer",
				"description": "Optional. The fact_id to update. Omit to create a new fact. Use with recall to find the id of an existing fact."
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "remember",
		Aliases_:     []string{"Remember"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Store a new fact", nil
			}
			if len(in.Content) > 80 {
				return in.Content[:80], nil
			}
			return in.Content, nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("parse input: %w", err)
			}
			if strings.TrimSpace(in.Content) == "" {
				return nil, fmt.Errorf("content is required")
			}
			if in.FactID != nil {
				newID, inserted, err := store.UpdateFact(*in.FactID, in.Content)
				if err != nil {
					return nil, fmt.Errorf("update fact: %w", err)
				}
				msg := "Updated fact."
				if !inserted {
					msg = "Fact already exists (duplicate)."
				}
				return &tool.ToolResult{Data: &Output{
					Stored:    inserted,
					FactID:    newID,
					Duplicate: !inserted,
					Message:   msg,
				}}, nil
			}
			id, inserted, err := store.AddFact(in.Content)
			if err != nil {
				return nil, fmt.Errorf("add fact: %w", err)
			}
			msg := "Stored new fact."
			if !inserted {
				msg = "Fact already exists (duplicate)."
			}
			return &tool.ToolResult{Data: &Output{
				Stored:    inserted,
				FactID:    id,
				Duplicate: !inserted,
				Message:   msg,
			}}, nil
		},
		IsReadOnly_:        func(json.RawMessage) bool { return false },
		IsConcurrencySafe_: func(json.RawMessage) bool { return false },
		InterruptBehavior_: tool.InterruptBlock,
		MaxResultSizeChars: 10000,
		Prompt_:            prompt,
		RenderResult_: func(data any) string {
			out, ok := data.(*Output)
			if !ok {
				b, _ := json.Marshal(data)
				return string(b)
			}
			return out.Message
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
			return &o, nil
		},
	})
}
