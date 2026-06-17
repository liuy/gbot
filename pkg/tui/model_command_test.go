package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/quota"
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
func newTestAppWithProviders(t *testing.T) *App {
	t.Helper()
	// Isolate HOME so persistModelSelection() writes to temp dir
	_ = os.Setenv("HOME", t.TempDir())
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
		Model: config.ModelSpec{"default": "openai/glm-5"},
		Providers: []config.Provider{
			{
				Name: "openai",
				URL:  "https://api.example.com",
				Keys: []string{"test-key"},
				Models: map[string]config.ModelConfig{
					"glm-lite": {},
					"glm-5":    {},
					"glm-max":  {},
				},
			},
			{
				Name: "anthropic",
				URL:  "https://api.anthropic.com",
				Keys: []string{"test-key-2"},
				Models: map[string]config.ModelConfig{
					"claude-sonnet": {},
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
func helperSetupModelPicker(a *App) []ModelItem {
	modelItems := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentModel)
	items := make([]PickerItem, len(modelItems))
	for i := range modelItems {
		items[i] = &modelItems[i]
	}
	currentIdx := findCurrentIndex(modelItems)
	a.activeDialog = NewListPicker("Select model", items, WithInitialCursor(currentIdx))

	captured := modelItems
	a.onDialogDone = func(p *Dialog) (tea.Model, tea.Cmd) {
		return a.handleModelPickerDone(p, captured)
	}
	return captured
}

// ---------------------------------------------------------------------------
// handleModel — streaming guard
// ---------------------------------------------------------------------------

func TestHandleModel_StreamingGuard(t *testing.T) {
	a := newTestAppWithProviders(t)
	a.repl.streaming = true

	cmd := a.handleModel("openai/glm-5", nil)
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

	cmd := a.handleModel("glm-5", nil)
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
	a := newTestAppWithProviders(t)

	cmd := a.handleModel("", nil)
	if cmd != nil {
		t.Error("expected nil cmd for empty args (commitCmd was nil)")
	}
	if a.activeDialog == nil {
		t.Error("listPicker should be set")
	}
	if a.onDialogDone == nil {
		t.Error("onPickerDone should be set")
	}
}

// ---------------------------------------------------------------------------
// switchProviderModel — success
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_Success(t *testing.T) {
	a := newTestAppWithProviders(t)

	_ = a.handleModel("anthropic/claude-sonnet", nil)

	if a.currentProvider != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.currentProvider, "anthropic")
	}
	if a.currentModel != "claude-sonnet" {
		t.Errorf("currentModel = %q, want %q", a.currentModel, "claude-sonnet")
	}
	if a.engine.Model() != "claude-sonnet" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "claude-sonnet")
	}
}

