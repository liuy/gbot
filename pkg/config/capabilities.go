package config

import "strings"

// DefaultCapabilities returns fallback context_window and max_tokens for known models.
// Provider config values take precedence; this is the fallback when config fields are 0.
// Source reference: TS utils/context.ts getContextWindowForModel +
// utils/model/modelCapabilities.ts getMaxOutputTokensForModel.
func DefaultCapabilities(model string) (contextWindow, maxTokens int) {
	switch {
	case strings.HasPrefix(model, "glm-5.1"):
		return 128000, 4096
	case strings.HasPrefix(model, "glm-5-turbo"):
		return 128000, 4096
	case strings.HasPrefix(model, "glm-5"):
		return 128000, 4096
	case strings.HasPrefix(model, "glm-4"):
		return 128000, 4096
	case strings.HasPrefix(model, "minimax-2"):
		return 128000, 4096
	default:
		return 200000, 16000
	}
}

// ResolveCapabilities returns the effective context_window and max_tokens,
// using config values if set, otherwise falling back to DefaultCapabilities.
func (p *Provider) ResolveCapabilities(model string) (contextWindow, maxTokens int) {
	contextWindow, maxTokens = DefaultCapabilities(model)
	if p.ContextWindow > 0 {
		contextWindow = p.ContextWindow
	}
	if p.MaxTokens > 0 {
		maxTokens = p.MaxTokens
	}
	return
}
