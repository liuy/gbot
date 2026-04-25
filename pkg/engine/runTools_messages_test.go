package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// TestExecuteAllResult_NewMessagesCollected verifies that NewMessages from
// tool results are collected and returned in ExecuteAllResult.
func TestExecuteAllResult_NewMessagesCollected(t *testing.T) {
	t.Parallel()

	msg1 := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("skill content")}}
	msg2 := types.Message{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("metadata")}}

	tools := map[string]tool.Tool{
		"msg_tool": tool.BuildTool(tool.ToolDef{
			Name_:        "msg_tool",
			InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
			Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data:       "ok",
					NewMessages: []types.Message{msg1, msg2},
				}, nil
			},
		}),
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "msg_tool", Input: json.RawMessage(`{}`)},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.ToolResultBlocks) != 1 {
		t.Fatalf("expected 1 tool result block, got %d", len(result.ToolResultBlocks))
	}
	if len(result.NewMessages) != 2 {
		t.Fatalf("expected 2 new messages, got %d", len(result.NewMessages))
	}

	// Verify messages are in order
	text1 := contentText(t, result.NewMessages[0])
	text2 := contentText(t, result.NewMessages[1])
	if !strings.Contains(text1, "skill content") {
		t.Errorf("first message should contain 'skill content', got %q", text1)
	}
	if !strings.Contains(text2, "metadata") {
		t.Errorf("second message should contain 'metadata', got %q", text2)
	}
}

// TestExecuteAllResult_NoNewMessages verifies that empty NewMessages is returned
// when tools don't produce any.
func TestExecuteAllResult_NoNewMessages(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"no_msg_tool": tool.BuildTool(tool.ToolDef{
			Name_:        "no_msg_tool",
			InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
			Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{Data: "ok"}, nil
			},
		}),
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "no_msg_tool", Input: json.RawMessage(`{}`)},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.NewMessages) != 0 {
		t.Errorf("expected 0 new messages, got %d", len(result.NewMessages))
	}
}

// TestExecuteAllResult_MultipleToolsWithMessages verifies NewMessages from
// multiple tools are collected in insertion order.
func TestExecuteAllResult_MultipleToolsWithMessages(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"a_tool": tool.BuildTool(tool.ToolDef{
			Name_:        "a_tool",
			InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
			Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data:       "a",
					NewMessages: []types.Message{
						{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg_a")}},
					},
				}, nil
			},
		}),
		"b_tool": tool.BuildTool(tool.ToolDef{
			Name_:        "b_tool",
			InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
			Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return &tool.ToolResult{
					Data:       "b",
					NewMessages: []types.Message{
						{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("msg_b")}},
					},
				}, nil
			},
		}),
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "a_tool", Input: json.RawMessage(`{}`)},
		{Type: types.ContentTypeToolUse, ID: "t2", Name: "b_tool", Input: json.RawMessage(`{}`)},
	}

	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.NewMessages) != 2 {
		t.Fatalf("expected 2 new messages, got %d", len(result.NewMessages))
	}

	// Order follows tool insertion order
	text0 := contentText(t, result.NewMessages[0])
	text1 := contentText(t, result.NewMessages[1])
	if !strings.Contains(text0, "msg_a") {
		t.Errorf("first message should be from tool a, got %q", text0)
	}
	if !strings.Contains(text1, "msg_b") {
		t.Errorf("second message should be from tool b, got %q", text1)
	}
}

// TestExecuteAllResult_NilResultNoPanic verifies that a tool returning nil
// ToolResult doesn't panic when checking NewMessages.
func TestExecuteAllResult_NilResultNoPanic(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{
		"nil_tool": tool.BuildTool(tool.ToolDef{
			Name_:        "nil_tool",
			InputSchema_: func() json.RawMessage { return json.RawMessage(`{"type":"object"}`) },
			Call_: func(ctx context.Context, input json.RawMessage, tctx *types.ToolUseContext) (*tool.ToolResult, error) {
				return nil, nil
			},
		}),
	}

	blocks := []types.ContentBlock{
		{Type: types.ContentTypeToolUse, ID: "t1", Name: "nil_tool", Input: json.RawMessage(`{}`)},
	}

	// Should not panic
	result := ConcurrentToolLoop(context.Background(), tools, blocks, nil, func(evt types.QueryEvent) {})

	if len(result.NewMessages) != 0 {
		t.Errorf("expected 0 new messages for nil result, got %d", len(result.NewMessages))
	}
}

// TestExecuteAllResult_EmptyBlocks returns nil result without panicking.
func TestExecuteAllResult_EmptyBlocks(t *testing.T) {
	t.Parallel()

	tools := map[string]tool.Tool{}
	result := ConcurrentToolLoop(context.Background(), tools, nil, nil, func(evt types.QueryEvent) {})

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.ToolResultBlocks) != 0 {
		t.Errorf("expected 0 tool result blocks, got %d", len(result.ToolResultBlocks))
	}
	if len(result.NewMessages) != 0 {
		t.Errorf("expected 0 new messages, got %d", len(result.NewMessages))
	}
}

// contentText extracts text from a message's content blocks.
func contentText(t *testing.T, msg types.Message) string {
	t.Helper()
	for _, b := range msg.Content {
		if b.Type == types.ContentTypeText {
			return b.Text
		}
	}
	return ""
}
