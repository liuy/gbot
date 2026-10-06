package config

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// Free providers re-fetch their top-10 at startup: the previously fetched
// set must be REPLACED (stale ids dropped), while hand-configured models
// survive untouched.
func TestCreateAllProviders_FreeRefreshReplacesStale(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":                   "shared:free",
					"name":                 "Shared (free)",
					"context_length":       131072,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"tools"},
				},
				{
					"id":                   "fresh:free",
					"name":                 "Fresh (free)",
					"context_length":       262144,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"tools"},
				},
				{
					// Hand-configured id that the fetch also returns: hand
					// metadata (context 8k) must survive the refresh.
					"id":                   "hand:free",
					"name":                 "Hand (free)",
					"context_length":       999999,
					"pricing":              map[string]string{"prompt": "0", "completion": "0"},
					"supported_parameters": []string{"tools"},
				},
			},
		})
	}))
	defer srv.Close()

	cfg := &Config{
		Providers: []Provider{
			{
				Name:        "openrouter-free",
				URL:         srv.URL,
				Type:        ProviderTypeOpenAI,
				Keys:        []string{"sk-free"},
				FreeFetched: []string{"stale:free", "shared:free"}, // previous fetch managed these
				Models: func() Models {
					var m Models
					_ = json.Unmarshal([]byte(`{
						"stale:free": {},
						"shared:free": {"context": "64k"},
						"hand:free": {"context": "8k"},
						"hand-added": {}
					}`), &m)
					return m
				}(),
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := m["openrouter-free"]
	if prov == nil {
		t.Fatal("missing free provider")
	}

	// stale:free dropped out of the fetch — it must be gone from the models.
	if _, ok := cfg.Providers[0].Models.Get("stale:free"); ok {
		t.Errorf("stale:free must be removed from stored config, still present")
	}
	// hand-added survives untouched.
	if _, ok := cfg.Providers[0].Models.Get("hand-added"); !ok {
		t.Errorf("hand-added model must survive a free refresh")
	}
	// shared:free is managed — its metadata REFRESHES to the fetch value.
	if shared, _ := cfg.Providers[0].Models.Get("shared:free"); shared.Context != IntOrHuman(131072) {
		t.Errorf("shared:free context = %v, want refreshed %v", shared.Context, IntOrHuman(131072))
	}
	// hand:free was hand-configured (never fetched) — metadata PRESERVED
	// even though the fetch also returns it, and it JOINS the snapshot.
	if hand, _ := cfg.Providers[0].Models.Get("hand:free"); hand.Context != IntOrHuman(8192) {
		t.Errorf("hand:free context = %v, want preserved %v", hand.Context, IntOrHuman(8192))
	}
	// fresh:free added.
	if _, ok := cfg.Providers[0].Models.Get("fresh:free"); !ok {
		t.Errorf("fresh:free must be added by the refresh")
	}
	// Snapshot tracks the new fetch exactly (hand:free joins).
	got := cfg.Providers[0].FreeFetched
	if len(got) != 3 || got[0] != "shared:free" || got[1] != "fresh:free" || got[2] != "hand:free" {
		t.Errorf("FreeFetched = %v, want [shared:free fresh:free hand:free]", got)
	}
}

// ---------------------------------------------------------------------------
// Per-provider microcompact switch
// ---------------------------------------------------------------------------

func TestProviderMicrocompact_ParseAndResolve(t *testing.T) {
	t.Parallel()
	raw := `{
		"providers": [
			{"name": "strata", "url": "http://localhost:9", "keys": ["k"], "models": {"m": {}}, "microcompact": false},
			{"name": "zhipu", "url": "https://open.bigmodel.cn", "keys": ["k"], "models": {"m": {}}},
			{"name": "glm", "url": "https://x.example", "keys": ["k"], "models": {"m": {}}, "microcompact": true}
		]
	}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Providers) != 3 {
		t.Fatalf("len(Providers) = %d, want 3", len(cfg.Providers))
	}

	strata := &cfg.Providers[0]
	if strata.Microcompact == nil || *strata.Microcompact {
		t.Errorf("strata.Microcompact = %v, want pointer to false", strata.Microcompact)
	}
	if strata.MicrocompactEnabled() {
		t.Error("strata.MicrocompactEnabled() = true, want false")
	}

	zhipu := &cfg.Providers[1]
	if zhipu.Microcompact != nil {
		t.Errorf("zhipu.Microcompact = %v, want nil (field absent)", zhipu.Microcompact)
	}
	if !zhipu.MicrocompactEnabled() {
		t.Error("zhipu.MicrocompactEnabled() = false, want true (absent defaults to enabled)")
	}

	glm := &cfg.Providers[2]
	if glm.Microcompact == nil || !*glm.Microcompact {
		t.Errorf("glm.Microcompact = %v, want pointer to true", glm.Microcompact)
	}
	if !glm.MicrocompactEnabled() {
		t.Error("glm.MicrocompactEnabled() = false, want true")
	}
}

// TestCreateAllProviders_MicrocompactPropagation checks the config → llm
// provider hop: the tri-state flag must land on the provider instance so
// every engine built from it (including runtime SetProvider switches) sees
// the right value.
func TestCreateAllProviders_MicrocompactPropagation(t *testing.T) {
	t.Parallel()
	off := false
	on := true
	cfg := &Config{
		Providers: []Provider{
			{
				Name:         "mc-off",
				URL:          "http://localhost:9",
				Keys:         []string{"sk-key"},
				Models:       NewModelsFromMap(map[string]ModelConfig{"m": {}}),
				Type:         ProviderTypeOpenAI,
				Microcompact: &off,
			},
			{
				Name:   "mc-default",
				URL:    "http://localhost:10",
				Keys:   []string{"sk-key"},
				Models: NewModelsFromMap(map[string]ModelConfig{"m": {}}),
				Type:   ProviderTypeOpenAI,
			},
			{
				Name:         "mc-on",
				URL:          "http://localhost:11",
				Keys:         []string{"sk-key"},
				Models:       NewModelsFromMap(map[string]ModelConfig{"m": {}}),
				Type:         ProviderTypeOpenAI,
				Microcompact: &on,
			},
		},
	}

	m, err := CreateAllProviders(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for name, want := range map[string]bool{"mc-off": false, "mc-default": true, "mc-on": true} {
		p, ok := m[name].(llm.MicrocompactProvider)
		if !ok {
			t.Fatalf("provider %s (%T) does not implement llm.MicrocompactProvider", name, m[name])
		}
		if got := p.MicrocompactEnabled(); got != want {
			t.Errorf("%s MicrocompactEnabled() = %v, want %v", name, got, want)
		}
	}
}
