package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Mock provider for benchmarks
// ---------------------------------------------------------------------------

type benchMockProvider struct{}

func (m *benchMockProvider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	return &llm.Response{
		ID:      "msg_bench",
		Model:   "test-model",
		Content: []types.ContentBlock{types.NewTextBlock("bench response")},
		Usage:   types.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (m *benchMockProvider) Stream(ctx context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 16)
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{
			Type: "message_start",
			Message: &llm.MessageStart{
				Model: "test-model",
				Usage: types.Usage{InputTokens: 100},
			},
		}
		ch <- llm.StreamEvent{
			Type:  "content_block_start",
			Index: 0,
			ContentBlock: &types.ContentBlock{
				Type: types.ContentTypeText,
			},
		}
		ch <- llm.StreamEvent{
			Type:  "content_block_delta",
			Index: 0,
			Delta: &llm.StreamDelta{Type: "text_delta", Text: "Hello from benchmark"},
		}
		ch <- llm.StreamEvent{
			Type:  "content_block_stop",
			Index: 0,
		}
		ch <- llm.StreamEvent{
			Type:     "message_delta",
			DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"},
			Usage:    &llm.UsageDelta{OutputTokens: 20},
		}
		ch <- llm.StreamEvent{Type: "message_stop"}
	}()
	return ch, nil
}

// ---------------------------------------------------------------------------
// Message marshaling benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMarshalMessages(b *testing.B) {
	mp := &benchMockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
	})
	eng.AddSystemMessage("system instruction")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eng.marshalMessages()
	}
}

func BenchmarkMarshalMessages_WithHistory(b *testing.B) {
	mp := &benchMockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
	})
	eng.AddSystemMessage("system instruction")
	eng.AddSystemMessage("another system instruction")
	eng.AddSystemMessage("third instruction")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eng.marshalMessages()
	}
}

func BenchmarkMarshalMessages_LargeHistory(b *testing.B) {
	mp := &benchMockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
	})

	// Simulate 20-turn conversation
	for range 20 {
		eng.messages = append(eng.messages, types.Message{
			Role: types.RoleUser,
			Content: []types.ContentBlock{
				types.NewTextBlock("This is a user message with some content about the codebase."),
			},
			Timestamp: time.Now(),
		})
		eng.messages = append(eng.messages, types.Message{
			Role: types.RoleAssistant,
			Content: []types.ContentBlock{
				types.NewTextBlock("This is an assistant response with analysis and recommendations."),
			},
			Timestamp: time.Now(),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = eng.marshalMessages()
	}
}

// ---------------------------------------------------------------------------
// Streaming response accumulation benchmark
// ---------------------------------------------------------------------------

func BenchmarkCallLLM_Accumulate(b *testing.B) {
	mp := &benchMockProvider{}
	eng := New(&Params{
		Provider: mp,
		Model:    "test-model",
	})

	systemPrompt := json.RawMessage(`"You are a helpful assistant."`)
	eventCh := make(chan types.QueryEvent, 128)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = eng.callLLM(context.Background(), systemPrompt, eventCh)
		// Drain events
		for {
			select {
			case <-eventCh:
			default:
				goto next3
			}
		}
	next3:
		eng.Reset()
	}
}

// ---------------------------------------------------------------------------
// Message JSON serialization benchmark
// ---------------------------------------------------------------------------

func BenchmarkMessageJSONMarshal(b *testing.B) {
	msg := types.Message{
		Role: types.RoleAssistant,
		Content: []types.ContentBlock{
			types.NewTextBlock("Here's the analysis of your codebase."),
			types.NewToolUseBlock("toolu_01", "Bash", json.RawMessage(`{"command":"go test ./..."}`)),
			types.NewToolResultBlock("toolu_01", json.RawMessage(`{"output":"ok  github.com/liuy/gbot/pkg/types  0.007s"}`), false),
		},
		StopReason: "end_turn",
		Usage:      &types.Usage{InputTokens: 500, OutputTokens: 200},
		Timestamp:  time.Now(),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(msg)
	}
}
