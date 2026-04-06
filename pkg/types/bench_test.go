package types_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/user/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// ContentBlock constructors
// ---------------------------------------------------------------------------

func BenchmarkNewTextBlock(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = types.NewTextBlock("The quick brown fox jumps over the lazy dog")
	}
}

func BenchmarkNewToolUseBlock(b *testing.B) {
	input := json.RawMessage(`{"command":"ls -la","working_dir":"/home/user/project"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = types.NewToolUseBlock("toolu_01ABCDEF", "Bash", input)
	}
}

func BenchmarkNewToolResultBlock(b *testing.B) {
	content := json.RawMessage(`{"output":"file1.go\nfile2.go\nfile3.go","exit_code":0}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = types.NewToolResultBlock("toolu_01ABCDEF", content, false)
	}
}

// ---------------------------------------------------------------------------
// Message JSON marshaling
// ---------------------------------------------------------------------------

func BenchmarkMessageMarshal_Simple(b *testing.B) {
	msg := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("hello")},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(msg)
	}
}

func BenchmarkMessageMarshal_Full(b *testing.B) {
	msg := types.Message{
		ID:         "msg_01ABCDEF123",
		Role:       types.RoleAssistant,
		Model:      "claude-sonnet-4-20250514",
		StopReason: "end_turn",
		Usage: &types.Usage{
			InputTokens:              1024,
			OutputTokens:             512,
			CacheCreationInputTokens: 128,
			CacheReadInputTokens:     64,
		},
		Content: []types.ContentBlock{
			types.NewTextBlock("Here is the analysis of your codebase."),
			types.NewToolUseBlock("toolu_01", "Read", json.RawMessage(`{"path":"/src/main.go"}`)),
			types.NewToolUseBlock("toolu_02", "Grep", json.RawMessage(`{"pattern":"TODO","type":"go"}`)),
		},
		Timestamp: time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(msg)
	}
}

func BenchmarkMessageMarshal_LargeContent(b *testing.B) {
	// Simulate a message with many content blocks (typical after tool use)
	blocks := make([]types.ContentBlock, 20)
	for i := range blocks {
		switch i % 3 {
		case 0:
			blocks[i] = types.NewTextBlock("Some analysis text that describes what was found in the file")
		case 1:
			blocks[i] = types.NewToolUseBlock(
				"toolu_id",
				"Bash",
				json.RawMessage(`{"command":"grep -rn pattern src/"}`),
			)
		case 2:
			blocks[i] = types.NewToolResultBlock(
				"toolu_id",
				json.RawMessage(`{"output":"matched line 1\nmatched line 2\nmatched line 3"}`),
				false,
			)
		}
	}
	msg := types.Message{
		ID:         "msg_large",
		Role:       types.RoleAssistant,
		Model:      "claude-sonnet-4-20250514",
		StopReason: "tool_use",
		Usage:      &types.Usage{InputTokens: 5000, OutputTokens: 2000},
		Content:    blocks,
		Timestamp:  time.Now(),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(msg)
	}
}

// ---------------------------------------------------------------------------
// Message JSON unmarshaling
// ---------------------------------------------------------------------------

func BenchmarkMessageUnmarshal_Simple(b *testing.B) {
	data := []byte(`{"role":"user","content":[{"type":"text","text":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg types.Message
		_ = json.Unmarshal(data, &msg)
	}
}

func BenchmarkMessageUnmarshal_Full(b *testing.B) {
	data := []byte(`{
		"id":"msg_01ABCDEF123",
		"role":"assistant",
		"model":"claude-sonnet-4-20250514",
		"stop_reason":"end_turn",
		"usage":{"input_tokens":1024,"output_tokens":512,"cache_creation_input_tokens":128,"cache_read_input_tokens":64},
		"content":[
			{"type":"text","text":"Here is the analysis."},
			{"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"/src/main.go"}},
			{"type":"tool_result","tool_use_id":"toolu_01","content":{"output":"package main\n\nfunc main() {}"},"is_error":false}
		],
		"timestamp":"2025-06-15T10:30:00Z"
	}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var msg types.Message
		_ = json.Unmarshal(data, &msg)
	}
}

// ---------------------------------------------------------------------------
// ContentBlock JSON round-trip
// ---------------------------------------------------------------------------

func BenchmarkContentBlockMarshal_Text(b *testing.B) {
	block := types.NewTextBlock("Hello, this is a text block with some content for benchmarking")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(block)
	}
}

func BenchmarkContentBlockMarshal_ToolUse(b *testing.B) {
	block := types.NewToolUseBlock("toolu_01", "Bash", json.RawMessage(`{"command":"go test ./..."}`))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(block)
	}
}

func BenchmarkContentBlockMarshal_ToolResult(b *testing.B) {
	block := types.NewToolResultBlock("toolu_01", json.RawMessage(`"ok"`), false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(block)
	}
}

func BenchmarkContentBlockUnmarshal_Text(b *testing.B) {
	data := []byte(`{"type":"text","text":"Hello world"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var block types.ContentBlock
		_ = json.Unmarshal(data, &block)
	}
}

func BenchmarkContentBlockUnmarshal_ToolUse(b *testing.B) {
	data := []byte(`{"type":"tool_use","id":"toolu_01","name":"Read","input":{"path":"/src/main.go"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var block types.ContentBlock
		_ = json.Unmarshal(data, &block)
	}
}

// ---------------------------------------------------------------------------
// Usage JSON
// ---------------------------------------------------------------------------

func BenchmarkUsageMarshal(b *testing.B) {
	u := types.Usage{
		InputTokens:              10000,
		OutputTokens:             5000,
		CacheCreationInputTokens: 2000,
		CacheReadInputTokens:     1000,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(u)
	}
}

func BenchmarkUsageUnmarshal(b *testing.B) {
	data := []byte(`{"input_tokens":10000,"output_tokens":5000,"cache_creation_input_tokens":2000,"cache_read_input_tokens":1000}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var u types.Usage
		_ = json.Unmarshal(data, &u)
	}
}
