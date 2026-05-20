package config

import (
	"strings"
	"testing"

	"github.com/liuy/gbot/pkg/llm"
)

func TestCreateAllProviders_AnthropicAndOpenAI(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Providers: []Provider{
			{
				Name: "claude",
				URL:  "https://api.anthropic.com",
				Keys: []string{"sk-test-key-1"},
				Models: map[Tier]string{
					TierPro: "claude-3-5-sonnet",
					TierMax: "claude-3-opus",
				},
				Type: ProviderTypeAnthropic,
			},
			{
				Name: "deepseek",
				URL:  "https://api.deepseek.com",
				Keys: []string{"sk-test-key-2"},
				Models: map[Tier]string{
					TierPro: "deepseek-chat",
				},
				Type: ProviderTypeOpenAI,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(m))
	}

	claude, ok := m["claude"]
	if !ok {
		t.Fatal("missing provider \"claude\"")
	}
	if _, ok := claude.(*llm.AnthropicProvider); !ok {
		t.Fatalf("expected *llm.AnthropicProvider, got %T", claude)
	}

	deepseek, ok := m["deepseek"]
	if !ok {
		t.Fatal("missing provider \"deepseek\"")
	}
	if _, ok := deepseek.(*llm.OpenAIProvider); !ok {
		t.Fatalf("expected *llm.OpenAIProvider, got %T", deepseek)
	}
}

func TestCreateAllProviders_NoAPIKey(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Providers: []Provider{
			{
				Name: "nokey",
				Keys: []string{},
				Models: map[Tier]string{
					TierPro: "some-model",
				},
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected 0 providers, got %d", len(m))
	}
}

func TestCreateAllProviders_NoTierProModel(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Providers: []Provider{
			{
				Name: "incomplete",
				Keys: []string{"sk-key"},
				Models: map[Tier]string{
					TierLite: "some-lite-model",
				},
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected 0 providers (no TierPro model), got %d", len(m))
	}
}

func TestCreateAllProviders_ParseModelError(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Model: "invalid-tier",
		Providers: []Provider{
			{
				Name: "any",
				Keys: []string{"sk-key"},
				Models: map[Tier]string{
					TierPro: "model",
				},
			},
		},
	}

	_, err := CreateAllProviders(cfg)
	if err == nil {
		t.Fatal("expected error for invalid tier, got nil")
	}
	if !strings.Contains(err.Error(), "invalid tier") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCreateAllProviders_TierFallback(t *testing.T) {
	t.Parallel()
	// Request "max" tier but provider only has "pro".
	cfg := &Config{
		Model: "provider/max",
		Providers: []Provider{
			{
				Name: "provider",
				Keys: []string{"sk-key"},
				Models: map[Tier]string{
					TierPro: "pro-model",
				},
				Type: ProviderTypeOpenAI,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(m))
	}
	p, ok := m["provider"]
	if !ok {
		t.Fatal("missing provider \"provider\"")
	}
	if _, ok := p.(*llm.OpenAIProvider); !ok {
		t.Fatalf("expected *llm.OpenAIProvider, got %T", p)
	}
}

func TestCreateAllProviders_DefaultAnthropicURL(t *testing.T) {
	t.Parallel()
	// Anthropic provider with no URL — should use default.
	cfg := &Config{
		Providers: []Provider{
			{
				Name: "claude-default-url",
				Keys: []string{"sk-key"},
				Models: map[Tier]string{
					TierPro: "claude-3-5-sonnet",
				},
				Type: ProviderTypeAnthropic,
				// URL intentionally empty
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(m))
	}
	p, ok := m["claude-default-url"]
	if !ok {
		t.Fatal("missing provider \"claude-default-url\"")
	}
	// Verify it created an AnthropicProvider (the default URL logic is internal).
	if _, ok := p.(*llm.AnthropicProvider); !ok {
		t.Fatalf("expected *llm.AnthropicProvider, got %T", p)
	}
}

func TestCreateAllProviders_EmptyConfig(t *testing.T) {
	t.Parallel()
	cfg := &Config{}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected 0 providers, got %d", len(m))
	}
}

func TestCreateAllProviders_SpecificTier(t *testing.T) {
	t.Parallel()
	// Request "lite" tier — provider has it, so it should use the lite model.
	cfg := &Config{
		Model: "myprovider/lite",
		Providers: []Provider{
			{
				Name: "myprovider",
				Keys: []string{"sk-key"},
				Models: map[Tier]string{
					TierPro:  "pro-model",
					TierLite: "lite-model",
				},
				Type: ProviderTypeOpenAI,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(m))
	}
	if _, ok := m["myprovider"]; !ok {
		t.Fatal("missing provider \"myprovider\"")
	}
}
