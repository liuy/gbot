package config

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/liuy/gbot/pkg/llm"
)

// ProviderMap maps provider names to their llm.Provider instances.
type ProviderMap map[string]llm.Provider

// CreateAllProviders creates llm.Provider instances for all configured providers.
// Each provider must have at least one model defined.
func CreateAllProviders(cfg *Config) (ProviderMap, error) {
	m := make(ProviderMap)

	// Fetch free models for providers with a fetch ledger (non-empty
	// FreeFetched).
	// Done once at startup; failures are logged and skipped (user can
	// still use other providers).
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		// Non-empty FreeFetched = this provider mirrors the OpenRouter free
		// top-10 (the fetch button manages it). Absent = hand-managed.
		if len(p.FreeFetched) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		models, err := FetchFreeModels(ctx, p.URL)
		cancel()
		if err != nil {
			slog.Warn("free models fetch failed", "provider", p.Name, "error", err)
			continue
		}
		if len(models) == 0 {
			slog.Warn("free models fetch returned 0 models", "provider", p.Name)
			continue
		}
		// Replace semantics: the fetch result OWNS every id it reported last
		// time (FreeFetched) — ids that dropped out of the current top list
		// are removed. Hand-configured models (never in FreeFetched) survive.
		newIDs := make([]string, 0, len(models))
		for _, mm := range models {
			newIDs = append(newIDs, mm.ID)
		}
		for _, old := range p.FreeFetched {
			if !slices.Contains(newIDs, old) {
				p.Models.Delete(old)
			}
		}
		for _, mm := range models {
			// Hand-configured entries (present but never fetched) take
			// precedence; auto-fetched ones refresh in place.
			if p.Models.Has(mm.ID) && !slices.Contains(p.FreeFetched, mm.ID) {
				continue
			}
			p.Models.Set(mm.ID, ModelConfig{
				Context: IntOrHuman(mm.ContextLength),
			})
		}
		p.FreeFetched = newIDs
		slog.Info("free models loaded", "provider", p.Name, "count", len(models))
	}

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
		if p.Models.Len() == 0 {
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
		case ProviderTypeResponses:
			m[p.Name] = llm.NewResponsesProvider(&llm.ResponsesConfig{
				Name:        p.Name,
				APIKey:      apiKey,
				BaseURL:     p.URL,
				Model:       model,
				ExtraParams: p.ExtraParams,
			})
		case ProviderTypeOpenAI:
			m[p.Name] = llm.NewOpenAIProvider(&llm.OpenAIConfig{
				Name:        p.Name,
				APIKey:      apiKey,
				BaseURL:     p.URL,
				Model:       model,
				ExtraParams: p.ExtraParams,
			})
		default: // anthropic
			url := p.URL
			if url == "" {
				url = "https://api.anthropic.com"
			}
			m[p.Name] = llm.NewAnthropicProvider(&llm.AnthropicConfig{
				Name:    p.Name,
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
