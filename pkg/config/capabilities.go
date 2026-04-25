package config

import "strings"

// DefaultCapabilities returns fallback context_window and max_tokens for known models.
// Provider config values take precedence; this is the fallback when config fields are 0.
// Source reference: TS utils/context.ts getContextWindowForModel +
// utils/model/modelCapabilities.ts getMaxOutputTokensForModel.
func DefaultCapabilities(model string) (contextWindow, maxTokens int) {
	switch {
	case strings.HasPrefix(model, "glm-5.1"):
		return 200 * 1024, 128 * 1024
	case strings.HasPrefix(model, "glm-5-turbo"):
		return 200 * 1024, 128 * 1024
	case strings.HasPrefix(model, "glm-5"):
		return 200 * 1024, 128 * 1024
	case strings.HasPrefix(model, "glm-4"):
		return 200 * 1024, 128 * 1024
	case strings.HasPrefix(model, "minimax-2"):
		return 200 * 1024, 128 * 1024
	default:
		return 200 * 1024, 16 * 1024
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
