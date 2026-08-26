package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/types"
)

// leakCapProvider records the raw serialized request body the way the
// Anthropic client sends it (json.Marshal of the whole Request struct).
type leakCapProvider struct {
	body string
}

func (p *leakCapProvider) Stream(context.Context, *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, nil
}
func (p *leakCapProvider) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	b, _ := json.Marshal(req)
	p.body = string(b)
	return &llm.Response{
		StopReason: "end_turn",
		Content:    []types.ContentBlock{types.NewTextBlock("summary text")},
	}, nil
}
func (p *leakCapProvider) Name() string { return "capture" }

// TestAutoCompactor_Summarize_NoToolResultDataLeak pins the leak regression: the
// summarize request marshals the whole Request (anthropic.go does
// json.Marshal(req)); a restored Message carrying ToolResultData must never
// serialize its rich payloads into the provider body. Guarded structurally
// by Message's json:"-" tag on the field.
func TestAutoCompactor_Summarize_NoToolResultDataLeak(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	db, err := short.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer db.Close()

	p := &leakCapProvider{}
	sc := NewAutoCompactor(db, &testEngineMeta{model: "model", sessionID: "sess", contextWindow: 1000, provider: p}) // REAL-TIME: fixed meta, no timing dependency

	// A head message whose engine form carries rich data (as restored from
	// the metadata column by StoreMessageToEngine) plus a text block so the
	// summarizer finds extractable text.
	rich := types.Message{
		Role:    types.RoleUser,
		Content: []types.ContentBlock{types.NewTextBlock("did some edits")},
		ToolResultData: map[string]any{
			"call_1": map[string]any{
				"filePath":  "secret.go",
				"oldString": "OLD_SECRET_CONTENT",
				"newString": "NEW_SECRET_CONTENT",
			},
		},
	}
	meta := rich.MetadataToJSON()
	msgs := []*short.TranscriptMessage{
		{Type: "user", Content: `[{"type":"text","text":"did some edits"}]`, Metadata: meta},
	}
	if _, err := sc.summarizeMessages(context.Background(), msgs, ""); err != nil {
		t.Fatalf("summarizeMessages: %v", err)
	}
	if p.body == "" {
		t.Fatal("provider never received a request")
	}
	for _, leak := range []string{"toolUseResult", "OLD_SECRET_CONTENT", "secret.go"} {
		if strings.Contains(p.body, leak) {
			t.Errorf("summarize request body leaks %q (rich slot serialized to provider)", leak)
		}
	}
}
