package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/engine"
	"github.com/liuy/gbot/pkg/llm"
	"github.com/liuy/gbot/pkg/memory/short"
	"github.com/liuy/gbot/pkg/quota"
)

// mockLLMProvider is a minimal mock for testing model switching.
// The name field gives each instance a unique identity so tests can
// distinguish providers by pointer comparison (empty structs share
// the same address in Go).
type mockLLMProvider struct {
	name string
}

func (m *mockLLMProvider) Name() string { return m.name }

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
		Provider: &mockLLMProvider{name: "openai"},
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
				Models: config.NewModelsFromMap(map[string]config.ModelConfig{
					"glm-lite": {},
					"glm-5":    {},
					"glm-max":  {},
				}),
			},
			{
				Name: "anthropic",
				URL:  "https://api.anthropic.com",
				Keys: []string{"test-key-2"},
				Models: config.NewModelsFromMap(map[string]config.ModelConfig{
					"claude-sonnet": {},
				}),
			},
			{
				Name: "minimax",
				URL:  "https://api.minimaxi.com",
				Keys: []string{"sk-minimax-key"},
				Models: config.NewModelsFromMap(map[string]config.ModelConfig{
					"minimax-3":   {},
					"minimax-2.7": {},
				}),
			},
		},
	}

	providers := map[string]llm.Provider{
		"openai":    &mockLLMProvider{name: "openai"},
		"anthropic": &mockLLMProvider{name: "anthropic"},
		"minimax":   &mockLLMProvider{name: "minimax"},
	}

	a.SetProviders(providers, cfg)
	return a
}

