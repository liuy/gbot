package mcp

// Mutation-killing tests for sampling.go (batch mcp-small8).
// Each assertion pins an observable behavior that a specific escaped mutant
// breaks; values are byte-exact contrasts against the mutant replacements.
// Mutation inventory and verdicts: /home/yliu/mutate-mcp1/ledger-A.txt

import (
	"context"
	"errors"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/types"
)

// samplingResp is the canned provider response shared by these tests.
func samplingResp() *llm.Response {
	return &llm.Response{
		Content:    []types.ContentBlock{types.NewTextBlock("ok")},
		Model:      "test-model",
		StopReason: "end_turn",
	}
}

func samplingHandlerWithCapture(fn func(ctx context.Context, req *llm.Request) (*llm.Response, error)) (func(context.Context, *mcp.ClientRequest[*mcp.CreateMessageParams]) (*mcp.CreateMessageResult, error), func() *llm.Request) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"
	var captured *llm.Request
	cm.SetSamplingProvider(&mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			captured = req
			if fn != nil {
				return fn(ctx, req)
			}
			return samplingResp(), nil
		},
	})
	return cm.makeSamplingHandler("mut-server"), func() *llm.Request { return captured }
}

// Kills sampling.go:63 numbers/decrementer (30→29), :63 numbers/incrementer
// (30→31), and :100 expression/context-nil (ctx→nil panics on Deadline).
// The 30s sampling timeout is a deliberate bound; ±1s drift must be caught.
func TestSamplingHandler_TimeoutDeadline30s(t *testing.T) {
	var deadline time.Time
	var hasDeadline bool
	handler, _ := samplingHandlerWithCapture(func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
		deadline, hasDeadline = ctx.Deadline()
		return samplingResp(), nil
	})

	_, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages:  []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasDeadline {
		t.Fatal("sampling context must carry a deadline (WithTimeout)")
	}
	remaining := time.Until(deadline) // REAL-TIME: deadline window is wall-clock by nature
	if remaining <= 29*time.Second+200*time.Millisecond {
		t.Errorf("timeout remaining = %v, want ~30s (mutation shrank the 30s bound)", remaining)
	}
	if remaining >= 31*time.Second-200*time.Millisecond {
		t.Errorf("timeout remaining = %v, want ~30s (mutation grew the 30s bound)", remaining)
	}
}

// Kills sampling.go:64 statement/defer-remove (defer cancel() → immediate
// cancel()): the provider must receive a live context (not pre-cancelled) and
// the context must be cancelled once the handler returns.
func TestSamplingHandler_CancelsContextOnReturn(t *testing.T) {
	var providerCtx context.Context
	var ctxErrAtEntry error
	handler, _ := samplingHandlerWithCapture(func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
		providerCtx = ctx
		ctxErrAtEntry = ctx.Err()
		return samplingResp(), nil
	})

	if _, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages:  []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ctxErrAtEntry != nil {
		t.Fatalf("context already cancelled at Complete entry: %v (defer cancel turned into immediate call)", ctxErrAtEntry)
	}

	select {
	case <-providerCtx.Done():
	case <-time.After(2 * time.Second): // REAL-TIME: cancellation propagation is wall-clock
		t.Fatal("sampling context not cancelled after handler returned")
	}
	if err := providerCtx.Err(); err != context.Canceled {
		t.Errorf("providerCtx.Err() = %v, want context.Canceled", err)
	}
}

// Kills sampling.go:88 expression/comparison (> → >=) and :88
// numbers/decrementer (0 → -1): temperature 0 means "not requested" and must
// leave llmReq.Temperature nil.
func TestSamplingHandler_ZeroTemperatureOmitted(t *testing.T) {
	handler, captured := samplingHandlerWithCapture(nil)

	if _, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:   100,
			Temperature: 0,
			Messages:    []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := captured()
	if req == nil {
		t.Fatal("provider request not captured")
	}
	if req.Temperature != nil {
		t.Errorf("temperature = %v, want nil for Temperature=0", *req.Temperature)
	}
}

