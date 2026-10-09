package app

import (
	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"
)

func loadConfig() (*config.Config, error) {
	return config.Load()
}

// buildProviderConfigMap converts cfg.Providers ([]Provider) into the
// map[string]*Provider shape used by TUI and wui for model listing +
// capability resolution. Mirrors tui.App.SetProviders.
func buildProviderConfigMap(cfg *config.Config) map[string]*config.Provider {
	m := make(map[string]*config.Provider, len(cfg.Providers))
	for i := range cfg.Providers {
		m[cfg.Providers[i].Name] = &cfg.Providers[i]
	}
	return m
}

// resolvePrimaryProvider resolves Config.Model into a concrete provider, model name,
// and Provider config. Delegates to config.Config.ResolvePrimary (moved there so
// the reload orchestrator shares one implementation).
func resolvePrimaryProvider(cfg *config.Config, providerMap config.ProviderMap) (llm.Provider, string, *config.Provider, error) {
	return cfg.ResolvePrimary(providerMap)
}