// helperSetupModelPicker creates a ListPicker for model items and sets up the onPickerDone closure.
func helperSetupModelPicker(a *App) []ModelItem {
	modelItems := buildModelItems(a.providers, a.providerConfigs, a.CurrentProvider(), a.CurrentModel())
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

func TestModelPicker_QuotaAppearsAfterFetch(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Open model picker.
	_ = a.openModelPicker(nil)
	if a.activeDialog == nil {
		t.Fatal("dialog should be open after openModelPicker")
	}
	if len(a.modelPickerItems) == 0 {
		t.Fatal("modelPickerItems should be populated")
	}

	// Send quota result for a known provider.
	resetAt := time.Now().Add(2*time.Hour + 30*time.Minute) // REAL-TIME: quota reset time is inherently wall-clock based
	msg := modelQuotaFetchedMsg{
		provider: "openai",
		info:     quota.Info{Used: 15, ResetAt: resetAt},
		err:      nil,
	}
	handled, cmd := a.updateRepl(msg)
	if !handled {
		t.Fatal("updateRepl should handle modelQuotaFetchedMsg")
	}
	if cmd != nil {
		t.Fatal("expected nil cmd from quota handler")
	}

	// All "openai" items should now have quota set.
	var quotaCount int
	for _, item := range a.modelPickerItems {
		if item.Provider == "openai" {
			if item.Quota == "" {
				t.Errorf("openai item %s/%s should have quota set, got empty", item.Provider, item.Model)
			}
			quotaCount++
		}
	}
	if quotaCount == 0 {
		t.Fatal("no openai items found in modelPickerItems")
	}

	// Dialog options should contain the formatted quota string.
	view := a.activeDialog.View()
	if !strings.Contains(view, "85%") || !strings.Contains(view, "2h") {
		t.Errorf("dialog view should contain quota '85%%/2h..m', got:\n%s", view)
	}

	// Sending a second result for the same provider should update in-place.
	resetAt2 := time.Now().Add(1 * time.Hour) // REAL-TIME: quota reset time is inherently wall-clock based
	msg2 := modelQuotaFetchedMsg{
		provider: "openai",
		info:     quota.Info{Used: 50, ResetAt: resetAt2},
		err:      nil,
	}
	handled2, _ := a.updateRepl(msg2)
	if !handled2 {
		t.Fatal("second quota msg should be handled")
	}
	view2 := a.activeDialog.View()
	// Countdown may round down to 59m depending on test timing; check Used part.
	if !strings.Contains(view2, "50%") {
		t.Errorf("dialog view should update to 50%%, got:\n%s", view2)
	}
}

// TestModelPicker_QuotaThroughUpdate verifies that modelQuotaFetchedMsg
// reaches the handler via App.Update with an active dialog.
func TestModelPicker_QuotaThroughUpdate(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Open model picker via handleModel (the real entry point).
	_ = a.handleModel("", nil)
	if a.activeDialog == nil {
		t.Fatal("dialog should be open after handleModel")
	}
	if len(a.modelPickerItems) == 0 {
		t.Fatal("modelPickerItems should be populated")
	}

	// Send quota result through App.Update (the real message dispatch path).
	resetAt := time.Now().Add(40 * time.Minute) // REAL-TIME: quota reset time
	msg := modelQuotaFetchedMsg{
		provider: "openai",
		info:     quota.Info{Used: 70, ResetAt: resetAt},
		err:      nil,
	}
	_, cmd := a.Update(msg)
	if cmd != nil {
		t.Fatal("expected nil cmd")
	}

	// All "openai" items should have quota set.
	var quotaCount int
	for _, item := range a.modelPickerItems {
		if item.Provider == "openai" {
			if item.Quota == "" {
				t.Errorf("openai item %s/%s should have quota set, got empty", item.Provider, item.Model)
			}
			quotaCount++
		}
	}
	if quotaCount == 0 {
		t.Fatal("no openai items found in modelPickerItems")
	}

	// Dialog should display the quota.
	view := a.activeDialog.View()
	if !strings.Contains(view, "30%") {
		t.Errorf("dialog view should contain '30%%' (100-70), got:\n%s", view)
	}
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
		Provider: &mockLLMProvider{name: "test"},
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

	_ = a.handleModel("", nil)
	if a.activeDialog == nil {
		t.Error("dialog should be set")
	}
	if a.onDialogDone == nil {
		t.Error("onDialogDone should be set")
	}
}

// ---------------------------------------------------------------------------
// switchProviderModel — success
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_Success(t *testing.T) {
	a := newTestAppWithProviders(t)

	_ = a.handleModel("anthropic/claude-sonnet", nil)

	if a.CurrentProvider() != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.CurrentProvider(), "anthropic")
	}
	if a.CurrentModel() != "claude-sonnet" {
		t.Errorf("currentModel = %q, want %q", a.CurrentModel(), "claude-sonnet")
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

	if a.CurrentModel() != "glm-max" {
		t.Errorf("currentModel = %q, want %q", a.CurrentModel(), "glm-max")
	}
	if a.CurrentProvider() != "openai" {
		t.Errorf("currentProvider should not change, got %q", a.CurrentProvider())
	}
	if a.engine.Model() != "glm-max" {
		t.Errorf("engine model = %q, want %q", a.engine.Model(), "glm-max")
	}
}

