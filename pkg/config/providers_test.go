package config

import (
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
				Models: NewModelsFromMap(map[string]ModelConfig{
					"claude-3-5-sonnet": {},
					"claude-3-opus":     {},
				}),
				Type: ProviderTypeAnthropic,
			},
			{
				Name: "deepseek",
				URL:  "https://api.deepseek.com",
				Keys: []string{"sk-test-key-2"},
				Models: NewModelsFromMap(map[string]ModelConfig{
					"deepseek-chat": {},
				}),
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
				Models: NewModelsFromMap(map[string]ModelConfig{
					"some-model": {},
				}),
			},
		},
	}

	_, err := CreateAllProviders(cfg)
	if err == nil {
		t.Fatal("CreateAllProviders should return error: no providers have API keys")
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
				Models: NewModelsFromMap(map[string]ModelConfig{
					"claude-3-5-sonnet": {},
				}),
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

	_, err := CreateAllProviders(cfg)
	if err == nil {
		t.Fatal("expected CreateAllProviders to fail on empty config (ResolveModel requires providers)")
	}
}

func TestCreateAllProviders_SpecificModel(t *testing.T) {
	t.Parallel()
	// Request a specific model by name.
	cfg := &Config{
		Model: ModelSpec{"default": "myprovider/lite-model"},
		Providers: []Provider{
			{
				Name: "myprovider",
				Keys: []string{"sk-key"},
				Models: NewModelsFromMap(map[string]ModelConfig{
					"pro-model":  {},
					"lite-model": {},
				}),
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

func TestCreateAllProviders_Responses(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Providers: []Provider{
			{
				Name: "glm-resp",
				URL:  "https://open.bigmodel.cn/api/v1",
				Keys: []string{"sk-glm-key"},
				Models: NewModelsFromMap(map[string]ModelConfig{
					"glm-4.6": {Context: IntOrHuman(200000)},
				}),
				Type: ProviderTypeResponses,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := m["glm-resp"]
	if !ok {
		t.Fatal("missing provider \"glm-resp\"")
	}
	resp, ok := p.(*llm.ResponsesProvider)
	if !ok {
		t.Fatalf("expected *llm.ResponsesProvider, got %T", p)
	}
	if resp.Name() != "glm-resp" {
		t.Errorf("Name() = %q, want glm-resp", resp.Name())
	}
}
