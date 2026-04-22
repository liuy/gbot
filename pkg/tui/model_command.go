package tui

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
)

// handleModel implements the /model command.
//
//	/model          → show model picker
//	/model provider/tier → switch to specific provider and tier
//	/model provider → switch provider, keep current tier
//	/model tier     → switch tier on current provider
func (a *App) handleModel(args string, commitCmd tea.Cmd) tea.Cmd {
	// Guard: no switching while streaming
	if a.repl.IsStreaming() {
		return a.showInfo("Cannot switch model while streaming")
	}

	// Guard: no providers
	if len(a.providers) == 0 {
		return a.showInfo("No providers configured")
	}

	if args == "" {
		return a.openModelPicker(commitCmd)
	}

	// "/" splits provider/tier
	if before, after, ok := strings.Cut(args, "/"); ok {
		return a.switchProviderTier(before, after, commitCmd)
	}

	// Check if arg is a valid tier
	if isValidTier(args) {
		return a.switchTier(args, commitCmd)
	}

	// Otherwise treat as provider name
	return a.switchProvider(args, commitCmd)
}

// openModelPicker opens the interactive model picker.
func (a *App) openModelPicker(commitCmd tea.Cmd) tea.Cmd {
	a.modelPicker = NewModelPicker(a.providers, a.providerConfigs, a.currentProvider, a.currentTier)
	a.pickerMode = pickerModel
	return commitCmd
}

// handleModelPickerResult processes the model picker selection or cancellation.
func (a *App) handleModelPickerResult() (tea.Model, tea.Cmd) {
	a.pickerMode = pickerNone

	if a.modelPicker.aborted {
		a.modelPicker = nil
		return a, nil
	}

	selected := a.modelPicker.selected
	a.modelPicker = nil

	if selected == nil {
		return a, nil
	}

	provider, ok := a.providers[selected.Provider]
	if !ok {
		return a, a.showInfo(fmt.Sprintf("unknown provider: %s", selected.Provider))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(selected.Model)
	a.currentProvider = selected.Provider
	a.currentTier = selected.Tier

	slog.Info("model: switched", "provider", selected.Provider, "tier", selected.Tier, "model", selected.Model)

	return a, a.showInfo(fmt.Sprintf("Switched to %s/%s (%s)", selected.Provider, selected.Tier, selected.Model))
}

// switchProviderTier switches both provider and tier.
func (a *App) switchProviderTier(providerName, tierName string, commitCmd tea.Cmd) tea.Cmd {
	provider, ok := a.providers[providerName]
	if !ok {
		return a.showInfo(fmt.Sprintf("unknown provider: %s, available: %s",
			providerName, strings.Join(slices.Collect(maps.Keys(a.providers)), ", ")))
	}
	tier := config.Tier(tierName)
	cfgProvider := a.providerConfigs[providerName]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", providerName))
	}
	model := cfgProvider.Models[tier]
	if model == "" {
		return a.showInfo(fmt.Sprintf("provider %s has no model for tier %s", providerName, tier))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(model)
	a.currentProvider = providerName
	a.currentTier = tier

	slog.Info("model: switched", "provider", providerName, "tier", tier, "model", model)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s (%s)", providerName, tier, model)))
}

// switchTier switches tier on current provider.
func (a *App) switchTier(tierName string, commitCmd tea.Cmd) tea.Cmd {
	tier := config.Tier(tierName)
	cfgProvider := a.providerConfigs[a.currentProvider]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", a.currentProvider))
	}
	model := cfgProvider.Models[tier]
	if model == "" {
		return a.showInfo(fmt.Sprintf("provider %s has no model for tier %s", a.currentProvider, tier))
	}

	a.engine.SetModel(model)
	a.currentTier = tier

	slog.Info("model: switched tier", "provider", a.currentProvider, "tier", tier, "model", model)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s (%s)", a.currentProvider, tier, model)))
}

// switchProvider switches provider, keeping current tier.
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
	model := cfgProvider.Models[a.currentTier]
	if model == "" {
		return a.showInfo(fmt.Sprintf("provider %s has no model for tier %s", providerName, a.currentTier))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(model)
	a.currentProvider = providerName

	slog.Info("model: switched provider", "provider", providerName, "tier", a.currentTier, "model", model)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s (%s)", providerName, a.currentTier, model)))
}

var validTiers = map[string]config.Tier{
	string(config.TierLite): config.TierLite,
	string(config.TierPro):  config.TierPro,
	string(config.TierMax):  config.TierMax,
}

func isValidTier(s string) bool {
	_, ok := validTiers[s]
	return ok
}