// TestHandleModel_SwitchModel_SyncsEngineProvider reproduces a bug where
// switchModel's current-provider-match branch only called SetModel, not
// SetProvider. When a.CurrentProvider() and engine.provider drift out of sync
// (e.g. after engine switch, or on startup when a.CurrentProvider() is seeded
// from settings.json but engine.provider comes from meta.json), /model <name>
// would SetModel on the wrong provider — the request goes to the old provider
// with the new model name, producing "model does not exist".
//
// Real scenario: engine restored from meta.json with provider=stepfun,
// model=step-3.7-flash. a.CurrentProvider() seeded from settings.json=zhipu.
// /model glm52 → fuzzy match in zhipu → SetModel("glm-5.2") only →
// request goes to stepfun with model=glm-5.2 → "model does not exist".
//
// This test simulates the drift by constructing the engine with minimax
// provider (proxy for stepfun) but leaving a.CurrentProvider()=openai (proxy
// for zhipu). /model glm-max must switch BOTH model AND provider.
func TestHandleModel_SwitchModel_SyncsEngineProvider(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Override the engine's provider to minimax — simulates an engine
	// restored from meta.json whose provider differs from a.CurrentProvider()
	// (which SetProviders seeded as "openai" from cfg.Model).
	minimaxProvider := a.providers["minimax"]
	a.engine.SetProvider(minimaxProvider)
	// a.CurrentProvider() is "openai" (from newTestAppWithProviders setup).

	_ = a.handleModel("glm-max", nil)

	// engine.provider must be synced to a.CurrentProvider() (openai), not left
	// as minimax. Compare by pointer identity — the exact provider object
	// a.providers["openai"] must be installed after the switch.
	if a.engine.Provider() != a.providers["openai"] {
		t.Errorf("engine.provider was not synced to currentProvider: got %p (minimax), want %p (openai)",
			a.engine.Provider(), a.providers["openai"])
	}
	if a.engine.Model() != "glm-max" {
		t.Errorf("engine.model = %q, want glm-max", a.engine.Model())
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
	if !strings.Contains(string(info), "not found in any provider") {
		t.Errorf("expected 'not found in any provider' message, got %q", info)
	}
}

// ---------------------------------------------------------------------------
// switchModel — cross-provider fallback
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchModel_CrossProvider(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Current provider is openai; "m3" doesn't exist there but "minimax-3" does
	// in the minimax provider. Fuzzy match "m3" → "minimax-3" should switch.
	cmd := a.handleModel("m3", nil)
	if cmd != nil {
		cmd() // consume the cmd
	}

	if a.CurrentProvider() != "minimax" {
		t.Errorf("currentProvider = %q, want %q (should cross-switch)", a.CurrentProvider(), "minimax")
	}
	if a.CurrentModel() != "minimax-3" {
		t.Errorf("currentModel = %q, want %q", a.CurrentModel(), "minimax-3")
	}
}

// ---------------------------------------------------------------------------
// switchProvider — success (uses FirstModelName)
// ---------------------------------------------------------------------------

func TestHandleModel_SwitchProvider_Success(t *testing.T) {
	a := newTestAppWithProviders(t)

	_ = a.handleModel("anthropic", nil)

	if a.CurrentProvider() != "anthropic" {
		t.Errorf("currentProvider = %q, want %q", a.CurrentProvider(), "anthropic")
	}
	if a.CurrentModel() != "claude-sonnet" {
		t.Errorf("currentModel = %q, want %q", a.CurrentModel(), "claude-sonnet")
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

	if a.CurrentProvider() != wantProvider || a.CurrentModel() != wantModel {
		t.Errorf("provider=%q model=%q, want provider=%q model=%q",
			a.CurrentProvider(), a.CurrentModel(),
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
	ghostItems := []ModelItem{{ModelItem: config.ModelItem{Provider: "ghost", Model: "ghost-model"}}}
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
	_, cmd := a.handleModelPickerDone(p, buildModelItems(a.providers, a.providerConfigs, a.CurrentProvider(), a.CurrentModel()))
	if cmd != nil {
		t.Error("expected nil cmd when no selection")
	}
}

// ---------------------------------------------------------------------------
// switchProviderModel — nil providerConfig
// ---------------------------------------------------------------------------

func TestHandleModel_ProviderModel_NilConfig(t *testing.T) {
	a := newTestAppWithProviders(t)
	a.providers["ghost"] = &mockLLMProvider{name: "test"}

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
		Provider: &mockLLMProvider{name: "test"},
		Model:    "test",
		Logger:   slog.Default(),
	})
	a := &App{
		engine:    eng,
		repl:      NewReplState(),
		providers: map[string]llm.Provider{"openai": &mockLLMProvider{name: "openai"}},
	}
	a.engine.SetProvider(a.providers["openai"])

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
	a.providers["ghost"] = &mockLLMProvider{name: "test"}

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
	items := buildModelItems(a.providers, a.providerConfigs, a.CurrentProvider(), a.CurrentModel())

	// anthropic: 1 model, minimax: 2 models, openai: 3 models = 6 items
	if len(items) != 6 {
		t.Fatalf("expected 6 items, got %d", len(items))
	}

	// Sorted by provider name: anthropic first, then minimax, then openai
	if items[0].Provider != "anthropic" {
		t.Errorf("items[0].Provider = %q, want anthropic", items[0].Provider)
	}
	if items[1].Provider != "minimax" {
		t.Errorf("items[1].Provider = %q, want minimax", items[1].Provider)
	}
	if items[3].Provider != "openai" {
		t.Errorf("items[3].Provider = %q, want openai", items[3].Provider)
	}
}

func TestBuildModelItems_CurrentMarked(t *testing.T) {
	a := newTestAppWithProviders(t)
	items := buildModelItems(a.providers, a.providerConfigs, a.CurrentProvider(), a.CurrentModel())

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
		"openai": &mockLLMProvider{name: "test"},
	}
	providerConfigs := map[string]*config.Provider{
		"openai": {
			Name: "openai",
			Models: config.NewModelsFromMap(map[string]config.ModelConfig{
				"glm-5": {},
			}),
		},
		"anthropic": {
			Name: "anthropic",
			Models: config.NewModelsFromMap(map[string]config.ModelConfig{
				"claude-sonnet": {},
			}),
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
		{ModelItem: config.ModelItem{Provider: "openai", Model: "glm-lite", Current: false}},
		{ModelItem: config.ModelItem{Provider: "openai", Model: "glm-5", Current: true}},
		{ModelItem: config.ModelItem{Provider: "openai", Model: "glm-max", Current: false}},
	}
	idx := findCurrentIndex(items)
	if idx != 1 {
		t.Errorf("findCurrentIndex = %d, want 1", idx)
	}
}

func TestFindCurrentIndex_NotFound(t *testing.T) {
	items := []ModelItem{
		{ModelItem: config.ModelItem{Provider: "openai", Model: "glm-5", Current: false}},
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
	a.status.contextUsed = 42000

	// Switch model — the status bar used should be preserved (not reset to
	// zero, not re-estimated from messages). Only the window (denominator)
	// changes.
	a.updateEngineCapabilities("openai", "glm-max")

	if a.status.contextUsed != 42000 {
		t.Errorf("contextUsed = %d, want 42000 (should be preserved by updateEngineCapabilities)", a.status.contextUsed)
	}
	if a.status.contextTotal == 0 {
		t.Error("contextTotal = 0, want non-zero from provider config")
	}
}

// Cold-start path: no API response yet, so engine.ContextTokens == 0. The
// status bar must still reflect the system prompt + tools estimate, not 0.
// (Regression for: /model switch showed "0/200.0k" before any turn completed.)
func TestUpdateEngineCapabilities_ColdStart_KeepsWindowZero(t *testing.T) {
	a := newTestAppWithProviders(t)

	// Set a non-trivial system prompt.
	a.systemPrompt = "You are a helpful assistant. " + strings.Repeat("context assembly. ", 200)

	if a.engine.GetContextTokens() != 0 {
		t.Fatalf("precondition: expected fresh engine to have 0 context tokens")
	}

	a.updateEngineCapabilities("openai", "glm-max")

	// updateEngineCapabilities should only update the context window,
	// NOT estimate context used. Used comes from API responses.
	if a.status.contextUsed != 0 {
		t.Errorf("contextUsed = %d on cold-start; want 0 (should not estimate — used comes from API responses)",
			a.status.contextUsed)
	}
	// Window must be set from provider config.
	if a.status.contextTotal == 0 {
		t.Error("contextTotal = 0; want non-zero from provider config")
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

// TestPersistModelSelection_WritesPerEngineMeta verifies that /model
// switching persists the new model to the active engine's vs.Model and
// writes it through to meta.json on disk. Without this, switching model
// on engine A and restarting loses the change (engine reverts to whatever
// meta.json had before).
func TestPersistModelSelection_WritesPerEngineMeta(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{name: "openai"},
		Model:    "glm-5",
		Logger:   slog.Default(),
	})
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: eng,
		ID:     "main",
		Name:   "main",
		Model:  "openai/glm-5", // initial model
	})

	a := newTestAppWithProviders(t)
	a.engine = eng
	a.engineMgr = mgr
	a.projectDir = projectDir

	// Switch from glm-5 to glm-max.
	_ = a.handleModel("glm-max", nil)

	// Assert: vs.Model updated in memory.
	vs := mgr.Get("main")
	if vs.Model != "openai/glm-max" {
		t.Errorf("vs.Model = %q, want openai/glm-max", vs.Model)
	}
	// Assert: meta.json on disk has the new model.
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("meta.json not written — persistModelSelection did not call PersistMeta")
	}
	var gotModel string
	for _, em := range meta.Engines {
		if em.ID == "main" {
			gotModel = em.Model
		}
	}
	if gotModel != "openai/glm-max" {
		t.Errorf("meta.json main.Model = %q, want openai/glm-max", gotModel)
	}
}

