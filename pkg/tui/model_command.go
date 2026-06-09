package tui

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
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

	modelItems := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentModel)
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
	return commitCmd
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
	a.currentProvider = selected.Provider
	a.currentModel = selected.Model
	a.updateEngineCapabilities(selected.Provider, selected.Model)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()

	slog.Info("model: switched", "provider", selected.Provider, "model", selected.Model)
	return a, a.showInfo(fmt.Sprintf("Switched to %s/%s", selected.Provider, selected.Model))
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
	matched := config.FindModelByLongestPrefix(modelName, cfgProvider.ModelNames())
	if matched == "" {
		return a.showInfo(fmt.Sprintf("model %q not found in provider %s", modelName, providerName))
	}

	a.engine.SetProvider(provider)
	a.engine.SetModel(matched)
	a.currentProvider = providerName
	a.currentModel = matched
	a.updateEngineCapabilities(providerName, matched)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()

	slog.Info("model: switched", "provider", providerName, "model", matched)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", providerName, matched)))
}

// switchModel switches model on current provider using fuzzy match.
func (a *App) switchModel(modelName string, commitCmd tea.Cmd) tea.Cmd {
	cfgProvider := a.providerConfigs[a.currentProvider]
	if cfgProvider == nil {
		return a.showInfo(fmt.Sprintf("no config for provider %s", a.currentProvider))
	}

	matched := config.FindModelByLongestPrefix(modelName, cfgProvider.ModelNames())
	if matched == "" {
		return a.showInfo(fmt.Sprintf("model %q not found in provider %s", modelName, a.currentProvider))
	}

	a.engine.SetModel(matched)
	a.currentModel = matched
	a.updateEngineCapabilities(a.currentProvider, matched)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()

	slog.Info("model: switched model", "provider", a.currentProvider, "model", matched)
	return tea.Batch(commitCmd, a.showInfo(fmt.Sprintf("Switched to %s/%s", a.currentProvider, matched)))
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
	a.currentProvider = providerName
	a.currentModel = model
	a.updateEngineCapabilities(providerName, model)
	a.status.SetModel(a.engine.Model())
	a.persistModelSelection()

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
	a.status.SetContext(0, cw)
}
