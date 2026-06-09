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

func TestResolveContext_ModelConfigOverride(t *testing.T) {
	t.Parallel()

	p := &Provider{
		Models: map[string]ModelConfig{
			"glm-5": {Context: IntOrHuman(256000)},
		},
	}
	cw := p.ResolveContext("glm-5")
	if cw != 256000 {
		t.Errorf("ResolveContext = %d, want 256000", cw)
	}
}

func TestResolveContext_FallbackToDefault(t *testing.T) {
	t.Parallel()

	p := &Provider{Models: map[string]ModelConfig{}}
	cw := p.ResolveContext("glm-5")
	if cw != 200*1024 {
		t.Errorf("fallback ResolveContext = %d, want %d", cw, 200*1024)
	}
}

func TestResolveMaxTokens_ModelConfigOverride(t *testing.T) {
	t.Parallel()

	p := &Provider{
		Models: map[string]ModelConfig{
			"glm-5": {MaxTokens: IntOrHuman(8192)},
		},
	}
	mt := p.ResolveMaxTokens("glm-5")
	if mt != 8192 {
		t.Errorf("ResolveMaxTokens = %d, want 8192", mt)
	}
}

func TestResolveMaxTokens_FallbackToDefault(t *testing.T) {
	t.Parallel()

	p := &Provider{Models: map[string]ModelConfig{}}
	mt := p.ResolveMaxTokens("glm-5")
	if mt != 32*1024 {
		t.Errorf("fallback ResolveMaxTokens = %d, want %d", mt, 32*1024)
	}
}
