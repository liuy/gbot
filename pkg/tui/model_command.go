package tui

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/quota"
)

// handleModel implements the /model command.
//
//	/model              → show model picker
//	/model provider/model → switch to specific provider and model
//	/model provider     → switch provider, keep current model
//	/model model        → switch model on current provider (fuzzy match)
func (a *App) handleModel(args string, commitCmd tea.Cmd) tea.Cmd {
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot switch model while streaming")
	}
	if len(a.providers) == 0 {
		return a.showInfo("No providers configured")
	}

	if args == "" {
		return a.openModelPicker(commitCmd)
	}

	if before, after, ok := strings.Cut(args, "/"); ok {
		return a.switchProviderModel(before, after, commitCmd)
	}

	// Try as provider name first
	if _, ok := a.providers[args]; ok {
		return a.switchProvider(args, commitCmd)
	}

	// Otherwise treat as model name (fuzzy match within current provider)
	return a.switchModel(args, commitCmd)
}

// openModelPicker opens the interactive model picker.
func (a *App) openModelPicker(commitCmd tea.Cmd) tea.Cmd {
	if a.activeDialog != nil {
		return a.showInfo("A picker is already open")
	}

	modelItems := buildModelItems(a.providers, a.providerConfigs, a.CurrentProvider(), a.CurrentModel())
	a.modelPickerItems = modelItems

	items := make([]PickerItem, len(modelItems))
	for i := range modelItems {
		items[i] = &modelItems[i]
	}
	currentIdx := findCurrentIndex(modelItems)
	a.activeDialog = NewDialog("Select model", pickerItemsToOptions(items))
	applyDialogOption(a.activeDialog, WithInitialCursor(currentIdx))
	a.activeDialog.width = a.width

	captured := modelItems
	a.onDialogDone = func(d *Dialog) (tea.Model, tea.Cmd) {
		return a.handleModelPickerDone(d, captured)
	}

	// Spawn a quota fetch for each provider that supports it.
	// Results come back as modelQuotaFetchedMsg and rebuild the dialog.
	fetchCmds := a.spawnModelQuotaFetches()
	return tea.Batch(append([]tea.Cmd{commitCmd}, fetchCmds...)...)
}

// modelQuotaFetchedMsg carries the result of a single provider's quota fetch
// for the model picker.
type modelQuotaFetchedMsg struct {
	provider string
	info     quota.Info
	err      error
}

// spawnModelQuotaFetches launches one async quota fetch per provider that has
// a quota endpoint. Each fetch returns a modelQuotaFetchedMsg.
func (a *App) spawnModelQuotaFetches() []tea.Cmd {
	// Track which providers already had their fetch spawned (deduplicate).
	seen := map[string]bool{}
	var cmds []tea.Cmd

	for _, item := range a.modelPickerItems {
		if seen[item.Provider] {
			continue
		}
		p, ok := a.providerConfigs[item.Provider]
		if !ok {
			seen[item.Provider] = true
			continue
		}
		f := quota.Detect(p)
		if f == nil {
			seen[item.Provider] = true
			continue
		}

		provider := item.Provider
		fetcher := f
		seen[provider] = true

		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			info, err := fetcher.Fetch(ctx)
			if err != nil {
				slog.Error("quota: model picker fetch failed", "provider", provider, "error", err)
			}
			return modelQuotaFetchedMsg{provider: provider, info: info, err: err}
		})
	}
	return cmds
}

