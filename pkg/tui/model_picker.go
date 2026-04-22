package tui

import (
	"fmt"
	"sort"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"
)

// ModelItem represents a single provider/tier/model entry in the picker.
type ModelItem struct {
	Provider string
	Tier     config.Tier
	Model    string
	Current  bool
}

// Label returns a display line for the model item.
func (m *ModelItem) Label() string {
	current := ""
	if m.Current {
		current = " *"
	}
	return fmt.Sprintf("%s / %-4s %s%s", m.Provider, m.Tier, m.Model, current)
}

// buildModelItems constructs an ordered list of model items from provider configs.
// Providers without an implementation in the providers map are skipped.
func buildModelItems(providers map[string]llm.Provider, providerConfigs map[string]*config.Provider, currentProvider string, currentTier config.Tier) []ModelItem {
	var items []ModelItem

	names := make([]string, 0, len(providerConfigs))
	for n := range providerConfigs {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := providerConfigs[name]
		if _, ok := providers[name]; !ok {
			continue
		}
		for _, tier := range []config.Tier{config.TierLite, config.TierPro, config.TierMax} {
			if model := cfg.Models[tier]; model != "" {
				items = append(items, ModelItem{
					Provider: name,
					Tier:     tier,
					Model:    model,
					Current:  name == currentProvider && tier == currentTier,
				})
			}
		}
	}

	return items
}

// findCurrentIndex returns the index of the current item, or -1 if none found.
func findCurrentIndex(items []ModelItem) int {
	for i, item := range items {
		if item.Current {
			return i
		}
	}
	return -1
}
