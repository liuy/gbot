package config

import "sort"

// ModelItem represents a single provider/model entry in a model picker.
// Shared by the TUI model picker and the webchat config message so both
// surfaces use the same ordering logic.
type ModelItem struct {
	Provider string
	Model    string
	Current  bool
}

// BuildModelItems constructs an ordered list of model items from provider
// configs, filtered to only providers whose llm.Provider instance is present.
//
// Regular providers appear first (alphabetical). Providers marked `free: true`
// appear last so they don't drown out the main configured providers.
// Within each provider, models are listed in config order (Models.Ordered()).
func BuildModelItems(providerConfigs map[string]*Provider, providerPresent func(string) bool, currentProvider, currentModel string) []ModelItem {
	var regular, free []string
	for n, cfg := range providerConfigs {
		if cfg.Free {
			free = append(free, n)
		} else {
			regular = append(regular, n)
		}
	}
	sort.Strings(regular)
	sort.Strings(free)

	var items []ModelItem
	for _, name := range regular {
		cfg := providerConfigs[name]
		if !providerPresent(name) {
			continue
		}
		for _, modelName := range cfg.Models.Ordered() {
			items = append(items, ModelItem{
				Provider: name,
				Model:    modelName,
				Current:  name == currentProvider && modelName == currentModel,
			})
		}
	}
	for _, name := range free {
		cfg := providerConfigs[name]
		if !providerPresent(name) {
			continue
		}
		for _, modelName := range cfg.Models.Ordered() {
			items = append(items, ModelItem{
				Provider: name,
				Model:    modelName,
				Current:  name == currentProvider && modelName == currentModel,
			})
		}
	}

	return items
}
