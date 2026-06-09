package config

// DefaultCapabilities returns fallback context_window and max_tokens for models
// that don't specify these values in their ModelConfig.
func DefaultCapabilities(model string) (contextWindow, maxTokens int) {
	return 200 * 1024, 32 * 1024
}

// ResolveContext returns the effective context window for a model.
// Uses ModelConfig.Context if set, otherwise falls back to DefaultCapabilities.
func (p *Provider) ResolveContext(model string) int {
	if mc := p.GetModelConfig(model); mc != nil && mc.Context.IsSet() {
		return mc.Context.Int()
	}
	cw, _ := DefaultCapabilities(model)
	return cw
}

// ResolveMaxTokens returns the effective max output tokens for a model.
// Uses ModelConfig.MaxTokens if set, otherwise falls back to DefaultCapabilities.
func (p *Provider) ResolveMaxTokens(model string) int {
	if mc := p.GetModelConfig(model); mc != nil && mc.MaxTokens.IsSet() {
		return mc.MaxTokens.Int()
	}
	_, mt := DefaultCapabilities(model)
	return mt
}

// ResolveInput returns the input modalities for a model.
// Uses ModelConfig.Input if set, otherwise defaults to ["text"].
func (p *Provider) ResolveInput(model string) []string {
	if mc := p.GetModelConfig(model); mc != nil && len(mc.Input) > 0 {
		return mc.Input
	}
	return []string{"text"}
}
