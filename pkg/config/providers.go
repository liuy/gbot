package config

import (
	"fmt"
	"log/slog"

	"github.com/liuy/gbot/pkg/llm"
)

// ProviderMap maps provider names to their llm.Provider instances.
type ProviderMap map[string]llm.Provider

// CreateAllProviders creates llm.Provider instances for all configured providers.
// Each provider must have at least one model defined.
func CreateAllProviders(cfg *Config) (ProviderMap, error) {
	m := make(ProviderMap)

	provider, modelName, err := cfg.ResolveModel()
	if err != nil {
		return nil, err
	}

	// The resolved model determines which model each provider uses.
	// For the resolved provider, use the matched model name.
	// For other providers, use their first model.
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		apiKey := p.ResolveKey()
		if apiKey == "" {
			continue
		}
		if len(p.Models) == 0 {
			slog.Warn("provider has no models defined, skipping", "provider", p.Name)
			continue
		}

		// Pick model: use resolved model if this is the resolved provider,
		// otherwise use first model.
		model := p.FirstModelName()
		if p == provider && modelName != "" {
			model = modelName
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

	if len(m) == 0 {
		return nil, fmt.Errorf("no providers could be created (check API keys and models)")
	}
	return m, nil
}
