package tui

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/config"
	"github.com/liuy/gbot/pkg/llm"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModelPicker() *ModelPicker {
	providers := map[string]llm.Provider{
		"openai":    &mockLLMProvider{},
		"anthropic": &mockLLMProvider{},
	}
	providerConfigs := map[string]*config.Provider{
		"openai": {
			Name: "openai",
			Models: map[config.Tier]string{
				config.TierLite: "glm-lite",
				config.TierPro:  "glm-5",
				config.TierMax:  "glm-max",
			},
		},
		"anthropic": {
			Name: "anthropic",
			Models: map[config.Tier]string{
				config.TierPro: "claude-sonnet",
			},
		},
	}
	return NewModelPicker(providers, providerConfigs, "openai", config.TierPro)
}

// ---------------------------------------------------------------------------
// NewModelPicker — item construction
// ---------------------------------------------------------------------------

func TestNewModelPicker_Items(t *testing.T) {
	p := newTestModelPicker()

	// openai: 3 tiers + anthropic: 1 tier = 4 items
	if len(p.items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(p.items))
	}

	// Sorted by provider name: anthropic first, then openai
	if p.items[0].Provider != "anthropic" {
		t.Errorf("items[0].Provider = %q, want anthropic", p.items[0].Provider)
	}
	if p.items[1].Provider != "openai" {
		t.Errorf("items[1].Provider = %q, want openai", p.items[1].Provider)
	}
}

func TestNewModelPicker_CurrentMarked(t *testing.T) {
	p := newTestModelPicker()

	// Find the openai/pro item — should be Current=true
	found := false
	for _, item := range p.items {
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

func TestNewModelPicker_CursorOnCurrent(t *testing.T) {
	p := newTestModelPicker()

	// Cursor should be on the openai/pro item
	// After sorting: anthropic/pro=0, openai/lite=1, openai/pro=2, openai/max=3
	if p.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (openai/pro)", p.cursor)
	}
}

func TestNewModelPicker_SkipsProviderWithoutImpl(t *testing.T) {
	providers := map[string]llm.Provider{
		"openai": &mockLLMProvider{},
		// "anthropic" is NOT in providers map
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
	p := NewModelPicker(providers, providerConfigs, "openai", config.TierPro)

	if len(p.items) != 1 {
		t.Fatalf("expected 1 item (anthropic skipped), got %d", len(p.items))
	}
	if p.items[0].Provider != "openai" {
		t.Errorf("expected openai item, got %q", p.items[0].Provider)
	}
}

func TestNewModelPicker_Empty(t *testing.T) {
	p := NewModelPicker(
		map[string]llm.Provider{},
		map[string]*config.Provider{},
		"openai",
		config.TierPro,
	)
	if len(p.items) != 0 {
		t.Errorf("expected 0 items, got %d", len(p.items))
	}
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

func TestModelPicker_Navigation(t *testing.T) {
	p := newTestModelPicker()

	if p.cursor != 2 {
		t.Fatalf("initial cursor = %d, want 2", p.cursor)
	}

	// Down
	model, _ := p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*ModelPicker)
	if p.cursor != 3 {
		t.Errorf("cursor after down = %d, want 3", p.cursor)
	}

	// Down again — wrap to top
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p = model.(*ModelPicker)
	if p.cursor != 0 {
		t.Errorf("cursor after 2nd down = %d, want 0 (wrap)", p.cursor)
	}

	// Up — wrap to bottom
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*ModelPicker)
	if p.cursor != 3 {
		t.Errorf("cursor after up = %d, want 3 (wrap)", p.cursor)
	}

	// Up — normal
	model, _ = p.Update(tea.KeyMsg{Type: tea.KeyUp})
	p = model.(*ModelPicker)
	if p.cursor != 2 {
		t.Errorf("cursor after 2nd up = %d, want 2", p.cursor)
	}
}

func TestModelPicker_EmptyNavigation(t *testing.T) {
	p := NewModelPicker(map[string]llm.Provider{}, map[string]*config.Provider{}, "", config.TierPro)
	// Navigation on empty should not panic
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

func TestModelPicker_Select(t *testing.T) {
	p := newTestModelPicker()
	p.cursor = 0 // anthropic/pro

	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*ModelPicker)

	if p.selected == nil {
		t.Fatal("expected selection after Enter")
	}
	if p.selected.Provider != "anthropic" {
		t.Errorf("selected.Provider = %q, want anthropic", p.selected.Provider)
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after selection")
	}
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

func TestModelPicker_Cancel(t *testing.T) {
	p := newTestModelPicker()

	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	p = model.(*ModelPicker)

	if !p.aborted {
		t.Error("expected aborted after Esc")
	}
	if cmd == nil {
		t.Error("expected tea.Quit cmd after cancel")
	}
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------

func TestModelPicker_Init(t *testing.T) {
	p := newTestModelPicker()
	cmd := p.Init()
	if cmd != nil {
		t.Errorf("Init() should return nil, got %v", cmd)
	}
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func TestModelPicker_View(t *testing.T) {
	p := newTestModelPicker()
	view := p.View()

	if view == "" {
		t.Fatal("expected non-empty view")
	}
	if !strings.Contains(view, "openai") {
		t.Error("view should contain 'openai'")
	}
	if !strings.Contains(view, "anthropic") {
		t.Error("view should contain 'anthropic'")
	}
	if !strings.Contains(view, "glm-5") {
		t.Error("view should contain model name 'glm-5'")
	}
	if !strings.Contains(view, "Select model") {
		t.Error("view should contain header")
	}
	if !strings.Contains(view, "Esc") {
		t.Error("view should contain key hints")
	}
}

func TestModelPicker_View_Empty(t *testing.T) {
	p := NewModelPicker(map[string]llm.Provider{}, map[string]*config.Provider{}, "", config.TierPro)
	view := p.View()
	if !strings.Contains(view, "No models available") {
		t.Errorf("empty picker should say no models, got %q", view)
	}
}

func TestModelPicker_View_ShowsCurrent(t *testing.T) {
	p := newTestModelPicker()
	view := p.View()
	if !strings.Contains(view, "*") {
		t.Error("view should show current marker '*'")
	}
}

// ---------------------------------------------------------------------------
// Empty list select
// ---------------------------------------------------------------------------

func TestModelPicker_EmptySelect(t *testing.T) {
	p := NewModelPicker(map[string]llm.Provider{}, map[string]*config.Provider{}, "", config.TierPro)
	model, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p = model.(*ModelPicker)
	if !p.aborted {
		t.Error("empty picker Enter should abort")
	}
	if cmd == nil {
		t.Error("expected tea.Quit")
	}
}
