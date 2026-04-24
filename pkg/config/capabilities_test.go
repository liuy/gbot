package config

import (
	"testing"
)

func TestDefaultCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model                 string
		wantContext, wantMax  int
	}{
		{"glm-5", 128000, 4096},
		{"glm-5.1", 128000, 4096},
		{"glm-5-turbo", 128000, 4096},
		{"glm-4.5", 128000, 4096},
		{"glm-4.6", 128000, 4096},
		{"minimax-2.7", 128000, 4096},
		{"unknown-model", 200000, 16000},
		{"claude-sonnet-4-6", 200000, 16000},
	}

	for _, tc := range tests {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			cw, mt := DefaultCapabilities(tc.model)
			if cw != tc.wantContext {
				t.Errorf("contextWindow = %d, want %d", cw, tc.wantContext)
			}
			if mt != tc.wantMax {
				t.Errorf("maxTokens = %d, want %d", mt, tc.wantMax)
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

	p := &Provider{} // no config values
	cw, mt := p.ResolveCapabilities("glm-5")
	if cw != 128000 {
		t.Errorf("fallback contextWindow = %d, want 128000", cw)
	}
	if mt != 4096 {
		t.Errorf("fallback maxTokens = %d, want 4096", mt)
	}
}

func TestResolveCapabilities_PartialOverride(t *testing.T) {
	t.Parallel()

	p := &Provider{ContextWindow: 64000} // only context set
	cw, mt := p.ResolveCapabilities("glm-5")
	if cw != 64000 {
		t.Errorf("partial override contextWindow = %d, want 64000", cw)
	}
	if mt != 4096 {
		t.Errorf("fallback maxTokens = %d, want 4096", mt)
	}
}
