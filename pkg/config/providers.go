package config

import (
	"log/slog"

	"github.com/liuy/gbot/pkg/llm"
)

// ProviderMap maps provider names to their llm.Provider instances.
type ProviderMap map[string]llm.Provider

// CreateAllProviders creates llm.Provider instances for all configured providers.
// Providers without a TierPro model are skipped with a warning.
func CreateAllProviders(cfg *Config) (ProviderMap, error) {
	m := make(ProviderMap)
	_, tier, err := cfg.ParseModel()
	if err != nil {
		return nil, err
	}
	for _, p := range cfg.Providers {
		apiKey := p.ResolveKey()
		if apiKey == "" {
			continue
		}
		if p.Models[TierPro] == "" {
			slog.Warn("provider has no pro model defined, skipping", "provider", p.Name)
			continue
		}
		model := p.Models[tier]
		if model == "" {
			model = p.Models[TierPro]
		}
		switch p.ProviderType() {
		case ProviderTypeOpenAI:
			m[p.Name] = llm.NewOpenAIProvider(&llm.OpenAIConfig{
				APIKey:  apiKey,
				BaseURL: p.URL,
				Model:   model,
			})
		default: // anthropic
			url := p.URL
			if url == "" {
				url = "https://api.anthropic.com"
			}
			m[p.Name] = llm.NewAnthropicProvider(&llm.AnthropicConfig{
				APIKey:  apiKey,
				BaseURL: url,
				Model:   model,
			})
		}
	}
	return m, nil
}