// ---------------------------------------------------------------------------
// switchProviderModel — unknown provider
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_UnknownProvider(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleModel("foo/some-model", nil)
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
// switchProviderModel — model not found in provider
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_ModelNotFound(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleModel("anthropic/nonexistent", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "not found in provider") {
		t.Errorf("expected 'not found' message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchModel — success (fuzzy match within current provider)
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchModel_Success(t *testing.T) {
	a := newTestAppWithProviders(t)

	_ = a.handleModel("glm-max", nil)

	if a.currentModel != "glm-max" {
		t.Errorf("currentModel = %q, want %q", a.currentModel, "glm-max")
	}
	if a.currentProvider != "openai" {
		t.Errorf("currentProvider should not change, got %q", a.currentProvider)
	}
	if a.engine.Model() != "glm-max" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "glm-max")
	}
}

// ---------------------------------------------------------------------------
// switchModel — model not found
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchModel_NotFound(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleModel("nonexistent", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	if !strings.Contains(string(info), "not found in provider") {
		t.Errorf("expected 'not found' message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchProvider — success (uses FirstModelName)
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_Success(t *testing.T) {
	a := newTestAppWithProviders(t)

	_ = a.handleModel("anthropic", nil)

	if a.currentProvider != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.currentProvider, "anthropic")
	}
	if a.currentModel != "claude-sonnet" {
		t.Errorf("currentModel = %q, want %q", a.currentModel, "claude-sonnet")
	}
}

// RED-LIGHT: switching from a provider with quota to one without
// must clear both the fetcher and the visible quota. Otherwise the
// status bar shows the old value indefinitely.
func TestHandleModel_SwitchProvider_ClearsQuotaWhenNoFetcher(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Simulate quota from the first provider (openai's URL is example.com → no fetcher,
	// so we inject one to verify the clear path).
	a.status.SetQuota(&quota.Info{Used: 40, ResetAt: time.UnixMilli(1800000000000)})

	// Sanity: before the switch, quota is visible.
	if v := a.status.View(); !strings.Contains(v, "60%") {
		t.Fatalf("precondition: status should show 60%%, got %q", v)
	}

	_ = a.handleModel("anthropic", nil)

	// After switching, fetcher must be nil and the bar must not show "%".
	if a.quotaFetcher != nil {
		t.Errorf("quotaFetcher = %T, want nil after switching to no-quota provider", a.quotaFetcher)
	}
	if v := a.status.View(); strings.Contains(v, "%") {
		t.Errorf("after switch, status should not show '%%', got %q", v)
	}
}

// RED-LIGHT: query_end on a provider with no fetcher must not leave
// a stale quota from the previous provider visible. Reproduce by
// seeding a quota value, then firing queryEndMsg and verifying the
// bar stays clean (or updates, but never shows stale data).
func TestUpdate_QueryEnd_WhenFetcherNil_KeepsQuotaCleared(t *testing.T) {
	a := newTestAppWithProviders(t)
	_ = a.handleModel("anthropic", nil) // switches to a no-quota provider
	if a.quotaFetcher != nil {
		t.Fatalf("precondition: fetcher should be nil, got %T", a.quotaFetcher)
	}
	// Simulate a stale value from a prior session leaking through.
	a.status.SetQuota(&quota.Info{Used: 40, ResetAt: time.UnixMilli(1800000000000)})
	if v := a.status.View(); !strings.Contains(v, "60%") {
		t.Fatalf("precondition: status should show 60%%, got %q", v)
	}
	// fetchQuota must return nil when the fetcher is nil, so query_end
	// can't accidentally revive the stale value.
	if cmd := a.fetchQuota(); cmd != nil {
		t.Errorf("fetchQuota() = %v, want nil cmd when fetcher is nil", cmd)
	}
}

// ---------------------------------------------------------------------------
// switchProvider — unknown provider
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_Unknown(t *testing.T) {
	a := newTestAppWithProviders(t)

	cmd := a.handleModel("unknown", nil)
	msg := cmd()
	info, ok := msg.(infoMsg)
	if !ok {
		t.Fatalf("expected infoMsg, got %T", msg)
	}
	// "unknown" is not a provider name → routed as model name → fuzzy match fails
	if !strings.Contains(string(info), "not found") {
		t.Errorf("expected model-not-found message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — cancel
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_Cancel(t *testing.T) {
	a := newTestAppWithProviders(t)
	captured := helperSetupModelPicker(a)
	if len(captured) == 0 {
		t.Fatal("expected at least one model item from helperSetupModelPicker")
	}

	p := a.activeDialog
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
	a := newTestAppWithProviders(t)
	captured := helperSetupModelPicker(a)

	wantProvider := captured[1].Provider
	wantModel := captured[1].Model

	p := a.activeDialog
	p.done = true
	p.cursor = 1

	_, cmd := a.handleModelPickerDone(p, captured)

	if a.currentProvider != wantProvider || a.currentModel != wantModel {
		t.Errorf("provider=%q model=%q, want provider=%q model=%q",
			a.currentProvider, a.currentModel,
			wantProvider, wantModel)
	}
	if cmd == nil {
		t.Error("expected non-nil cmd on selection")
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — unknown provider in selection
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_UnknownProvider(t *testing.T) {
	a := newTestAppWithProviders(t)
	helperSetupModelPicker(a)

	p := a.activeDialog
	ghostItems := []ModelItem{{Provider: "ghost", Model: "ghost-model"}}
	p.done = true
	p.cursor = 0

	_, cmd := a.handleModelPickerDone(p, ghostItems)
	if cmd == nil {
		t.Error("expected non-nil cmd for unknown provider error")
	}
}

// ---------------------------------------------------------------------------
// handleModelPickerDone — no selection
// ---------------------------------------------------------------------------

func TestHandleModelPickerDone_NilSelected(t *testing.T) {
	a := newTestAppWithProviders(t)
	helperSetupModelPicker(a)

	p := a.activeDialog
	_, cmd := a.handleModelPickerDone(p, buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentModel))
	if cmd != nil {
		t.Error("expected nil cmd when no selection")
	}
}

// ---------------------------------------------------------------------------
// switchProviderModel — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_NilConfig(t *testing.T) {
	a := newTestAppWithProviders(t)
	a.providers["ghost"] = &mockLLMProvider{}

	cmd := a.handleModel("ghost/some-model", nil)
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
// switchModel — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchModel_NilConfig(t *testing.T) {
	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{},
		Model:    "test",
		Logger:   slog.Default(),
	})
	a := &App{
		engine:    eng,
		repl:      NewReplState(),
		providers: map[string]llm.Provider{"openai": &mockLLMProvider{}},
	}
	a.currentProvider = "openai"

	cmd := a.handleModel("glm-5", nil)
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
	a := newTestAppWithProviders(t)
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
	a := newTestAppWithProviders(t)
	items := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentModel)

	// anthropic: 1 model + openai: 3 models = 4 items
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
	a := newTestAppWithProviders(t)
	items := buildModelItems(a.providers, a.providerConfigs, a.currentProvider, a.currentModel)

	found := false
	for _, item := range items {
		if item.Provider == "openai" && item.Model == "glm-5" {
			if !item.Current {
				t.Error("openai/glm-5 should be Current=true")
			}
			found = true
		} else {
			if item.Current {
				t.Errorf("%s/%s should not be Current", item.Provider, item.Model)
			}
		}
	}
	if !found {
		t.Error("openai/glm-5 item not found")
	}
}

func TestBuildModelItems_SkipsProviderWithoutImpl(t *testing.T) {
	providers := map[string]llm.Provider{
		"openai": &mockLLMProvider{},
	}
	providerConfigs := map[string]*config.Provider{
		"openai": {
			Name: "openai",
			Models: map[string]config.ModelConfig{
				"glm-5": {},
			},
		},
		"anthropic": {
			Name: "anthropic",
			Models: map[string]config.ModelConfig{
				"claude-sonnet": {},
			},
		},
	}
	items := buildModelItems(providers, providerConfigs, "openai", "glm-5")

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
		"glm-5",
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
		{Provider: "openai", Model: "glm-lite", Current: false},
		{Provider: "openai", Model: "glm-5", Current: true},
		{Provider: "openai", Model: "glm-max", Current: false},
	}
	idx := findCurrentIndex(items)
	if idx != 1 {
		t.Errorf("findCurrentIndex = %d, want 1", idx)
	}
}

func TestFindCurrentIndex_NotFound(t *testing.T) {
	items := []ModelItem{
		{Provider: "openai", Model: "glm-5", Current: false},
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

// ---------------------------------------------------------------------------
// updateEngineCapabilities — preserves ContextTokens on model switch
// ---------------------------------------------------------------------------

func TestUpdateEngineCapabilities_PreservesContextTokens(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Simulate that the engine has accumulated context tokens from prior turns.
	a.engine.SetContextTokens(42000)

	// Switch model — the status bar should reflect the existing token count,
	// not reset to zero.
	a.updateEngineCapabilities("openai", "glm-max")

	if a.status.contextUsed != 42000 {
		t.Errorf("contextUsed = %d, want 42000 (preserved from engine)", a.status.contextUsed)
	}
}

// Cold-start path: no API response yet, so engine.ContextTokens == 0. The
// status bar must still reflect the system prompt + tools estimate, not 0.
// (Regression for: /model switch showed "0/200.0k" before any turn completed.)
func TestUpdateEngineCapabilities_ColdStart_EstimatesFromPrompt(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Set a non-trivial system prompt so EstimateTokens yields a non-zero value.
	a.systemPrompt = "You are a helpful assistant. " + strings.Repeat("context assembly. ", 200)

	if a.engine.GetContextTokens() != 0 {
		t.Fatalf("precondition: expected fresh engine to have 0 context tokens")
	}

	a.updateEngineCapabilities("openai", "glm-max")

	if a.status.contextUsed == 0 {
		t.Errorf("contextUsed = 0 on cold-start switch; want >0 from systemPrompt estimate")
	}
	if a.status.contextUsed < len(a.systemPrompt)/8 {
		t.Errorf("contextUsed = %d; want at least ~len/4 of systemPrompt (%d chars)",
			a.status.contextUsed, len(a.systemPrompt))
	}
}

func TestOpenModelPicker_AlreadyOpen(t *testing.T) {
	a := newTestAppWithProviders(t)
	a.handleModel("", nil)
	if a.activeDialog == nil {
		t.Fatal("expected listPicker to be set")
	}

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
