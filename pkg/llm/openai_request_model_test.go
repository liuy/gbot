package llm

import (
	"encoding/json"
	"testing"

	"github.com/liuy/gbot/pkg/types"
)

func TestTranslateRequest_ReqModelOverridesProviderDefault(t *testing.T) {
	t.Parallel()
	p := newTestProvider() // p.model is a fixed default (gpt-4)
	req := &Request{
		Model:     "glm-5.3-flash",
		MaxTokens: 64,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		},
	}
	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Model != "glm-5.3-flash" {
		t.Errorf("wire model = %q, want %q (req.Model must override the provider default)", got.Model, "glm-5.3-flash")
	}
}

func TestTranslateRequest_EmptyReqModelFallsBack(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	req := &Request{
		MaxTokens: 64,
		Messages: []types.Message{
			{Role: types.RoleUser, Content: []types.ContentBlock{types.NewTextBlock("hi")}},
		},
	}
	body, err := p.translateRequest(req, false)
	if err != nil {
		t.Fatalf("translateRequest() error: %v", err)
	}
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Model != "gpt-4" {
		t.Errorf("wire model = %q, want %q (provider default fallback)", got.Model, "gpt-4")
	}
}
