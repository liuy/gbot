package tui

import (
	"fmt"
	"sort"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"
)

// ModelItem represents a single provider/model entry in the picker.
type ModelItem struct {
	Provider string
	Model    string
	Current  bool
	Quota    string // non-empty = quota display for this provider (shared across models)
}

// Label returns a display line for the model item.
func (m *ModelItem) Label() string {
	current := ""
	if m.Current {
		current = " *"
	}
	label := fmt.Sprintf("%s / %s%s", m.Provider, m.Model, current)
	if m.Quota != "" {
		label += "  " + m.Quota
	}
	return label
}

// buildModelItems constructs an ordered list of model items from provider configs.
//
// Regular providers appear first (alphabetical). Providers marked `free: true`
// (OpenRouter free models) appear last so they don't drown out the main
// configured providers in the picker.
func buildModelItems(providers map[string]llm.Provider, providerConfigs map[string]*config.Provider, currentProvider string, currentModel string) []ModelItem {
	// Split names into regular and free groups so free providers sort last.
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
		if _, ok := providers[name]; !ok {
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
		if _, ok := providers[name]; !ok {
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

// findCurrentIndex returns the index of the current item, or -1 if none found.
func findCurrentIndex(items []ModelItem) int {
	for i, item := range items {
		if item.Current {
			return i
		}
	}
	return -1
}
