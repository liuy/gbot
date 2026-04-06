package grep_test

import (
	"encoding/json"
	"testing"

	"github.com/user/gbot/pkg/tool/grep"
)

// ---------------------------------------------------------------------------
// parseRGOutput benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMatchMarshal(b *testing.B) {
	m := grep.Match{
		File:    "pkg/engine/engine.go",
		Line:    42,
		Content: "func processRequest(ctx context.Context, input json.RawMessage) (*types.Response, error) {",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(m)
	}
}

func BenchmarkMatchUnmarshal(b *testing.B) {
	data := []byte(`{"file":"pkg/engine/engine.go","line":42,"content":"func processRequest(ctx context.Context, input json.RawMessage) (*types.Response, error) {"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var m grep.Match
		_ = json.Unmarshal(data, &m)
	}
}

func BenchmarkOutputMarshal_Small(b *testing.B) {
	output := grep.Output{
		Matches: make([]grep.Match, 10),
		Count:   10,
	}
	for i := range output.Matches {
		output.Matches[i] = grep.Match{
			File:    "pkg/types/types.go",
			Line:    i + 1,
			Content: "type ContentBlock struct { Type ContentType `json:\"type\"` }",
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(output)
	}
}

func BenchmarkOutputMarshal_Medium(b *testing.B) {
	output := grep.Output{
		Matches: make([]grep.Match, 100),
		Count:   100,
	}
	for i := range output.Matches {
		output.Matches[i] = grep.Match{
			File:    "pkg/engine/engine.go",
			Line:    i + 1,
			Content: "func processRequest(ctx context.Context, input json.RawMessage) (*types.Response, error) {",
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(output)
	}
}

func BenchmarkOutputMarshal_Large(b *testing.B) {
	output := grep.Output{
		Matches: make([]grep.Match, 1000),
		Count:   1000,
	}
	for i := range output.Matches {
		output.Matches[i] = grep.Match{
			File:    "pkg/llm/anthropic.go",
			Line:    i + 1,
			Content: "func (p *AnthropicProvider) ParseEvent(eventType, data string) StreamEvent {",
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(output)
	}
}

func BenchmarkOutputUnmarshal_Small(b *testing.B) {
	matches := make([]grep.Match, 10)
	for i := range matches {
		matches[i] = grep.Match{File: "types.go", Line: i + 1, Content: "type Foo struct {}"}
	}
	output := grep.Output{Matches: matches, Count: len(matches)}
	data, _ := json.Marshal(output)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out grep.Output
		_ = json.Unmarshal(data, &out)
	}
}

func BenchmarkOutputUnmarshal_Large(b *testing.B) {
	matches := make([]grep.Match, 500)
	for i := range matches {
		matches[i] = grep.Match{
			File:    "engine.go",
			Line:    i + 1,
			Content: "func processRequest(ctx context.Context, input json.RawMessage) (*types.Response, error) {",
		}
	}
	output := grep.Output{Matches: matches, Count: len(matches)}
	data, _ := json.Marshal(output)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out grep.Output
		_ = json.Unmarshal(data, &out)
	}
}

// ---------------------------------------------------------------------------
// Grep tool construction benchmark
// ---------------------------------------------------------------------------

func BenchmarkNew(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = grep.New()
	}
}

// ---------------------------------------------------------------------------
// Input JSON unmarshal benchmark
// ---------------------------------------------------------------------------

func BenchmarkInputUnmarshal(b *testing.B) {
	data := json.RawMessage(`{"pattern":"func.*Benchmark","path":"/src","include":"*.go","type":"go"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var in grep.Input
		_ = json.Unmarshal(data, &in)
	}
}
