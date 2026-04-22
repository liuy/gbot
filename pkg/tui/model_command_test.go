package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"log/slog"
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
	if a.pickerMode != pickerModel {
		t.Errorf("pickerMode = %v, want pickerModel", a.pickerMode)
	}
	if a.modelPicker == nil {
		t.Error("modelPicker should be set")
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
// handleModelPickerResult
// ---------------------------------------------------------------------------

func TestHandleModelPickerResult_Cancel(t *testing.T) {
	a := newTestAppWithProviders()
	a.pickerMode = pickerModel
	a.modelPicker = NewModelPicker(a.providers, a.providerConfigs, "openai", config.TierPro)
	a.modelPicker.aborted = true

	_, cmd := a.handleModelPickerResult()
	if a.pickerMode != pickerNone {
		t.Errorf("pickerMode = %v, want pickerNone", a.pickerMode)
	}
	if cmd != nil {
		t.Error("expected nil cmd on cancel")
	}
}

func TestHandleModelPickerResult_Select(t *testing.T) {
	a := newTestAppWithProviders()
	a.pickerMode = pickerModel
	a.modelPicker = NewModelPicker(a.providers, a.providerConfigs, "openai", config.TierPro)
	// Select second item (index 1)
	a.modelPicker.cursor = 1
	a.modelPicker.selected = &a.modelPicker.items[1]

	wantProvider := a.modelPicker.items[1].Provider
	wantTier := a.modelPicker.items[1].Tier

	model, cmd := a.handleModelPickerResult()
	_ = model

	if a.pickerMode != pickerNone {
		t.Errorf("pickerMode = %v, want pickerNone", a.pickerMode)
	}
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
// handleModelPickerResult — unknown provider in selection
// ---------------------------------------------------------------------------

func TestHandleModelPickerResult_UnknownProvider(t *testing.T) {
	a := newTestAppWithProviders()
	a.pickerMode = pickerModel
	a.modelPicker = NewModelPicker(a.providers, a.providerConfigs, "openai", config.TierPro)
	// Manually craft a selection with a provider not in a.providers
	a.modelPicker.selected = &ModelItem{Provider: "ghost", Tier: config.TierPro, Model: "ghost-model"}

	_, cmd := a.handleModelPickerResult()
	if a.pickerMode != pickerNone {
		t.Errorf("pickerMode = %v, want pickerNone", a.pickerMode)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for unknown provider error")
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

func TestHandleModelPickerResult_NilSelected(t *testing.T) {
	a := newTestAppWithProviders()
	a.pickerMode = pickerModel
	a.modelPicker = NewModelPicker(a.providers, a.providerConfigs, "openai", config.TierPro)
	// Neither aborted nor selected
	_, cmd := a.handleModelPickerResult()
	if a.pickerMode != pickerNone {
		t.Errorf("pickerMode should be pickerNone, got %v", a.pickerMode)
	}
	if cmd != nil {
		t.Error("expected nil cmd when no selection")
	}
}
