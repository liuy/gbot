// Package forget implements the forget tool, which deletes a fact by id.
// Registered for both the dream agent and the main agent so the main agent
// can honor explicit user requests to forget facts.
package forget

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
)

// Input is the forget tool input schema.
type Input struct {
	FactID int64 `json:"fact_id"`
}

// Output is the forget tool result.
type Output struct {
	Deleted bool   `json:"deleted"`
	FactID  int64  `json:"fact_id"`
	Message string `json:"message"`
}

// New creates the forget tool.
func New(store *short.Store) tool.Tool {
	schema := json.RawMessage(`{
		"type": "object",
		"required": ["fact_id"],
		"properties": {
			"fact_id": {
				"type": "integer",
				"description": "The fact_id to delete (obtained from recall results)"
			}
		}
	}`)

	return tool.BuildTool(tool.ToolDef{
		Name_:        "Forget",
		Aliases_:     []string{"forget"},
		InputSchema_: func() json.RawMessage { return schema },
		Description_: func(input json.RawMessage) (string, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return "Delete a fact by id", nil
			}
			return fmt.Sprintf("fact_id=%d", in.FactID), nil
		},
		Call_: func(ctx context.Context, input json.RawMessage, tctx *tool.ToolUseContext) (*tool.ToolResult, error) {
			var in Input
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, fmt.Errorf("parse input: %w", err)
			}
			if err := store.DeleteFact(in.FactID); err != nil {
				return nil, fmt.Errorf("delete fact: %w", err)
			}
			return &tool.ToolResult{Data: &Output{
				Deleted: true,
				FactID:  in.FactID,
				Message: fmt.Sprintf("Deleted fact %d.", in.FactID),
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