// handleModelPickerDone processes the model picker selection or cancellation.
func (a *App) handleModelPickerDone(d *Dialog, items []ModelItem) (tea.Model, tea.Cmd) {
	if d.Aborted() {
		return a, nil
	}

	idx := d.SelectedIndex()
	if idx < 0 || idx >= len(items) {
		return a, nil
	}

	selected := items[idx]
	provider, ok := a.providers[selected.Provider]
	if !ok {
		return a, a.showInfo(fmt.Sprintf("unknown provider: %s", selected.Provider))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(selected.Model)
	a.updateEngineCapabilities(selected.Provider, selected.Model)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()
	a.refreshQuotaFromProvider()

	slog.Info("model: switched", "provider", selected.Provider, "model", selected.Model)
	return a, a.showInfo(fmt.Sprintf("Switched to %s/%s", selected.Provider, selected.Model))
}

// refreshQuotaFromProvider rebuilds the fetcher from the new provider and
// updates the status bar immediately (clearing the old value if the new
// provider has no quota endpoint).
func (a *App) refreshQuotaFromProvider() {
	if p, ok := a.providerConfigs[a.CurrentProvider()]; ok {
		a.quotaFetcher = quota.Detect(p)
	} else {
		a.quotaFetcher = nil
	}
	a.quotaFetchSeq++
	a.status.SetQuota(nil)
}

// switchProviderModel switches both provider and model.
func (a *App) switchProviderModel(providerName, modelName string, commitCmd tea.Cmd) tea.Cmd {
	provider, ok := a.providers[providerName]
	if !ok {
		return a.showInfo(fmt.Sprintf("unknown provider: %s, available: %s",
			providerName, strings.Join(slices.Collect(maps.Keys(a.providers)), ", ")))
	}
	cfgProvider := a.providerConfigs[providerName]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", providerName))
	}

	// Fuzzy match model name within provider
	matched := config.FindClosestMatch(modelName, cfgProvider.ModelNames())
	if matched == "" {
		return a.showInfo(fmt.Sprintf("model %q not found in provider %s", modelName, providerName))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(matched)
	a.updateEngineCapabilities(providerName, matched)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()
	a.refreshQuotaFromProvider()

	slog.Info("model: switched", "provider", providerName, "model", matched)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", providerName, matched)))
}

// switchModel switches model on current provider using fuzzy match.
// If not found in current provider, searches all providers and switches
// to the globally closest match (lowest Levenshtein distance).
func (a *App) switchModel(modelName string, commitCmd tea.Cmd) tea.Cmd {
	currentProvider := a.CurrentProvider()
	cfgProvider := a.providerConfigs[currentProvider]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", currentProvider))
	}

	if matched := config.FindClosestMatch(modelName, cfgProvider.ModelNames()); matched != "" {
		// SetProvider is required: the engine's provider may have drifted
		// from what the user expects (e.g. restored from meta.json with a
		// different provider). Sync it to the current provider so the
		// request goes to the right endpoint.
		a.engine.SetProvider(a.providers[currentProvider])
		a.engine.SetModel(matched)
		a.updateEngineCapabilities(currentProvider, matched)
		a.status.SetModel(a.engine.Model())
		a.persistModelSelection()
		a.refreshQuotaFromProvider()

		slog.Info("model: switched model", "provider", currentProvider, "model", matched)
		return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", currentProvider, matched)))
	}

	// Cross-provider fallback: pick the globally best (lowest distance) match.
	bestProvider := ""
	bestModel := ""
	bestDistance := -1
	for providerName := range a.providers {
		if providerName == currentProvider {
			continue
		}
		cfg := a.providerConfigs[providerName]
		if cfg == nil {
			continue
		}
		matched, distance := config.FindClosestMatchRank(modelName, cfg.ModelNames())
		if distance < 0 {
			continue
		}
		if bestDistance < 0 || distance < bestDistance {
			bestProvider = providerName
			bestModel = matched
			bestDistance = distance
		}
	}

	if bestModel == "" {
		return a.showInfo(fmt.Sprintf("model %q not found in any provider", modelName))
	}

	a.engine.SetProvider(a.providers[bestProvider])
	a.engine.SetModel(bestModel)
	a.updateEngineCapabilities(bestProvider, bestModel)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()
	a.refreshQuotaFromProvider()

	slog.Info("model: switched", "provider", bestProvider, "model", bestModel, "distance", bestDistance)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", bestProvider, bestModel)))
}

// switchProvider switches provider, using first model of new provider.
func (a *App) switchProvider(providerName string, commitCmd tea.Cmd) tea.Cmd {
	provider, ok := a.providers[providerName]
	if !ok {
		return a.showInfo(fmt.Sprintf("unknown provider: %s, available: %s",
			providerName, strings.Join(slices.Collect(maps.Keys(a.providers)), ", ")))
	}
	cfgProvider := a.providerConfigs[providerName]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", providerName))
	}

	model := cfgProvider.FirstModelName()
	if model == "" {
		return a.showInfo(fmt.Sprintf("provider %s has no models", providerName))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(model)
	a.updateEngineCapabilities(providerName, model)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()
	a.refreshQuotaFromProvider()

	slog.Info("model: switched provider", "provider", providerName, "model", model)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", providerName, model)))
}

// updateEngineCapabilities updates the engine's context window and max tokens.
func (a *App) updateEngineCapabilities(providerName, model string) {
	cfgProvider := a.providerConfigs[providerName]
	if cfgProvider == nil {
		return
	}
	cw := cfgProvider.ResolveContext(model)
	mt := cfgProvider.ResolveMaxTokens(model)
	a.engine.SetMaxTokens(mt)
	a.engine.UpdateAutoCompactConfig(engine.AutoCompactConfig{
		ContextWindow:          cw,
		MaxConsecutiveFailures: 3,
	})
	// Only update the context window (denominator). The used value
	// (numerator) comes exclusively from API responses via usageMsg —
	// estimating it here produces wrong values that exceed the window.
	a.status.SetContextWindow(cw)
	slog.Info("ui:setContext", "used", a.status.contextUsed, "window", cw, "source", "engineCapabilities",
		"provider", providerName, "model", model)
}
