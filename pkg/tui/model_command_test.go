package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
)

// mockLLMProvider is a minimal mock for testing model switching.
type mockLLMProvider struct{}

func (m *mockLLMProvider) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLLMProvider) Stream(_ context.Context, _ *llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, fmt.Errorf("not implemented")
}

// newTestAppWithProviders creates an App with providers configured for testing.
func newTestAppWithProviders() *App {
	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{},
		Model:    "glm-5",
		Logger:   slog.Default(),
	})

	a := &App{
		engine: eng,
		repl:   NewReplState(),
	}

	cfg := &config.Config{
		DefaultTier: config.TierPro,
		Providers: []config.Provider{
			{
				Name: "openai",
				URL:  "https://api.example.com",
				Keys: []string{"test-key"},
				Models: map[config.Tier]string{
					config.TierLite: "glm-lite",
					config.TierPro:  "glm-5",
					config.TierMax:  "glm-max",
				},
			},
			{
				Name: "anthropic",
				URL:  "https://api.anthropic.com",
				Keys: []string{"test-key-2"},
				Models: map[config.Tier]string{
					config.TierPro: "claude-sonnet",
				},
			},
		},
	}

	providers := map[string]llm.Provider{
		"openai":    &mockLLMProvider{},
		"anthropic": &mockLLMProvider{},
	}

	a.SetProviders(providers, cfg)
	return a
}

// helperSetupModelPicker creates a ListPicker for model items and sets up the onPickerDone closure.
// Returns the captured modelItems for assertion after picker interaction.
func helperSetupModelPicker(a *App) []ModelItem {
	modelItems := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentTier)
	items := make([]PickerItem, len(modelItems))
	for i := range modelItems {
		items[i] = &modelItems[i]
	}
	currentIdx := findCurrentIndex(modelItems)
	a.listPicker = NewListPicker("Select model", items, WithInitialCursor(currentIdx))

	captured := modelItems
	a.onPickerDone = func(p *ListPicker) (tea.Model, tea.Cmd) {
		return a.handleModelPickerDone(p, captured)
	}
	return captured
}

// ---------------------------------------------------------------------------
// handleModel — streaming guard
// ---------------------------------------------------------------------------

func TestHandleModel_StreamingGuard(t *testing.T) {
	a := newTestAppWithProviders()
	a.repl.streaming = true

	cmd := a.handleModel("openai/pro", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "Cannot switch model while streaming") {
		t.Errorf("expected streaming guard message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// handleModel — no providers
// ---------------------------------------------------------------------------

func TestHandleModel_NoProviders(t *testing.T) {
	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{},
		Model:    "test",
		Logger:   slog.Default(),
	})
	a := &App{
		engine:    eng,
		repl:      NewReplState(),
		providers: map[string]llm.Provider{},
	}

	cmd := a.handleModel("pro", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "No providers configured") {
		t.Errorf("expected no providers message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// handleModel — open picker
// ---------------------------------------------------------------------------

func TestHandleModel_OpenPicker(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("", nil)
	// openModelPicker returns commitCmd (nil in this case)
	if cmd != nil {
		t.Error("expected nil cmd for empty args (commitCmd was nil)")
	}
	if a.listPicker == nil {
		t.Error("listPicker should be set")
	}
	if a.onPickerDone == nil {
		t.Error("onPickerDone should be set")
	}
}

// ---------------------------------------------------------------------------
// switchProviderTier — success
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderTier_Success(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("anthropic/pro", nil)
	_ = cmd // tea.Batch cmd

	if a.currentProvider != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.currentProvider, "anthropic")
	}
	if a.currentTier != config.TierPro {
		t.Errorf("currentTier = %q, want %q", a.currentTier, config.TierPro)
	}
	if a.engine.Model() != "claude-sonnet" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "claude-sonnet")
	}
}

