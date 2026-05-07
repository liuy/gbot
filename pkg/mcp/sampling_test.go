package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// ---------------------------------------------------------------------------
// Sampling handler tests
// ---------------------------------------------------------------------------

func TestSamplingHandler_NilProvider(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	handler := cm.makeSamplingHandler("test-server")

	_, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{MaxTokens: 100},
	})
	if err == nil {
		t.Fatal("expected error when provider is nil")
	}
	if providerErr := "no provider configured"; !strings.Contains(err.Error(), providerErr) {
		t.Errorf("error = %q, should contain %q", err.Error(), providerErr)
	}
}

func TestSamplingHandler_BasicCompletion(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"

	mock := &mockSamplingProvider{
		resp: &llm.Response{
			Content:    []types.ContentBlock{types.NewTextBlock("Hello from sampling")},
			Model:      "test-model",
			StopReason: "end_turn",
		},
	}
	cm.SetSamplingProvider(mock)

	handler := cm.makeSamplingHandler("test-server")
	result, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:    100,
			SystemPrompt: "You are helpful",
			Messages: []*mcp.SamplingMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hello"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Model != "test-model" {
		t.Errorf("model = %q, want %q", result.Model, "test-model")
	}
	if result.StopReason != "endTurn" {
		t.Errorf("stopReason = %q, want %q", result.StopReason, "endTurn")
	}
	if result.Role != "assistant" {
		t.Errorf("role = %q, want %q", result.Role, "assistant")
	}

	// Check content is text
	tc, ok := result.Content.(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content)
	}
	if tc.Text != "Hello from sampling" {
		t.Errorf("text = %q, want %q", tc.Text, "Hello from sampling")
	}
}

func TestSamplingHandler_MessageConversion(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"

	var capturedReq *llm.Request
	mock := &mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			capturedReq = req
			return &llm.Response{
				Content:    []types.ContentBlock{types.NewTextBlock("ok")},
				Model:      "test-model",
				StopReason: "end_turn",
			}, nil
		},
	}
	cm.SetSamplingProvider(mock)

	handler := cm.makeSamplingHandler("srv")
	_, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:    200,
			SystemPrompt: "Be concise",
			Messages: []*mcp.SamplingMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hi"}},
				{Role: "assistant", Content: &mcp.TextContent{Text: "Hello!"}},
				{Role: "user", Content: &mcp.TextContent{Text: "How are you?"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedReq == nil {
		t.Fatal("expected request to be captured")
	}
	if capturedReq.MaxTokens != 200 {
		t.Errorf("maxTokens = %d, want 200", capturedReq.MaxTokens)
	}
	if len(capturedReq.Messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(capturedReq.Messages))
	}
	if capturedReq.Messages[0].Role != types.RoleUser {
		t.Errorf("message[0] role = %q, want %q", capturedReq.Messages[0].Role, types.RoleUser)
	}
	if len(capturedReq.System) == 0 {
		t.Error("system prompt should be set")
	}
}

func TestSamplingHandler_TemperatureAndStopSequences(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"

	var capturedReq *llm.Request
	mock := &mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			capturedReq = req
			return &llm.Response{
				Content:    []types.ContentBlock{types.NewTextBlock("ok")},
				Model:      "test-model",
				StopReason: "end_turn",
			}, nil
		},
	}
	cm.SetSamplingProvider(mock)

	handler := cm.makeSamplingHandler("srv")
	_, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:     100,
			Temperature:   0.7,
			StopSequences: []string{"END", "STOP"},
			Messages: []*mcp.SamplingMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hi"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedReq.Temperature == nil || *capturedReq.Temperature != 0.7 {
		t.Errorf("temperature = %v, want 0.7", capturedReq.Temperature)
	}
	if len(capturedReq.StopSequences) != 2 {
		t.Fatalf("stopSequences count = %d, want 2", len(capturedReq.StopSequences))
	}
	if capturedReq.StopSequences[0] != "END" {
		t.Errorf("stopSequences[0] = %q, want %q", capturedReq.StopSequences[0], "END")
	}
}

func TestSamplingHandler_ProviderError(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"

	mock := &mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			return nil, fmt.Errorf("provider unavailable")
		},
	}
	cm.SetSamplingProvider(mock)

	handler := cm.makeSamplingHandler("srv")
	_, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages: []*mcp.SamplingMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hi"}},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error when provider fails")
	}
	if !strings.Contains(err.Error(), "provider unavailable") {
		t.Errorf("error = %q, should contain provider error", err.Error())
	}
}

func TestMcpContentToBlock_ImageContent(t *testing.T) {
	c := &mcp.ImageContent{MIMEType: "image/png", Data: []byte("fake-data")}
	block := mcpContentToBlock(c)
	if block.Type != types.ContentTypeText {
		t.Errorf("block type = %q, want %q", block.Type, types.ContentTypeText)
	}
	if !strings.Contains(block.Text, "image/png") {
		t.Errorf("block text should contain MIME type, got %q", block.Text)
	}
	if !strings.Contains(block.Text, "9 bytes") {
		t.Errorf("block text should contain byte count, got %q", block.Text)
	}
}

func TestMcpContentToBlock_UnknownContent(t *testing.T) {
	// Use a content type that doesn't match TextContent or ImageContent
	c := &mcp.AudioContent{Data: []byte("audio-data"), MIMEType: "audio/wav"}
	block := mcpContentToBlock(c)
	if block.Type != types.ContentTypeText {
		t.Errorf("block type = %q, want %q", block.Type, types.ContentTypeText)
	}
	if !strings.Contains(block.Text, "audio") {
		t.Errorf("block text should contain fallback representation, got %q", block.Text)
	}
}

func TestMapStopReason(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"end_turn", "endTurn"},
		{"max_tokens", "maxTokens"},
		{"tool_use", "toolUse"},
		{"stop_sequence", "stopSequence"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		got := mapStopReason(tt.input)
		if got != tt.want {
			t.Errorf("mapStopReason(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Mock
// ---------------------------------------------------------------------------

type mockSamplingProvider struct {
	resp *llm.Response
	fn   func(ctx context.Context, req *llm.Request) (*llm.Response, error)
}

func (m *mockSamplingProvider) Complete(ctx context.Context, req *llm.Request) (*llm.Response, error) {
	if m.fn != nil {
		return m.fn(ctx, req)
	}
	return m.resp, nil
}