// Kills sampling.go:94 numbers/decrementer (0 → -1): an empty-but-present
// StopSequences must not be forwarded (nil vs empty slice is observable).
func TestSamplingHandler_EmptyStopSequencesOmitted(t *testing.T) {
	handler, captured := samplingHandlerWithCapture(nil)

	if _, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:     100,
			StopSequences: []string{},
			Messages:      []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := captured()
	if req.StopSequences != nil {
		t.Errorf("stopSequences = %v, want nil for empty input", req.StopSequences)
	}
}

// Kills sampling.go:94 numbers/incrementer (0 → 1): a single stop sequence
// (len 1) must still be forwarded.
func TestSamplingHandler_SingleStopSequenceForwarded(t *testing.T) {
	handler, captured := samplingHandlerWithCapture(nil)

	if _, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens:     100,
			StopSequences: []string{"END"},
			Messages:      []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := captured()
	if len(req.StopSequences) != 1 {
		t.Fatalf("stopSequences len = %d, want 1", len(req.StopSequences))
	}
	if req.StopSequences[0] != "END" {
		t.Errorf("stopSequences[0] = %q, want %q", req.StopSequences[0], "END")
	}
}

// Kills sampling.go:102 expression/errorf-wrap (%w → %v): the provider error
// must stay unwrappable so callers can errors.Is/As into provider failures.
func TestSamplingHandler_ProviderErrorUnwrapped(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"
	sentinel := errors.New("provider boom")
	cm.SetSamplingProvider(&mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			return nil, sentinel
		},
	})

	_, err := cm.makeSamplingHandler("srv")(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages:  []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error from failing provider")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %q, must wrap the provider sentinel error", err)
	}
}

// Kills sampling.go:114 numbers/decrementer (0 → -1): with no content blocks
// the result must keep Content nil — the mutant indexes Content[0] on an empty
// slice and panics.
func TestSamplingHandler_EmptyContentResultContentNil(t *testing.T) {
	cm := NewClientManager(TransportFactory{}, true, "")
	cm.samplingModel = "test-model"
	cm.SetSamplingProvider(&mockSamplingProvider{
		fn: func(ctx context.Context, req *llm.Request) (*llm.Response, error) {
			return &llm.Response{Model: "test-model", StopReason: "end_turn"}, nil
		},
	})

	result, err := cm.makeSamplingHandler("srv")(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages:  []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "Hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != nil {
		t.Errorf("result.Content = %v, want nil for empty provider content", result.Content)
	}
}

// Kills sampling.go:129 conditional/negated, :130 branch/if, :130
// statement/remove, :134 composite/field-clear — non-nil message content must
// convert to exactly one text block, and nil content to none.
func TestSamplingHandler_MessageContentBlocksConverted(t *testing.T) {
	handler, captured := samplingHandlerWithCapture(nil)

	if _, err := handler(context.Background(), &mcp.ClientRequest[*mcp.CreateMessageParams]{
		Params: &mcp.CreateMessageParams{
			MaxTokens: 100,
			Messages: []*mcp.SamplingMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "Hi"}},
				{Role: "user"},
			},
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := captured()
	if req == nil {
		t.Fatal("provider request not captured")
	}
	if len(req.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(req.Messages))
	}
	withContent := req.Messages[0].Content
	if len(withContent) != 1 {
		t.Fatalf("message[0] content blocks len = %d, want 1", len(withContent))
	}
	if withContent[0].Type != types.ContentTypeText {
		t.Errorf("message[0] block type = %q, want %q", withContent[0].Type, types.ContentTypeText)
	}
	if withContent[0].Text != "Hi" {
		t.Errorf("message[0] block text = %q, want %q", withContent[0].Text, "Hi")
	}
	if blockCount := len(req.Messages[1].Content); blockCount != 0 {
		t.Errorf("message[1] content blocks len = %d, want 0 for nil content", blockCount)
	}
}

// Kills sampling.go:142 branch/case and :142 statement/return — a TextContent
// must convert to a text block carrying the exact string (both mutants return
// an empty ContentBlock).
func TestMcpContentToBlock_TextContentExact(t *testing.T) {
	block := mcpContentToBlock(&mcp.TextContent{Text: "Hi"})
	if block.Type != types.ContentTypeText {
		t.Errorf("block type = %q, want %q", block.Type, types.ContentTypeText)
	}
	if block.Text != "Hi" {
		t.Errorf("block text = %q, want %q", block.Text, "Hi")
	}
}
