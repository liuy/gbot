package app

import (
	"github.com/liuy/gbot/pkg/config"
)

// loadConfigForgiving is the boot-time settings loader: a broken file is
// skipped and reported, never fatal (invariant: boot must not die on config).
func loadConfigForgiving() (*config.Config, *config.Degraded) {
	return config.LoadForgiving()
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
