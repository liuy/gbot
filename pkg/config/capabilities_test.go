package config

import (
	"testing"
)

func TestDefaultCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model string
	}{
		{"glm-5"},
		{"glm-5.1"},
		{"claude-sonnet-4-6"},
		{"unknown-model"},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			cw, mt := DefaultCapabilities(tc.model)
			if cw != 200*1024 {
				t.Errorf("contextWindow = %d, want %d", cw, 200*1024)
			}
			if mt != 32*1024 {
				t.Errorf("maxTokens = %d, want %d", mt, 32*1024)
			}
		})
	}
}

func TestResolveCapabilities_ConfigOverrides(t *testing.T) {
	t.Parallel()

	p := &Provider{
		ContextWindow: 256000,
		MaxTokens:     8192,
	}
	cw, mt := p.ResolveCapabilities("glm-5")
	if cw != 256000 {
		t.Errorf("config override contextWindow = %d, want 256000", cw)
	}
	if mt != 8192 {
		t.Errorf("config override maxTokens = %d, want 8192", mt)
	}
}

func TestResolveCapabilities_FallbackToDefault(t *testing.T) {
	t.Parallel()

	p := &Provider{}
	cw, mt := p.ResolveCapabilities("glm-5")
	if cw != 200*1024 {
		t.Errorf("fallback contextWindow = %d, want %d", cw, 200*1024)
	}
	if mt != 32*1024 {
		t.Errorf("fallback maxTokens = %d, want %d", mt, 32*1024)
	}
}

func TestResolveCapabilities_PartialOverride(t *testing.T) {
	t.Parallel()

	p := &Provider{ContextWindow: 64000}
	cw, mt := p.ResolveCapabilities("glm-5")
	if cw != 64000 {
		t.Errorf("partial override contextWindow = %d, want 64000", cw)
	}
	if mt != 32*1024 {
		t.Errorf("fallback maxTokens = %d, want %d", mt, 32*1024)
	}
}
