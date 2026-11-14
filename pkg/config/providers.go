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
	refreshFreeModels(cfg)

	provider, modelName, err := cfg.ResolveModel()
	if err != nil {
		return nil, err
	}

	m := buildProviderMap(cfg, provider.Name, modelName)
	if len(m) == 0 {
		return nil, fmt.Errorf("no providers could be created (check API keys and models)")
	}
	return m, nil
}

// refreshFreeModels fetches free models for providers with a fetch ledger
// (non-empty FreeFetched). Done once at startup; failures are logged and
// skipped (user can still use other providers).
func refreshFreeModels(cfg *Config) {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		// Audio providers are STT-only; the free-model mirror is an LLM
		// concern and audio providers never carry FreeFetched.
		if p.ProviderType() == ProviderTypeAudio {
			continue
		}
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
}

// buildProviderMap constructs llm.Provider instances for every provider that
// has a resolvable key and at least one model. The provider named by
// overrideProvider embeds overrideModel as its default (preserving the
// resolved-model fidelity the original CreateAllProviders loop had); every
// other provider embeds its first declared model. Audio providers are
// STT-only: they have no llm.Provider representation and would fall through
// the switch into the anthropic default below.
func buildProviderMap(cfg *Config, overrideProvider, overrideModel string) ProviderMap {
	m := make(ProviderMap)
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.ProviderType() == ProviderTypeAudio {
			continue
		}
		apiKey := p.ResolveKey()
		if apiKey == "" {
			continue
		}
		if p.Models.Len() == 0 {
			slog.Warn("provider has no models defined, skipping", "provider", p.Name)
			continue
		}

		model := p.FirstModelName()
		if p.Name == overrideProvider && overrideModel != "" {
			model = overrideModel
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
				Name:            p.Name,
				APIKey:          apiKey,
				BaseURL:         p.URL,
				Model:           model,
				ExtraParams:     p.ExtraParams,
				InputModalities: p.InputModalities(),
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
			})
		}
	}
	return m
}

// ResolvePrimary resolves the configured model into a live provider, its
// model name, and the provider's config. Moved from pkg/app so the reload
// orchestrator shares one implementation.
func (c *Config) ResolvePrimary(pm ProviderMap) (llm.Provider, string, *Provider, error) {
	p, modelName, err := c.ResolveModel()
	if err != nil {
		return nil, "", nil, err
	}
	if p == nil {
		return nil, "", nil, fmt.Errorf("no providers configured")
	}

	prov, ok := pm[p.Name]
	if !ok {
		return nil, "", nil, fmt.Errorf("provider %q has no API key configured", p.Name)
	}
	return prov, modelName, p, nil
}