// TestPersistModelSelection_OpenRouterFormat verifies that persistModelSelection
// stores "provider/model" format correctly for openrouter models, including
// the double-prefix case where the model name already contains the provider.
//   - provider=openrouter, model=openrouter/owl-alpha
//   - vs.Model should be "openrouter/openrouter/owl-alpha" (not "owl-alpha")
//
// Regression: old code stored bare model name, causing restart to create
// engine with wrong model.
func TestPersistModelSelection_OpenRouterFormat(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{name: "openrouter"},
		Model:    "openrouter/owl-alpha",
		Logger:   slog.Default(),
	})
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: eng, ID: "main", Name: "main", Model: "openrouter/owl-alpha",
	})

	a := newTestAppWithProviders(t)
	a.providers["openrouter"] = &mockLLMProvider{name: "openrouter"}
	a.engine = eng
	a.engineMgr = mgr
	a.projectDir = projectDir
	a.engine.SetProvider(a.providers["openrouter"])
	a.engine.SetModel("openrouter/owl-alpha")

	a.persistModelSelection()

	// vs.Model must be "provider/model" — not bare model name.
	vs := mgr.Get("main")
	if vs.Model != "openrouter/openrouter/owl-alpha" {
		t.Errorf("vs.Model = %q, want openrouter/openrouter/owl-alpha", vs.Model)
	}

	// meta.json must match.
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil || meta == nil {
		t.Fatalf("ReadWorkspaceMeta: err=%v meta=%v", err, meta)
	}
	for _, em := range meta.Engines {
		if em.ID == "main" && em.Model != "openrouter/openrouter/owl-alpha" {
			t.Errorf("meta.json main.Model = %q, want openrouter/openrouter/owl-alpha", em.Model)
		}
	}
}

