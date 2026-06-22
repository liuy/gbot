package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/types"
)

// spyProviderForCompact records every Complete() call with the model name.
type spyProviderForCompact struct {
	mu            sync.Mutex
	models        []string
	completeCalls int
}

func (s *spyProviderForCompact) Name() string { return "spy" }
func (s *spyProviderForCompact) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}
func (s *spyProviderForCompact) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	s.models = append(s.models, req.Model)
	return &llm.Response{
		Role:    "assistant",
		Model:   req.Model,
		Content: []types.ContentBlock{types.NewTextBlock("Summary.")},
		Usage:   types.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

// TestCompact_UsesLiveProviderAndModel is the red-light test for the bug
// where compact used the provider/model captured at construction time
// instead of the current one. After switching provider+model, compact
// must call the NEW provider with the NEW model.
//
// Without the fix (compactor storing stale provider), this test fails
// because the old provider receives the summarize call.
func TestCompact_UsesLiveProviderAndModel(t *testing.T) {
	oldProv := &spyProviderForCompact{}
	newProv := &spyProviderForCompact{}

	store, err := short.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := New(&Params{
		Provider:      oldProv,
		ToolsProvider: func() map[string]tool.Tool { return nil },
		Model:         "old-model",
		AutoCompact:   AutoCompactConfig{ContextWindow: 1000},
	})
	eng.SetStore(store, t.TempDir())

	// Switch to new provider + model.
	eng.SetProvider(newProv)
	eng.SetModel("new-model")

	// Seed enough messages to trigger compact.
	msgs := make([]types.Message, 0, 10)
	for range 10 {
		msgs = append(msgs, types.Message{
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")},
		})
		msgs = append(msgs, types.Message{
			Role:    types.RoleAssistant,
			Content: []types.ContentBlock{types.NewTextBlock("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")},
		})
	}
	eng.SetMessages(msgs)

	// Trigger compact directly via the compactor.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := eng.compactor.Compact(ctx, msgs)
	if err != nil {
		t.Fatalf("Compact failed: %v", err)
	}
	if result == nil {
		t.Fatal("Compact returned nil result")
	}

	// oldProv must NOT have been called.
	oldProv.mu.Lock()
	oldCalls := oldProv.completeCalls
	oldProv.mu.Unlock()
	if oldCalls > 0 {
		t.Errorf("old provider was called %d times — compact should use the live (new) provider", oldCalls)
	}

	// newProv must have been called with "new-model".
	newProv.mu.Lock()
	defer newProv.mu.Unlock()
	if newProv.completeCalls == 0 {
		t.Fatal("new provider was never called — compact did not use the live provider")
	}
	if len(newProv.models) == 0 || newProv.models[0] != "new-model" {
		t.Errorf("compact used model %v, want new-model", newProv.models)
	}
}
