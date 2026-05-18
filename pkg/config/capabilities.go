package config

// DefaultCapabilities returns fallback context_window and max_tokens for known models.
// Provider config values take precedence; this is the fallback when config fields are 0.
// 32k maxTokens is a universal default for modern LLMs (100k-200k context windows).
// Users can override via provider config if their model needs a different limit.
func DefaultCapabilities(model string) (contextWindow, maxTokens int) {
	return 200 * 1024, 32 * 1024
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
