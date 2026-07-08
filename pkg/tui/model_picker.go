package tui

import (
	"fmt"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"
)

// ModelItem embeds config.ModelItem (shared ordering) and adds the TUI-only
// Quota field (populated async by spawnModelQuotaFetches).
type ModelItem struct {
	config.ModelItem
	Quota string
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

// buildModelItems delegates the ordering logic to config.BuildModelItems and
// copies the results into the TUI's ModelItem type (preserving the Quota
// field for async population).
func buildModelItems(providers map[string]llm.Provider, providerConfigs map[string]*config.Provider, currentProvider string, currentModel string) []ModelItem {
	items := config.BuildModelItems(
		providerConfigs,
		func(name string) bool { _, ok := providers[name]; return ok },
		currentProvider,
		currentModel,
	)
	out := make([]ModelItem, len(items))
	for i, it := range items {
		out[i] = ModelItem{ModelItem: it}
	}
	return out
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