// ---------------------------------------------------------------------------
// switchProviderTier — unknown provider
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderTier_UnknownProvider(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("foo/pro", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "unknown provider: foo") {
		t.Errorf("expected unknown provider message, got %q", info)
	}
	if !strings.Contains(string(info), "available:") {
		t.Errorf("error should list available providers, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchProviderTier — missing tier on provider
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderTier_MissingTier(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("anthropic/lite", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "provider anthropic has no model for tier lite") {
		t.Errorf("expected missing tier message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchTier — success
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchTier_Success(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("lite", nil)
	_ = cmd

	if a.currentTier != config.TierLite {
		t.Errorf("currentTier = %q, want %q", a.currentTier, config.TierLite)
	}
	if a.currentProvider != "openai" {
		t.Errorf("currentProvider should not change, got %q", a.currentProvider)
	}
	if a.engine.Model() != "glm-lite" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "glm-lite")
	}
}

// ---------------------------------------------------------------------------
// switchTier — missing tier
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchTier_MissingTier(t *testing.T) {
	a := newTestAppWithProviders()
	// Switch to anthropic which has no max tier
	a.currentProvider = "anthropic"

	cmd := a.handleModel("max", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "provider anthropic has no model for tier max") {
		t.Errorf("expected missing tier message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchProvider — success
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_Success(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("anthropic", nil)
	_ = cmd

	if a.currentProvider != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.currentProvider, "anthropic")
	}
	// Tier should stay at pro (default)
	if a.currentTier != config.TierPro {
		t.Errorf("currentTier = %q, want %q (unchanged)", a.currentTier, config.TierPro)
	}
	if a.engine.Model() != "claude-sonnet" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "claude-sonnet")
	}
}

// ---------------------------------------------------------------------------
// switchProvider — unknown provider
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_Unknown(t *testing.T) {
	a := newTestAppWithProviders()

	cmd := a.handleModel("unknown", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "unknown provider: unknown") {
		t.Errorf("expected unknown provider message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchProvider — missing tier on target
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_MissingTierOnTarget(t *testing.T) {
	a := newTestAppWithProviders()
	// Current tier is max (openai has it, anthropic does not)
	a.currentTier = config.TierMax

	cmd := a.handleModel("anthropic", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "provider anthropic has no model for tier max") {
		t.Errorf("expected missing tier message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// isValidTier
// ---------------------------------------------------------------------------

func TestIsValidTier(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"lite", true},
		{"pro", true},
		{"max", true},
		{"unknown", false},
		{"", false},
		{"PRO", false}, // case-sensitive
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := isValidTier(tc.input); got != tc.want {
				t.Errorf("isValidTier(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — cancel
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_Cancel(t *testing.T) {
	a := newTestAppWithProviders()
	captured := helperSetupModelPicker(a)
	_ = captured

	// Simulate abort
	p := a.listPicker
	p.aborted = true

	model, cmd := a.handleModelPickerDone(p, captured)
	if _, ok := model.(*App); !ok {
		t.Fatal("expected *App")
	}
	if cmd != nil {
		t.Error("expected nil cmd on cancel")
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — select
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_Select(t *testing.T) {
	a := newTestAppWithProviders()
	captured := helperSetupModelPicker(a)

	// Select second item (index 1)
	wantProvider := captured[1].Provider
	wantTier := captured[1].Tier

	p := a.listPicker
	p.selected = 1

	model, cmd := a.handleModelPickerDone(p, captured)
	_ = model

	if a.currentProvider != wantProvider || a.currentTier != wantTier {
		t.Errorf("provider=%q tier=%q, want provider=%q tier=%q",
			a.currentProvider, a.currentTier,
			wantProvider, wantTier)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd on selection")
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — unknown provider in selection
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_UnknownProvider(t *testing.T) {
	a := newTestAppWithProviders()
	helperSetupModelPicker(a)

	// Create a picker with ghost provider item not in a.providers
	p := a.listPicker
	ghostItems := []ModelItem{{Provider: "ghost", Tier: config.TierPro, Model: "ghost-model"}}
	p.selected = 0

	_, cmd := a.handleModelPickerDone(p, ghostItems)
	if cmd == nil {
		t.Error("expected non-nil cmd for unknown provider error")
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — no selection
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_NilSelected(t *testing.T) {
	a := newTestAppWithProviders()
	helperSetupModelPicker(a)

	// Neither aborted nor selected
	p := a.listPicker
	_, cmd := a.handleModelPickerDone(p, buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentTier))
	if cmd != nil {
		t.Error("expected nil cmd when no selection")
	}
}

// ---------------------------------------------------------------------------
// switchProviderTier — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderTier_NilConfig(t *testing.T) {
	a := newTestAppWithProviders()
	// Add a provider in providers map but not in providerConfigs
	a.providers["ghost"] = &mockLLMProvider{}

	cmd := a.handleModel("ghost/pro", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "no config for provider ghost") {
		t.Errorf("expected no config message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchTier — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchTier_NilConfig(t *testing.T) {
	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{},
		Model:    "test",
		Logger:   slog.Default(),
	})
	a := &App{
		engine:    eng,
		repl:      NewReplState(),
		providers: map[string]llm.Provider{"openai": &mockLLMProvider{}},
		// providerConfigs is nil → no config for current provider
	}
	a.currentProvider = "openai"

	cmd := a.handleModel("pro", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "no config for provider openai") {
		t.Errorf("expected no config message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchProvider — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_NilConfig(t *testing.T) {
	a := newTestAppWithProviders()
	// Add a provider in providers map but not in providerConfigs
	a.providers["ghost"] = &mockLLMProvider{}

	cmd := a.handleModel("ghost", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "no config for provider ghost") {
		t.Errorf("expected no config message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// buildModelItems tests
// ---------------------------------------------------------------------------

func TestBuildModelItems_Items(t *testing.T) {
	a := newTestAppWithProviders()
	items := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentTier)

	// openai: 3 tiers + anthropic: 1 tier = 4 items
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(items))
	}

	// Sorted by provider name: anthropic first, then openai
	if items[0].Provider != "anthropic" {
		t.Errorf("items[0].Provider = %q, want anthropic", items[0].Provider)
	}
	if items[1].Provider != "openai" {
		t.Errorf("items[1].Provider = %q, want openai", items[1].Provider)
	}
}

func TestBuildModelItems_CurrentMarked(t *testing.T) {
	a := newTestAppWithProviders()
	items := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentTier)

	found := false
	for _, item := range items {
		if item.Provider == "openai" && item.Tier == config.TierPro {
			if !item.Current {
				t.Error("openai/pro should be Current=true")
			}
			found = true
		} else {
			if item.Current {
				t.Errorf("%s/%s should not be Current", item.Provider, item.Tier)
			}
		}
	}
	if !found {
		t.Error("openai/pro item not found")
	}
}

func TestBuildModelItems_SkipsProviderWithoutImpl(t *testing.T) {
	providers := map[string]llm.Provider{
		"openai": &mockLLMProvider{},
	}
	providerConfigs := map[string]*config.Provider{
		"openai": {
			Name: "openai",
			Models: map[config.Tier]string{
				config.TierPro: "glm-5",
			},
		},
		"anthropic": {
			Name: "anthropic",
			Models: map[config.Tier]string{
				config.TierPro: "claude-sonnet",
			},
		},
	}
	items := buildModelItems(providers, providerConfigs, "openai", config.TierPro)

	if len(items) != 1 {
		t.Fatalf("expected 1 item (anthropic skipped), got %d", len(items))
	}
	if items[0].Provider != "openai" {
		t.Errorf("expected openai item, got %q", items[0].Provider)
	}
}

func TestBuildModelItems_Empty(t *testing.T) {
	items := buildModelItems(
		map[string]llm.Provider{},
		map[string]*config.Provider{},
		"openai",
		config.TierPro,
	)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

// ---------------------------------------------------------------------------
// findCurrentIndex tests
// ---------------------------------------------------------------------------

func TestFindCurrentIndex_Found(t *testing.T) {
	items := []ModelItem{
		{Provider: "openai", Tier: config.TierLite, Model: "glm-lite", Current: false},
		{Provider: "openai", Tier: config.TierPro, Model: "glm-5", Current: true},
		{Provider: "openai", Tier: config.TierMax, Model: "glm-max", Current: false},
	}
	idx := findCurrentIndex(items)
	if idx != 1 {
		t.Errorf("findCurrentIndex = %d, want 1", idx)
	}
}

func TestFindCurrentIndex_NotFound(t *testing.T) {
	items := []ModelItem{
		{Provider: "openai", Tier: config.TierPro, Model: "glm-5", Current: false},
	}
	idx := findCurrentIndex(items)
	if idx != -1 {
		t.Errorf("findCurrentIndex = %d, want -1 (not found)", idx)
	}
}

func TestFindCurrentIndex_Empty(t *testing.T) {
	idx := findCurrentIndex(nil)
	if idx != -1 {
		t.Errorf("findCurrentIndex(nil) = %d, want -1", idx)
	}
}

// ---------------------------------------------------------------------------
// picker-already-open guard
// ---------------------------------------------------------------------------

func TestOpenModelPicker_AlreadyOpen(t *testing.T) {
	a := newTestAppWithProviders()
	// Open a model picker first
	a.handleModel("", nil)
	if a.listPicker == nil {
		t.Fatal("expected listPicker to be set")
	}
	// Try opening again — should show info, not replace picker
	cmd := a.handleModel("", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "picker is already open") {
		t.Errorf("expected already-open message, got %q", info)
	}
}
