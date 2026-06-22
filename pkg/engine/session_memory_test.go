package engine

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/session"
	"github.com/liuy/gbot/pkg/tool"
	"github.com/liuy/gbot/pkg/tool/fileedit"
	"github.com/liuy/gbot/pkg/tool/fileread"
	"github.com/liuy/gbot/pkg/types"
)

// smSpyProvider records the model name from every Stream call.
type smSpyProvider struct {
	mu     sync.Mutex
	models []string
}

func (s *smSpyProvider) Name() string { return "sm-spy" }
func (s *smSpyProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, nil
}
func (s *smSpyProvider) Stream(_ context.Context, req *llm.Request) (<-chan llm.StreamEvent, error) {
	s.mu.Lock()
	s.models = append(s.models, req.Model)
	s.mu.Unlock()
	ch := make(chan llm.StreamEvent, 6)
	go func() {
		ch <- llm.StreamEvent{Type: "message_start", Message: &llm.MessageStart{}}
		ch <- llm.StreamEvent{Type: "content_block_start", Index: 0, ContentBlock: &types.ContentBlock{Type: types.ContentTypeText}}
		ch <- llm.StreamEvent{Type: "content_block_delta", Index: 0, Delta: &llm.StreamDelta{Type: "text_delta", Text: "OK"}}
		ch <- llm.StreamEvent{Type: "content_block_stop", Index: 0}
		ch <- llm.StreamEvent{Type: "message_delta", Usage: &types.Usage{InputTokens: 100, OutputTokens: 10}, DeltaMsg: &llm.MessageDelta{StopReason: "end_turn"}}
		ch <- llm.StreamEvent{Type: "message_stop"}
		close(ch)
	}()
	return ch, nil
}

// TestSessionMemory_FollowsModelSwitch verifies that SM extraction uses the
// engine's current model at extract time, not a value captured at wire time.
// SM's extractFn creates a subengine via NewSubEngine(Model:""), which copies
// the parent's live model — so SetModel before extraction is reflected.
func TestSessionMemory_FollowsModelSwitch(t *testing.T) {
	projectDir := t.TempDir()
	spy := &smSpyProvider{}

	eng := New(&Params{
		Provider:    spy,
		Logger:      slog.Default(),
		Model:       "glm-5",
		EngineID:    "main",
		AutoCompact: AutoCompactConfig{ContextWindow: 5000},
	})

	smExtractFn := func(ctx context.Context, prompt string, notesPath string, messages []types.Message, sysPrompt string) error {
		subEng := eng.NewSubEngine(SubEngineOptions{
			SystemPrompt: sysPrompt,
			Tools:        map[string]tool.Tool{"Edit": fileedit.New(), "Read": fileread.New()},
			Model:        "", // inherit from parent
			AgentType:    "session_memory",
		})
		defer subEng.Close()
		extractionUserMsg := types.Message{
			ID:      uuid.New().String(),
			Role:    types.RoleUser,
			Content: []types.ContentBlock{types.NewTextBlock(prompt)},
		}
		forkMessages := append(slices.Clone(messages), extractionUserMsg)
		result := subEng.RunForkedQuery(ctx, forkMessages, sysPrompt)
		return result.Error
	}
	sm := session.New(session.DefaultConfig(), projectDir, "main", smExtractFn, slog.Default())
	eng.SetSessionMemory(sm)

	msgs := []types.Message{
		{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("task A")}},
		{Role: types.RoleAssistant, Content: []types.ContentBlock{types.NewTextBlock("done A")}},
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()
	if err := sm.Extract(ctx1, msgs, 5000); err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	spy.mu.Lock()
	firstModels := slices.Clone(spy.models)
	spy.mu.Unlock()
	if len(firstModels) == 0 || firstModels[0] != "glm-5" {
		t.Fatalf("first extraction models = %v, want [glm-5]", firstModels)
	}

	eng.SetModel("mimo-v2.5")

	spy.mu.Lock()
	spy.models = nil
	spy.mu.Unlock()

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if err := sm.Extract(ctx2, msgs, 10000); err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	spy.mu.Lock()
	secondModels := slices.Clone(spy.models)
	spy.mu.Unlock()
	if len(secondModels) == 0 {
		t.Fatal("second extraction did not call provider")
	}
	if secondModels[0] != "mimo-v2.5" {
		t.Errorf("second extraction used model %q, want mimo-v2.5", secondModels[0])
	}
	if slices.Contains(secondModels, "glm-5") {
		t.Errorf("second extraction used glm-5 — SM is using stale model: %v", secondModels)
	}
}
