package llm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
)

// ---------------------------------------------------------------------------
// ParseEvent benchmarks
// ---------------------------------------------------------------------------

func BenchmarkParseEvent_MessageStart(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"message":{"id":"msg_01ABCDEF","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":2500,"output_tokens":0,"cache_creation_input_tokens":500,"cache_read_input_tokens":200}}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("message_start", data)
	}
}

func BenchmarkParseEvent_ContentBlockStart(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"content_block":{"type":"text","text":""}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("content_block_start", data)
	}
}

func BenchmarkParseEvent_ContentBlockStart_ToolUse(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":1,"content_block":{"type":"tool_use","id":"toolu_01ABCDEF","name":"bash","input":{}}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("content_block_start", data)
	}
}

func BenchmarkParseEvent_ContentBlockDelta(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"delta":{"type":"text_delta","text":"The quick brown fox jumps over the lazy dog. This is a longer text delta to simulate realistic streaming content."}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("content_block_delta", data)
	}
}

func BenchmarkParseEvent_ContentBlockDelta_JSON(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"go test -v -count=1 -timeout 120s ./pkg/engine/ ./pkg/llm/ ./pkg/types/\""}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("content_block_delta", data)
	}
}

func BenchmarkParseEvent_MessageDelta(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("message_delta", data)
	}
}

func BenchmarkParseEvent_Error(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	data := `{"error":{"type":"overloaded_error","message":"The API is temporarily overloaded. Please retry after a brief wait."}}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseEvent("error", data)
	}
}

// ---------------------------------------------------------------------------
// ParseSSE benchmarks — realistic SSE streams
// ---------------------------------------------------------------------------

// buildSSEStream creates a synthetic SSE stream with the given number of
// content_block_delta events, bookended by the standard message lifecycle.
func buildSSEStream(numDeltas int) string {
	var buf strings.Builder
	bp := &buf

	// message_start
	buf.WriteString("event: message_start\n")
	buf.WriteString(`data: {"message":{"id":"msg_bench","type":"message","role":"assistant","model":"claude-sonnet-4-20250514","usage":{"input_tokens":1000}}}`)
	buf.WriteString("\n\n")

	// content_block_start
	buf.WriteString("event: content_block_start\n")
	buf.WriteString(`data: {"index":0,"content_block":{"type":"text","text":""}}`)
	buf.WriteString("\n\n")

	// content_block_delta events
	for i := range numDeltas {
		buf.WriteString("event: content_block_delta\n")
		fmt.Fprintf(bp, `data: {"index":0,"delta":{"type":"text_delta","text":"word_%d "}}`, i)
		buf.WriteString("\n\n")
	}

	// content_block_stop
	buf.WriteString("event: content_block_stop\n")
	buf.WriteString(`data: {"index":0}`)
	buf.WriteString("\n\n")

	// message_delta
	buf.WriteString("event: message_delta\n")
	buf.WriteString(`data: {"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`)
	buf.WriteString("\n\n")

	// message_stop
	buf.WriteString("event: message_stop\n")
	buf.WriteString(`data: {}`)
	buf.WriteString("\n\n")

	return buf.String()
}

func BenchmarkParseSSE_SmallStream(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	sseInput := buildSSEStream(10)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		eventCh := make(chan llm.StreamEvent, 256)
		go func() {
			p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
			close(eventCh)
		}()
		for range eventCh {
		}
	}
}

func BenchmarkParseSSE_MediumStream(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	sseInput := buildSSEStream(100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		eventCh := make(chan llm.StreamEvent, 256)
		go func() {
			p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
			close(eventCh)
		}()
		for range eventCh {
		}
	}
}

func BenchmarkParseSSE_LargeStream(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	sseInput := buildSSEStream(500)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		eventCh := make(chan llm.StreamEvent, 1024)
		go func() {
			p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
			close(eventCh)
		}()
		for range eventCh {
		}
	}
}

func BenchmarkParseSSE_ToolUseStream(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})

	sseInput := "event: message_start\ndata: {\"message\":{\"id\":\"msg_bench\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-20250514\",\"usage\":{\"input_tokens\":500}}}\n\n" +
		"event: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_01\",\"name\":\"bash\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"go test ./...\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":15}}\n\n" +
		"event: message_stop\ndata: {}\n\n"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := context.Background()
		eventCh := make(chan llm.StreamEvent, 64)
		go func() {
			p.ParseSSE(ctx, strings.NewReader(sseInput), eventCh)
			close(eventCh)
		}()
		for range eventCh {
		}
	}
}

// ---------------------------------------------------------------------------
// CalculateBackoff benchmarks
// ---------------------------------------------------------------------------

func BenchmarkCalculateBackoff(b *testing.B) {
	cfg := llm.DefaultRetryConfig()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.CalculateBackoff(i%11, cfg)
	}
}

func BenchmarkCalculateBackoff_Attempt0(b *testing.B) {
	cfg := llm.DefaultRetryConfig()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.CalculateBackoff(0, cfg)
	}
}

func BenchmarkCalculateBackoff_Attempt5(b *testing.B) {
	cfg := llm.DefaultRetryConfig()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.CalculateBackoff(5, cfg)
	}
}

func BenchmarkCalculateBackoff_Attempt10(b *testing.B) {
	cfg := llm.DefaultRetryConfig()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.CalculateBackoff(10, cfg)
	}
}

// ---------------------------------------------------------------------------
// Error classification benchmarks
// ---------------------------------------------------------------------------

func BenchmarkIsRetryable(b *testing.B) {
	err := &llm.APIError{Retryable: true}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.IsRetryable(err)
	}
}

func BenchmarkIsRetryable_GenericError(b *testing.B) {
	err := fmt.Errorf("some generic error")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.IsRetryable(err)
	}
}

func BenchmarkIsContextOverflow(b *testing.B) {
	err := &llm.APIError{Status: 400, ErrorCode: "prompt_too_long"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.IsContextOverflow(err)
	}
}

func BenchmarkIsRateLimit(b *testing.B) {
	err := &llm.APIError{Status: 429}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.IsRateLimit(err)
	}
}

func BenchmarkIsRetryableStatus(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.IsRetryableStatus(429)
	}
}

// ---------------------------------------------------------------------------
// ParseAPIError benchmarks
// ---------------------------------------------------------------------------

func BenchmarkParseAPIError_ValidJSON(b *testing.B) {
	p := llm.NewAnthropicProvider(&llm.AnthropicConfig{APIKey: "key", Model: "m"})
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Too many requests. Please retry after a brief wait."}}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = p.ParseAPIError(body, 429)
	}
}

// ---------------------------------------------------------------------------
// DefaultRetryConfig benchmark
// ---------------------------------------------------------------------------

func BenchmarkDefaultRetryConfig(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = llm.DefaultRetryConfig()
	}
}

// ---------------------------------------------------------------------------
// Ensure time package is imported (used by buildSSEStream indirectly)
// ---------------------------------------------------------------------------

var _ = time.Millisecond