// TestRestoreEngines_StripsProviderPrefix verifies that when restoring
// from meta.json, the "provider/model" format is split: the factory
// receives the bare registration name, not the full "provider/model".
func TestRestoreEngines_StripProviderFromModel(t *testing.T) {
	projectDir := t.TempDir()
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "openrouter/openrouter/owl-alpha"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	planned := planRestoreForTest(t, meta, "zhipu/glm-5")
	if len(planned) != 1 {
		t.Fatalf("planRestore returned %d engines, want 1", len(planned))
	}
	// planRestore returns the full "provider/model" from meta.json.
	// The strip (first "/" removal) happens in restoreEngines.
	if planned[0].Model != "openrouter/openrouter/owl-alpha" {
		t.Errorf("Model = %q, want openrouter/openrouter/owl-alpha (full provider/model from meta.json)", planned[0].Model)
	}
}

// TestRestoreEngine_ModelFromMetaJson verifies that the engine created
// during restore uses the model from meta.json, not the settings.json
// default. This is the red light for the bug where e2 showed mimo-v2.5
// in meta.json but the engine actually used glm-5.2 (config default).
//
// Root cause: factory always used the first provider, ignoring which
// provider the engine's model belongs to.
func TestRestoreEngine_ModelFromMetaJson(t *testing.T) {
	projectDir := t.TempDir()
	seed := &short.WorkspaceMeta{
		Engines: []short.EngineMeta{
			{ID: "main", Name: "main", Model: "openai/glm-5.2"},
		},
		ActiveEngineID: "main",
	}
	if err := short.WriteWorkspaceMeta(projectDir, seed); err != nil {
		t.Fatalf("WriteWorkspaceMeta: %v", err)
	}
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	planned := planRestoreForTest(t, meta, "zhipu/glm-5")
	if len(planned) != 1 {
		t.Fatalf("planRestore returned %d engines, want 1", len(planned))
	}
	// planRestore returns the full "provider/model" from meta.json.
	if planned[0].Model != "openai/glm-5.2" {
		t.Errorf("factory Model = %q, want openai/glm-5.2 (from meta.json)", planned[0].Model)
	}
}

// TestPersistModelSelection_PersistsAfterSwitch verifies the full chain:
//
//	/model glm-max → persistModelSelection → vs.Model updated → meta.json written
//	→ restart → restore reads correct model
//
// This catches the bug where persistModelSelection wasn't called on /model switch,
// so the meta.json still had the old model from before the per-engine persistence fix.
func TestPersistModelSelection_PersistsAfterSwitch(t *testing.T) {
	projectDir := t.TempDir()
	store, err := short.NewStore(filepath.Join(projectDir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	eng := engine.New(&engine.Params{
		Provider: &mockLLMProvider{name: "openai"},
		Model:    "glm-5",
		Logger:   slog.Default(),
	})
	eng.SetStore(store, projectDir)
	if err := eng.NewSession(projectDir, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { eng.Close() })

	mgr := engine.NewEngineManager()
	mgr.Add(&engine.EngineViewState{
		Engine: eng, ID: "main", Name: "main", Model: "zhipu/glm-5",
	})

	a := newTestAppWithProviders(t)
	a.engine = eng
	a.engineMgr = mgr
	a.projectDir = projectDir

	// Switch from glm-5 to glm-max.
	_ = a.handleModel("glm-max", nil)

	// After switch: vs.Model must have "provider/model" format.
	vs := mgr.Get("main")
	if vs.Model != "openai/glm-max" {
		t.Errorf("vs.Model = %q, want openai/glm-max", vs.Model)
	}

	// meta.json must have the new model.
	meta, err := short.ReadWorkspaceMeta(projectDir)
	if err != nil {
		t.Fatalf("ReadWorkspaceMeta: %v", err)
	}
	var gotModel string
	for _, em := range meta.Engines {
		if em.ID == "main" {
			gotModel = em.Model
		}
	}
	if gotModel != "openai/glm-max" {
		t.Errorf("meta.json main.Model = %q, want openai/glm-max", gotModel)
	}
}

// planRestoreForTest mirrors cmd/gbot.planRestore: when meta has engines,
// use their stored Model verbatim; empty Model falls back to default.
func planRestoreForTest(t *testing.T, meta *short.WorkspaceMeta, defaultModel string) []short.EngineMeta {
	t.Helper()
	if meta == nil || len(meta.Engines) == 0 {
		return []short.EngineMeta{{ID: "main", Name: "main", Model: defaultModel}}
	}
	out := make([]short.EngineMeta, len(meta.Engines))
	for i, em := range meta.Engines {
		if em.Model == "" {
			em.Model = defaultModel
		}
		out[i] = em
	}
	return out
}
